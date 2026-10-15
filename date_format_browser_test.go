//go:build browser

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod/lib/proto"
)

func TestDateFormatTokensBrowser(t *testing.T) {
	user, book := featureFixture(t)
	settingsPath := fmt.Sprintf("/admin/guestbook/%d/edit", book.ID)
	page, base := frontendAdminPage(t, user, settingsPath)
	for _, test := range dateFormatValidationCases {
		if !utf8.ValidString(test.pattern) {
			continue
		}
		t.Run("validation/"+test.name, func(t *testing.T) {
			got := page.MustEval(`pattern => {
				try {
					guestbooksDateFormat.createFormatter("custom", pattern);
					return "";
				} catch (error) {
					if (!(error instanceof guestbooksDateFormat.PatternError)) throw error;
					return error.message;
				}
			}`, test.pattern).Str()
			if got != "" {
				// Go errors omit the capitalization and final period used in the UI.
				got = strings.ToLower(got[:1]) + strings.TrimSuffix(got[1:], ".")
			}
			want := ""
			if err := validateDateFormatPattern(test.pattern); err != nil {
				want = err.Error()
			}
			if got != want {
				t.Fatalf("browser validation = %q, server validation = %q", got, want)
			}
		})
	}
	if !page.MustEval(`() => {
		try {
			guestbooksDateFormat.createFormatter("custom", "%Y\uD800");
			return false;
		} catch (error) {
			return error instanceof guestbooksDateFormat.PatternError;
		}
	}`).Bool() {
		t.Fatal("browser accepted an unpaired surrogate")
	}

	for _, test := range []struct {
		name, locale, timezone, timestamp, pattern, want string
	}{
		{"all tokens", "en_US", "UTC", "2026-01-02T12:34:56Z",
			"%Y|%y|%m|%d|%e|%b|%B|%a|%A|%j|%G|%g|%V|%u|%w|%%",
			"2026|26|01|02| 2|Jan|January|Fri|Friday|002|2026|26|01|5|5|%"},
		{"compact ISO", "en_US", "UTC", "2026-10-14T12:00:00Z", "%Y%m%d", "20261014"},
		{"localized names", "de_DE", "UTC", "2026-01-02T12:00:00Z", "%A, %d. %B %Y", "Freitag, 02. Januar 2026"},
		{"Gregorian numbers", "th_TH", "UTC", "2026-01-02T12:00:00Z", "%Y-%m-%d", "2026-01-02"},
		{"previous ISO year", "en_US", "UTC", "2021-01-01T12:00:00Z", "%G-W%V-%u", "2020-W53-5"},
		{"next ISO year", "en_US", "UTC", "2018-12-31T12:00:00Z", "%G-W%V-%u", "2019-W01-1"},
		{"Sunday", "en_US", "UTC", "2021-01-03T12:00:00Z", "%G-W%V-%u (%w)", "2020-W53-7 (0)"},
		{"leap day", "en_US", "UTC", "2024-02-29T12:00:00Z", "%Y-%j", "2024-060"},
		{"leap year end", "en_US", "UTC", "2024-12-31T12:00:00Z", "%Y-%j", "2024-366"},
		{"spring DST", "en_US", "America/Los_Angeles", "2024-03-11T07:30:00Z", "%Y-%j %G-W%V-%u", "2024-071 2024-W11-1"},
		{"autumn DST", "en_US", "America/Los_Angeles", "2024-11-04T08:30:00Z", "%Y-%j %G-W%V-%u", "2024-309 2024-W45-1"},
		{"early year", "en_US", "UTC", "0099-01-02T12:00:00Z", "%Y-%j", "0099-002"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := (proto.EmulationSetLocaleOverride{Locale: test.locale}).Call(page); err != nil {
				t.Fatal(err)
			}
			if err := (proto.EmulationSetTimezoneOverride{TimezoneID: test.timezone}).Call(page); err != nil {
				t.Fatal(err)
			}
			page.MustNavigate(base + settingsPath).MustWaitLoad()
			got := page.MustEval(`(pattern, timestamp) =>
				guestbooksDateFormat.createFormatter("custom", pattern)(new Date(timestamp))
			`, test.pattern, test.timestamp).Str()
			if got != test.want {
				t.Fatalf("formatted date = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCustomDateFormatBrowser(t *testing.T) {
	user, book := featureFixture(t)
	message := Message{Name: "Visitor", Text: "A dated message", GuestbookID: book.ID, Approved: true}
	message.CreatedAt = time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC)
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	settingsPath := fmt.Sprintf("/admin/guestbook/%d/edit", book.ID)
	page, base := frontendAdminPage(t, user, settingsPath)
	if got := page.MustEval(`() => Array.from(document.getElementById("dateFormat").options, option => option.value).join(",")`).Str(); got != ",custom" {
		t.Fatalf("date selector must offer only Browser default and Custom format: %q", got)
	}
	if !page.MustElement("#custom-date-format-settings").MustProperty("hidden").Bool() {
		t.Fatal("advanced controls are visible by default")
	}
	page.MustElement("#dateFormat").MustSelect("Custom format (advanced)")
	if page.MustElement("#dateFormatPattern").MustProperty("value").Str() != defaultDateFormatPattern {
		t.Fatal("new custom formats must start with the year-month-day pattern")
	}
	page.MustElement("#dateFormatPattern").MustSelectAllText().MustInput("On %Y")
	today := page.MustEval(`() => "On " + new Date().getFullYear()`).Str()
	if page.MustElement("#date-format-preview").MustProperty("textContent").Str() != today ||
		page.MustElement("#date-format-status").MustText() != "Valid format." {
		t.Fatal("custom format has no valid, current preview")
	}
	page.MustElement("#dateFormatPattern").MustSelectAllText().MustInput("%Y %Q")
	if page.MustElement("#dateFormatPattern").MustProperty("validationMessage").Str() == "" ||
		!strings.Contains(page.MustElement("#date-format-status").MustText(), "Unsupported date token %Q") ||
		page.MustEval(`() => document.getElementById("guestbook-edit-form").checkValidity()`).Bool() {
		t.Fatal("invalid format was not explained or blocked")
	}
	if !strings.Contains(page.MustElement("#date-format-preview").MustText(), "Unavailable") {
		t.Fatal("invalid format left a misleading preview")
	}

	pattern := `%Y <img id=date-injected src=x onerror=alert(1)> "'\ </script>`
	page.MustElement("#dateFormatPattern").MustSelectAllText().MustInput(pattern)
	today = strings.ReplaceAll(pattern, "%Y", page.MustEval(`() => String(new Date().getFullYear())`).Str())
	if page.MustElement("#date-format-preview").MustProperty("textContent").Str() != today || page.MustHas("#date-injected") {
		t.Fatal("preview did not treat the custom pattern as literal text")
	}
	submitBrowserForm(page, "#guestbook-edit-form")
	page.MustNavigate(base + settingsPath).MustWaitLoad()
	if page.MustElement("#dateFormat").MustProperty("value").Str() != "custom" ||
		page.MustElement("#dateFormatPattern").MustProperty("value").Str() != pattern || page.MustHas("#date-injected") {
		t.Fatal("custom mode/pattern did not safely survive saving")
	}
	page.MustElement("#date-format-reference summary").MustClick()
	if !strings.Contains(page.MustElement("#date-format-reference").MustText(), "%G") ||
		!page.MustHas("#date-format-reference a[href^='https://developer.mozilla.org/']") {
		t.Fatal("format reference or MDN context is missing")
	}
	page.MustSetViewport(375, 900, 1, false)
	if page.MustEval(`() => document.documentElement.scrollWidth > window.innerWidth`).Bool() {
		t.Fatal("date-format controls or reference overflow a narrow screen")
	}
	page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
	waitForGuestbook(page)
	if got := page.MustElement(".guestbook-message small").MustProperty("textContent").Str(); got != " - "+strings.ReplaceAll(pattern, "%Y", "2026") {
		t.Fatalf("custom message date = %q", got)
	}
	if page.MustHas("#date-injected") {
		t.Fatal("custom message date became HTML")
	}

	page.MustNavigate(base + settingsPath).MustWaitLoad()
	page.MustElement("#dateFormat").MustSelect("Browser default")
	if !page.MustElement("#dateFormatPattern").MustProperty("disabled").Bool() {
		t.Fatal("inactive pattern was not disabled")
	}
	submitBrowserForm(page, "#guestbook-edit-form")
	var saved Guestbook
	if err := db.First(&saved, book.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.DateFormat != DateFormatBrowser || saved.DateFormatPattern != pattern {
		t.Fatal("turning off custom formatting erased its pattern")
	}
}

func TestDateFormatSettingsLayoutBrowser(t *testing.T) {
	user, book := featureFixture(t)
	page, _ := frontendAdminPage(t, user, fmt.Sprintf("/admin/guestbook/%d/edit", book.ID))
	for _, width := range []int{1200, 768, 375, 320} {
		page.MustSetViewport(width, 1000, 1, false)
		for _, state := range []struct {
			name, mode, pattern string
		}{
			{"browser", "Browser default", ""},
			{"custom", "Custom format (advanced)", "%Y-%m-%d"},
			{"invalid", "Custom format (advanced)", "%Y %Q"},
		} {
			t.Run(fmt.Sprintf("%d/%s", width, state.name), func(t *testing.T) {
				page.MustElement("#dateFormat").MustSelect(state.mode)
				if state.pattern != "" {
					page.MustElement("#dateFormatPattern").MustSelectAllText().MustInput(state.pattern)
				}
				page.MustEval(`() => {document.getElementById("date-format-reference").open = true;}`)
				geometry := page.MustEval(`() => {
					const section = document.getElementById("date-display-settings");
					const fields = section.querySelector(".date-format-fields").getBoundingClientRect();
					const panel = section.querySelector(".date-format-preview-panel");
					const preview = panel.getBoundingClientRect();
					const output = document.getElementById("date-format-preview").getBoundingClientRect();
					const status = document.getElementById("date-format-status");
					const statusBounds = status.getBoundingClientRect();
					const reference = document.getElementById("date-format-reference");
					return {
						overflow: document.documentElement.scrollWidth > innerWidth,
						separate: document.getElementById("requiresApproval").closest(".form-section") !== section &&
							document.getElementById("websiteURL").closest(".form-section") !== section,
						beforeGap: section.getBoundingClientRect().top - section.previousElementSibling.getBoundingClientRect().bottom,
						afterGap: section.nextElementSibling.getBoundingClientRect().top - section.getBoundingClientRect().bottom,
						aligned: innerWidth <= 768
							? preview.top - fields.bottom >= 16 && Math.abs(preview.left - fields.left) < 1
							: preview.left - fields.right >= 16 && Math.abs(preview.top - fields.top) < 1,
						statusHidden: status.hidden,
						statusGrouped: panel.contains(status) && !status.classList.contains("callout"),
						statusGap: status.hidden ? 0 : statusBounds.top - output.bottom,
						statusHeight: statusBounds.height,
						referenceHidden: reference.hidden,
						referenceGap: reference.hidden ? 0 : reference.getBoundingClientRect().top - Math.max(fields.bottom, preview.bottom)
					};
				}`)
				custom := state.pattern != ""
				if geometry.Get("overflow").Bool() || !geometry.Get("separate").Bool() ||
					geometry.Get("beforeGap").Num() < 20 || geometry.Get("afterGap").Num() < 20 ||
					!geometry.Get("aligned").Bool() || !geometry.Get("statusGrouped").Bool() {
					t.Fatalf("date settings are not visually separated: %s", geometry.JSON("", "  "))
				}
				if geometry.Get("statusHidden").Bool() == custom || geometry.Get("referenceHidden").Bool() == custom ||
					custom && (geometry.Get("statusGap").Num() < 8 || geometry.Get("referenceGap").Num() < 16) ||
					state.name == "custom" && geometry.Get("statusHeight").Num() > 40 {
					t.Fatalf("date feedback/reference layout is incorrect: %s", geometry.JSON("", "  "))
				}
			})
		}
	}
}

func TestCustomDateFormatLiteralLayoutBrowser(t *testing.T) {
	_, book := featureFixture(t)
	message := Message{Name: "Visitor", Text: "A dated message", GuestbookID: book.ID, Approved: true}
	message.CreatedAt = time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC)
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	page, base := featureBrowser(t)
	if err := (proto.EmulationSetTimezoneOverride{TimezoneID: "UTC"}).Call(page); err != nil {
		t.Fatal(err)
	}
	page.MustSetViewport(375, 900, 1, false)
	for _, test := range []struct {
		name, pattern, want string
	}{
		{"literal spacing", "%Y    %m    %e", "2026    01     2"},
		{"long literal", strings.Repeat("x", 126) + "%Y", strings.Repeat("x", 126) + "2026"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := db.Model(&book).Updates(map[string]any{
				"date_format": DateFormatCustom, "date_format_pattern": test.pattern,
			}).Error; err != nil {
				t.Fatal(err)
			}
			page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
			waitForGuestbook(page)
			if got := page.MustElement(".guestbook-message small").MustProperty("innerText").Str(); got != " - "+test.want {
				t.Errorf("visible date = %q, want %q", got, " - "+test.want)
			}
			if page.MustEval(`() => document.documentElement.scrollWidth > innerWidth`).Bool() {
				t.Error("custom date overflows the guestbook on a narrow screen")
			}
		})
	}
}

func TestInvalidStoredDateFormatBrowser(t *testing.T) {
	user, book := featureFixture(t)
	if err := db.Model(&book).Updates(map[string]any{
		"date_format": DateFormatCustom, "date_format_pattern": "%Q",
	}).Error; err != nil {
		t.Fatal(err)
	}
	message := Message{Name: "Visitor", Text: "Keep displaying this message", GuestbookID: book.ID, Approved: true}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	page, base := featureBrowser(t)
	page.MustNavigate(base + fmt.Sprintf("/guestbook/%d", book.ID))
	waitForGuestbook(page)
	if !strings.Contains(page.MustElement("#guestbooks___date-format-warning").MustText(), "needs repair") {
		t.Fatal("invalid stored format was silently ignored")
	}
	want := page.MustEval(`timestamp => " - " + new Date(timestamp).toLocaleDateString()`, message.CreatedAt.Format(time.RFC3339Nano)).Str()
	if got := page.MustElement(".guestbook-message small").MustProperty("textContent").Str(); got != want {
		t.Fatalf("invalid stored format did not preserve readable dates: %q, want %q", got, want)
	}
	page.MustSetCookies(&proto.NetworkCookieParam{Name: string(AdminTokenCookieName), Value: user.SessionToken, URL: base})
	page.MustNavigate(base + fmt.Sprintf("/admin/guestbook/%d/edit", book.ID)).MustWaitLoad()
	if !strings.Contains(page.MustElement("#date-format-status").MustText(), "Unsupported date token %Q") {
		t.Fatal("owner cannot identify the invalid stored format")
	}
}
