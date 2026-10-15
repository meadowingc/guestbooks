package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

const defaultDateFormatPattern = "%Y-%m-%d"

var dateFormatValidationCases = []struct {
	name, pattern string
	valid         bool
}{
	{"calendar", "%Y-%m-%d", true},
	{"compact", "%Y%m%d", true},
	{"week", "%G-W%V-%u", true},
	{"ordinal", "%Y-%j", true},
	{"all tokens", "%Y %y %m %d %e %b %B %a %A %j %G %g %V %u %w %%", true},
	{"literal text", `"On" %A, %B %d \ %Y (100%%)`, true},
	{"HTML is literal", `%Y <img id=date-injected src=x onerror=alert(1)> "'\ </script>`, true},
	{"unicode boundary", strings.Repeat("😀", maxDateFormatLength-2) + "%Y", true},
	{"too long", strings.Repeat("😀", maxDateFormatLength-1) + "%Y", false},
	{"empty", "", false},
	{"no tokens", "YYYY-MM-DD", false},
	{"only escaped percent", "%%Y", false},
	{"unknown token", "%Y-%Q", false},
	{"time token", "%Y %H", false},
	{"padding flag", "%Y %-d", false},
	{"trailing percent", "%Y %", false},
	{"newline", "%Y\n%m", false},
	{"carriage return", "%Y\r%m", false},
	{"tab", "%Y\t%m", false},
	{"null", "%Y\x00", false},
	{"control", "%Y\u0085", false},
	{"line separator", "%Y\u2028", false},
	{"paragraph separator", "%Y\u2029", false},
	{"invalid unicode", "%Y" + string([]byte{0xff}), false},
}

func TestDateFormatPatternValidation(t *testing.T) {
	for _, test := range dateFormatValidationCases {
		t.Run(test.name, func(t *testing.T) {
			err := validateDateFormatPattern(test.pattern)
			if (err == nil) != test.valid {
				t.Fatalf("validation error = %v, want valid=%v", err, test.valid)
			}
		})
	}
}

func TestCustomDateFormatSettings(t *testing.T) {
	user, book := featureFixture(t)
	router := initRouter()
	editPath := fmt.Sprintf("/admin/guestbook/%d/edit", book.ID)
	form := url.Values{
		"websiteURL": {book.WebsiteURL}, "dateFormat": {"custom"}, "dateFormatPattern": {"%G-W%V-%u"},
	}
	for _, path := range []string{"/admin/guestbook/new", editPath} {
		requireStatus(t, featureRequest(router, "POST", path, form, &user, false), 303)
	}
	var created Guestbook
	if err := db.Where("admin_user_id = ?", user.ID).Last(&created).Error; err != nil {
		t.Fatal(err)
	}
	if created.ID == book.ID || created.DateFormat != DateFormatCustom || created.DateFormatPattern != "%G-W%V-%u" {
		t.Fatal("custom format was not saved by the create handler")
	}
	for _, values := range []url.Values{
		{"websiteURL": {book.WebsiteURL}},
		{"websiteURL": {book.WebsiteURL}, "dateFormat": {""}},
		{"websiteURL": {book.WebsiteURL}, "dateFormat": {"custom"}},
	} {
		requireStatus(t, featureRequest(router, "POST", editPath, values, &user, false), 303)
		if err := db.First(&book, book.ID).Error; err != nil {
			t.Fatal(err)
		}
		if book.DateFormatPattern != "%G-W%V-%u" {
			t.Fatal("older forms or mode changes erased the saved pattern")
		}
	}
	for _, test := range dateFormatValidationCases {
		if test.valid {
			continue
		}
		t.Run(test.name, func(t *testing.T) {
			form.Set("dateFormatPattern", test.pattern)
			for _, path := range []string{"/admin/guestbook/new", editPath} {
				response := featureRequest(router, "POST", path, form, &user, false)
				requireStatus(t, response, 400)
				if !strings.Contains(response.Body.String(), validateDateFormatPattern(test.pattern).Error()) {
					t.Fatal("invalid format was not explained")
				}
			}
		})
	}
	var count int64
	if err := db.Model(&Guestbook{}).Where("admin_user_id = ?", user.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&book, book.ID).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 || book.DateFormat != DateFormatCustom || book.DateFormatPattern != "%G-W%V-%u" {
		t.Fatal("invalid formats created or modified guestbooks")
	}
}

func TestCustomDateFormatEscaping(t *testing.T) {
	user, book := featureFixture(t)
	router := initRouter()
	pattern := `%Y <img id=date-injected src=x onerror=alert(1)> "'\ </script>`
	editPath := fmt.Sprintf("/admin/guestbook/%d/edit", book.ID)
	form := url.Values{
		"websiteURL": {book.WebsiteURL}, "dateFormat": {"custom"}, "dateFormatPattern": {pattern},
	}
	requireStatus(t, featureRequest(router, "POST", editPath, form, &user, false), 303)
	response := featureRequest(router, "GET", editPath, nil, &user, false)
	requireStatus(t, response, 200)
	if strings.Contains(response.Body.String(), "<img id=date-injected") {
		t.Fatal("saved pattern became HTML in settings")
	}
	response = featureRequest(router, "GET", fmt.Sprintf("/resources/js/embed_script/%d/script.js", book.ID), nil, nil, false)
	requireStatus(t, response, 200)
	script := response.Body.String()
	if strings.Contains(script, "<img") || strings.Contains(script, "</script>") {
		t.Fatal("pattern was not JSON-escaped in the generated script")
	}
	_, config, found := strings.Cut(script, "var config = ")
	if !found {
		t.Fatal("embed configuration is missing")
	}
	config, _, found = strings.Cut(config, ";\n")
	if !found {
		t.Fatal("embed configuration is incomplete")
	}
	var decoded struct {
		DateFormat        DateFormat `json:"dateFormat"`
		DateFormatPattern string     `json:"dateFormatPattern"`
	}
	if err := json.Unmarshal([]byte(config), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.DateFormat != DateFormatCustom || decoded.DateFormatPattern != pattern {
		t.Fatal("pattern did not round-trip through the script configuration")
	}
}
