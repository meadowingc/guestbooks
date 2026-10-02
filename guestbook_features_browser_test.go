package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"guestbook/constants"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

func featureBrowser(t *testing.T) (*rod.Page, string) {
	t.Helper()
	server := httptest.NewServer(initRouter())
	t.Cleanup(server.Close)
	instance := browser.MustIncognito()
	t.Cleanup(instance.MustClose)
	return instance.MustPage().Timeout(30 * time.Second), server.URL
}

func waitForGuestbook(page *rod.Page) {
	page.MustWait(`() => document.querySelector("#guestbooks___guestbook-messages-container").textContent.length > 0`)
}

func fillGuestbook(page *rod.Page) {
	page.MustElement("#guestbooks___guestbook-form input[name='name']").MustInput("Browser visitor")
	page.MustElement("#guestbooks___guestbook-form textarea[name='text']").MustInput("Browser message")
}

func clickGuestbookSubmit(page *rod.Page) {
	page.MustElement("#guestbooks___guestbook-form input[type='submit'], #guestbooks___guestbook-form button[type='submit']").MustClick()
}

func TestSubmissionFeedbackBrowser(t *testing.T) {
	for _, moderated := range []bool{false, true} {
		t.Run(fmt.Sprintf("moderated=%v", moderated), func(t *testing.T) {
			_, book := featureFixture(t)
			book.RequiresApproval = moderated
			book.SubmissionAction = SubmissionMessage
			book.SubmissionMessage = "Danke! <script>alert('x')</script>\nありがとう 😺"
			if err := db.Save(&book).Error; err != nil {
				t.Fatal(err)
			}
			page, base := featureBrowser(t)
			page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
			waitForGuestbook(page)
			for i := 0; i < 2; i++ {
				fillGuestbook(page)
				clickGuestbookSubmit(page)
				page.MustWait(`message => document.querySelector("#guestbooks___success-message")?.textContent === message`, book.SubmissionMessage)
				if page.MustHas("#guestbooks___success-message script") {
					t.Fatal("confirmation was interpreted as HTML")
				}
				if page.MustElement("input[name='name']").MustProperty("value").Str() != "" {
					t.Fatal("successful submission did not reset the form")
				}
			}
			if moderated {
				if strings.Contains(page.MustElement("#guestbooks___guestbook-messages-container").MustText(), "Browser message") {
					t.Fatal("pending message became public")
				}
			} else {
				page.MustWait(`() => document.querySelector("#guestbooks___guestbook-messages-container").textContent.includes("Browser message")`)
			}
		})
	}
}

func TestSubmissionFeedbackBrowserRedirect(t *testing.T) {
	for _, iframe := range []bool{false, true} {
		t.Run(fmt.Sprintf("iframe=%v", iframe), func(t *testing.T) {
			navigationModes := make(chan string, 4)
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/thanks" {
					navigationModes <- r.Header.Get("Sec-Fetch-Mode")
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprint(w, "<!doctype html><p id='thanks'>Merci!</p>")
			}))
			defer destination.Close()
			_, book := featureFixture(t)
			book.SubmissionAction = SubmissionRedirect
			book.SubmissionRedirectURL = destination.URL + "/thanks"
			if err := db.Save(&book).Error; err != nil {
				t.Fatal(err)
			}
			page, base := featureBrowser(t)
			publicURL := base + fmt.Sprintf("/guestbook/%d", book.ID)
			formPage := page
			parentURL := ""
			if iframe {
				parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprintf(w, "<!doctype html><iframe src=%q style='width:1000px;height:800px'></iframe>", publicURL)
				}))
				defer parent.Close()
				parentURL = parent.URL + "/"
				page.MustNavigate(parentURL).MustWaitLoad()
				formPage = page.MustElement("iframe").MustFrame().Timeout(20 * time.Second)
			} else {
				page.MustNavigate(publicURL)
			}
			waitForGuestbook(formPage)
			fillGuestbook(formPage)
			clickGuestbookSubmit(formPage)
			formPage.MustElement("#thanks")
			if iframe && page.MustEval(`() => location.href`).Str() != parentURL {
				t.Fatal("redirect navigated the containing website")
			}
			select {
			case mode := <-navigationModes:
				if mode != "navigate" {
					t.Fatalf("thank-you page fetched as %q instead of navigation", mode)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no navigation to thank-you page")
			}
		})
	}
}

func TestPrivateEmailBrowserGeneratedEmbed(t *testing.T) {
	user, book := featureFixture(t)
	book.CollectEmail = true
	book.EmailFieldLabel = `Email "<input id=bad>" & ありがとう`
	book.EmailFieldHelp = "Private <script>no</script>\nSeulement pour le propriétaire."
	book.SubmissionAction = SubmissionMessage
	book.SubmissionMessage = "Merci !"
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	page, base := featureBrowser(t)
	page.MustSetCookies(&proto.NetworkCookieParam{Name: string(AdminTokenCookieName), Value: user.SessionToken, URL: base})
	page.MustNavigate(base + fmt.Sprintf("/admin/guestbook/%d/embed", book.ID)).MustWaitLoad()
	snippet := page.MustElement("#js-code").MustText()
	customSite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><body>"+snippet+"</body>")
	}))
	defer customSite.Close()
	page.MustNavigate(customSite.URL)
	waitForGuestbook(page)
	if got := page.MustElement("label[for='email']").MustText(); got != book.EmailFieldLabel {
		t.Fatalf("label did not survive copying: %q", got)
	}
	if page.MustHas("#bad") || page.MustHas("#guestbooks___email-help script") {
		t.Fatal("email label/help became executable markup")
	}
	if page.MustElement("input[name='email']").MustProperty("required").Bool() {
		t.Fatal("visitor email must be optional")
	}
	originalLabel, originalHelp := book.EmailFieldLabel, book.EmailFieldHelp
	book.EmailFieldLabel = "New label"
	book.EmailFieldHelp = "New help"
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	page.MustNavigate(customSite.URL)
	waitForGuestbook(page)
	if page.MustElement("label[for='email']").MustText() != originalLabel ||
		page.MustEval(`() => document.querySelector("#guestbooks___email-help").textContent`).Str() != originalHelp {
		t.Fatal("dashboard changes overwrote owner-controlled copied email markup")
	}
	fillGuestbook(page)
	page.MustElement("input[name='email']").MustInput("browser-private@example.test")
	clickGuestbookSubmit(page)
	page.MustWait(`() => document.querySelector("#guestbooks___success-message")?.textContent === "Merci !"`)
	if strings.Contains(page.MustElement("#guestbooks___guestbook-messages-container").MustText(), "browser-private@example.test") {
		t.Fatal("email appeared in public messages")
	}
	var message Message
	if err := db.Where("guestbook_id = ?", book.ID).First(&message).Error; err != nil {
		t.Fatal(err)
	}
	if message.Email == nil || *message.Email != "browser-private@example.test" {
		t.Fatal("copied email field did not submit")
	}
	book.CollectEmail = false
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	fillGuestbook(page)
	page.MustElement("input[name='email']").MustInput("discarded@example.test")
	clickGuestbookSubmit(page)
	page.MustWait(`() => document.querySelector("input[name='name']").value === ""`)
	var latest Message
	if err := db.Where("guestbook_id = ?", book.ID).Last(&latest).Error; err != nil {
		t.Fatal(err)
	}
	if latest.ID == message.ID || latest.Email != nil {
		t.Fatal("stale copied field did not submit successfully without saving its email")
	}
	if err := db.First(&message, message.ID).Error; err != nil {
		t.Fatal(err)
	}
	if message.Email == nil || *message.Email != "browser-private@example.test" {
		t.Fatal("disabling collection erased a historical address")
	}
}

func TestPrivateEmailBrowserResponsiveLayout(t *testing.T) {
	user, book := featureFixture(t)
	book.CollectEmail = true
	book.EmailFieldLabel = "Email (optional)"
	book.EmailFieldHelp = "Only the guestbook owner can see your email address."
	book.SubmissionAction = SubmissionMessage
	book.SubmissionMessage = "Merci !"
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	email := strings.Repeat("a", 64) + "@" + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 57) + ".org"
	if len(email) != maxEmailBytes {
		t.Fatal("layout fixture must cover the maximum email length")
	}
	message := Message{GuestbookID: book.ID, Name: "A visitor with a long address", Text: "Please reply privately.", Email: &email}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{320, 390, 1440} {
		t.Run(fmt.Sprintf("width=%d", width), func(t *testing.T) {
			page, base := featureBrowser(t)
			page.MustSetViewport(width, 1000, 1, false)
			page.MustSetCookies(&proto.NetworkCookieParam{Name: string(AdminTokenCookieName), Value: user.SessionToken, URL: base})
			for _, route := range []string{
				fmt.Sprintf("/admin/guestbook/%d", book.ID),
				fmt.Sprintf("/admin/guestbook/%d/message/%d/edit", book.ID, message.ID),
				fmt.Sprintf("/admin/guestbook/%d/edit", book.ID),
				fmt.Sprintf("/admin/guestbook/%d/embed", book.ID),
			} {
				page.MustNavigate(base + route).MustWaitLoad()
				if strings.HasSuffix(route, "/embed") {
					page.MustElementR("button", "Custom JavaScript").MustClick()
				}
				page.MustWait(`() => Array.from(document.querySelectorAll(".fade-in")).every(el => getComputedStyle(el).opacity === "1")`)
				if page.MustEval(`() => document.documentElement.scrollWidth > innerWidth`).Bool() {
					t.Fatalf("%s overflows at %dpx", route, width)
				}
				if strings.HasSuffix(route, "/embed") && page.MustEval(`() => getComputedStyle(document.querySelector("#js-code")).color === getComputedStyle(document.querySelector(".code-section")).backgroundColor`).Bool() {
					t.Fatal("embed code is unreadable without the syntax-highlighting CDN")
				}
			}
		})
	}
}

func TestSubmissionFeedbackBrowserDefaultAppearance(t *testing.T) {
	_, book := featureFixture(t)
	book.SubmissionAction = SubmissionMessage
	book.SubmissionMessage = strings.Repeat("x", maxConfirmationLength)
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	page, base := featureBrowser(t)
	page.MustSetViewport(320, 844, 1, false)
	page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
	waitForGuestbook(page)
	if page.MustHas("#guestbooks___success-message") {
		t.Fatal("confirmation should only be inserted when a message is shown")
	}
	fillGuestbook(page)
	clickGuestbookSubmit(page)
	page.MustWait(`() => document.querySelector("#guestbooks___success-message")?.textContent.length === 2000`)
	if page.MustEval(`() => document.documentElement.scrollWidth > innerWidth`).Bool() {
		t.Fatal("long confirmation overflows the default mobile layout")
	}
	if !page.MustEval(`() => {
		const style = getComputedStyle(document.querySelector("#guestbooks___success-message"));
		return style.borderInlineStartStyle === "solid" && style.backgroundColor !== "rgba(0, 0, 0, 0)";
	}`).Bool() {
		t.Fatal("default confirmation lacks visible feedback styling")
	}
	book.CustomPageCSS = "#guestbooks___success-message { color: purple; }"
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	page.MustNavigate(base + fmt.Sprintf("/guestbook/%d?submitted=1", book.ID)).MustWaitLoad()
	if page.MustEval(`() => getComputedStyle(document.querySelector("#guestbooks___success-message")).backgroundColor`).Str() != "rgba(0, 0, 0, 0)" {
		t.Fatal("default feedback styling was injected into a custom theme")
	}
}

func TestPrivateEmailBrowserBuiltInThemes(t *testing.T) {
	_, book := featureFixture(t)
	book.CollectEmail = true
	book.EmailFieldLabel = "Email (optional)"
	page, base := featureBrowser(t)
	for _, theme := range []string{"gray-bear", "webcomic", "cherry-mint", "cabernete", "peaceful-sky"} {
		book.CustomPageCSS = "<<built__in>>" + theme + ".css<</built__in>>"
		if err := db.Save(&book).Error; err != nil {
			t.Fatal(err)
		}
		for _, width := range []int{320, 1440} {
			page.MustSetViewport(width, 1000, 1, false)
			page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID)).MustWaitLoad()
			if !page.MustEval(`() => {
				const name = document.querySelector("#name");
				const email = document.querySelector("#email");
				return name.getBoundingClientRect().width === email.getBoundingClientRect().width
					&& getComputedStyle(name).padding === getComputedStyle(email).padding
					&& getComputedStyle(name).borderRadius === getComputedStyle(email).borderRadius;
			}`).Bool() {
				t.Fatalf("private email does not match other fields in %s at %dpx", theme, width)
			}
		}
	}
}

func TestSubmissionFeedbackBrowserLegacyEmbed(t *testing.T) {
	_, book := featureFixture(t)
	book.CollectEmail = true
	book.EmailFieldLabel = "Email (optional)"
	book.SubmissionAction = SubmissionMessage
	book.SubmissionMessage = "Recibido."
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	page, base := featureBrowser(t)
	customSite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html>
<form id="guestbooks___guestbook-form" action="%s/guestbook/%d/submit" method="post">
<input name="name" required><div><input id="website" name="website"></div><textarea name="text" required></textarea>
<button type="submit">Submit</button></form><div id="guestbooks___guestbook-messages-container"></div>
<script src="%s/resources/js/embed_script/%d/script.js"></script>`, base, book.ID, base, book.ID)
	}))
	defer customSite.Close()
	page.MustNavigate(customSite.URL)
	waitForGuestbook(page)
	if page.MustHas("input[name='email']") {
		t.Fatal("email field injected into an old embed")
	}
	fillGuestbook(page)
	clickGuestbookSubmit(page)
	page.MustWait(`() => document.querySelector("#guestbooks___success-message")?.textContent === "Recibido."`)
	if got := page.MustElement("#guestbooks___success-message").MustAttribute("role"); got == nil || *got != "status" {
		t.Fatal("missing accessible status role")
	}
}

func TestSubmissionFeedbackBrowserLegacyDefaults(t *testing.T) {
	for _, control := range []string{`<input id="submit" type="submit">`, `<button id="submit" type="submit">Sign</button>`} {
		t.Run(control, func(t *testing.T) {
			_, book := featureFixture(t)
			page, base := featureBrowser(t)
			site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprintf(w, `<!doctype html>
<style>#guestbooks___guestbook-form { display: grid; gap: 12px; } #guestbooks___guestbook-form > div { padding: 10px; border: 1px solid; }</style>
<form id="guestbooks___guestbook-form" action="%s/guestbook/%d/submit" method="post">
<div><input name="name" required></div><div><input id="website" name="website"></div>
<textarea name="text" required></textarea>%s<button id="owner-disabled" type="submit" disabled>Unavailable</button>
</form><div id="guestbooks___guestbook-messages-container"></div>
<script src="%s/resources/js/embed_script/%d/script.js"></script>`, base, book.ID, control, base, book.ID)
			}))
			defer site.Close()
			page.MustNavigate(site.URL)
			waitForGuestbook(page)
			if !page.MustElement("#owner-disabled").MustProperty("disabled").Bool() {
				t.Error("loading the script enabled an owner-disabled control")
			}
			before := page.MustEval(`() => document.querySelector("#guestbooks___guestbook-form").getBoundingClientRect().height`).Num()
			fillGuestbook(page)
			page.MustElement("#submit").MustClick()
			page.MustWait(`() => document.querySelector("#guestbooks___guestbook-messages-container").textContent.includes("Browser message")`)
			if page.MustHas("#guestbooks___success-message") || page.MustHas("#guestbooks___error-message") || page.MustHas("input[name='email']") {
				t.Error("an unchanged successful legacy embed acquired new DOM elements")
			}
			if after := page.MustEval(`() => document.querySelector("#guestbooks___guestbook-form").getBoundingClientRect().height`).Num(); after != before {
				t.Errorf("unchanged form height changed from %v to %v", before, after)
			}
			if !page.MustElement("#owner-disabled").MustProperty("disabled").Bool() {
				t.Error("submitting enabled an owner-disabled control")
			}
			if page.MustElement("#submit").MustProperty("disabled").Bool() {
				t.Error("submit control did not recover after saving")
			}
		})
	}
}

func TestSubmissionFeedbackBrowserLiveSettings(t *testing.T) {
	_, book := featureFixture(t)
	page, base := featureBrowser(t)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/thanks" {
			fmt.Fprint(w, `<p id="thanks">Saved!</p>`)
			return
		}
		fmt.Fprintf(w, `<!doctype html><style>#guestbooks___success-message { padding: 14px; border: 1px solid; }</style>
<form id="guestbooks___guestbook-form" action="%s/guestbook/%d/submit" method="post">
<input name="name" required><input id="website" name="website"><textarea name="text" required></textarea>
<button type="submit">Sign</button></form><div id="guestbooks___guestbook-messages-container"></div>
<script src="%s/resources/js/embed_script/%d/script.js"></script>`, base, book.ID, base, book.ID)
	}))
	defer site.Close()
	page.MustNavigate(site.URL)
	waitForGuestbook(page)
	// Keep the same document and script loaded while the owner changes settings.
	for _, text := range []string{"", "Merci !", "ありがとう！", ""} {
		book.SubmissionAction = SubmissionUnchanged
		if text != "" {
			book.SubmissionAction = SubmissionMessage
		}
		book.SubmissionMessage = text
		book.CollectEmail = true
		book.EmailFieldLabel = "Email (optional)"
		if err := db.Save(&book).Error; err != nil {
			t.Fatal(err)
		}
		fillGuestbook(page)
		clickGuestbookSubmit(page)
		page.MustWait(`() => document.querySelector("input[name='name']").value === "" && !document.querySelector("[type='submit']").disabled`)
		if got := page.MustEval(`() => document.querySelector("#guestbooks___success-message")?.textContent || ""`).Str(); got != text {
			t.Fatalf("already-open embed displayed %q, want %q", got, text)
		}
		if text == "" && page.MustEval(`() => {
			const notice = document.querySelector("#guestbooks___success-message");
			return notice && notice.getBoundingClientRect().height > 0;
		}`).Bool() {
			t.Fatal("disabling confirmation left a visible empty notice")
		}
		if page.MustHas("input[name='email']") {
			t.Fatal("enabling email collection changed the copied form")
		}
	}
	book.SubmissionAction = SubmissionRedirect
	book.SubmissionRedirectURL = site.URL + "/thanks"
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	fillGuestbook(page)
	clickGuestbookSubmit(page)
	page.MustElement("#thanks")
}

func TestSubmissionFeedbackBrowserHostedDefaults(t *testing.T) {
	_, book := featureFixture(t)
	page, base := featureBrowser(t)
	page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
	waitForGuestbook(page)
	if page.MustHas("#guestbooks___success-message") || page.MustHas("input[name='email']") {
		t.Error("opted-out hosted form acquired new elements")
	}
	if got := page.MustEval(`() => getComputedStyle(document.querySelector(".guestbooks___input-container")).marginBottom`).Str(); got != "0px" {
		t.Errorf("existing field spacing changed to %q", got)
	}
	if got := page.MustEval(`() => getComputedStyle(document.querySelector("#guestbooks___error-message")).backgroundColor`).Str(); got != "rgba(0, 0, 0, 0)" {
		t.Errorf("opted-out error styling changed to %q", got)
	}
	fillGuestbook(page)
	clickGuestbookSubmit(page)
	page.MustWait(`() => document.querySelector("#guestbooks___guestbook-messages-container").textContent.includes("Browser message")`)
	if page.MustHas("#guestbooks___success-message") {
		t.Error("opted-out hosted submission inserted a success notice")
	}
}

func TestSubmissionFeedbackBrowserFailuresAndDuplicates(t *testing.T) {
	_, book := featureFixture(t)
	book.SubmissionAction = SubmissionMessage
	book.SubmissionMessage = "Saved!"
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	page, base := featureBrowser(t)
	publicURL := base + fmt.Sprintf("/guestbook/%d", book.ID)
	for _, failure := range []string{"network", "malformed", "rejected"} {
		page.MustNavigate(publicURL)
		waitForGuestbook(page)
		page.MustEval(`failure => {
			const original = window.fetch;
			window.fetch = (url, options) => {
				if (!String(url).endsWith("/submit")) return original(url, options);
				if (failure === "network") return Promise.reject(new Error("fake network failure"));
				if (failure === "malformed") return Promise.resolve(new Response('{"success":true}', {status:201}));
				return Promise.resolve(new Response("Invalid <input id=unsafe> message", {status:400}));
			};
		}`, failure)
		fillGuestbook(page)
		clickGuestbookSubmit(page)
		page.MustWait(`() => document.querySelector("#guestbooks___error-message").textContent.length > 0`)
		if page.MustElement("input[name='name']").MustProperty("value").Str() != "Browser visitor" {
			t.Fatal("failure erased the entered data")
		}
		if page.MustEval(`() => document.querySelector("#guestbooks___success-message")?.textContent || ""`).Str() != "" || page.MustHas("#unsafe") {
			t.Fatal("failure showed success or interpreted HTML")
		}
	}
	page.MustNavigate(publicURL)
	waitForGuestbook(page)
	page.MustEval(`() => {
		const original = window.fetch;
		window.submitCalls = 0;
		window.fetch = (url, options) => {
			if (!String(url).endsWith("/submit")) return original(url, options);
			window.submitCalls++;
			return new Promise(resolve => { window.releaseSubmit = () => original(url, options).then(resolve); });
		};
	}`)
	fillGuestbook(page)
	page.MustEval(`() => {
		const form = document.querySelector("#guestbooks___guestbook-form");
		form.dispatchEvent(new Event("submit", {cancelable:true}));
		form.dispatchEvent(new Event("submit", {cancelable:true}));
	}`)
	if page.MustEval(`() => window.submitCalls`).Int() != 1 {
		t.Fatal("duplicate requests were sent")
	}
	page.MustEval(`() => window.releaseSubmit()`)
	page.MustWait(`() => document.querySelector("#guestbooks___success-message")?.textContent === "Saved!"`)
	var count int64
	if err := db.Model(&Message{}).Where("guestbook_id = ?", book.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stored %d messages after one successful request", count)
	}
}

func TestSubmissionFeedbackBrowserPoW(t *testing.T) {
	_, book := featureFixture(t)
	book.PowEnabled = true
	book.SubmissionAction = SubmissionMessage
	book.SubmissionMessage = "Saved!"
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}

	challenges := []string{}
	nonces := map[string]string{}
	for i := 0; i < 2; i++ {
		challenge, err := powChallengeStore.GenerateChallenge(book.ID)
		if err != nil {
			t.Fatal(err)
		}
		for nonce := 0; ; nonce++ {
			value := fmt.Sprintf("%x", nonce)
			hash := sha256.Sum256([]byte(challenge + value))
			if hasLeadingZeroBits(hash[:], constants.POW_DIFFICULTY) {
				challenges = append(challenges, challenge)
				nonces[challenge] = value
				break
			}
		}
	}
	page, base := featureBrowser(t)
	page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
	waitForGuestbook(page)
	page.MustEval(`(challenges, nonces) => {
		const original = window.fetch;
		window.fetch = (url, options) => String(url).includes("/api/pow-challenge/")
			? Promise.resolve(new Response(JSON.stringify({challenge:challenges.shift(), difficulty:19})))
			: original(url, options);
		window.Worker = class {
			postMessage(data) {
				setTimeout(() => this.onmessage({data:{found:true, nonce:nonces[data.challenge]}}), 0);
			}
			terminate() {}
		};
	}`, challenges, nonces)
	for i := 0; i < 2; i++ {
		if !page.MustElement("input[type='submit']").MustProperty("disabled").Bool() {
			t.Fatal("submission enabled without proof of work")
		}
		fillGuestbook(page)
		page.MustElement("#guestbooks___pow-checkbox").MustClick()
		page.MustWait(`() => !document.querySelector("input[type=submit]").disabled`)
		clickGuestbookSubmit(page)
		page.MustWait(`() => document.querySelector("#guestbooks___success-message")?.textContent === "Saved!" && document.querySelector("input[type=submit]").disabled`)
		if page.MustElement("input[name='powChallenge']").MustProperty("value").Str() != "" {
			t.Fatal("consumed challenge was not cleared")
		}
	}
}

func TestGuestbookSettingsBrowserOptIn(t *testing.T) {
	user, book := featureFixture(t)
	page, base := featureBrowser(t)
	page.MustSetCookies(&proto.NetworkCookieParam{Name: string(AdminTokenCookieName), Value: user.SessionToken, URL: base})
	settingsURL := base + fmt.Sprintf("/admin/guestbook/%d/edit", book.ID)
	rendered := featureRequest(initRouter(), "GET", fmt.Sprintf("/admin/guestbook/%d/edit", book.ID), nil, &user, false).Body.String()
	if !strings.Contains(rendered, `id="accept-cookies"`) {
		t.Fatalf("incomplete settings template: %s", rendered)
	}
	page.MustNavigate(settingsURL).MustWaitLoad()
	page.MustElement("#accept-cookies").MustClick()
	if page.MustElement("#submissionAction").MustProperty("value").Str() != "" ||
		page.MustElement("#collectEmail").MustProperty("checked").Bool() {
		t.Fatal("features were enabled by default")
	}
	page.MustElement("#submissionAction").MustSelect("Show a confirmation message")
	page.MustElement("#submissionMessage").MustInput("ありがとう！")
	page.MustElement("#collectEmail").MustClick()
	page.MustElement("#emailFieldLabel").MustSelectAllText().MustInput("返信先 (任意)")
	page.MustElement("#guestbook-edit-form button[type='submit']").MustClick()
	page.MustWaitLoad()
	page.MustNavigate(settingsURL).MustWaitLoad()
	if page.MustElement("#submissionMessage").MustProperty("value").Str() != "ありがとう！" ||
		!page.MustElement("#collectEmail").MustProperty("checked").Bool() {
		t.Fatal("enabled options were not saved")
	}
	page.MustElement("#submissionAction").MustSelect("Keep current behavior (no confirmation)")
	page.MustElement("#collectEmail").MustClick()
	page.MustElement("#guestbook-edit-form button[type='submit']").MustClick()
	page.MustWaitLoad()
	page.MustNavigate(settingsURL).MustWaitLoad()
	if err := db.First(&book, book.ID).Error; err != nil {
		t.Fatal(err)
	}
	if book.CollectEmail || book.SubmissionAction != SubmissionUnchanged || book.SubmissionMessage != "ありがとう！" || book.EmailFieldLabel != "返信先 (任意)" {
		t.Fatal("disabling options failed or erased customized text")
	}
	page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
	waitForGuestbook(page)
	if page.MustHas("input[name='email']") {
		t.Fatal("disabled hosted field is still visible")
	}
	fillGuestbook(page)
	clickGuestbookSubmit(page)
	page.MustWait(`() => document.querySelector("input[name=name]").value === ""`)
	if page.MustHas("#guestbooks___success-message") {
		t.Fatal("disabled confirmation still appeared")
	}
}
