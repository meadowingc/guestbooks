package main

import (
	"fmt"
	"guestbook/constants"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var compiledCssCheckPatterns = []*regexp.Regexp{
	regexp.MustCompile(`<\s*style[^>]*>`),
	regexp.MustCompile(`<\s*/\s*style\s*>`),
	regexp.MustCompile(`<\s*script[^>]*>`),
	regexp.MustCompile(`<\s*/\s*script\s*>`),
	regexp.MustCompile(`@import\s+url\([^\)]+\)`),
	regexp.MustCompile(`expression\([^\)]+\)`),
	regexp.MustCompile(`javascript:`),
	regexp.MustCompile(`behavior:`),
	regexp.MustCompile(`url\([^\)]+\)`),
	regexp.MustCompile(`data:`),
	regexp.MustCompile(`mhtml:`),
	regexp.MustCompile(`vbscript:`),
	regexp.MustCompile(`livescript:`),
	regexp.MustCompile(`moz-binding:`),
	regexp.MustCompile(`chrome:`),
	regexp.MustCompile(`<svg[^>]*>.*?</svg>`),
}

func validateCSS(css string) (ok bool, message string) {
	if len(css) > constants.MAX_CSS_LENGTH {
		return false, "Custom CSS is too long. Maximum allowed length is " + fmt.Sprint(constants.MAX_CSS_LENGTH) + " characters. Your CSS is " + fmt.Sprint(len(css)) + " characters."
	}

	for _, re := range compiledCssCheckPatterns {
		if re.MatchString(css) {
			return false, "Custom CSS contains potentially dangerous content. Violating pattern: " + re.String()
		}
	}

	return true, ""
}

// CompareCSSWithThemes compares the submitted CSS with built-in themes and
// returns the name of the theme if it matches one of the built-in themes.
// Returns an empty string if the submitted CSS does not match any built-in
// theme (so it is a custom CSS).
func CompareCSSWithThemes(submittedCSS string) (string, error) {
	submittedCSS = strings.TrimSpace(submittedCSS)
	submittedCSS = strings.ReplaceAll(submittedCSS, "\r\n", "\n")

	// Check if top comment is from a built-in theme, if not exit early
	if !strings.HasPrefix(submittedCSS, "/* [::Built in::] ") {
		return "", nil
	}

	files, err := os.ReadDir(constants.BUILT_IN_THEMES_DIR)
	if err != nil {
		return "", err
	}

	for _, file := range files {
		if filepath.Ext(file.Name()) == ".css" {
			content, err := os.ReadFile(filepath.Join(constants.BUILT_IN_THEMES_DIR, file.Name()))
			if err != nil {
				return "", err
			}

			trimmedContent := strings.TrimSpace(string(content))
			if submittedCSS == trimmedContent {
				return file.Name(), nil
			}
		}
	}

	return "", nil
}
