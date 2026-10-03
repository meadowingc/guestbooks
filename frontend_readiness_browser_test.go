//go:build browser

package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

func TestFrontendCSSCompatibility(t *testing.T) {
	for _, test := range []struct {
		name, source, color string
	}{
		{"nested-condition", `body { color: red; @media (max-width: 700px) { color: blue; } }`, "rgb(0, 0, 255)"},
		{"property-name-in-selector", `.behavior { color: red; }`, "rgb(255, 0, 0)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, book := featureFixture(t)
			if err := db.Model(&book).Update("custom_page_css", test.source).Error; err != nil {
				t.Fatal(err)
			}
			page, base := featureBrowser(t)
			page.MustSetViewport(600, 800, 1, false)
			page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
			waitForGuestbook(page)
			result := page.MustEval(`async url => {
				document.body.classList.add("behavior");
				const response=await fetch(url);
				return {status:response.status, source:await response.text(),
					type:response.headers.get("Content-Type"), nosniff:response.headers.get("X-Content-Type-Options"),
					color:getComputedStyle(document.body).color};
			}`, fmt.Sprintf("/resources/css/guestbook/%d.css", book.ID))
			if result.Get("status").Int() != http.StatusOK || result.Get("source").Str() != test.source ||
				result.Get("color").Str() != test.color || result.Get("type").Str() != "text/css; charset=utf-8" ||
				result.Get("nosniff").Str() != "nosniff" {
				t.Fatalf("valid saved CSS was not delivered and applied unchanged: %s", result.JSON("", ""))
			}
		})
	}
}

func TestFrontendBuiltInThemeHydrationSave(t *testing.T) {
	for _, mode := range []string{"pending", "failed"} {
		t.Run(mode, func(t *testing.T) {
			user, book := featureFixture(t)
			marker := "<<built__in>>gray-bear.css<</built__in>>"
			if err := db.Model(&book).Update("custom_page_css", marker).Error; err != nil {
				t.Fatal(err)
			}
			page, base := featureBrowser(t)
			page.MustSetCookies(&proto.NetworkCookieParam{Name: string(AdminTokenCookieName), Value: user.SessionToken, URL: base})
			page.MustEvalOnNewDocument(`(() => {
				const original=window.fetch;
				window.themeAttempts=0;
				window.fetch=(url,options)=>{
					if(!String(url).includes("/premade_styles/"))return original(url,options);
					window.themeAttempts++;
					return ` + fmt.Sprintf("%q", mode) + `==="pending" ? new Promise(()=>{}) :
						Promise.resolve(new Response("Unavailable",{status:503}));
				};
			})()`)
			page.MustNavigate(base + fmt.Sprintf("/admin/guestbook/%d/edit", book.ID)).MustWaitLoad()
			page.MustWait(`() => window.themeAttempts === 1`)
			if mode == "failed" {
				page.MustWait(`() => !document.getElementById("css-warning").hidden`)
			}
			page.MustEval(`() => {document.getElementById("cookie-banner").style.display="none";}`)
			if page.MustElement("#customPageCSS").MustProperty("value").Str() != marker {
				t.Fatal("unavailable theme fetch changed the saved reference")
			}
			page.MustElement("#challengeQuestion").MustInput("New question")
			page.MustElement("#challengeAnswer").MustInput("Answer")
			submitBrowserForm(page, "#guestbook-edit-form")
			if !page.MustHas("#guestbook-edit-form") {
				t.Fatalf("saving navigated away from the editor: %s", page.MustElement("body").MustText())
			}
			var saved Guestbook
			if err := db.First(&saved, book.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.CustomPageCSS != marker || saved.ChallengeQuestion != "New question" || saved.ChallengeAnswer != "Answer" {
				t.Fatal("saving with an unavailable theme preview lost the theme or other settings")
			}
		})
	}
}

func TestFrontendSynchronousMessageContainerReplacement(t *testing.T) {
	_, book := featureFixture(t)
	message := Message{GuestbookID: book.ID, Name: "Visitor", Text: "Existing message", Approved: true}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	page, base := featureBrowser(t)
	page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
	waitForGuestbook(page)
	page.MustEval(`async url => {
		const source=await (await fetch(url)).text();
		const original=window.fetch;
		window.messageLoads=0;window.posts=0;
		window.fetch=(url,options)=>{
			if(String(url).includes("/api/v2/"))window.messageLoads++;
			if(options?.method==="POST")window.posts++;
			return original(url,options);
		};
		const previous=document.getElementById("guestbooks___guestbook-messages-container");
		const replacement=document.createElement("div");
		replacement.id=previous.id;
		previous.replaceWith(replacement);
		for(let i=0;i<2;i++){
			const script=document.createElement("script");
			script.textContent=source;
			document.body.appendChild(script);
		}
	}`, base+fmt.Sprintf("/resources/js/embed_script/%d/script.js", book.ID))
	page.MustWait(`() => document.getElementById("guestbooks___guestbook-messages-container").textContent.includes("Existing message")`)
	if !page.MustEval(`() => window.guestbooks___instance?.messagesContainer === document.getElementById("guestbooks___guestbook-messages-container") &&
		window.messageLoads===1 && document.querySelectorAll("#guestbooks___message-loading").length===1`).Bool() {
		t.Fatal("synchronous reinjection lost or duplicated the replacement instance")
	}
	fillGuestbook(page)
	clickGuestbookSubmit(page)
	page.MustWait(`() => document.getElementById("guestbooks___guestbook-messages-container").textContent.includes("Browser message")`)
	if page.MustEval(`() => window.posts`).Int() != 1 {
		t.Fatal("replacement left duplicate submission listeners")
	}
	page.MustEval(`() => document.getElementById("guestbooks___guestbook-messages-container").remove()`)
	page.MustWait(`() => !window.guestbooks___instance`)
	if page.MustHas("#guestbooks___message-loading") {
		t.Fatal("replacement instance did not clean up on removal")
	}
}

func TestFrontendNormalizedTextBoundary(t *testing.T) {
	user, book := featureFixture(t)
	page, base := featureBrowser(t)
	page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
	waitForGuestbook(page)
	setText := func(selector, text string) {
		t.Helper()
		if !page.MustEval(`(selector,text) => {
			const field=document.querySelector(selector);
			field.value=text+"X";field.dispatchEvent(new Event("input"));
			const rejectsExtra=!field.validity.valid;
			field.value=text;field.dispatchEvent(new Event("input"));
			return rejectsExtra && field.validity.valid && Array.from(field.value).length===2500;
		}`, selector, text).Bool() {
			t.Fatal("normalized textarea boundary did not match 2500 Unicode codepoints")
		}
	}
	postFormData := func(selector string) {
		t.Helper()
		result := page.MustEval(`async selector => {
			const form=document.querySelector(selector);
			const response=await fetch(form.action,{method:"POST",headers:{Accept:"application/json"},body:new FormData(form)});
			return {status:response.status, error:response.ok?"":await response.text()};
		}`, selector)
		if result.Get("status").Int() != http.StatusOK {
			t.Fatalf("browser FormData boundary rejected: %s", result.JSON("", ""))
		}
	}
	prefix := strings.Repeat("😀", 2498) + "\n"
	page.MustElement("input[name=name]").MustInput("Visitor")
	setText("textarea[name=text]", prefix+"V")
	clickGuestbookSubmit(page)
	page.MustWait(`() => !document.querySelector("[type=submit]").disabled`)
	if page.MustElement("input[name=name]").MustProperty("value").Str() != "" {
		t.Fatalf("visitor boundary rejected: %s", page.MustElement("#guestbooks___error-message").MustText())
	}
	var message Message
	if err := db.Where("guestbook_id = ?", book.ID).First(&message).Error; err != nil {
		t.Fatal(err)
	}
	if message.Text != prefix+"V" {
		t.Fatal("visitor FormData did not store normalized LF text")
	}
	page.MustSetCookies(&proto.NetworkCookieParam{Name: string(AdminTokenCookieName), Value: user.SessionToken, URL: base})
	page.MustNavigate(base + fmt.Sprintf("/admin/guestbook/%d/message/%d/edit", book.ID, message.ID)).MustWaitLoad()
	setText("textarea[name=text]", prefix+"E")
	postFormData("form[action$='/edit']")
	if err := db.First(&message, message.ID).Error; err != nil || message.Text != prefix+"E" {
		t.Fatalf("admin edit did not store normalized LF text: %v", err)
	}
	page.MustNavigate(base + fmt.Sprintf("/admin/guestbook/%d", book.ID)).MustWaitLoad()
	page.MustEval(`() => {document.getElementById("cookie-banner").style.display="none";}`)
	page.MustElement(".reply-btn").MustClick()
	setText("#reply-text", prefix+"R")
	postFormData("#reply-form")
	var reply Message
	if err := db.Where("parent_message_id = ?", message.ID).First(&reply).Error; err != nil || reply.Text != prefix+"R" {
		t.Fatalf("admin reply did not store normalized LF text: %v", err)
	}
}
