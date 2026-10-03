package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"guestbook/constants"

	"github.com/spf13/viper"
	"gorm.io/gorm"
)

func TestReplyCreationRechecksActiveAncestry(t *testing.T) {
	for _, removed := range []string{"message", "guestbook", "owner"} {
		t.Run(removed, func(t *testing.T) {
			user, book := featureFixture(t)
			parent := Message{Name: "Visitor", Text: "Original", GuestbookID: book.ID, Approved: true}
			if err := db.Create(&parent).Error; err != nil {
				t.Fatal(err)
			}
			callback := "regression:reply-ancestor-deleted"
			var once sync.Once
			if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if selected, ok := tx.Statement.Dest.(*Message); ok && selected.ID == parent.ID {
					once.Do(func() {
						var result *gorm.DB
						switch removed {
						case "message":
							result = db.Delete(&parent)
						case "guestbook":
							result = db.Delete(&book)
						case "owner":
							result = db.Delete(&user)
						}
						if result.Error != nil {
							t.Error(result.Error)
						}
					})
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Callback().Query().Remove(callback) })
			response := featureRequest(initRouter(), "POST",
				fmt.Sprintf("/admin/guestbook/%d/message/%d/reply", book.ID, parent.ID),
				url.Values{"text": {"Owner reply"}}, &user, false)
			requireStatus(t, response, http.StatusNotFound)
			var count int64
			if err := db.Unscoped().Model(&Message{}).Where("parent_message_id = ?", parent.ID).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("reply inserted after ancestor deletion")
			}
		})
	}
}

func TestConfigurationValidation(t *testing.T) {
	settings, previous := viper.AllSettings(), appConfig
	t.Cleanup(func() {
		viper.Reset()
		if err := viper.MergeConfigMap(settings); err != nil {
			t.Error(err)
		}
		appConfig = previous
	})
	for _, test := range []struct {
		key   string
		value any
	}{
		{"server.port", "not-a-port"}, {"server.port", 0}, {"server.port", 65536},
		{"server.public_url", "https://example.test/path"}, {"server.public_url", "https://user@example.test"},
		{"server.public_url", "https://example.test/?q=1"}, {"server.bind_host", "0.0.0.0:6235"},
		{"server.trusted_proxies", []string{"invalid"}}, {"mailer.mailer_name", "unknown"},
	} {
		t.Run(fmt.Sprint(test.key, test.value), func(t *testing.T) {
			viper.Reset()
			viper.Set(test.key, test.value)
			if err := initRuntimeConfig(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	viper.Reset()
	if err := initRuntimeConfig(); err != nil {
		t.Fatal(err)
	}
	if viper.GetString("mailer.mailer_name") != "none" || appConfig.Port != 6235 || len(appConfig.TrustedProxies) != 0 {
		t.Fatal("incorrect safe defaults")
	}
}

func TestPositiveIDContract(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "+1", "1-invalid", " 1", "4294967296"} {
		if id, err := parsePositiveID(value); err == nil {
			t.Fatalf("accepted %q as %d", value, id)
		}
	}
	for _, value := range []string{"1", "01", "0001"} {
		if id, err := parsePositiveID(value); err != nil || id != 1 {
			t.Fatalf("canonical ID failed for %q", value)
		}
	}
}

func TestProxyTrust(t *testing.T) {
	previous := appConfig
	t.Cleanup(func() { appConfig = previous })
	appConfig.TrustedProxies = []netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("10.0.0.0/24"),
	}
	for _, test := range []struct{ peer, header, want string }{
		{"198.51.100.1:99", "203.0.113.9", "198.51.100.1"},
		{"127.0.0.1:99", "203.0.113.9,10.0.0.1", "203.0.113.9"},
		{"127.0.0.1:99", "203.0.113.9, 10.0.0.1", "203.0.113.9"},
		{"127.0.0.1:99", "spoofed,198.51.100.1", "198.51.100.1"},
		{"127.0.0.1:99", "203.0.113.9,garbage", "127.0.0.1"},
		{"[::1]:99", "2001:db8::1", "2001:db8::1"},
		{"[::ffff:127.0.0.1]:99", "203.0.113.9", "203.0.113.9"},
		{"127.0.0.1:99", "", "127.0.0.1"},
	} {
		request := httptest.NewRequest("GET", "/", nil)
		request.RemoteAddr = test.peer
		request.Header.Set("X-Forwarded-For", test.header)
		if got := clientAddress(request).String(); got != test.want {
			t.Errorf("peer=%s header=%q: %s want %s", test.peer, test.header, got, test.want)
		}
	}
}

func TestAdminCSRFContract(t *testing.T) {
	previous := appConfig
	appConfig.PublicURL = "http://example.com"
	t.Cleanup(func() { appConfig = previous })
	handler := adminCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	initial := httptest.NewRecorder()
	handler.ServeHTTP(initial, httptest.NewRequest("GET", "http://example.com/form", nil))
	for _, test := range []struct {
		name, origin, referer string
		token                 bool
		status                int
	}{
		{"same origin", "http://example.com", "", true, 204},
		{"default origin port", "http://example.com:80", "", true, 204},
		{"case insensitive host", "http://EXAMPLE.COM", "", true, 204},
		{"missing headers with token", "", "", true, 204},
		{"missing token", "http://example.com", "", false, 403},
		{"missing all", "", "", false, 403},
		{"host suffix", "http://example.com.attacker.test", "", true, 403},
		{"foreign port", "http://example.com:123", "", true, 403},
		{"opaque origin", "null", "", true, 403},
		{"wrong referer", "", "http://attacker.test/example.com", true, 403},
		{"same referer", "", "http://example.com/form", true, 204},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "http://example.com/form", nil)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Referer", test.referer)
			for _, cookie := range initial.Result().Cookies() {
				request.AddCookie(cookie)
			}
			if test.token {
				request.Header.Set("X-CSRF-Token", initial.Header().Get("X-CSRF-Token"))
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			requireStatus(t, response, test.status)
		})
	}
}

func TestSubmissionRequiredFieldsAndLimits(t *testing.T) {
	for _, test := range []struct {
		name, text, website string
		status              int
	}{
		{"", "message", "", 400}, {" \t", "message", "", 400},
		{"name", "", "", 400}, {"name", " \n", "", 400},
		{"name", strings.Repeat("界", 2500), "", 201},
		{"name", strings.Repeat("🙂", 2500), "", 201},
		{"name", strings.Repeat("é", 2501), "", 400},
		{strings.Repeat("界", 200), "message", "", 201},
		{strings.Repeat("界", 201), "message", "", 400},
		{"name", "message", strings.Repeat("a", 2049), 400},
		{"name", string([]byte{0xff}), "", 400},
	} {
		_, book := featureFixture(t)
		form := url.Values{"name": {test.name}, "text": {test.text}, "website": {test.website}}
		response := featureRequest(initRouter(), "POST", fmt.Sprintf("/guestbook/%d/submit", book.ID), form, nil, true)
		requireStatus(t, response, test.status)
		var count int64
		if err := db.Model(&Message{}).Where("guestbook_id = ?", book.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if (count == 1) != (test.status == 201) {
			t.Fatal("validation and storage disagree")
		}
	}
	_, book := featureFixture(t)
	router := initRouter()
	form := url.Values{"name": {"name"}, "text": {"ok"}, "unused": {strings.Repeat("x", maxSubmissionBytes)}}
	requireStatus(t, featureRequest(router, "POST", fmt.Sprintf("/guestbook/%d/submit", book.ID), form, nil, true), 413)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writer.WriteField("name", "name")
	writer.WriteField("text", "ok")
	part, err := writer.CreateFormFile("file", "unused.txt")
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("unused"))
	writer.Close()
	request := httptest.NewRequest("POST", fmt.Sprintf("/guestbook/%d/submit", book.ID), &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	requireStatus(t, response, 400)
}

func TestCanonicalSubmissionRateLimit(t *testing.T) {
	_, book := featureFixture(t)
	router := initRouter()
	for i := 0; i < 6; i++ {
		id := strings.Repeat("0", i) + strconv.Itoa(int(book.ID))
		expected := 201
		if i == 5 {
			expected = 429
		}
		response := featureRequest(router, "POST", "/guestbook/"+id+"/submit",
			url.Values{"name": {"name"}, "text": {"message"}}, nil, true)
		requireStatus(t, response, expected)
	}
}

func TestChallengeConfigurationFailClosed(t *testing.T) {
	user, book := featureFixture(t)
	form := url.Values{"websiteURL": {book.WebsiteURL}, "challengeQuestion": {"Question"}, "challengeAnswer": {" "}}
	requireStatus(t, featureRequest(initRouter(), "POST", fmt.Sprintf("/admin/guestbook/%d/edit", book.ID), form, &user, false), 400)
	if err := db.Model(&book).Update("challenge_question", "Legacy broken question").Error; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, featureRequest(initRouter(), "POST", fmt.Sprintf("/guestbook/%d/submit", book.ID),
		url.Values{"name": {"name"}, "text": {"message"}}, nil, true), 503)
}

func TestProofConsumptionAndReplay(t *testing.T) {
	store := NewChallengeStore()
	challenge, err := store.GenerateChallenge(7)
	if err != nil {
		t.Fatal(err)
	}
	valid, invalid := "", ""
	for i := 0; valid == "" || invalid == ""; i++ {
		nonce := strconv.Itoa(i)
		hash := sha256.Sum256([]byte(challenge + nonce))
		if hasLeadingZeroBits(hash[:], constants.POW_DIFFICULTY) {
			valid = nonce
		} else {
			invalid = nonce
		}
	}
	if store.VerifyPow(challenge, invalid, 7) || store.VerifyPow(challenge, valid, 8) {
		t.Fatal("invalid proof accepted")
	}
	var winners atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if store.VerifyPow(challenge, valid, 7) {
				winners.Add(1)
			}
		}()
	}
	workers.Wait()
	if winners.Load() != 1 {
		t.Fatalf("proof winners=%d", winners.Load())
	}
	expired, err := store.GenerateChallenge(7)
	if err != nil {
		t.Fatal(err)
	}
	store.challenges[expired] = challengeEntry{guestbookID: 7, createdAt: time.Now().Add(-time.Hour)}
	if store.VerifyPow(expired, valid, 7) {
		t.Fatal("expired proof accepted")
	}
}
