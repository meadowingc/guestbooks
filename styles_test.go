package main

import (
	"fmt"
	"guestbook/constants"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestCSSPolicy(t *testing.T) {
	valid := []string{
		"",
		`/* selectors, strings and escapes */ .a\:b:hover, [title="a:b"] { content: "a;{}"; color: red; }`,
		`:root { --accent: #cfc; --text: "a{};b"; } @media (max-width: 700px) { body { color: var(--accent); } }`,
		`@supports (display: grid) { .grid { display: grid; } } @keyframes fade { from { opacity: 0; } to { opacity: 1; } }`,
		`@font-face { font-family: "Example"; src: local("Example"), url("https://fonts.example.test/font.woff2") format("woff2"); font-display: swap; }`,
		`@FONT-FACE { FONT-FAMILY: Example; SRC: URL(HTTPS://fonts.example.test/Font.woff); unicode-range: U+0000-00FF; }`,
		`@\66 ont-face { font-family: Example; src: u\72l("https://fonts.example.test/font.woff2"); }`,
		`@font-face { font-family: "braces } in strings"; src: url('https://fonts.example.test/a.woff2'); }`,
		`body { color: red; @media (max-width: 700px) { color: blue; } }`,
		`body { @supports (display: grid) { display: grid; @media (min-width: 300px) { color: blue; } } }`,
		`body { @container (min-width: 300px) { --accent: blue; color: var(--accent); } }`,
		`@container (min-width: 300px) { body { color: blue; } }`,
		`@scope (.container) { :scope { color: blue; @starting-style { opacity: 0; } } }`,
		`body { @layer theme { color: blue; .child { color: red; } #child { color: green; } [title] { color: black; } } }`,
		`@MEDIA (min-width: 300px) { body { @\73 upports (display: grid) { color: blue; } } }`,
		`.behavior { color: red; } .behavior:hover { color: blue; }`,
		`behavior:hover { color: blue; } body { font-family: behavior; animation-name: -moz-binding; --binding: moz-binding; }`,
		`@supports (behavior: none) { .behavior { font-family: behavior; } }`,
		`:root { --blocks: { color: red; }; --content: "behavior: expression()"; }`,
	}
	for _, source := range valid {
		t.Run("valid/"+source, func(t *testing.T) {
			if ok, reason := validateCSS(source); !ok {
				t.Fatalf("valid stylesheet rejected: %s", reason)
			}
		})
	}
	invalid := []string{
		`</StYlE><script>alert(1)</script>`,
		`/* </style> */ body { color: red; }`,
		`@font-face { font-family: "</style><script>alert(1)</script>"; src: url(https://fonts.example.test/font.woff2); }`,
		`@import "https://example.test/style.css";`,
		`@\69mport url(https://example.test/style.css);`,
		`@namespace x "https://example.test/";`,
		`body { background: URL(https://example.test/image); }`,
		`body { background: u\72l(https://example.test/image); }`,
		`body { background: image-set("https://example.test/image" 1x); }`,
		`:root { --image: url(https://example.test/image); }`,
		`body { color: e\78pression(alert(1)); }`,
		`body { b\65havior: "something"; }`,
		`body { -moz-binding: "something"; }`,
		`@font-face { src: url(http://example.test/font.woff); }`,
		`@font-face { src: url(data:font/woff;base64,abcd); }`,
		`@font-face { src: url(j\61vascript:alert); }`,
		`@font-face { src: url(https://user:password@example.test/font); }`,
		`@font-face { background: url(https://example.test/image); }`,
		`@font-face { @media screen { src: url(https://example.test/font); } }`,
		`body { @media screen { behavior: "something"; } }`,
		`body { @supports (display: grid) { b\65havior: "something"; } }`,
		`body { @container (min-width: 1px) { -moz-binding: "something"; } }`,
		`@container (min-width: 1px) { body { -moz-binding: "something"; } }`,
		`@scope (.container) { .child { behavior: "something"; } }`,
		`@starting-style { body { behavior: "something"; } }`,
		`@future-rule { body { behavior: "something"; } }`,
		`body { @media screen { background: url(https://example.test/image); } }`,
		`body { @supports (display: grid) { --image: url(https://example.test/image); } }`,
		`body { @container (min-width: 1px) { color: expression(alert(1)); } }`,
		`body { @media screen { @import "https://example.test/style.css"; } }`,
		`body { @media screen { color red; } }`,
		`body { color: red;`,
		`body { color red; }`,
		`/* missing ending`,
		`body { content: "missing ending; }`,
		`body { color: rgb(1, 2, 3]; }`,
		"body { color: \x00red; }",
		string([]byte{0xff}),
		strings.Repeat(" ", constants.MAX_CSS_LENGTH+1),
	}
	for index, source := range invalid {
		t.Run(fmt.Sprintf("invalid/%d", index), func(t *testing.T) {
			if ok, reason := validateCSS(source); ok || reason == "" {
				t.Fatalf("unsafe/malformed CSS was not explicitly rejected: %q", source)
			}
		})
	}
}

func TestCSSSettingsInputReferences(t *testing.T) {
	for _, name := range []string{"gray-bear", "webcomic", "cherry-mint", "cabernete", "peaceful-sky"} {
		marker := "<<built__in>>" + name + ".css<</built__in>>"
		if ok, reason := validateGuestbookCSSInput(marker); !ok {
			t.Fatalf("valid settings reference rejected: %s", reason)
		}
		if ok, _ := validateCSS(marker); ok {
			t.Fatal("settings reference was accepted as delivered CSS")
		}
		if matched, err := CompareCSSWithThemes(marker); err != nil || matched != "" {
			t.Fatalf("existing marker must survive settings canonicalization unchanged: %q, %v", matched, err)
		}
		for _, suffix := range []string{"body{color:red}", "\nbody { color: red; }", "junk", "<<built__in>>webcomic.css<</built__in>>"} {
			if ok, reason := validateGuestbookCSSInput(marker + suffix); ok || reason == "" {
				t.Fatalf("reference with trailing CSS/junk accepted: %q", marker+suffix)
			}
		}
	}
	for _, source := range []string{
		"<<built__in>>../css/admin-styles.css<</built__in>>",
		"<<built__in>>missing.css<</built__in>>",
		"<<built__in>>gray-bear.css",
		"<<built__in>>gray-bear.css<</built__in>>extra",
		`body { background: url(https://example.test/image); }`,
		`body { color: red;`,
	} {
		if ok, reason := validateGuestbookCSSInput(source); ok || reason == "" {
			t.Fatalf("invalid settings CSS accepted: %q", source)
		}
	}
	for _, source := range []string{
		"",
		`body { color: red; }`,
		`body { color: red; @media (min-width: 300px) { color: blue; } }`,
	} {
		if ok, reason := validateGuestbookCSSInput(source); !ok {
			t.Fatalf("valid raw settings CSS rejected: %s", reason)
		}
	}
}

func TestCSSBuiltInThemesRemainValid(t *testing.T) {
	themes, err := filepath.Glob(filepath.Join(constants.BUILT_IN_THEMES_DIR, "*.css"))
	if err != nil || len(themes) != 5 {
		t.Fatalf("expected five built-in themes: %v (%d)", err, len(themes))
	}
	for _, path := range themes {
		t.Run(filepath.Base(path), func(t *testing.T) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if ok, reason := validateCSS(string(content)); !ok {
				t.Fatalf("built-in theme rejected: %s", reason)
			}
			if ok, reason := validateGuestbookCSSInput(string(content)); !ok {
				t.Fatalf("built-in CSS settings input rejected: %s", reason)
			}
			name, err := CompareCSSWithThemes(string(content))
			if err != nil || name != filepath.Base(path) {
				t.Fatalf("theme comparison lost built-in theme: %q, %v", name, err)
			}
			if name, ok := validBuiltInTheme("<<built__in>>" + filepath.Base(path) + "<</built__in>>"); !ok || name != filepath.Base(path) {
				t.Fatal("valid built-in reference rejected")
			}
		})
	}
	for _, source := range []string{
		"<<built__in>>../css/admin-styles.css<</built__in>>",
		"<<built__in>>missing.css<</built__in>>",
		"<<built__in>>gray-bear.css",
		"<<built__in>>gray-bear.css<</built__in>>extra",
	} {
		if _, ok := validBuiltInTheme(source); ok {
			t.Fatalf("invalid reference accepted: %q", source)
		}
	}
}

func TestCSSResourceStoredValidation(t *testing.T) {
	user, book := featureFixture(t)
	router := chi.NewRouter()
	router.Get("/resources/css/guestbook/{guestbookID}.css", GuestbookStyles)
	request := func(id string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/resources/css/guestbook/"+id+".css", nil))
		if recorder.Header().Get("Content-Type") != "text/css; charset=utf-8" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("stylesheet lost typed nosniff response: %v", recorder.Header())
		}
		return recorder
	}
	for _, source := range []string{
		`body { color: red; }`,
		`body { color: red; @media (max-width: 700px) { color: blue; } }`,
		`.behavior { color: red; }`,
		`@font-face { font-family: Example; src: url(https://fonts.example.test/a.woff2); }`,
		"<<built__in>>gray-bear.css<</built__in>>",
	} {
		if err := db.Model(&book).Update("custom_page_css", source).Error; err != nil {
			t.Fatal(err)
		}
		response := request(fmt.Sprint(book.ID))
		if response.Code != http.StatusOK {
			t.Fatalf("valid stored CSS returned %d: %s", response.Code, response.Body.String())
		}
		if !strings.HasPrefix(source, "<<built__in>>") && response.Body.String() != source {
			t.Fatal("valid old CSS was transformed")
		}
	}
	unsafe := `@font-face { font-family: "</style><script id='stored'>alert(1)</script>"; }`
	if err := db.Model(&book).Update("custom_page_css", unsafe).Error; err != nil {
		t.Fatal(err)
	}
	response := request(fmt.Sprint(book.ID))
	if response.Code != http.StatusUnprocessableEntity || strings.Contains(response.Body.String(), "<script") || !strings.Contains(response.Body.String(), "repair") {
		t.Fatalf("stored CSS did not fail safely: %d %s", response.Code, response.Body.String())
	}
	var saved Guestbook
	if err := db.First(&saved, book.ID).Error; err != nil || saved.CustomPageCSS != unsafe {
		t.Fatalf("delivery changed stored CSS: %v", err)
	}
	for _, id := range []string{"0", "-1", "1suffix", "184467440737095516160"} {
		if got := request(id).Code; got != http.StatusBadRequest {
			t.Errorf("invalid ID %q returned %d", id, got)
		}
	}
	if err := db.Delete(&user).Error; err != nil {
		t.Fatal(err)
	}
	if got := request(fmt.Sprint(book.ID)).Code; got != http.StatusNotFound {
		t.Fatalf("deleted owner's stylesheet visible: %d", got)
	}
}
