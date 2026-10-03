package main

import (
	"errors"
	"guestbook/constants"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

func validBuiltInTheme(source string) (string, bool) {
	const prefix, suffix = "<<built__in>>", "<</built__in>>"
	if !strings.HasPrefix(source, prefix) || !strings.HasSuffix(source, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(source, prefix), suffix)
	switch name {
	case "gray-bear.css", "webcomic.css", "cherry-mint.css", "cabernete.css", "peaceful-sky.css":
		return name, true
	default:
		return "", false
	}
}

// Settings may retain a built-in reference when its editor fetch has not succeeded.
// Delivery still validates the resolved stylesheet, never this input representation.
func validateGuestbookCSSInput(source string) (bool, string) {
	if _, ok := validBuiltInTheme(source); ok {
		return true, ""
	}
	if strings.HasPrefix(source, "<<built__in>>") {
		return false, "Invalid built-in theme reference."
	}
	return validateCSS(source)
}

func GuestbookStyles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	fail := func(status int, message string) {
		w.WriteHeader(status)
		if _, err := w.Write([]byte("/* " + message + " */\n")); err != nil {
			log.Printf("Stylesheet error response could not be written")
		}
	}
	id, err := parsePositiveID(chi.URLParam(r, "guestbookID"))
	if err != nil {
		fail(http.StatusBadRequest, "Invalid guestbook ID.")
		return
	}
	var book Guestbook
	err = db.WithContext(r.Context()).Joins("JOIN admin_users ON admin_users.id = guestbooks.admin_user_id AND admin_users.deleted_at IS NULL").
		Where("guestbooks.id = ?", id).First(&book).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(http.StatusNotFound, "Guestbook not found.")
		return
	}
	if err != nil {
		log.Printf("Stylesheet lookup failed for guestbook=%d", id)
		fail(http.StatusInternalServerError, "Stylesheet unavailable.")
		return
	}
	source := book.CustomPageCSS
	if theme, ok := validBuiltInTheme(source); ok {
		content, err := os.ReadFile(filepath.Join(constants.BUILT_IN_THEMES_DIR, theme))
		if err != nil {
			log.Printf("Built-in stylesheet unavailable for guestbook=%d", id)
			fail(http.StatusInternalServerError, "Built-in stylesheet unavailable.")
			return
		}
		source = string(content)
	}
	if ok, _ := validateCSS(source); !ok {
		fail(http.StatusUnprocessableEntity, "Custom stylesheet blocked. The owner must repair the CSS in guestbook settings.")
		return
	}
	if _, err := w.Write([]byte(source)); err != nil {
		log.Printf("Stylesheet response failed for guestbook=%d", id)
	}
}
