package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
)

const privateMailDiagnostic = "credential-SENTINEL recipient-SENTINEL@example.test private-body-SENTINEL https://private.example.test/?token=secret-SENTINEL"

func assertSafeMailDiagnostic(t *testing.T, diagnostic string, fields ...string) {
	t.Helper()
	for _, private := range strings.Fields(privateMailDiagnostic) {
		if strings.Contains(diagnostic, private) {
			t.Fatal("mail diagnostic exposed sensitive data")
		}
	}
	for _, field := range fields {
		if !strings.Contains(diagnostic, field) {
			t.Errorf("diagnostic %q is missing %q", diagnostic, field)
		}
	}
}

func captureMailQueueFailure(t *testing.T, send func(context.Context) error) string {
	t.Helper()
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	queue := newMailTaskQueue()
	if err := queue.queue("diagnostic fixture", send); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := queue.shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func TestMailDiagnosticsACSRecipientsAndQueue(t *testing.T) {
	acsTestConfig(t)
	calls := 0
	statuses := []int{401, 429, 500, 202}
	mailTestHTTPClient(t, func(*http.Request) (*http.Response, error) {
		if calls >= len(statuses) {
			t.Error("ACS retried a delivery")
			return nil, errors.New(privateMailDiagnostic)
		}
		status := statuses[calls]
		calls++
		return &http.Response{StatusCode: status, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(privateMailDiagnostic))}, nil
	})
	var result error
	output := captureMailQueueFailure(t, func(ctx context.Context) error {
		result = SendMailContext(ctx, []string{
			"recipient-SENTINEL@example.test", "second@example.test", "third@example.test", "last@example.test",
		}, "subject", "body")
		return result
	})
	if result == nil || calls != len(statuses) {
		t.Fatalf("delivery result=%v, calls=%d", result, calls)
	}
	for index, category := range []string{"authentication", "rate_limited", "provider_failure"} {
		fields := []string{
			"provider=azure_communication_service phase=response category=" + category,
			fmt.Sprintf("status=%d recipient=%d", statuses[index], index+1),
		}
		assertSafeMailDiagnostic(t, result.Error(), fields...)
		assertSafeMailDiagnostic(t, output, fields...)
	}
	if strings.Contains(output, "recipient=4") || strings.Contains(output, "second@example.test") {
		t.Fatal("diagnostic reported successful delivery as failed or exposed a recipient")
	}
	var recipientError *mailRecipientError
	var providerError *mailProviderError
	if !errors.As(result, &recipientError) || recipientError.recipient != 1 ||
		!errors.As(result, &providerError) || providerError.status != 401 {
		t.Fatal("aggregate did not preserve typed recipient and provider errors")
	}
}

type mailFailureReader struct{ cause error }

func (reader mailFailureReader) Read([]byte) (int, error) { return 0, reader.cause }

func TestMailDiagnosticsACSTransportErrors(t *testing.T) {
	acsTestConfig(t)
	for _, phase := range []string{"request", "response", "canceled"} {
		t.Run(phase, func(t *testing.T) {
			cause := errors.New(privateMailDiagnostic)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			mailTestHTTPClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				if phase == "response" {
					return &http.Response{StatusCode: 202,
						Body: io.NopCloser(mailFailureReader{cause})}, nil
				}
				if phase == "canceled" {
					cancel()
				}
				return nil, &url.Error{Op: "POST", URL: privateMailDiagnostic, Err: cause}
			})
			result := SendMailContext(ctx, []string{"recipient-SENTINEL@example.test"}, "subject", "body")
			if result == nil || !errors.Is(result, cause) || calls != 1 {
				t.Fatalf("transport cause lost or delivery retried: result=%v, calls=%d", result, calls)
			}
			category, expectedPhase := "transport", phase
			if phase == "canceled" {
				category, expectedPhase = "canceled", "request"
				if !errors.Is(result, context.Canceled) {
					t.Fatal("cancellation identity was lost")
				}
			}
			if phase != "response" {
				var requestError *url.Error
				if !errors.As(result, &requestError) {
					t.Fatal("typed transport cause was lost")
				}
			}
			fields := []string{"provider=azure_communication_service", "phase=" + expectedPhase,
				"category=" + category, "recipient=1"}
			assertSafeMailDiagnostic(t, result.Error(), fields...)
			output := captureMailQueueFailure(t, func(context.Context) error {
				return fmt.Errorf("%s: %w", privateMailDiagnostic, result)
			})
			assertSafeMailDiagnostic(t, output, fields...)
		})
	}
}

func TestMailDiagnosticsSMTPStatuses(t *testing.T) {
	for _, test := range []struct {
		status          int
		phase, category string
	}{
		{421, "tls", "temporary_rejection"},
		{450, "submit", "temporary_rejection"},
		{535, "auth", "authentication"},
		{550, "submit", "permanent_rejection"},
	} {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			cause := &smtp.SMTPError{Code: test.status, Message: privateMailDiagnostic}
			failure := smtpFailure(context.Background(), test.phase,
				fmt.Errorf("%s: %w", privateMailDiagnostic, cause))
			result := &mailRecipientError{recipient: 2, cause: failure}
			var response *smtp.SMTPError
			if !errors.Is(result, cause) || !errors.As(result, &response) || response != cause {
				t.Fatal("SMTP error identity/type was lost")
			}
			fields := []string{"provider=smtp", "phase=" + test.phase, "category=" + test.category,
				fmt.Sprintf("status=%d recipient=2", test.status)}
			assertSafeMailDiagnostic(t, result.Error(), fields...)
			output := captureMailQueueFailure(t, func(context.Context) error {
				return errors.Join(errors.New(privateMailDiagnostic), result)
			})
			assertSafeMailDiagnostic(t, output, fields...)
		})
	}
}

func TestMailDiagnosticsSMTPWireRejection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
		done <- textproto.NewConn(connection).PrintfLine("421 %s", privateMailDiagnostic)
	}()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := sendSMTPMessage(ctx, mailerConfig{smtpHost: host, smtpPort: port,
		from: &mail.Address{Address: "sender@example.test"}}, "recipient-SENTINEL@example.test",
		[]byte("synthetic body"), &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var response *smtp.SMTPError
	if !errors.As(result, &response) || response.Code != 421 {
		t.Fatalf("SMTP wire status was lost: %v", result)
	}
	assertSafeMailDiagnostic(t, result.Error(),
		"provider=smtp phase=tls category=temporary_rejection status=421")
}

func TestMailDiagnosticsAllowlistAndContext(t *testing.T) {
	for _, test := range []struct {
		name, category string
		ctx            context.Context
		want           error
	}{
		{"canceled", "canceled", canceledMailTestContext(), context.Canceled},
		{"deadline", "timeout", expiredMailTestContext(), context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			cause := &smtp.SMTPError{Code: 535, Message: privateMailDiagnostic}
			result := smtpFailure(test.ctx, "auth", cause)
			if !errors.Is(result, test.want) || !errors.Is(result, cause) {
				t.Fatal("context or original SMTP cause was lost")
			}
			assertSafeMailDiagnostic(t, result.Error(), "category="+test.category, "status=535")
		})
	}
	result := &mailProviderError{
		provider: privateMailDiagnostic, phase: privateMailDiagnostic,
		category: privateMailDiagnostic, status: 999, cause: errors.New(privateMailDiagnostic),
	}
	assertSafeMailDiagnostic(t, result.Error(), "provider=unknown phase=unknown category=unknown")
	if strings.Contains(result.Error(), "status=") {
		t.Fatal("invalid provider status was surfaced")
	}
}

func canceledMailTestContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func expiredMailTestContext() context.Context {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	cancel()
	return ctx
}
