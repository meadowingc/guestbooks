package main

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func passwordFixture(t *testing.T) (AdminUser, Guestbook) {
	t.Helper()
	user, book := featureFixture(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("original-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&user).Update("password_hash", string(hash)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&user, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	return user, book
}

func sessionFrom(response *httptest.ResponseRecorder) string {
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == string(AdminTokenCookieName) && cookie.MaxAge >= 0 {
			return cookie.Value
		}
	}
	return ""
}

func TestAccountSessionLifecycle(t *testing.T) {
	for _, action := range []string{"logout", "change", "reset", "expired", "legacy"} {
		t.Run(action, func(t *testing.T) {
			user, _ := passwordFixture(t)
			router := initRouter()
			old := user
			switch action {
			case "logout":
				requireStatus(t, featureRequest(router, "POST", "/admin/logout", nil, &user, false), 303)
			case "change":
				response := featureRequest(router, "POST", "/admin/change-password", url.Values{
					"current-password": {"original-password"},
					"new-password":     {"replacement"}, "confirm-password": {"replacement"},
				}, &user, false)
				requireStatus(t, response, 303)
				user.SessionToken = sessionFrom(response)
				if user.SessionToken == "" || user.SessionToken == old.SessionToken {
					t.Fatal("password change did not rotate the cookie")
				}
				requireStatus(t, featureRequest(router, "GET", "/admin/settings", nil, &user, true), 200)
			case "reset":
				token := fmt.Sprintf("reset-%d", user.ID)
				if err := db.Model(&user).Updates(map[string]any{"password_reset_token": token, "password_reset_expiry": time.Now().Add(time.Hour).Unix()}).Error; err != nil {
					t.Fatal(err)
				}
				form := url.Values{"token": {token}, "new-password": {"replacement"}, "confirm-password": {"replacement"}}
				requireStatus(t, featureRequest(router, "POST", "/reset-password", form, nil, false), 303)
				requireStatus(t, featureRequest(router, "POST", "/reset-password", form, nil, false), 400)
			case "expired", "legacy":
				expiry := int64(0)
				if action == "expired" {
					expiry = time.Now().Add(-time.Second).Unix()
				}
				if err := db.Model(&user).Update("session_expires_at", expiry).Error; err != nil {
					t.Fatal(err)
				}
			}
			requireStatus(t, featureRequest(router, "GET", "/admin/settings", nil, &old, true), 401)
		})
	}
}

func TestAccountRecoveryInvalidation(t *testing.T) {
	for _, change := range []string{"email", "password"} {
		t.Run(change, func(t *testing.T) {
			user, _ := passwordFixture(t)
			token := fmt.Sprintf("old-link-%d", user.ID)
			if err := db.Model(&user).Updates(map[string]any{"password_reset_token": token, "password_reset_expiry": time.Now().Add(time.Hour).Unix()}).Error; err != nil {
				t.Fatal(err)
			}
			router := initRouter()
			var response *httptest.ResponseRecorder
			if change == "email" {
				response = featureRequest(router, "POST", "/admin/settings",
					url.Values{"settings_section": {"email"}, "email": {"new@example.test"}}, &user, false)
			} else {
				response = featureRequest(router, "POST", "/admin/change-password", url.Values{
					"current-password": {"original-password"}, "new-password": {"replacement"}, "confirm-password": {"replacement"},
				}, &user, false)
			}
			requireStatus(t, response, 303)
			requireStatus(t, featureRequest(router, "POST", "/reset-password",
				url.Values{"token": {token}, "new-password": {"stale-link"}, "confirm-password": {"stale-link"}}, nil, false), 400)
		})
	}
}

func TestAccountStaleSignInCannotUndoReset(t *testing.T) {
	user, _ := passwordFixture(t)
	callback := "regression:stale-sign-in"
	var once sync.Once
	newHash, err := bcrypt.GenerateFromPassword([]byte("newer-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if selected, ok := tx.Statement.Dest.(*AdminUser); ok && selected.ID == user.ID {
			once.Do(func() {
				if err := db.Model(&AdminUser{}).Where("id = ?", user.ID).Updates(map[string]any{
					"password_hash": string(newHash), "password_reset_token": "", "password_reset_expiry": 0,
				}).Error; err != nil {
					tx.AddError(err)
				}
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(callback) })
	request := httptest.NewRequest("POST", "/admin/signin",
		strings.NewReader(url.Values{"username": {user.Username}, "password": {"original-password"}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	AdminSignIn(response, request)
	requireStatus(t, response, 409)
	if sessionFrom(response) != "" {
		t.Fatal("stale sign-in issued a session")
	}
	var saved AdminUser
	if err := db.First(&saved, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword(saved.PasswordHash, []byte("newer-password")) != nil {
		t.Fatal("stale sign-in reverted the password")
	}
}

func TestAccountSignInStorageFailure(t *testing.T) {
	user, _ := passwordFixture(t)
	callback := "regression:session-write"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "admin_users" {
			tx.AddError(errors.New("injected session failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Update().Remove(callback) })
	response := featureRequest(initRouter(), "POST", "/admin/signin",
		url.Values{"username": {user.Username}, "password": {"original-password"}}, nil, false)
	requireStatus(t, response, 500)
	if sessionFrom(response) != "" {
		t.Fatal("failed session write issued a cookie")
	}
}

func TestAccountLegacyUsernameAndBlankLogin(t *testing.T) {
	user, _ := passwordFixture(t)
	name := " \tlegacy-" + fmt.Sprint(user.ID) + " \t"
	if err := db.Model(&user).Update("username", name).Error; err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{name, strings.TrimSpace(name)} {
		found, err := lookupUsername(value)
		if err != nil || found.ID != user.ID {
			t.Fatalf("legacy lookup %q: user=%d err=%v", value, found.ID, err)
		}
	}
	if _, err := lookupUsername(""); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("blank username must not select an account")
	}
	for _, value := range []string{"", " padded ", strings.TrimSpace(name)} {
		status := 400
		if value == strings.TrimSpace(name) {
			status = 409
		}
		requireStatus(t, featureRequest(initRouter(), "POST", "/admin/signup",
			url.Values{"username": {value}, "password": {"valid-password"}}, nil, false), status)
	}
}

func TestAccountVerificationConditionalUpdate(t *testing.T) {
	user, _ := passwordFixture(t)
	token := fmt.Sprintf("verification-%d", user.ID)
	if err := db.Model(&user).Updates(map[string]any{
		"email": "old@example.test", "email_verification_token": token, "email_verified": false,
	}).Error; err != nil {
		t.Fatal(err)
	}
	callback := "regression:verification-change"
	var once sync.Once
	if err := db.Callback().Update().Before("gorm:begin_transaction").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "admin_users" {
			return
		}
		updates, ok := tx.Statement.Dest.(map[string]any)
		if !ok || updates["email_verified"] != true {
			return
		}
		once.Do(func() {
			if err := db.Exec("UPDATE admin_users SET email=?, email_verification_token=?, email_notifications=? WHERE id=?",
				"new@example.test", "new-"+token, false, user.ID).Error; err != nil {
				tx.AddError(err)
			}
		})
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Update().Remove(callback) })
	response := httptest.NewRecorder()
	VerifyEmailHandler(response, httptest.NewRequest("GET", "/verify-email?token="+token, nil))
	requireStatus(t, response, 400)
	var saved AdminUser
	if err := db.First(&saved, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Email != "new@example.test" || saved.EmailVerified || saved.EmailVerificationToken != "new-"+token {
		t.Fatal("verification overwrote newer settings")
	}
}

func TestAccountResetTokenOneWinner(t *testing.T) {
	user, _ := passwordFixture(t)
	token := fmt.Sprintf("racing-reset-%d", user.ID)
	if err := db.Model(&user).Updates(map[string]any{
		"password_reset_token": token, "password_reset_expiry": time.Now().Add(time.Hour).Unix(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	callback := "regression:reset-race"
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if selected, ok := tx.Statement.Dest.(*AdminUser); ok && selected.ID == user.ID && selected.PasswordResetToken == token {
			arrived <- struct{}{}
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(callback) })
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			form := url.Values{"token": {token}, "new-password": {"replacement"}, "confirm-password": {"replacement"}}
			request := httptest.NewRequest("POST", "/reset-password", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			ResetPasswordHandler(response, request)
			results <- response.Code
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-ctx.Done():
			close(release)
			t.Fatal("reset handlers did not reach read barrier")
		}
	}
	close(release)
	first, second := <-results, <-results
	if !((first == 303 && second == 400) || (first == 400 && second == 303)) {
		t.Fatalf("reset outcomes = %d/%d; want exactly one success and one consumed-token error", first, second)
	}
}
