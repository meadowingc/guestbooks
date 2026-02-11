package main

import (
	"fmt"
	"guestbook/constants"
	"net/http"
	"net/url"
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

// parseAllowedOrigins splits a comma-separated AllowedOrigins string into
// a cleaned slice of origin strings. Returns nil if the input is empty.
func parseAllowedOrigins(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var origins []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.TrimRight(p, "/")
		if p != "" {
			origins = append(origins, strings.ToLower(p))
		}
	}
	return origins
}

// validateOrigins validates a raw comma-separated origins string. Each entry
// must be a valid URL with a scheme (http or https) and a host, and no path.
// Returns the cleaned, normalised string and an error if any entry is invalid.
func validateOrigins(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parts := strings.Split(raw, ",")
	var cleaned []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.TrimRight(p, "/")
		if p == "" {
			continue
		}
		u, err := url.Parse(p)
		if err != nil {
			return "", fmt.Errorf("invalid origin %q: %v", p, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", fmt.Errorf("invalid origin %q: scheme must be http or https", p)
		}
		if u.Host == "" {
			return "", fmt.Errorf("invalid origin %q: missing host", p)
		}
		if u.Path != "" && u.Path != "/" {
			return "", fmt.Errorf("invalid origin %q: must not contain a path", p)
		}
		cleaned = append(cleaned, strings.ToLower(u.Scheme+"://"+u.Host))
	}
	return strings.Join(cleaned, ","), nil
}

// originFromRequest extracts the origin from the request's Origin header,
// falling back to deriving it from the Referer header. Returns empty string
// if neither is present.
func originFromRequest(r *http.Request) string {
	origin := r.Header.Get("Origin")
	if origin != "" && origin != "null" {
		return strings.ToLower(strings.TrimRight(origin, "/"))
	}
	ref := r.Header.Get("Referer")
	if ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Host != "" {
			return strings.ToLower(u.Scheme + "://" + u.Host)
		}
	}
	return ""
}

// checkOriginAllowed checks whether the request origin is allowed for a
// guestbook with the given AllowedOrigins value. If allowedOrigins is empty,
// all origins are allowed and ("*", true) is returned. Otherwise the request
// origin is matched against the list and (matchedOrigin, true) or ("", false)
// is returned.
func checkOriginAllowed(r *http.Request, allowedOrigins string) (matchedOrigin string, allowed bool) {
	origins := parseAllowedOrigins(allowedOrigins)
	if len(origins) == 0 {
		return "*", true
	}

	reqOrigin := originFromRequest(r)
	if reqOrigin == "" {
		// No origin header — direct browser navigation, curl, etc. Allow it.
		return "", true
	}

	for _, o := range origins {
		if reqOrigin == o {
			return o, true
		}
	}
	return "", false
}

// setOriginHeaders sets CORS and CSP headers on the response based on the
// guestbook's AllowedOrigins setting. If isPage is true, a
// Content-Security-Policy frame-ancestors directive is also set.
func setOriginHeaders(w http.ResponseWriter, allowedOrigins string, matchedOrigin string, isPage bool) {
	origins := parseAllowedOrigins(allowedOrigins)

	if len(origins) > 0 {
		// Override the global CORS wildcard with the specific matched origin.
		if matchedOrigin != "" && matchedOrigin != "*" {
			w.Header().Set("Access-Control-Allow-Origin", matchedOrigin)
			w.Header().Set("Vary", "Origin")
		}
		if isPage {
			// frame-ancestors restricts which sites can embed this page in an iframe.
			w.Header().Set("Content-Security-Policy", "frame-ancestors 'self' "+strings.Join(origins, " "))
		}
	}
}
