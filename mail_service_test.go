package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func mailTestConfig(t *testing.T, settings map[string]any) {
	t.Helper()
	for key, value := range settings {
		previous := viper.Get(key)
		viper.Set(key, value)
		t.Cleanup(func() { viper.Set(key, previous) })
	}
}

// Runtime tests must stop their server before cleanup and must not run in parallel.
func isolateApplicationMail(t *testing.T) *mailTaskQueue {
	t.Helper()
	previous := applicationMail
	queue := newMailTaskQueue()
	applicationMail = queue
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := queue.shutdown(ctx); err != nil {
			t.Errorf("isolated mail shutdown: %v", err)
		}
		applicationMail = previous
	})
	return queue
}

func acsTestConfig(t *testing.T) {
	t.Helper()
	mailTestConfig(t, map[string]any{
		"mailer.mailer_name":                            "azure_communication_service",
		"mailer.azure_communication_service.host":       "https://resource.communication.azure.com/",
		"mailer.azure_communication_service.key":        base64.StdEncoding.EncodeToString([]byte("test-access-key")),
		"mailer.azure_communication_service.from_email": "Sender <sender@example.test>",
	})
}

func TestMailerConfiguration(t *testing.T) {
	testMailer(t, true, func(string, string) error { return nil })
	acsTestConfig(t)
	for _, test := range []struct {
		name     string
		settings map[string]any
		valid    bool
	}{
		{"disabled", map[string]any{"mailer.mailer_name": "none"}, true},
		{"smtp", map[string]any{"mailer.mailer_name": "smtp"}, true},
		{"smtp IPv6", map[string]any{"mailer.mailer_name": "smtp", "mailer.smtp.host": "::1"}, true},
		{"ACS URL", nil, true},
		{"ACS bare host", map[string]any{"mailer.azure_communication_service.host": "resource.communication.azure.com"}, true},
		{"unknown provider", map[string]any{"mailer.mailer_name": "sensitive-invalid-value"}, false},
		{"SMTP URL", map[string]any{"mailer.mailer_name": "smtp", "mailer.smtp.host": "https://localhost"}, false},
		{"SMTP missing password", map[string]any{"mailer.mailer_name": "smtp", "mailer.smtp.password": ""}, false},
		{"SMTP missing username", map[string]any{"mailer.mailer_name": "smtp", "mailer.smtp.username": ""}, false},
		{"SMTP zero port", map[string]any{"mailer.mailer_name": "smtp", "mailer.smtp.port": 0}, false},
		{"SMTP oversized port", map[string]any{"mailer.mailer_name": "smtp", "mailer.smtp.port": 65536}, false},
		{"SMTP fractional port", map[string]any{"mailer.mailer_name": "smtp", "mailer.smtp.port": 25.5}, false},
		{"SMTP invalid sender", map[string]any{"mailer.mailer_name": "smtp", "mailer.smtp.from_email": "sensitive-invalid-value"}, false},
		{"SMTP injected sender", map[string]any{"mailer.mailer_name": "smtp", "mailer.smtp.from_email": "sender@example.test\r\nBcc: hidden@example.test"}, false},
		{"SMTP control in credentials", map[string]any{"mailer.mailer_name": "smtp", "mailer.smtp.password": "sensitive-invalid-value\n"}, false},
		{"ACS bad key", map[string]any{"mailer.azure_communication_service.key": "sensitive-invalid-value"}, false},
		{"ACS empty key", map[string]any{"mailer.azure_communication_service.key": ""}, false},
		{"ACS invalid sender", map[string]any{"mailer.azure_communication_service.from_email": "sensitive-invalid-value"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			mailTestConfig(t, test.settings)
			err := validateMailerConfig()
			if (err == nil) != test.valid {
				t.Fatalf("validation = %v, valid = %v", err, test.valid)
			}
			if err != nil && strings.Contains(err.Error(), "sensitive-invalid-value") {
				t.Fatal("validation exposed a configuration value")
			}
			if emailDeliveryEnabled() != (test.valid && viper.GetString("mailer.mailer_name") != "none") {
				t.Fatal("readiness disagrees with configuration validation")
			}
		})
	}
}

func TestACSEndpointNormalization(t *testing.T) {
	for _, value := range []string{"resource.communication.azure.com", "https://resource.communication.azure.com", "https://resource.communication.azure.com/"} {
		endpoint, err := normalizeACSEndpoint(value)
		if err != nil || endpoint.String() != "https://resource.communication.azure.com" {
			t.Fatalf("normalization failed: %v %v", endpoint, err)
		}
	}
	for _, value := range []string{"", "http://localhost", "https://user:secret@localhost", "https://localhost/path",
		"https://localhost?secret=1", "https://localhost?", "https://localhost#fragment", "https://localhost#",
		"https://localhost:0", "https://localhost:65536", "https://localhost:", "https://localhost/%2f", "localhost\r\nInjected: secret"} {
		if _, err := normalizeACSEndpoint(value); err == nil {
			t.Errorf("accepted invalid endpoint %q", value)
		}
	}
}

type mailRoundTripper func(*http.Request) (*http.Response, error)

func (transport mailRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func mailTestHTTPClient(t *testing.T, transport mailRoundTripper) {
	t.Helper()
	previous := mailHTTPClient
	mailHTTPClient = &http.Client{Transport: transport}
	t.Cleanup(func() { mailHTTPClient = previous })
}

func TestACSRequestSignature(t *testing.T) {
	acsTestConfig(t)
	config, err := readMailerConfig()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 2, 17, 0, 0, 0, time.UTC)
	calls := 0
	client := &http.Client{Transport: mailRoundTripper(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != "POST" || request.URL.String() != "https://resource.communication.azure.com/emails:send?api-version=2023-03-31" {
			t.Fatalf("incorrect ACS operation: %s %s", request.Method, request.URL)
		}
		payload, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		expected := `{"senderAddress":"sender@example.test","content":{"subject":"Subject \u0026 text","plainText":"Body \u003ctag\u003e"},"recipients":{"to":[{"address":"recipient@example.test"}]}}`
		if string(payload) != expected {
			t.Fatalf("unexpected payload: %s", payload)
		}
		hash := sha256.Sum256([]byte(expected))
		digest := base64.StdEncoding.EncodeToString(hash[:])
		signed := "POST\n/emails:send?api-version=2023-03-31\nFri, 02 Oct 2026 17:00:00 GMT;resource.communication.azure.com;" + digest
		mac := hmac.New(sha256.New, []byte("test-access-key"))
		_, _ = mac.Write([]byte(signed))
		auth := "HMAC-SHA256 SignedHeaders=x-ms-date;host;x-ms-content-sha256&Signature=" + base64.StdEncoding.EncodeToString(mac.Sum(nil))
		if request.Header.Get("Authorization") != auth || request.Header.Get("x-ms-content-sha256") != digest ||
			request.Header.Get("x-ms-date") != "Fri, 02 Oct 2026 17:00:00 GMT" || request.Header.Get("Content-Type") != "application/json" {
			t.Fatal("incorrect ACS signature or headers")
		}
		if deadline, ok := request.Context().Deadline(); !ok || time.Until(deadline) > mailSendTimeout {
			t.Fatal("ACS request has no bounded deadline")
		}
		return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader(`{"id":"accepted-operation"}`)), Header: make(http.Header)}, nil
	})}
	if err := sendACS(context.Background(), client, config, "recipient@example.test", "Subject & text", "Body <tag>", now); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("unexpected ACS retry")
	}
}

func TestACSTransportResponses(t *testing.T) {
	acsTestConfig(t)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/do-not-follow", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	mailTestConfig(t, map[string]any{"mailer.azure_communication_service.host": server.URL})
	config, err := readMailerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := sendACS(context.Background(), server.Client(), config, "recipient@example.test", "subject", "body", time.Now()); err == nil {
		t.Fatal("redirect was reported as acceptance")
	}
	if calls.Load() != 1 {
		t.Fatal("signed ACS request followed a redirect or retried")
	}
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"rejected", 401, "private provider diagnostics and credentials"},
		{"unexpected OK", 200, "{}"},
		{"oversized", 202, strings.Repeat("x", maxACSResponseBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: mailRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}
			err := sendACS(context.Background(), client, config, "recipient@example.test", "subject", "body", time.Now())
			if err == nil || strings.Contains(err.Error(), test.body) {
				t.Fatalf("missing or unsafe response error: %v", err)
			}
		})
	}
}

func TestACSCancellation(t *testing.T) {
	acsTestConfig(t)
	for _, responseStarted := range []bool{false, true} {
		t.Run(fmt.Sprintf("response started=%v", responseStarted), func(t *testing.T) {
			started := make(chan struct{})
			released := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(released)
				_, _ = io.Copy(io.Discard, r.Body)
				if responseStarted {
					w.WriteHeader(http.StatusAccepted)
					w.(http.Flusher).Flush()
				}
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			mailTestConfig(t, map[string]any{"mailer.azure_communication_service.host": server.URL})
			config, err := readMailerConfig()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- sendACS(ctx, server.Client(), config, "recipient@example.test", "subject", "body", time.Now())
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("local ACS request did not start")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("ACS send ignored cancellation")
			}
			select {
			case <-released:
			case <-time.After(2 * time.Second):
				t.Fatal("ACS connection was not canceled")
			}
		})
	}
}

func TestMailDisabledAndRecipientFailures(t *testing.T) {
	mailTestConfig(t, map[string]any{"mailer.mailer_name": "none"})
	mailTestHTTPClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("disabled mail contacted a transport")
		return nil, errors.New("unexpected send")
	})
	if err := SendMail([]string{"recipient@example.test"}, "subject", "body"); !errors.Is(err, ErrMailDisabled) {
		t.Fatalf("disabled mail result: %v", err)
	}
	if err := queueMail("disabled test", func(context.Context) error { t.Fatal("disabled task ran"); return nil }); !errors.Is(err, ErrMailDisabled) {
		t.Fatalf("disabled queue result: %v", err)
	}
	acsTestConfig(t)
	var recipients []string
	mailTestHTTPClient(t, func(request *http.Request) (*http.Response, error) {
		var payload struct {
			Recipients struct {
				To []struct{ Address string }
			}
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Recipients.To) != 1 {
			t.Fatal("recipients were exposed to each other")
		}
		recipients = append(recipients, payload.Recipients.To[0].Address)
		status := 202
		if len(recipients) == 1 {
			status = 500
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	err := SendMail([]string{"first@example.test", "invalid address", "last@example.test"}, "subject", "body")
	if err == nil || !strings.Contains(err.Error(), "recipient=1") || !strings.Contains(err.Error(), "recipient=2") {
		t.Fatalf("earlier recipient errors lost: %v", err)
	}
	if strings.Contains(err.Error(), "first@example.test") || len(recipients) != 2 || recipients[1] != "last@example.test" {
		t.Fatal("mail errors exposed recipients or prevented independent sends")
	}
	if err := SendMail(nil, "subject", "body"); err == nil {
		t.Fatal("empty recipient set reported success")
	}
	if err := SendMail([]string{"last@example.test"}, "Injected\r\nBcc: private@example.test", "body"); err == nil {
		t.Fatal("header injection accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SendMailContext(ctx, []string{"last@example.test"}, "subject", "body"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled send: %v", err)
	}
}

func TestSMTPMIMEEncoding(t *testing.T) {
	from := &mail.Address{Name: strings.Repeat("Sender 名称 ", 30), Address: "sender@example.test"}
	to := &mail.Address{Name: strings.Repeat("Recipient ", 30), Address: "recipient@example.test"}
	subject := strings.Repeat("A long subject with unicode 世界 🙂 ", 90)
	body := strings.Repeat("An unbroken <>& UTF-8 世界 🙂 line ", 300) + "\nsecond\r\nthird\rlast\t \n"
	encoded, err := buildSMTPMessage(from, to, subject, body)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bytes.ReplaceAll(encoded, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("SMTP message contains bare LF")
	}
	for _, line := range bytes.Split(encoded, []byte("\r\n")) {
		if len(line) > 78 {
			t.Fatalf("mail line exceeds the folding limit: %d", len(line))
		}
	}
	message, err := mail.ReadMessage(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	decodedSubject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	if err != nil || decodedSubject != subject {
		t.Fatalf("subject roundtrip failed: %v", err)
	}
	for name, expected := range map[string]*mail.Address{"From": from, "To": to} {
		address, err := mail.ParseAddress(message.Header.Get(name))
		if err != nil || address.Name != expected.Name || address.Address != expected.Address {
			t.Fatalf("%s header roundtrip failed: %v", name, err)
		}
	}
	decoded, err := io.ReadAll(quotedprintable.NewReader(message.Body))
	expected := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n"), "\n", "\r\n")
	if err != nil || string(decoded) != expected {
		t.Fatal("body did not survive quoted-printable encoding")
	}
	if message.Header.Get("Content-Type") != "text/plain; charset=UTF-8" || message.Header.Get("Content-Transfer-Encoding") != "quoted-printable" {
		t.Fatal("missing MIME encoding headers")
	}
	if _, err := mail.ParseDate(message.Header.Get("Date")); err != nil || message.Header.Get("Message-ID") == "" {
		t.Fatal("missing message identification headers")
	}
	quoted, err := parseMailAddress(`"Display Name" <"quoted local"@example.test>`)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = buildSMTPMessage(quoted, to, "subject", "body")
	if err != nil || !bytes.Contains(encoded, []byte(`<"quoted local"@example.test>`)) {
		t.Fatal("quoted local part was not preserved")
	}
}

func TestSMTPTransportAndCancellation(t *testing.T) {
	// Borrow a local test certificate, never an insecure client configuration.
	certificateServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := certificateServer.TLS.Certificates[0]
	certificateServer.Close()
	cert, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	for _, mode := range []string{"normal", "cancel greeting", "deadline greeting", "deadline data"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			host, port, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			serverResult := make(chan error, 1)
			var received []byte
			go func() {
				connection, err := listener.Accept()
				if err != nil {
					serverResult <- err
					return
				}
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
				close(started)
				if mode == "cancel greeting" || mode == "deadline greeting" {
					_, err = connection.Read(make([]byte, 1))
					if errors.Is(err, io.EOF) {
						err = nil
					}
					serverResult <- err
					return
				}
				received, err = serveTestSMTP(connection, certificate, mode == "deadline data")
				serverResult <- err
			}()
			timeout := 2 * time.Second
			if strings.HasPrefix(mode, "deadline") {
				timeout = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if mode == "cancel greeting" {
				go func() { <-started; cancel() }()
			}
			config := mailerConfig{smtpHost: host, smtpPort: port, username: "test-user", password: "test-password", from: &mail.Address{Address: "sender@example.test"}}
			message, err := buildSMTPMessage(config.from, &mail.Address{Address: "recipient@example.test"}, "subject", "body")
			if err != nil {
				t.Fatal(err)
			}
			err = sendSMTPMessage(ctx, config, "recipient@example.test", message, &tls.Config{ServerName: host, RootCAs: roots, MinVersion: tls.VersionTLS12})
			if mode == "cancel greeting" && !errors.Is(err, context.Canceled) {
				t.Fatalf("SMTP cancellation: %v", err)
			}
			if strings.HasPrefix(mode, "deadline") {
				var netErr net.Error
				if !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &netErr) && netErr.Timeout()) {
					t.Fatalf("SMTP deadline: %v", err)
				}
			}
			if mode == "normal" && err != nil {
				t.Fatalf("SMTP send: %v", err)
			}
			if err := <-serverResult; err != nil {
				t.Fatal(err)
			}
			if (mode == "normal" || mode == "deadline data") && !bytes.Contains(received, []byte("Subject:")) {
				t.Fatal("local SMTP server did not receive the message")
			}
		})
	}
}

func serveTestSMTP(connection net.Conn, certificate tls.Certificate, stallAfterData bool) ([]byte, error) {
	wire := textproto.NewConn(connection)
	if err := wire.PrintfLine("220 local test server"); err != nil {
		return nil, err
	}
	expect := func(prefix string) error {
		line, err := wire.ReadLine()
		if err != nil {
			return err
		}
		if !strings.HasPrefix(line, prefix) {
			return fmt.Errorf("expected SMTP command %s", prefix)
		}
		return nil
	}
	if err := expect("EHLO "); err != nil {
		return nil, err
	}
	if err := wire.PrintfLine("250-localhost\r\n250 STARTTLS"); err != nil {
		return nil, err
	}
	if err := expect("STARTTLS"); err != nil {
		return nil, err
	}
	if err := wire.PrintfLine("220 start TLS"); err != nil {
		return nil, err
	}
	secure := tls.Server(connection, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err := secure.Handshake(); err != nil {
		return nil, err
	}
	wire = textproto.NewConn(secure)
	for _, step := range []struct{ command, response string }{
		{"EHLO ", "250-localhost\r\n250 AUTH LOGIN"},
		{"AUTH LOGIN " + base64.StdEncoding.EncodeToString([]byte("test-user")), "334 UGFzc3dvcmQ6"},
		{base64.StdEncoding.EncodeToString([]byte("test-password")), "235 authenticated"},
		{"MAIL FROM:<sender@example.test>", "250 sender accepted"},
		{"RCPT TO:<recipient@example.test>", "250 recipient accepted"},
		{"DATA", "354 send data"},
	} {
		if err := expect(step.command); err != nil {
			return nil, err
		}
		if err := wire.PrintfLine("%s", step.response); err != nil {
			return nil, err
		}
	}
	message, err := wire.ReadDotBytes()
	if err != nil {
		return nil, err
	}
	if stallAfterData {
		if _, err := wire.ReadLine(); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("stalled SMTP connection did not close: %w", err)
		}
		return message, nil
	}
	return message, wire.PrintfLine("250 accepted")
}

func TestMailQueueLifecycle(t *testing.T) {
	queue := newMailTaskQueue()
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	if err := queue.queue("test", func(ctx context.Context) error {
		started <- ctx
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx := <-started
	if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
		t.Fatal("background mail context is not live and bounded")
	}
	shutdown := make(chan error, 1)
	go func() { shutdown <- queue.shutdown(context.Background()) }()
	select {
	case <-shutdown:
		t.Fatal("shutdown did not drain pending mail")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-shutdown; err != nil {
		t.Fatal(err)
	}
	if err := queue.queue("late", func(context.Context) error { return nil }); !errors.Is(err, ErrMailShuttingDown) {
		t.Fatalf("queued work after shutdown: %v", err)
	}
	if err := queue.shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMailQueueForcedShutdownRace(t *testing.T) {
	queue := newMailTaskQueue()
	started := make(chan struct{})
	if err := queue.queue("initial cancellation test", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}

	<-started
	var senders sync.WaitGroup
	for range 20 {
		senders.Add(1)
		go func() {
			defer senders.Done()
			err := queue.queue("cancel test", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
			if err != nil && !errors.Is(err, ErrMailShuttingDown) {
				t.Error(err)
			}
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := queue.shutdown(ctx)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	senders.Wait()
	select {
	case <-queue.done:
	case <-time.After(time.Second):
		t.Fatal("canceled mail did not finish")
	}
}

func TestMailQueueSafeFailureLogging(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	queue := newMailTaskQueue()
	if err := queue.queue("notification for message=123", func(context.Context) error {
		return errors.New("credential-value visitor@example.test private message")
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := queue.shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "notification for message=123: delivery failed") {
		t.Fatal("asynchronous delivery failure was not logged")
	}
	for _, private := range []string{"credential-value", "visitor@example.test", "private message"} {
		if strings.Contains(output.String(), private) {
			t.Fatal("mail queue logged sensitive delivery diagnostics")
		}
	}

}

func TestMailQueueConfigurationSnapshot(t *testing.T) {
	acsTestConfig(t)
	queue := isolateApplicationMail(t)
	previousURL := appConfig.PublicURL
	appConfig.PublicURL = "https://original.example.test"
	t.Cleanup(func() { appConfig.PublicURL = previousURL })
	var requests atomic.Int32
	mailTestHTTPClient(t, func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if request.URL.Host != "resource.communication.azure.com" {
			t.Error("queued mail did not retain its original endpoint")
		}
		var payload struct {
			SenderAddress string
			Content       struct{ PlainText string }
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.SenderAddress != "sender@example.test" ||
			!strings.Contains(payload.Content.PlainText, "https://original.example.test/") {
			t.Error("queued mail did not retain its original sender and public URL")
		}
		return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	started := make(chan struct{})
	release := make(chan struct{})
	sendResult := make(chan error, 1)
	if err := queueMail("configuration snapshot test", func(ctx context.Context) error {
		close(started)
		<-release
		if err := SendPasswordResetEmailContext(ctx, "recipient@example.test", "reset-token"); err != nil {
			sendResult <- err
			return err
		}
		err := SendVerificationEmailContext(ctx, "recipient@example.test", "verification-token")
		sendResult <- err
		return err
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	mailTestHTTPClient(t, func(*http.Request) (*http.Response, error) {
		t.Error("queued mail used the replacement HTTP client")
		return nil, errors.New("unexpected transport")
	})
	viper.Set("mailer.mailer_name", "none")
	viper.Set("mailer.azure_communication_service.from_email", "changed@example.test")
	appConfig.PublicURL = "https://changed.example.test"
	close(release)
	for range 100 {
		viper.Set("mailer.mailer_name", "none")
		appConfig.PublicURL = "https://changed.example.test"
	}
	if err := <-sendResult; err != nil {
		t.Fatalf("queued send reread changed configuration: %v", err)
	}
	if err := queue.shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatal("queued reset and verification helpers did not use their captured transport")
	}
}

func TestMailQueueRuntimeIsolation(t *testing.T) {
	original := applicationMail
	t.Run("runtime shutdown", func(t *testing.T) {
		isolated := isolateApplicationMail(t)
		if isolated == original {
			t.Fatal("runtime test reused the application mail queue")
		}
		if err := shutdownMail(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := isolated.queue("late runtime task", func(context.Context) error { return nil }); !errors.Is(err, ErrMailShuttingDown) {
			t.Fatalf("isolated runtime queue was not shut down: %v", err)
		}
	})
	if applicationMail != original {
		t.Fatal("runtime shutdown changed the global test mail queue")
	}
}
