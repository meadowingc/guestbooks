package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"guestbook/constants"

	"github.com/spf13/viper"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func featureFixture(t *testing.T) (AdminUser, Guestbook) {
	t.Helper()
	user := AdminUser{
		Username:     fmt.Sprintf("feature_%d", time.Now().UnixNano()),
		SessionToken: fmt.Sprintf("feature_token_%d", time.Now().UnixNano()),
		PasswordHash: []byte("test"),
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	book := Guestbook{WebsiteURL: "https://example.test", AdminUserID: user.ID}
	if err := db.Create(&book).Error; err != nil {
		t.Fatal(err)
	}
	return user, book
}

func featureRequest(handler http.Handler, method, target string, form url.Values, user *AdminUser, jsonResponse bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if jsonResponse {
		request.Header.Set("Accept", "application/json")
	}
	if user != nil {
		request.AddCookie(&http.Cookie{Name: string(AdminTokenCookieName), Value: user.SessionToken})
		request.Header.Set("Origin", "http://"+request.Host)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func requireStatus(t *testing.T, response *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if response.Code != expected {
		t.Fatalf("status = %d, want %d: %s", response.Code, expected, response.Body.String())
	}
}

func TestGuestbookSettingsMigration(t *testing.T) {
	migrationDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migration.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := migrationDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	for _, statement := range []string{
		"CREATE TABLE guestbooks (id integer PRIMARY KEY, website_url text, admin_user_id integer, requires_approval numeric, pow_enabled numeric)",
		"CREATE TABLE messages (id integer PRIMARY KEY, name text, text text, guestbook_id integer, approved numeric)",
		"INSERT INTO guestbooks VALUES (1, 'https://legacy.test', 1, 1, 0)",
		"INSERT INTO messages VALUES (1, 'Legacy visitor', 'Keep this message', 1, 0)",
	} {
		if err := migrationDB.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := migrationDB.AutoMigrate(&Guestbook{}, &Message{}, &AdminUser{}); err != nil {
		t.Fatal(err)
	}
	var book Guestbook
	var message Message
	if err := migrationDB.First(&book, 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrationDB.First(&message, 1).Error; err != nil {
		t.Fatal(err)
	}
	if book.CollectEmail || book.SubmissionAction != SubmissionUnchanged || !book.RequiresApproval {
		t.Fatalf("migration changed legacy behavior: %+v", book)
	}
	if message.Email != nil || message.Text != "Keep this message" {
		t.Fatalf("migration changed the message: %+v", message)
	}
	fresh := Guestbook{WebsiteURL: "https://new.test"}
	if err := migrationDB.Create(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.CollectEmail || fresh.SubmissionAction != SubmissionUnchanged {
		t.Fatal("new guestbooks must also opt out")
	}
}

func TestGuestbookSettingsValidation(t *testing.T) {
	valid := url.Values{
		"submissionAction": {"message"}, "submissionMessage": {strings.Repeat("界", maxConfirmationLength)},
		"emailFieldLabel": {strings.Repeat("é", maxEmailLabelLength)},
		"emailFieldHelp":  {strings.Repeat("界", maxEmailHelpLength)}, "collectEmail": {"on"},
	}
	for _, test := range []struct {
		name, field, value string
		valid              bool
	}{
		{"unicode bounds", "", "", true},
		{"long confirmation", "submissionMessage", strings.Repeat("界", maxConfirmationLength+1), false},
		{"long label", "emailFieldLabel", strings.Repeat("é", maxEmailLabelLength+1), false},
		{"long help", "emailFieldHelp", strings.Repeat("界", maxEmailHelpLength+1), false},
		{"missing label", "emailFieldLabel", " ", false},
		{"missing confirmation", "submissionMessage", " ", false},
		{"invalid utf8", "submissionMessage", string([]byte{0xff}), false},
		{"unknown action", "submissionAction", "something-else", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			form := url.Values{}
			for key, values := range valid {
				form[key] = append([]string(nil), values...)
			}
			if test.field != "" {
				form.Set(test.field, test.value)
			}
			request := httptest.NewRequest("POST", "/admin/guestbook/new", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			var book Guestbook
			err := readGuestbookOptions(request, &book)
			if (err == nil) != test.valid {
				t.Fatalf("validation error = %v, want valid=%v", err, test.valid)
			}
		})
	}
	user, book := featureFixture(t)
	router := initRouter()
	for _, path := range []string{"/admin/guestbook/new", fmt.Sprintf("/admin/guestbook/%d/edit", book.ID)} {
		response := featureRequest(router, "GET", path, nil, &user, false)
		requireStatus(t, response, 200)
		if !strings.Contains(response.Body.String(), "</html>") {
			body := response.Body.String()
			t.Fatalf("incomplete form at %s: %s", path, body[max(0, len(body)-500):])
		}
	}
	form := url.Values{
		"websiteURL": {book.WebsiteURL}, "submissionAction": {"message"}, "submissionMessage": {"Merci !"},
		"collectEmail": {"on"}, "emailFieldLabel": {"Courriel (facultatif)"}, "emailFieldHelp": {"Visible uniquement par le propriétaire."},
	}
	requireStatus(t, featureRequest(router, "POST", fmt.Sprintf("/admin/guestbook/%d/edit", book.ID), form, &user, false), 303)
	requireStatus(t, featureRequest(router, "POST", fmt.Sprintf("/admin/guestbook/%d/edit", book.ID),
		url.Values{"websiteURL": {"https://updated.test"}}, &user, false), 303)
	if err := db.First(&book, book.ID).Error; err != nil {
		t.Fatal(err)
	}
	if book.SubmissionMessage != "Merci !" || !book.CollectEmail {
		t.Fatal("an older form reset new settings")
	}
	form.Set("submissionMessage", "")
	requireStatus(t, featureRequest(router, "POST", fmt.Sprintf("/admin/guestbook/%d/edit", book.ID), form, &user, false), 400)
	form.Set("submissionMessage", "Danke!")
	requireStatus(t, featureRequest(router, "POST", "/admin/guestbook/new", form, &user, false), 303)
	var created Guestbook
	if err := db.Where("admin_user_id = ?", user.ID).Last(&created).Error; err != nil {
		t.Fatal(err)
	}
	if created.SubmissionMessage != "Danke!" || !created.CollectEmail {
		t.Fatal("create handler did not save the settings")
	}
}

func TestSubmissionFeedbackResponses(t *testing.T) {
	for _, test := range []struct {
		name                        string
		action                      SubmissionAction
		override                    string
		json, moderated             bool
		location, message, redirect string
	}{
		{name: "unchanged JSON", json: true},
		{name: "moderated confirmation", action: SubmissionMessage, json: true, moderated: true, message: "Merci <ami> !\nありがとう"},
		{name: "published confirmation", action: SubmissionMessage, json: true, message: "Merci <ami> !\nありがとう"},
		{name: "configured redirect", action: SubmissionRedirect, json: true, redirect: "https://thanks.test/done"},
		{name: "override confirmation", action: SubmissionMessage, override: "https://override.test/thanks", json: true, redirect: "https://override.test/thanks"},
		{name: "override redirect", action: SubmissionRedirect, override: "/thanks", json: true, redirect: "/thanks"},
		{name: "native unchanged"},
		{name: "native confirmation", action: SubmissionMessage, moderated: true, location: "?submitted=1"},
		{name: "native redirect", action: SubmissionRedirect, location: "https://thanks.test/done"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, book := featureFixture(t)
			book.SubmissionAction = test.action
			book.SubmissionMessage = "Merci <ami> !\nありがとう"
			book.SubmissionRedirectURL = "https://thanks.test/done"
			book.RequiresApproval = test.moderated
			if err := db.Save(&book).Error; err != nil {
				t.Fatal(err)
			}
			router := initRouter()
			response := featureRequest(router, "POST", fmt.Sprintf("/guestbook/%d/submit", book.ID),
				url.Values{"name": {"Visitor"}, "text": {"Hello"}, "redirect_to_url": {test.override}}, nil, test.json)
			if test.json {
				requireStatus(t, response, 201)
				var payload map[string]any
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				expectedRedirect := test.redirect
				if strings.HasPrefix(expectedRedirect, "/") {
					origin := PublicURL()
					if constants.DEBUG_MODE {
						origin = "http://example.com"
					}
					expectedRedirect = origin + expectedRedirect
				}
				if len(payload) != 3 || payload["success"] != true || payload["message"] != test.message || payload["redirectUrl"] != expectedRedirect {
					t.Fatalf("unexpected response: %s", response.Body.String())
				}
			} else {
				requireStatus(t, response, 303)
				location := test.location
				if !strings.HasPrefix(location, "https:") {
					location = fmt.Sprintf("/guestbook/%d", book.ID) + location
				}
				if response.Header().Get("Location") != location {
					t.Fatalf("redirect = %q, want %q", response.Header().Get("Location"), location)
				}
				if test.action == SubmissionMessage {
					page := featureRequest(router, "GET", location, nil, nil, false)
					requireStatus(t, page, 200)
					if !strings.Contains(page.Body.String(), "Merci &lt;ami&gt; !") {
						t.Fatal("native confirmation missing or not escaped")
					}
				}
			}
			var messages []Message
			if err := db.Where("guestbook_id = ?", book.ID).Find(&messages).Error; err != nil {
				t.Fatal(err)
			}
			if len(messages) != 1 || messages[0].Approved == test.moderated {
				t.Fatalf("wrong persistence/moderation result: %+v", messages)
			}
		})
	}
}

func TestSubmissionFeedbackRedirectValidation(t *testing.T) {
	request := httptest.NewRequest("POST", "http://example.com/guestbook/42/submit", nil)
	for _, test := range []struct {
		value           string
		relative, valid bool
		want            string
	}{
		{"https://thanks.test/ok", false, true, "https://thanks.test/ok"},
		{"/thanks", true, true, "http://example.com/thanks"},
		{"thanks", true, true, "http://example.com/guestbook/42/thanks"},
		{"//thanks.test/ok", true, true, "http://thanks.test/ok"},
		{"/thanks", false, false, ""},
		{"javascript:alert(1)", true, false, ""},
		{"data:text/html,hi", true, false, ""},
		{"https://user:pass@thanks.test", false, false, ""},
		{"https://", false, false, ""},
		{"https://thanks.test/\nnext", false, false, ""},
		{"https://thanks.test/" + strings.Repeat("a", maxRedirectBytes-len("https://thanks.test/")), false, true, ""},
		{"https://thanks.test/" + strings.Repeat("a", maxRedirectBytes-len("https://thanks.test/")+1), false, false, ""},
	} {
		got, err := validatedRedirect(request, test.value, test.relative)
		if (err == nil) != test.valid || (test.want != "" && got != test.want) {
			t.Errorf("redirect %q: got %q, %v", test.value, got, err)
		}
	}
	_, book := featureFixture(t)
	router := initRouter()
	for _, target := range []string{"javascript:alert(1)", "https://bad.test/" + strings.Repeat("x", 2048)} {
		response := featureRequest(router, "POST", fmt.Sprintf("/guestbook/%d/submit", book.ID),
			url.Values{"name": {"Visitor"}, "text": {"Do not save"}, "redirect_to_url": {target}}, nil, true)
		requireStatus(t, response, 400)
	}
	var count int64
	db.Model(&Message{}).Where("guestbook_id = ?", book.ID).Count(&count)
	if count != 0 {
		t.Fatal("invalid redirect stored a message")
	}
}

func TestPrivateEmailValidation(t *testing.T) {
	atLimit := strings.Repeat("a", 64) + "@" + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	if len(atLimit) != maxEmailBytes {
		t.Fatal("incorrect boundary fixture")
	}
	for _, test := range []struct {
		value string
		valid bool
	}{
		{"", true}, {"  ", true}, {"  Visitor+tag@example.test  ", true}, {atLimit, true},
		{"a" + atLimit, false}, {"invalid", false}, {"Name <visitor@example.test>", false},
		{"a@example.test,b@example.test", false}, {"a@example.test\r\nBcc: b@example.test", false},
	} {
		address, err := optionalEmail(test.value)
		if (err == nil) != test.valid {
			t.Errorf("validation of %q: %v", test.value, err)
		}
		if address != nil && *address != strings.TrimSpace(test.value) {
			t.Error("email changed beyond trimming")
		}
	}
	address := "visitor?subject=hidden+tag@example.test"
	message := Message{Email: &address}
	if strings.ContainsAny(message.EmailLink(), "?&") || !strings.Contains(message.EmailLink(), "%3F") {
		t.Fatalf("unsafe mailto: %s", message.EmailLink())
	}
}

func TestPrivateEmailPrivacyAndLifecycle(t *testing.T) {
	user, book := featureFixture(t)
	book.CollectEmail = true
	book.EmailFieldLabel = "Courriel (facultatif)"
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	router := initRouter()
	submitURL := fmt.Sprintf("/guestbook/%d/submit", book.ID)
	privateAddress := "private-sentinel+reply@example.test"
	requireStatus(t, featureRequest(router, "POST", submitURL,
		url.Values{"name": {"Visitor"}, "text": {"Public text"}, "email": {" " + privateAddress + " "}}, nil, true), 201)
	var message Message
	if err := db.Where("guestbook_id = ?", book.ID).First(&message).Error; err != nil {
		t.Fatal(err)
	}
	if message.Email == nil || *message.Email != privateAddress {
		t.Fatal("private email was not stored")
	}
	reply := Message{Name: "Reply", Text: "Public reply", Approved: true, GuestbookID: book.ID, ParentMessageID: &message.ID, Email: &privateAddress}
	if err := db.Create(&reply).Error; err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"v1", "v2"} {
		for _, cacheState := range []string{"MISS", "HIT"} {
			response := featureRequest(router, "GET", fmt.Sprintf("/api/%s/get-guestbook-messages/%d", version, book.ID), nil, nil, false)
			requireStatus(t, response, 200)
			if response.Header().Get("X-Cache") != cacheState {
				t.Fatalf("expected %s response", cacheState)
			}
			body := response.Body.String()
			for _, forbidden := range []string{privateAddress, `"Email"`, `"CollectEmail"`, `"SubmissionAction"`, `"EmailFieldLabel"`} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("%s exposed %q", version, forbidden)
				}
			}
			if !strings.Contains(body, "Public text") || !strings.Contains(body, "Public reply") {
				t.Fatal("public content disappeared")
			}
		}
	}
	for _, path := range []string{
		fmt.Sprintf("/guestbook/%d", book.ID),
		fmt.Sprintf("/resources/js/embed_script/%d/script.js", book.ID),
	} {
		response := featureRequest(router, "GET", path, nil, nil, false)
		requireStatus(t, response, 200)
		if strings.Contains(html.UnescapeString(response.Body.String()), privateAddress) {
			t.Fatal("public page/script exposed email")
		}
	}
	editURL := fmt.Sprintf("/admin/guestbook/%d/message/%d/edit", book.ID, message.ID)
	for _, path := range []string{fmt.Sprintf("/admin/guestbook/%d", book.ID), editURL} {
		response := featureRequest(router, "GET", path, nil, &user, false)
		requireStatus(t, response, 200)
		body := html.UnescapeString(response.Body.String())
		if !strings.Contains(body, privateAddress) || !strings.Contains(body, message.EmailLink()) {
			t.Fatal("owner cannot see private email/mailto")
		}
		other, _ := featureFixture(t)
		requireStatus(t, featureRequest(router, "GET", path, nil, &other, false), 401)
		requireStatus(t, featureRequest(router, "GET", path, nil, nil, false), 303)
	}
	requireStatus(t, featureRequest(router, "POST", editURL,
		url.Values{"name": {"Visitor"}, "text": {"Edited"}, "isApproved": {"on"}}, &user, false), 303)
	requireStatus(t, featureRequest(router, "POST", fmt.Sprintf("/admin/guestbook/%d/message/%d/reply", book.ID, message.ID),
		url.Values{"text": {"Owner's reply"}}, &user, false), 303)
	var ownerReply Message
	if err := db.Where("guestbook_id = ? AND text = ?", book.ID, "Owner's reply").First(&ownerReply).Error; err != nil {
		t.Fatal(err)
	}
	if ownerReply.Email != nil {
		t.Fatal("reply inherited private email")
	}
	if err := db.Model(&book).Update("collect_email", false).Error; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, featureRequest(router, "POST", submitURL,
		url.Values{"name": {"Stale embed"}, "text": {"Still accepted"}, "email": {"invalid and discarded"}}, nil, true), 201)
	var latest Message
	if err := db.Where("guestbook_id = ?", book.ID).Last(&latest).Error; err != nil {
		t.Fatal(err)
	}
	if latest.Email != nil {
		t.Fatal("disabled collection saved a new email")
	}
	if err := db.First(&message, message.ID).Error; err != nil {
		t.Fatal(err)
	}
	if message.Email == nil || *message.Email != privateAddress {
		t.Fatal("editing/disabling collection lost the existing email")
	}
}

func TestPrivateEmailRejectedBeforeSave(t *testing.T) {
	_, book := featureFixture(t)
	book.CollectEmail = true
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	router := initRouter()
	target := fmt.Sprintf("/guestbook/%d/submit", book.ID)
	requireStatus(t, featureRequest(router, "POST", target,
		url.Values{"name": {"Visitor"}, "text": {"Invalid"}, "email": {"not an email"}}, nil, true), 400)
	var count int64
	db.Model(&Message{}).Where("guestbook_id = ?", book.ID).Count(&count)
	if count != 0 {
		t.Fatal("invalid email saved a message")
	}
	requireStatus(t, featureRequest(router, "POST", target,
		url.Values{"name": {"Visitor"}, "text": {"Optional email omitted"}}, nil, true), 201)
}

func TestSubmissionFeedbackNotificationFailure(t *testing.T) {
	_, book := featureFixture(t)
	book.CollectEmail = true
	book.SubmissionAction = SubmissionMessage
	book.SubmissionMessage = "Received."
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	previous := sendMessageNotification
	t.Cleanup(func() { sendMessageNotification = previous })
	sendMessageNotification = func(Guestbook, Message, AdminUser) error {
		return errors.New("fake notification preparation failure")
	}
	response := featureRequest(initRouter(), "POST", fmt.Sprintf("/guestbook/%d/submit", book.ID),
		url.Values{"name": {"Visitor"}, "text": {"Public message"}, "email": {"private@example.test"}}, nil, true)
	requireStatus(t, response, 201)
	var feedback submissionFeedback
	if err := json.Unmarshal(response.Body.Bytes(), &feedback); err != nil {
		t.Fatal(err)
	}
	if !feedback.Success || feedback.Message != "Received." || strings.Contains(response.Body.String(), "private@example.test") {
		t.Fatal("saved-message acknowledgement lost or private data returned")
	}
	var message Message
	if err := db.Where("guestbook_id = ?", book.ID).First(&message).Error; err != nil {
		t.Fatal(err)
	}
	body, err := messageNotificationBody(book, message)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "private@example.test") || !strings.Contains(body, "Public message") {
		t.Fatal("notification body leaked email or lost the message")
	}
}

func TestPrivateEmailStorageFailure(t *testing.T) {
	_, book := featureFixture(t)
	book.CollectEmail = true
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_feature_message BEFORE INSERT ON messages
		WHEN NEW.text = 'simulate-storage-failure' BEGIN SELECT RAISE(ABORT, 'simulated storage failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Exec("DROP TRIGGER fail_feature_message").Error; err != nil {
			t.Error(err)
		}
	})
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	response := featureRequest(initRouter(), "POST", fmt.Sprintf("/guestbook/%d/submit", book.ID),
		url.Values{"name": {"Visitor"}, "text": {"simulate-storage-failure"}, "email": {"never-log-this@example.test"}}, nil, true)
	requireStatus(t, response, 500)
	if strings.Contains(logs.String(), "never-log-this@example.test") {
		t.Fatal("database failure logged a private email address")
	}
	var count int64
	if err := db.Model(&Message{}).Where("guestbook_id = ?", book.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed insert created a message")
	}
}

func TestVerificationResendLegacyNullSettings(t *testing.T) {
	user, _ := featureFixture(t)
	if err := db.Model(&user).Updates(map[string]any{"email": nil, "email_verified": nil}).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	testMailer(t, true, func(email, token string) error {
		calls++
		if email != "legacy-owner@example.test" || token == "" {
			t.Fatal("wrong recipient or token for legacy account")
		}
		return nil
	})
	router := initRouter()
	requireStatus(t, featureRequest(router, "POST", "/admin/settings",
		url.Values{"settings_section": {"email"}, "email": {"legacy-owner@example.test"}, "notify": {"on"}}, &user, false), 303)
	if err := db.Model(&user).Updates(map[string]any{"email_verified": nil, "email_verification_token": nil}).Error; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, featureRequest(router, "POST", "/admin/settings/resend-verification", nil, &user, false), 303)
	if calls != 2 {
		t.Fatalf("expected initial and resend email requests, got %d", calls)
	}
}

func testMailer(t *testing.T, enabled bool, sender func(string, string) error) {
	t.Helper()
	previousMailer := viper.GetString("mailer.mailer_name")
	previousSender := sendVerificationEmail
	if enabled {
		viper.Set("mailer.mailer_name", "smtp")
	} else {
		viper.Set("mailer.mailer_name", "none")
	}
	sendVerificationEmail = sender
	t.Cleanup(func() {
		viper.Set("mailer.mailer_name", previousMailer)
		sendVerificationEmail = previousSender
	})
}

func TestNotificationStatus(t *testing.T) {
	testMailer(t, true, func(string, string) error { t.Fatal("unexpected email"); return nil })
	user, book := featureFixture(t)
	book.RequiresApproval = true
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		state, email             string
		verified, notify, resend bool
	}{
		{"missing-email", "", false, false, false},
		{"unverified", "owner@example.test", false, false, true},
		{"unverified", "owner@example.test", false, true, true},
		{"disabled", "owner@example.test", true, false, false},
		{"enabled", "owner@example.test", true, true, false},
	} {
		user.Email, user.EmailVerified, user.EmailNotifications = test.email, test.verified, test.notify
		if err := db.Save(&user).Error; err != nil {
			t.Fatal(err)
		}
		status := notificationStatusFor(&user)
		if status.State != test.state || status.CanResend != test.resend {
			t.Fatalf("wrong status: %+v", status)
		}
		for _, path := range []string{"/admin/settings", fmt.Sprintf("/admin/guestbook/%d/edit", book.ID), fmt.Sprintf("/admin/guestbook/%d", book.ID)} {
			response := featureRequest(initRouter(), "GET", path, nil, &user, false)
			requireStatus(t, response, 200)
			if !strings.Contains(response.Body.String(), `data-notification-state="`+test.state+`"`) {
				t.Fatalf("missing status on %s", path)
			}
		}
	}
	viper.Set("mailer.mailer_name", "none")
	if notificationStatusFor(&user).State != "unavailable" {
		t.Fatal("disabled mailer claimed notifications were enabled")
	}
}

func TestVerificationResend(t *testing.T) {
	calls := 0
	testMailer(t, true, func(email, token string) error {
		calls++
		var stored AdminUser
		if err := db.Where("email = ?", email).First(&stored).Error; err != nil {
			t.Fatal(err)
		}
		if token == "" || stored.EmailVerificationToken != token || stored.EmailVerified {
			t.Fatal("verification was sent before the token was saved")
		}
		return nil
	})
	user, _ := featureFixture(t)
	user.Email = "resend-owner@example.test"
	user.EmailVerificationToken = "existing-pending-token"
	if err := db.Save(&user).Error; err != nil {
		t.Fatal(err)
	}
	router := initRouter()
	path := "/admin/settings/resend-verification"
	requireStatus(t, featureRequest(router, "POST", path, nil, nil, false), 303)
	requireStatus(t, featureRequest(router, "POST", path, url.Values{"email": {"not-the-owner@example.test"}}, &user, false), 303)
	response := featureRequest(router, "POST", path, nil, &user, false)
	requireStatus(t, response, 429)
	if response.Header().Get("Retry-After") == "" || calls != 1 {
		t.Fatal("resend was not limited")
	}
	other, _ := featureFixture(t)
	other.Email = "other-resend-owner@example.test"
	if err := db.Save(&other).Error; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, featureRequest(router, "POST", path, nil, &other, false), 303)
	if calls != 2 {
		t.Fatal("resend limit was not per account")
	}
	if err := db.First(&user, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if user.EmailVerificationToken != "existing-pending-token" {
		t.Fatal("resend rotated a pending token")
	}
	verifyURL := "/verify-email?token=" + user.EmailVerificationToken
	requireStatus(t, featureRequest(router, "GET", verifyURL, nil, nil, false), 200)
	requireStatus(t, featureRequest(router, "GET", verifyURL, nil, nil, false), 400)
}

func TestVerificationResendFailures(t *testing.T) {
	for _, test := range []struct {
		name                    string
		email                   string
		verified, mailer, fails bool
		status, calls           int
	}{
		{"no email", "", false, true, false, 400, 0},
		{"already verified", "verified@example.test", true, true, false, 409, 0},
		{"mailer disabled", "disabled@example.test", false, false, false, 503, 0},
		{"send failure", "failure@example.test", false, true, true, 502, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			testMailer(t, test.mailer, func(string, string) error {
				calls++
				if test.fails {
					return errors.New("fake delivery failure")
				}
				return nil
			})
			user, _ := featureFixture(t)
			user.Email, user.EmailVerified = test.email, test.verified
			if err := db.Save(&user).Error; err != nil {
				t.Fatal(err)
			}
			response := featureRequest(initRouter(), "POST", "/admin/settings/resend-verification", nil, &user, false)
			requireStatus(t, response, test.status)
			if calls != test.calls {
				t.Fatalf("send calls = %d", calls)
			}
		})
	}
}

func TestVerificationResendEmailChanges(t *testing.T) {
	user, _ := featureFixture(t)
	user.Email = "old-owner@example.test"
	user.EmailVerificationToken = "old-owner-token"
	user.DisplayName = "Keep my display name"
	if err := db.Save(&user).Error; err != nil {
		t.Fatal(err)
	}

	testMailer(t, true, func(email, token string) error {
		var saved AdminUser
		if err := db.First(&saved, user.ID).Error; err != nil {
			t.Fatal(err)
		}
		if saved.Email != email || saved.EmailVerificationToken != token || saved.EmailVerified {
			t.Fatal("new account email/token was not saved before sending")
		}
		return errors.New("fake delivery failure")
	})
	router := initRouter()
	response := featureRequest(router, "POST", "/admin/settings",
		url.Values{"settings_section": {"email"}, "email": {"new-owner@example.test"}, "notify": {"on"}}, &user, false)
	requireStatus(t, response, 502)
	if !strings.Contains(response.Body.String(), "Settings saved, but") {
		t.Fatal("send failure not explained")
	}
	requireStatus(t, featureRequest(router, "GET", "/verify-email?token=old-owner-token", nil, nil, false), 400)
	if err := db.First(&user, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if user.Email != "new-owner@example.test" || user.EmailVerified || !user.EmailNotifications || user.DisplayName != "Keep my display name" {
		t.Fatal("wrong saved settings after failed send")
	}
	token := user.EmailVerificationToken
	requireStatus(t, featureRequest(router, "POST", "/admin/settings",
		url.Values{"settings_section": {"display_name"}, "display_name": {"Updated display"}, "email": {"stale@example.test"}}, &user, false), 303)
	if err := db.First(&user, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if user.EmailVerificationToken != token || user.Email != "new-owner@example.test" || !user.EmailNotifications {
		t.Fatal("display-name form changed email settings")
	}
}

func TestVerificationResendStorageFailure(t *testing.T) {
	user, _ := featureFixture(t)
	user.Email = "token-storage@example.test"
	if err := db.Save(&user).Error; err != nil {
		t.Fatal(err)
	}
	testMailer(t, true, func(string, string) error {
		t.Fatal("sent verification without saving its token")
		return nil
	})
	if err := db.Exec(fmt.Sprintf(`CREATE TRIGGER fail_feature_token BEFORE UPDATE OF email_verification_token ON admin_users
		WHEN NEW.id = %d BEGIN SELECT RAISE(ABORT, 'simulated token storage failure'); END`, user.ID)).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Exec("DROP TRIGGER fail_feature_token").Error; err != nil {
			t.Error(err)
		}
	})
	requireStatus(t, featureRequest(initRouter(), "POST", "/admin/settings/resend-verification", nil, &user, false), 500)
}
