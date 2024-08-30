package main

import (
	"guestbook/constants"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func validateCSS(css string) (ok bool, message string) {
	patterns := []string{
		`<\s*style[^>]*>`,
		`<\s*/\s*style\s*>`,
		`<\s*script[^>]*>`,
		`<\s*/\s*script\s*>`,
		`@import\s+url\([^\)]+\)`,
		`expression\([^\)]+\)`,
		`javascript:`,
		`behavior:`,
		`url\([^\)]+\)`,
		`data:`,
		`mhtml:`,
		`vbscript:`,
		`livescript:`,
		`moz-binding:`,
		`chrome:`,
		`<svg[^>]*>.*?</svg>`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if re.MatchString(css) {
			return false, "Custom CSS contains potentially dangerous content. Violating pattern: " + pattern
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

	// check if top comment is from a built-in theme, if not exit early
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
