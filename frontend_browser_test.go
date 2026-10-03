//go:build browser

package main

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

func addBrowserRequestCSRF(t *testing.T, request *http.Request) {
	t.Helper()
	origin := request.URL.Scheme + "://" + request.URL.Host
	get, err := http.NewRequest(http.MethodGet, origin+"/admin/signin", nil)
	if err != nil {
		t.Fatal(err)
	}
	get.Header.Set("Cookie", request.Header.Get("Cookie"))
	response, err := http.DefaultClient.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	token := regexp.MustCompile(`<meta name="csrf-token" content="([^"]+)">`).FindSubmatch(body)
	if response.StatusCode != http.StatusOK || len(token) != 2 {
		t.Fatalf("could not get a CSRF token from the real sign-in page: %d", response.StatusCode)
	}
	request.Header.Set("X-CSRF-Token", html.UnescapeString(string(token[1])))
	request.Header.Set("Origin", origin)
	request.Header.Set("Referer", origin+"/admin/signin")
	for _, cookie := range response.Cookies() {
		request.AddCookie(cookie)
	}
}

func TestFrontendInvalidChallengeRepairWarning(t *testing.T) {
	user, book := featureFixture(t)
	book.ChallengeQuestion = "What color is the sky?"
	book.ChallengeAnswer = " \t "
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	page, base := frontendAdminPage(t, user, fmt.Sprintf("/admin/guestbook/%d/edit", book.ID))
	page.MustWait(`() => !document.getElementById("challenge-settings-warning").hidden`)
	if !strings.Contains(page.MustElement("#challenge-settings-warning").MustText(), "submissions are blocked") ||
		page.MustElement("#challengeAnswer").MustProperty("validationMessage").Str() == "" {
		t.Fatal("invalid saved challenge has no actionable repair warning and validation")
	}
	status := page.MustEval(`url => fetch(url, {
		method:"POST", headers:{"Accept":"application/json"},
		body:new URLSearchParams({name:"Visitor",text:"Not accepted",challengeQuestionAnswer:""})
	}).then(response=>response.status)`, base+fmt.Sprintf("/guestbook/%d/submit", book.ID)).Int()
	if status != http.StatusServiceUnavailable {
		t.Fatalf("invalid saved challenge did not fail closed: %d", status)
	}
	page.MustElement("#challengeAnswer").MustSelectAllText().MustInput("blue")
	submitBrowserForm(page, "#guestbook-edit-form")
	page.MustWaitLoad()
	page.MustWait(`() => document.getElementById("challenge-settings-warning").hidden`)
	var saved Guestbook
	if err := db.First(&saved, book.ID).Error; err != nil || saved.ChallengeAnswer != "blue" {
		t.Fatalf("repair was not saved: %v", err)
	}
}

func TestFrontendStoredCSSBrowser(t *testing.T) {
	user, book := featureFixture(t)
	source := `@font-face { font-family: "</style><script>window.cssExecuted=true</script>"; }`
	if err := db.Model(&book).Update("custom_page_css", source).Error; err != nil {
		t.Fatal(err)
	}
	page, base := featureBrowser(t)
	page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID)).MustWaitLoad()
	if page.MustEval(`() => window.cssExecuted === true`).Bool() || page.MustHas("style:not([id]) script") {
		t.Fatal("stored stylesheet executed as HTML")
	}
	resource := fmt.Sprintf("/resources/css/guestbook/%d.css", book.ID)
	if !page.MustHas("link[href='" + resource + "']") {
		t.Fatal("custom stylesheet was not isolated in a typed resource")
	}
	page.MustSetCookies(&proto.NetworkCookieParam{Name: string(AdminTokenCookieName), Value: user.SessionToken, URL: base})
	page.MustNavigate(base + fmt.Sprintf("/admin/guestbook/%d/edit", book.ID)).MustWaitLoad()
	page.MustWait(`() => !document.querySelector("#css-warning").hidden`)
	if !strings.Contains(page.MustElement("#css-warning").MustText(), "blocked") ||
		page.MustElement("#customPageCSS").MustProperty("value").Str() != source {
		t.Fatal("unsafe stored CSS was silently discarded or its repair warning was missing")
	}
	if page.MustHas("button[onclick*='formatCSS']") {
		t.Fatal("unsafe Format control remains")
	}
}

func TestFrontendChallengeTextAndLimits(t *testing.T) {
	_, book := featureFixture(t)
	book.ChallengeQuestion = "Question \"quoted\"\n<img id=challenge-injected src=x onerror=alert(1)>"
	book.ChallengeHint = "'><script id=hint-injected>alert(1)</script>"
	book.ChallengeAnswer = "yes"
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	page, base := featureBrowser(t)
	page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
	waitForGuestbook(page)
	if page.MustElement("label[for='challengeQuestionAnswer']").MustProperty("textContent").Str() != book.ChallengeQuestion ||
		page.MustElement("#challengeQuestionAnswer").MustProperty("placeholder").Str() != book.ChallengeHint ||
		page.MustHas("#challenge-injected") || page.MustHas("#hint-injected") {
		t.Fatal("challenge question/hint was not rendered literally")
	}
	for _, field := range []struct {
		id    string
		limit int
	}{
		{"name", 200},
		{"text", 2500},
	} {
		if !page.MustEval(`(id, limit) => {
			const input = document.getElementById(id);
			input.value = "😀".repeat(limit);
			input.dispatchEvent(new Event("input"));
			const boundary = input.validity.valid;
			input.value += "😀";
			input.dispatchEvent(new Event("input"));
			return boundary && !input.validity.valid;
		}`, field.id, field.limit).Bool() {
			t.Fatalf("%s limit is not measured in Unicode code points", field.id)
		}
	}
	if !page.MustEval(`() => {
		const input = document.getElementById("website");
		input.value = "https://example.test/" + "a".repeat(2048 - "https://example.test/".length);
		input.dispatchEvent(new Event("input"));
		const boundary = input.validity.valid;
		input.value += "a";
		input.dispatchEvent(new Event("input"));
		return boundary && !input.validity.valid;
	}`).Bool() {
		t.Fatal("website byte limit is not enforced")
	}
}

func TestFrontendDelayedAndDuplicateEmbed(t *testing.T) {
	_, book := featureFixture(t)
	page, base := featureBrowser(t)
	scriptURL := base + fmt.Sprintf("/resources/js/embed_script/%d/script.js", book.ID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><script>
			window.messageLoads=0;window.posts=0;
			const originalFetch=window.fetch;
			window.fetch=(url,options)=>{if(String(url).includes("/api/v2/"))window.messageLoads++;
				if(options?.method==="POST")window.posts++;return originalFetch(url,options);};
			</script><script src="%s"></script><script src="%s"></script>`, scriptURL, scriptURL)
		w.(http.Flusher).Flush()
		fmt.Fprintf(w, `<form id="guestbooks___guestbook-form" action="%s/guestbook/%d/submit" method="post">
			<input name="name"><textarea name="text"></textarea><button type="submit">Submit</button></form>
			<div id="guestbooks___guestbook-messages-container"></div>`, base, book.ID)
	}))
	defer server.Close()
	page.MustNavigate(server.URL).MustWaitLoad()
	waitForGuestbook(page)
	page.MustEval(`url => new Promise((resolve,reject)=>{
		const script=document.createElement("script");script.src=url;script.onload=resolve;script.onerror=reject;document.head.append(script);
	})`, scriptURL)
	if page.MustEval(`() => window.messageLoads`).Int() != 1 {
		t.Fatal("duplicate initialization loaded the message list again")
	}
	fillGuestbook(page)
	clickGuestbookSubmit(page)
	page.MustWait(`() => document.querySelector("#guestbooks___guestbook-messages-container").textContent.includes("Browser message")`)
	if page.MustEval(`() => window.posts`).Int() != 1 {
		t.Fatal("one submit sent duplicate POST requests")
	}
	page.MustEval(`() => document.getElementById("guestbooks___guestbook-form").remove()`)
	page.MustWait(`() => !window.guestbooks___instance`)
	if page.MustHas("#guestbooks___message-loading") {
		t.Fatal("disposed embed left its loading controls behind")
	}
}

func TestFrontendMessageLoadingRetry(t *testing.T) {
	_, book := featureFixture(t)
	for _, failure := range []string{"http", "shape", "network"} {
		t.Run(failure, func(t *testing.T) {
			page, base := featureBrowser(t)
			page.MustEvalOnNewDocument(`(() => {
				window.IntersectionObserver = undefined;
				const original=window.fetch;
				window.requestedPages=[];
				window.failList=true;
				window.fetch=(url,options)=>{
					if(!String(url).includes("/api/v2/"))return original(url,options);
					const page=Number(new URL(url,location.href).searchParams.get("page"));window.requestedPages.push(page);
					if(window.failList) {
						if(` + fmt.Sprintf("%q", failure) + `==="http")return Promise.resolve(new Response("unavailable",{status:503}));
						if(` + fmt.Sprintf("%q", failure) + `==="network")return Promise.reject(new Error("offline"));
						return Promise.resolve(new Response(JSON.stringify({oops:[]})));
					}
					return Promise.resolve(new Response(JSON.stringify({messages:[{Name:"Visitor "+page,Text:"page "+page,CreatedAt:"2026-01-01T00:00:00Z",Replies:null}],pagination:{hasNext:page<2}})));
				};
			})()`)
			page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
			page.MustWait(`() => document.querySelector("#guestbooks___load-more")?.textContent.includes("Retry")`)
			if strings.Contains(page.MustElement("#guestbooks___guestbook-messages-container").MustText(), "no messages") {
				t.Fatal("a failed/malformed response was presented as an empty guestbook")
			}
			page.MustEval(`() => window.failList=false`)
			page.MustElement("#guestbooks___load-more").MustClick()
			page.MustWait(`() => document.querySelector("#guestbooks___guestbook-messages-container").textContent.includes("page 1")`)
			page.MustEval(`() => window.failList=true`)
			page.MustElement("#guestbooks___load-more").MustClick()
			page.MustWait(`() => document.querySelector("#guestbooks___load-more").textContent.includes("Retry")`)
			if !strings.Contains(page.MustElement("#guestbooks___guestbook-messages-container").MustText(), "page 1") {
				t.Fatal("later-page failure erased loaded messages")
			}
			page.MustEval(`() => window.failList=false`)
			page.MustElement("#guestbooks___load-more").MustClick()
			page.MustWait(`() => document.querySelector("#guestbooks___guestbook-messages-container").textContent.includes("page 2")`)
			if page.MustEval(`() => JSON.stringify(window.requestedPages)`).Str() != "[1,1,2,2]" {
				t.Fatal("retry advanced or reset the failed page cursor")
			}
		})
	}
}

func TestFrontendThemesViewportMatrix(t *testing.T) {
	_, book := featureFixture(t)
	book.WebsiteURL = "https://" + strings.Repeat("a", 63) + ".example.test/" + strings.Repeat("b", 100)
	book.CollectEmail = true
	book.EmailFieldLabel = "Optional email"
	book.ChallengeQuestion = "A verification question"
	book.ChallengeAnswer = "yes"
	parent := Message{
		GuestbookID: book.ID, Approved: true,
		Name: strings.Repeat("N", 200), Text: strings.Repeat("M", 1000),
	}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	reply := Message{
		GuestbookID: book.ID, ParentMessageID: &parent.ID, Approved: true,
		Name: strings.Repeat("R", 200), Text: strings.Repeat("T", 1000),
	}
	if err := db.Create(&reply).Error; err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"cabernete", "peaceful-sky", "cherry-mint", "gray-bear", "webcomic"} {
		book.CustomPageCSS = "<<built__in>>" + theme + ".css<</built__in>>"
		if err := db.Save(&book).Error; err != nil {
			t.Fatal(err)
		}
		page, base := featureBrowser(t)
		for _, size := range [][2]int{{320, 568}, {500, 300}, {700, 600}, {751, 600}, {951, 600}, {1440, 900}} {
			page.MustSetViewport(size[0], size[1], 1, false)
			page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID)).MustWaitLoad()
			waitForGuestbook(page)
			result := page.MustEval(`() => ({
				overflow:document.documentElement.scrollWidth-innerWidth,
				controls:Array.from(document.querySelectorAll("input:not([type=hidden]), textarea, button")).every(element=>{
					const bounds=element.getBoundingClientRect();
					return !bounds.width || bounds.left>=-1 && bounds.right<=innerWidth+1;
				}),
				labels:["name","website","text","email","challengeQuestionAnswer"].every(id=>document.querySelector('label[for="'+id+'"]'))
			})`)
			if result.Get("overflow").Int() > 1 || !result.Get("controls").Bool() || !result.Get("labels").Bool() {
				t.Errorf("%s at %dx%d: %s", theme, size[0], size[1], result.JSON("", ""))
			}
		}
	}
}

func frontendAdminPage(t *testing.T, user AdminUser, path string) (*rod.Page, string) {
	t.Helper()
	page, base := featureBrowser(t)
	page.MustSetCookies(&proto.NetworkCookieParam{Name: string(AdminTokenCookieName), Value: user.SessionToken, URL: base})
	page.MustNavigate(base + path).MustWaitLoad()
	page.MustWait(`() => document.readyState === "complete"`)
	page.MustEval(`() => {localStorage.setItem("acceptedCookies","true");document.getElementById("cookie-banner").style.display="none";}`)
	return page, base
}

func TestFrontendThemeFetchKeepsEdits(t *testing.T) {
	user, book := featureFixture(t)
	page, _ := frontendAdminPage(t, user, fmt.Sprintf("/admin/guestbook/%d/edit", book.ID))
	page.MustEval(`() => {
		window.themeRequests=[];
		const original=window.fetch;
		window.fetch=(url,options)=>String(url).includes("/premade_styles/")?
			new Promise(resolve=>window.themeRequests.push({url,resolve})):original(url,options);
		return true;
	}`)
	selectTheme := func(theme string) {
		page.MustEval(`theme => {const dropdown=document.getElementById("premadeStyles");dropdown.value="/assets/premade_styles/"+theme+".css";dropdown.dispatchEvent(new Event("change"));}`, theme)
	}
	selectTheme("gray-bear")
	selectTheme("cabernete")
	page.MustEval(`() => {
		window.themeRequests[1].resolve(new Response("body { color: red; }",{headers:{"Content-Type":"text/css"}}));
		window.themeRequests[0].resolve(new Response("body { color: blue; }",{headers:{"Content-Type":"text/css"}}));
	}`)
	page.MustWait(`() => document.getElementById("customPageCSS").value === "body { color: red; }"`)
	selectTheme("peaceful-sky")
	page.MustElement("#customPageCSS").MustSelectAllText().MustInput("body { color: green; }")
	page.MustEval(`() => window.themeRequests[2].resolve(new Response("body { color: pink; }",{headers:{"Content-Type":"text/css"}}))`)
	page.MustEval(`() => new Promise(resolve=>setTimeout(resolve,0))`)
	if page.MustElement("#customPageCSS").MustProperty("value").Str() != "body { color: green; }" {
		t.Fatal("a delayed theme fetch overwrote typed CSS")
	}
	selectTheme("webcomic")
	page.MustEval(`() => window.themeRequests[3].resolve(new Response("not found",{status:404}))`)
	page.MustWait(`() => !document.getElementById("css-warning").hidden`)
	if page.MustElement("#customPageCSS").MustProperty("value").Str() != "body { color: green; }" {
		t.Fatal("failed theme fetch replaced the editor")
	}
}

func TestFrontendPasswordStrengthAndRecoveryLink(t *testing.T) {
	testMailer(t, true, func(string, string) error {
		return fmt.Errorf("an unknown account must not request verification mail")
	})
	user, _ := featureFixture(t)
	page, base := frontendAdminPage(t, user, "/admin/settings")
	page.MustElement("#new-password").MustInput("StrongPassword123!@")
	page.MustWait(`() => document.getElementById("new-password").parentElement.textContent.includes("Password strength: Strong")`)
	if strings.Contains(page.MustElement("#new-password").MustParent().MustText(), "undefined") {
		t.Fatal("maximum password strength overflowed the labels")
	}
	page.MustNavigate(base + "/forgot-password").MustWaitLoad()
	page.MustElement("input[name='username']").MustInput("unknown-browser-account")
	submitBrowserForm(page, "form.auth-form")
	if !page.MustHas("a[href='/forgot-password']") || page.MustHas("a[href='/admin/forgot-password']") {
		t.Fatal("password recovery retry URL is broken")
	}
}

func TestFrontendPoWUnsupportedAndWorkerFailures(t *testing.T) {
	_, book := featureFixture(t)
	book.PowEnabled = true
	if err := db.Save(&book).Error; err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"insecure", "worker-error", "worker-message-error"} {
		t.Run(mode, func(t *testing.T) {
			page, base := featureBrowser(t)
			if mode == "insecure" {
				page.MustEvalOnNewDocument(`Object.defineProperty(window,"isSecureContext",{value:false})`)
			} else {
				page.MustEvalOnNewDocument(`window.Worker=class {
					constructor(){window.workerCreated=(window.workerCreated||0)+1;}
					postMessage(){queueMicrotask(()=>{` + map[string]string{
					"worker-error":         `this.onerror({preventDefault(){}})`,
					"worker-message-error": `this.onmessage({data:{error:true}})`,
				}[mode] + `;});}
					terminate(){window.workerTerminated=(window.workerTerminated||0)+1;}
				}`)
			}
			page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
			waitForGuestbook(page)
			if mode == "insecure" {
				if !strings.Contains(page.MustElement("#guestbooks___pow-status").MustText(), "HTTPS") {
					t.Fatal("insecure PoW context has no actionable HTTPS requirement")
				}
			} else {
				for i := 0; i < 2; i++ {
					page.MustElement("#guestbooks___pow-checkbox").MustClick()
					page.MustWait(`() => document.getElementById("guestbooks___pow-status").textContent.includes("failed")`)
				}
				if page.MustEval(`() => window.workerCreated===2 && window.workerTerminated===2`).Bool() != true {
					t.Fatal("worker failure did not clean up or permit retry")
				}
			}
			if !page.MustElement("input[type='submit']").MustProperty("disabled").Bool() {
				t.Fatal("failed verification enabled submission")
			}
		})
	}
}

func TestFrontendBulkRejectsHTMLAndRedirects(t *testing.T) {
	for _, operation := range []string{"approve", "delete"} {
		for _, responseKind := range []string{"html", "redirect", "unexpected-text"} {
			t.Run(operation+"/"+responseKind, func(t *testing.T) {
				user, book := featureFixture(t)
				message := Message{GuestbookID: book.ID, Name: "Visitor", Text: "Keep this message"}
				if err := db.Create(&message).Error; err != nil {
					t.Fatal(err)
				}
				page, _ := frontendAdminPage(t, user, fmt.Sprintf("/admin/guestbook/%d", book.ID))
				page.MustEval(`kind => {
						const original=window.fetch;
						window.fetch=(url,options)=>{
							if(!String(url).includes("/messages/bulk-"))return original(url,options);
							window.mutationHeaders=options.headers;
							const response=new Response(kind==="unexpected-text"?"unrecognized":"<html><h1>Sign in</h1></html>",{headers:{"Content-Type":kind==="unexpected-text"?"text/plain":"text/html"}});
							if(kind==="redirect")Object.defineProperty(response,"redirected",{value:true});
							return Promise.resolve(response);
						};
					}`, responseKind)
				page.MustElement(".message-checkbox").MustClick()
				page.MustElement("#bulk-" + operation + "-btn").MustClick()
				if operation == "delete" {
					page.MustElement("#confirm-bulk-delete").MustClick()
				}
				page.MustWait(`() => document.querySelector("[role=alert]")?.textContent.length > 0`)
				if !page.MustHas(fmt.Sprintf(".message-row[data-message-id='%d']", message.ID)) ||
					page.MustElement("#selected-count").MustText() != "1 message selected" {
					t.Fatal("unexpected response erased content or selection")
				}
				if !page.MustEval(`() => window.mutationHeaders.Accept==="application/json" && window.mutationHeaders["X-CSRF-Token"]===document.querySelector("meta[name=csrf-token]").content`).Bool() {
					t.Fatal("async mutation omitted its authentication/CSRF request headers")
				}
			})
		}
	}
}

func TestFrontendMixedReplyModeration(t *testing.T) {
	user, book := featureFixture(t)
	parent := Message{GuestbookID: book.ID, Name: "Parent", Text: "Parent"}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	reply := Message{GuestbookID: book.ID, Name: "Reply", Text: "Reply", ParentMessageID: &parent.ID}
	sibling := Message{GuestbookID: book.ID, Name: "Sibling", Text: "Sibling", ParentMessageID: &parent.ID}
	for _, message := range []*Message{&reply, &sibling} {
		if err := db.Create(message).Error; err != nil {
			t.Fatal(err)
		}
	}
	page, base := frontendAdminPage(t, user, fmt.Sprintf("/admin/guestbook/%d", book.ID))
	page.MustElement(fmt.Sprintf(".message-checkbox[data-message-id='%d']", reply.ID)).MustClick()
	page.MustElement("#bulk-approve-btn").MustClick()
	page.MustWait(`id => !!document.querySelector('.message-row[data-message-id="'+id+'"] .badge-success')`, reply.ID)
	page.MustWaitLoad()
	var storedParent, storedReply Message
	if err := db.First(&storedParent, parent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&storedReply, reply.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedParent.Approved || !storedReply.Approved {
		t.Fatal("reply approval changed its parent or failed to approve the reply")
	}
	data := page.MustEval(`url => fetch(url).then(response=>response.json())`, base+fmt.Sprintf("/api/v2/get-guestbook-messages/%d", book.ID))
	if len(data.Get("messages").Arr()) != 0 {
		t.Fatal("approved reply escaped its pending parent")
	}
	page.MustElement(fmt.Sprintf(".message-checkbox[data-message-id='%d']", parent.ID)).MustClick()
	page.MustElement(fmt.Sprintf(".message-checkbox[data-message-id='%d']", reply.ID)).MustClick()
	if page.MustElement("#selected-count").MustText() != "2 messages selected" {
		t.Fatal("mixed parent/reply selection counted cards instead of message IDs")
	}
	page.MustElement("#bulk-delete-btn").MustClick()
	if !strings.Contains(page.MustElement("#confirm-bulk-delete").MustParent().MustParent().MustText(), "all its replies") {
		t.Fatal("bulk deletion did not explain parent/reply deletion")
	}
	page.MustElement("#confirm-bulk-delete").MustClick()
	page.MustWait(`() => document.body.textContent.includes("No Messages Yet")`)
	page.MustWaitLoad()
	var count int64
	if err := db.Model(&Message{}).Where("guestbook_id = ?", book.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("parent deletion left %d selected/unselected replies", count)
	}
}

func TestFrontendReplySelectionAndExpiredSession(t *testing.T) {
	user, book := featureFixture(t)
	parent := Message{GuestbookID: book.ID, Name: "Parent", Text: "Pending parent"}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	reply := Message{GuestbookID: book.ID, Name: "Reply", Text: "Pending reply", ParentMessageID: &parent.ID}
	if err := db.Create(&reply).Error; err != nil {
		t.Fatal(err)
	}
	page, _ := frontendAdminPage(t, user, fmt.Sprintf("/admin/guestbook/%d", book.ID))
	replyRow := fmt.Sprintf(".message-row[data-message-id='%d']", reply.ID)
	parentRow := fmt.Sprintf(".message-row[data-message-id='%d']", parent.ID)
	page.MustElement(replyRow + " .message-checkbox").MustClick()
	if page.MustElement("#selected-count").MustText() != "1 message selected" ||
		!strings.Contains(page.MustElement(replyRow).MustText(), "Pending") {
		t.Fatal("pending reply is not independently selectable and identifiable")
	}
	if page.MustEval(`selector => document.querySelector(selector).style.background.includes("primary-light")`, parentRow).Bool() {
		t.Fatal("selecting a reply highlighted its parent card")
	}
	if err := db.Model(&user).Update("session_expires_at", time.Now().Add(-time.Hour).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	page.MustElement("#bulk-approve-btn").MustClick()
	page.MustWait(`() => Array.from(document.querySelectorAll("[role=alert]")).some(e=>/sign in|session/i.test(e.textContent))`)
	if !page.MustHas(replyRow) || !page.MustHas(parentRow) ||
		page.MustElement("#selected-count").MustText() != "1 message selected" {
		t.Fatal("lost authentication removed content or selection")
	}
	var stored Message
	if err := db.First(&stored, reply.ID).Error; err != nil || stored.Approved {
		t.Fatal("lost authentication approved the selected reply")
	}
}
