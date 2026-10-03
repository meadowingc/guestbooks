//go:build browser

package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"guestbook/constants"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

func submitBrowserForm(page *rod.Page, selector string) {
	submit := page.MustElement(selector + " [type='submit']")
	navigationPage, cancel := page.WithCancel()
	defer cancel()
	// WaitLoad alone can still observe the old document immediately after a click.
	wait := navigationPage.EachEvent(func(event *proto.PageFrameNavigated) bool {
		return event.Frame.ID == page.FrameID
	})
	submit.MustClick()
	wait()
	page.MustWaitLoad()
}

func TestNotificationStatusBrowserJourney(t *testing.T) {
	type delivery struct{ email, token string }
	deliveries := make(chan delivery, 4)
	var failDelivery atomic.Bool
	failDelivery.Store(true)
	testMailer(t, true, func(email, token string) error {
		deliveries <- delivery{email, token}
		if failDelivery.Load() {
			return errors.New("simulated mail provider failure")
		}
		return nil
	})
	user, book := featureFixture(t)
	book.RequiresApproval = true
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	page, base := featureBrowser(t)
	page.MustSetViewport(390, 844, 1, false)
	page.MustSetCookies(&proto.NetworkCookieParam{Name: string(AdminTokenCookieName), Value: user.SessionToken, URL: base})
	page.MustNavigate(base + fmt.Sprintf("/admin/guestbook/%d", book.ID)).MustWaitLoad()
	page.MustElement("#accept-cookies").MustClick()
	page.MustElement("[data-notification-state='missing-email'] a").MustClick()
	page.MustWaitLoad()
	page.MustElement("#email-settings-form input[name='email']").MustInput("browser-owner@example.test")
	page.MustElement("#notify").MustClick()
	submitBrowserForm(page, "#email-settings-form")
	page.MustElement("[data-notification-state='unverified']")
	if !strings.Contains(page.MustElement("[role='status']").MustText(), "Settings saved, but") {
		t.Fatal("settings did not explain the verification delivery failure")
	}
	var first delivery
	select {
	case first = <-deliveries:
	case <-time.After(5 * time.Second):
		t.Fatal("saving the email did not request verification")
	}
	if first.email != "browser-owner@example.test" || first.token == "" {
		t.Fatal("verification requested for the wrong address or without a token")
	}
	var saved AdminUser
	if err := db.First(&saved, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Email != first.email || saved.EmailVerificationToken != first.token || saved.EmailVerified || !saved.EmailNotifications {
		t.Fatal("failed email delivery lost the saved account settings")
	}
	failDelivery.Store(false)
	resendForm := "form[action='/admin/settings/resend-verification']"
	submitBrowserForm(page, resendForm)
	if !strings.Contains(page.MustElement("[role='status']").MustText(), "wait one minute") {
		t.Fatal("failed send did not reserve the verification cooldown")
	}
	if len(deliveries) != 0 {
		t.Fatal("cooldown after a failed send allowed another delivery request")
	}
	if err := db.Model(&saved).Update("verification_attempt_at", time.Now().Add(-2*time.Minute).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	submitBrowserForm(page, resendForm)
	page.MustElement("[data-notification-state='unverified']")
	if !strings.Contains(page.MustElement("[role='status']").MustText(), "Check your inbox") {
		t.Fatal("resend did not show its success notice")
	}
	page.MustWait(`() => {
		const notice = document.querySelector("#settings-notice").getBoundingClientRect();
		return notice.top >= 0 && notice.bottom <= innerHeight;
	}`)
	select {
	case resent := <-deliveries:
		if resent != first {
			t.Fatal("resend changed the pending token or recipient")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resend did not request verification")
	}
	submitBrowserForm(page, resendForm)
	if !strings.Contains(page.MustElement("[role='status']").MustText(), "wait one minute") {
		t.Fatal("resend cooldown was not visible")
	}
	if len(deliveries) != 0 {
		t.Fatal("cooldown allowed another delivery request")
	}
	page.MustNavigate(base + "/verify-email?token=" + url.QueryEscape(first.token)).MustWaitLoad()
	if page.MustElement("body").MustText() != "Email verified successfully!" {
		t.Fatal("following the verification link did not verify the email")
	}
	for _, path := range []string{"/admin/settings", fmt.Sprintf("/admin/guestbook/%d/edit", book.ID), fmt.Sprintf("/admin/guestbook/%d", book.ID)} {
		page.MustNavigate(base + path).MustWaitLoad()
		page.MustElement("[data-notification-state='enabled']")
		if page.MustHas(resendForm) {
			t.Fatal("verified account still offers email verification")
		}
	}
	page.MustNavigate(base + "/verify-email?token=" + url.QueryEscape(first.token)).MustWaitLoad()
	status := page.MustEval(`url => fetch(url).then(response=>response.status)`, base+"/verify-email?token="+url.QueryEscape(first.token)).Int()
	if status != http.StatusBadRequest || strings.Contains(page.MustElement("body").MustText(), "verified successfully") {
		t.Fatal("verification link was reusable")
	}
}

func TestSubmissionFeedbackBrowserRealPoW(t *testing.T) {
	_, book := featureFixture(t)
	book.PowEnabled = true
	book.CollectEmail = true
	book.EmailFieldLabel = "Email (optional)"
	book.SubmissionAction = SubmissionMessage
	book.SubmissionMessage = "Received."
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	page, base := featureBrowser(t)
	page = page.CancelTimeout().Timeout(90 * time.Second)
	page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
	waitForGuestbook(page)
	fillGuestbook(page)
	page.MustElement("input[name='email']").MustInput("real-worker@example.test")
	if !page.MustElement("input[type='submit']").MustProperty("disabled").Bool() {
		t.Fatal("submission was enabled before proof of work")
	}
	started := time.Now()
	page.MustElement("#guestbooks___pow-checkbox").MustClick()
	page.MustWait(`() => !document.querySelector("input[type='submit']").disabled`)
	challenge := page.MustElement("input[name='powChallenge']").MustProperty("value").Str()
	nonce := page.MustElement("input[name='powNonce']").MustProperty("value").Str()
	hash := sha256.Sum256([]byte(challenge + nonce))
	if challenge == "" || nonce == "" || !hasLeadingZeroBits(hash[:], constants.POW_DIFFICULTY) {
		t.Fatal("the browser worker did not produce a valid proof")
	}
	t.Logf("Real browser worker solved difficulty %d in %s", constants.POW_DIFFICULTY, time.Since(started).Round(time.Millisecond))
	clickGuestbookSubmit(page)
	page.MustWait(`() => document.querySelector("#guestbooks___success-message")?.textContent === "Received."`)
	if powChallengeStore.VerifyPow(challenge, nonce, book.ID) {
		t.Fatal("the saved submission did not consume its proof")
	}
	page.MustWait(`() => document.querySelector("input[type='submit']").disabled && !document.querySelector("#guestbooks___pow-checkbox").checked`)
	var message Message
	if err := db.Where("guestbook_id = ?", book.ID).First(&message).Error; err != nil {
		t.Fatal(err)
	}
	if message.Email == nil || *message.Email != "real-worker@example.test" {
		t.Fatal("real-worker submission did not store the private email")
	}
}

func TestSubmissionFeedbackBrowserNativeForm(t *testing.T) {
	for _, action := range []SubmissionAction{SubmissionUnchanged, SubmissionMessage, SubmissionRedirect} {
		t.Run("action="+string(action), func(t *testing.T) {
			_, book := featureFixture(t)
			book.RequiresApproval = true
			book.CollectEmail = true
			book.EmailFieldLabel = "Email (optional)"
			book.SubmissionAction = action
			book.SubmissionMessage = "Merci ! Votre message attend une approbation."
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprint(w, "<!doctype html><p id='thanks'>Received.</p>")
			}))
			defer destination.Close()
			book.SubmissionRedirectURL = destination.URL
			if err := db.Save(&book).Error; err != nil {
				t.Fatal(err)
			}
			page, base := featureBrowser(t)
			if err := (proto.EmulationSetScriptExecutionDisabled{Value: true}).Call(page); err != nil {
				t.Fatal(err)
			}
			page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID)).MustWaitLoad()
			// Rod's focus/input helpers wait on page-side animation frames.
			// Focus synchronously through DevTools, then use native text input.
			for _, field := range []struct{ selector, value string }{
				{"input[name='name']", "Native visitor"},
				{"textarea[name='text']", "Native message"},
				{"input[name='email']", "native-form@example.test"},
			} {
				page.MustEval(`selector => document.querySelector(selector).focus()`, field.selector)
				page.MustInsertText(field.value)
			}
			wait := page.MustWaitNavigation()
			page.MustEval(`() => document.querySelector("#guestbooks___guestbook-form").requestSubmit()`)
			wait()
			switch action {
			case SubmissionUnchanged:
				if page.MustHas("#guestbooks___success-message") {
					t.Fatal("disabled feedback appeared on native submission")
				}
			case SubmissionMessage:
				if page.MustElement("#guestbooks___success-message").MustText() != book.SubmissionMessage {
					t.Fatal("native submission lost its configured confirmation")
				}
			case SubmissionRedirect:
				page.MustElement("#thanks")
				if !strings.HasPrefix(page.MustInfo().URL, destination.URL) {
					t.Fatal("native submission did not navigate to the configured destination")
				}
			}
			var messages []Message
			if err := db.Where("guestbook_id = ?", book.ID).Find(&messages).Error; err != nil {
				t.Fatal(err)
			}
			if len(messages) != 1 || messages[0].Approved || messages[0].Email == nil || *messages[0].Email != "native-form@example.test" {
				t.Fatalf("unexpected native submission result: %+v", messages)
			}
		})
	}
}

func TestSubmissionFeedbackBrowserLegacyRedirectOverride(t *testing.T) {
	_, book := featureFixture(t)
	book.SubmissionAction = SubmissionMessage
	book.SubmissionMessage = "The hidden input should override this."
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><p id='override-thanks'>Received.</p>")
	}))
	defer destination.Close()
	page, base := featureBrowser(t)
	customSite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><form id="guestbooks___guestbook-form" action="%s/guestbook/%d/submit" method="post">
<input name="name" required><input id="website" name="website"><textarea name="text" required></textarea>
<input type="hidden" name="redirect_to_url" value="%s"><button type="submit">Send</button></form>
<div id="guestbooks___guestbook-messages-container"></div><script src="%s/resources/js/embed_script/%d/script.js"></script>`,
			base, book.ID, destination.URL, base, book.ID)
	}))
	defer customSite.Close()
	page.MustNavigate(customSite.URL)
	waitForGuestbook(page)
	fillGuestbook(page)
	clickGuestbookSubmit(page)
	page.MustElement("#override-thanks")
	if !strings.HasPrefix(page.MustInfo().URL, destination.URL) {
		t.Fatal("the copied embed's redirect did not override the configured confirmation")
	}
}

func TestPrivateEmailBrowserModerationJourney(t *testing.T) {
	user, book := featureFixture(t)
	book.CollectEmail = true
	book.EmailFieldLabel = "Private email (optional)"
	book.RequiresApproval = true
	book.SubmissionAction = SubmissionMessage
	book.SubmissionMessage = "Awaiting approval."
	book.ChallengeQuestion = "What color is the sky?"
	book.ChallengeAnswer = "blue"
	book.ChallengeFailedMessage = "Please try again."
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	page, base := featureBrowser(t)
	publicURL := base + fmt.Sprintf("/guestbook/%d", book.ID)
	page.MustNavigate(publicURL)
	waitForGuestbook(page)
	fillGuestbook(page)
	page.MustElement("input[name='email']").MustInput("moderation-private@example.test")
	page.MustElement("input[name='challengeQuestionAnswer']").MustInput("green")
	clickGuestbookSubmit(page)
	page.MustWait(`() => document.querySelector("#guestbooks___error-message").textContent === "Please try again."`)
	if page.MustElement("input[name='email']").MustProperty("value").Str() != "moderation-private@example.test" {
		t.Fatal("challenge rejection erased the email")
	}
	var count int64
	if err := db.Model(&Message{}).Where("guestbook_id = ?", book.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("challenge rejection created a message")
	}
	page.MustElement("input[name='challengeQuestionAnswer']").MustSelectAllText().MustInput("BLUE")
	clickGuestbookSubmit(page)
	page.MustWait(`() => document.querySelector("#guestbooks___success-message")?.textContent === "Awaiting approval."`)
	if strings.Contains(page.MustElement("#guestbooks___guestbook-messages-container").MustText(), "Browser message") {
		t.Fatal("pending message became public")
	}
	page.MustSetCookies(&proto.NetworkCookieParam{Name: string(AdminTokenCookieName), Value: user.SessionToken, URL: base})
	page.MustNavigate(base + fmt.Sprintf("/admin/guestbook/%d", book.ID)).MustWaitLoad()
	page.MustElement("#accept-cookies").MustClick()
	if page.MustElement("a[href^='mailto:']").MustText() != "moderation-private@example.test" {
		t.Fatal("the owner cannot read the private email")
	}
	page.MustElement(".message-checkbox").MustClick()
	page.MustElement("#bulk-approve-btn").MustClick()
	page.MustWait(`() => document.querySelector(".message-card .badge").textContent === "Approved"`)
	page.MustNavigate(publicURL)
	waitForGuestbook(page)
	page.MustWait(`() => document.querySelector("#guestbooks___guestbook-messages-container").textContent.includes("Browser message")`)
	if strings.Contains(page.MustElement("body").MustText(), "moderation-private@example.test") {
		t.Fatal("approval exposed the private email")
	}
}
