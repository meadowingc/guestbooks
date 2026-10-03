package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"guestbook/constants"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

const (
	maxNameCharacters  = 200
	maxWebsiteBytes    = 2048
	maxSubmissionBytes = 64 << 10
)

func parsePositiveID(value string) (uint, error) {
	if value == "" {
		return 0, errors.New("ID is required")
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, errors.New("ID must be a positive decimal integer")
		}
	}
	id, err := strconv.ParseUint(value, 10, 32)
	if err != nil || id == 0 {
		return 0, errors.New("ID is outside the supported range")
	}
	return uint(id), nil
}

func validRouteIDs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, key := range []string{"guestbookID", "messageID"} {
			if value := chi.URLParam(r, key); value != "" {
				id, err := parsePositiveID(value)
				if err != nil {
					http.Error(w, "Invalid "+key, http.StatusBadRequest)
					return
				}
				chi.RouteContext(r.Context()).URLParams.Add(key, strconv.FormatUint(uint64(id), 10))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func normalizedMessageText(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n"))
}

func messageInputError(name, text, website string) error {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxNameCharacters {
		return fmt.Errorf("name must contain 1 to %d characters", maxNameCharacters)
	}
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > constants.MAX_MESSAGE_LENGTH {
		return fmt.Errorf("message must contain 1 to %d characters", constants.MAX_MESSAGE_LENGTH)
	}
	if !utf8.ValidString(website) || len(website) > maxWebsiteBytes {
		return fmt.Errorf("website must contain at most %d bytes", maxWebsiteBytes)
	}
	return nil
}

func challengeSettingsError(question, answer string) error {
	if strings.TrimSpace(question) != "" && strings.TrimSpace(answer) == "" {
		return errors.New("enter an expected answer when a challenge question is enabled")
	}
	return nil
}

func recordLookupError(w http.ResponseWriter, err error, resource string) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		http.Error(w, resource+" not found", http.StatusNotFound)
		return
	}
	log.Printf("Error looking up %s: %v", resource, err)
	http.Error(w, "Storage is temporarily unavailable", http.StatusInternalServerError)
}
