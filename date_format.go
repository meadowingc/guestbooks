package main

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type DateFormat string

const (
	DateFormatBrowser DateFormat = ""
	DateFormatCustom  DateFormat = "custom"

	maxDateFormatLength = 128
)

func validateDateFormatPattern(pattern string) error {
	if !utf8.ValidString(pattern) {
		return errors.New("use valid Unicode characters in the date format")
	}
	if utf8.RuneCountInString(pattern) > maxDateFormatLength {
		return fmt.Errorf("use at most %d characters in the date format", maxDateFormatLength)
	}
	if strings.IndexFunc(pattern, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
	}) >= 0 {
		return errors.New("date formats must be a single line without control characters")
	}
	characters := []rune(pattern)
	hasDateToken := false
	for i := 0; i < len(characters); i++ {
		if characters[i] != '%' {
			continue
		}
		i++
		if i == len(characters) {
			return errors.New("a trailing % needs a token; use %% for a literal percent sign")
		}
		token := characters[i]
		if !strings.ContainsRune("YymdebBaAjGVguw%", token) {
			return fmt.Errorf("unsupported date token %%%c. Use %%%% for a literal percent sign", token)
		}
		hasDateToken = hasDateToken || token != '%'
	}
	if !hasDateToken {
		return errors.New("include at least one date token, such as %Y")
	}
	return nil
}
