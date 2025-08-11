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

// stripAndValidateFontFaces removes valid @font-face blocks (so generic URL checks don't block them)
// after validating they only contain allowed properties and safe URLs. If any block is invalid,
// it returns the original CSS and an error.
func stripAndValidateFontFaces(css string) (string, bool, string) {
	// Match @font-face { ... } blocks, case-insensitive, dot matches newline
	reFontFace := regexp.MustCompile(`(?is)@font-face\s*\{[^}]*\}`)

	firstErr := ""
	valid := true

	cleaned := reFontFace.ReplaceAllStringFunc(css, func(block string) string {
		if ok, msg := validateFontFaceBlock(block); !ok {
			valid = false
			if firstErr == "" {
				firstErr = msg
			}
			return block // keep as-is so user can edit; caller will reject anyway
		}
		// Replace with harmless comment to keep length similar and positions reasonable
		return "/* __ALLOWED_FONT_FACE__ */"
	})

	if !valid {
		return css, false, firstErr
	}
	return cleaned, true, ""
}

func validateFontFaceBlock(block string) (bool, string) {
	// Minimal, sensible checks: disallow clearly dangerous constructs inside @font-face.
	lower := strings.ToLower(block)
	banned := []string{
		"<style", "<script", "expression(", "javascript:", "vbscript:", "livescript:",
		"behavior:", "mhtml:", "moz-binding:", "chrome:", "@import", "data:", "http:",
	}
	for _, b := range banned {
		if strings.Contains(lower, b) {
			return false, "@font-face contains disallowed content: '" + b + "'"
		}
	}
	return true, ""
}

func validateCSS(css string) (ok bool, message string) {
	if len(css) > constants.MAX_CSS_LENGTH {
		return false, "Custom CSS is too long. Maximum allowed length is " + fmt.Sprint(constants.MAX_CSS_LENGTH) + " characters. Your CSS is " + fmt.Sprint(len(css)) + " characters."
	}

	// Allow safe custom fonts via @font-face blocks.
	// We temporarily remove valid @font-face blocks (validated strictly) so the remaining CSS
	// can still be checked against the generic dangerous patterns (including url()).
	cleanedCSS, okFont, msg := stripAndValidateFontFaces(css)
	if !okFont {
		return false, msg
	}

	for _, re := range compiledCssCheckPatterns {
		if re.MatchString(cleanedCSS) {
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
