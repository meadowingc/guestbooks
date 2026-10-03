package main

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
)

const sessionLifetime = 30 * 24 * time.Hour

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: string(AdminTokenCookieName), Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: strings.HasPrefix(PublicURL(), "https://"),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: string(AdminTokenCookieName), Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: strings.HasPrefix(PublicURL(), "https://"),
	})
}

func asyncAdminRequest(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json") ||
		strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
}

func authenticationRequired(w http.ResponseWriter, r *http.Request) {
	if asyncAdminRequest(r) {
		http.Error(w, "Your session has expired. Sign in again before retrying.", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/admin/signin", http.StatusSeeOther)
}

func normalizedUsernameMatches(value string) ([]AdminUser, error) {
	var names []AdminUser
	if err := db.Select("id", "username").Find(&names).Error; err != nil {
		return nil, err
	}
	var matches []AdminUser
	for _, user := range names {
		if strings.TrimSpace(user.Username) == strings.TrimSpace(value) {
			matches = append(matches, user)
			if len(matches) == 2 {
				break
			}
		}
	}
	return matches, nil
}

func lookupUsername(value string) (AdminUser, error) {
	var user AdminUser
	if strings.TrimSpace(value) == "" {
		return user, gorm.ErrRecordNotFound
	}
	err := db.Where("username = ?", value).First(&user).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return user, err
	}
	matches, err := normalizedUsernameMatches(value)
	if err != nil {
		return user, err
	}
	if len(matches) != 1 {
		return user, gorm.ErrRecordNotFound
	}
	err = db.First(&user, matches[0].ID).Error
	return user, err
}
