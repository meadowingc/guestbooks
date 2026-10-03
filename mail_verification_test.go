package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestVerificationAttemptFailureCooldown(t *testing.T) {
	user, _ := featureFixture(t)
	user.Email = "cooldown@example.test"
	user.EmailVerificationToken = "pending-token"
	if err := db.Save(&user).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	failure := errors.New("simulated delivery failure")
	testMailer(t, true, func(string, string) error { calls++; return failure })
	retryAfter, err := attemptVerification(context.Background(), &user)
	if retryAfter != 0 || !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("first attempt = %v, %v; calls=%d", retryAfter, err, calls)
	}
	var reloaded AdminUser
	if err := db.First(&reloaded, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.VerificationAttemptAt == 0 || reloaded.EmailVerificationToken != "pending-token" {
		t.Fatal("failed send did not persist the attempt with its existing token")
	}
	retryAfter, err = attemptVerification(context.Background(), &reloaded)
	if err != nil || retryAfter <= 0 || retryAfter > time.Minute || calls != 1 {
		t.Fatalf("persisted cooldown = %v, %v; calls=%d", retryAfter, err, calls)
	}
	response := featureRequest(initRouter(), "POST", "/admin/settings/resend-verification", nil, &user, false)
	requireStatus(t, response, 429)
	seconds, err := strconv.Atoi(response.Header().Get("Retry-After"))
	if err != nil || seconds < 1 || seconds > 60 {
		t.Fatal("blocked explicit resend did not give an accurate Retry-After")
	}
	if err := db.Model(&user).Update("verification_attempt_at", time.Now().Add(-time.Minute).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	retryAfter, err = attemptVerification(context.Background(), &reloaded)
	if retryAfter != 0 || !errors.Is(err, failure) || calls != 2 {
		t.Fatalf("expired cooldown = %v, %v; calls=%d", retryAfter, err, calls)
	}
}

func TestVerificationAttemptAtomicMissingToken(t *testing.T) {
	user, _ := featureFixture(t)
	user.Email = "atomic-token@example.test"
	if err := db.Save(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&user).Updates(map[string]any{
		"email_verified": nil, "email_verification_token": nil, "verification_attempt_at": nil,
	}).Error; err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	testMailer(t, true, func(email, token string) error {
		calls.Add(1)
		var stored AdminUser
		if err := db.First(&stored, user.ID).Error; err != nil {
			return err
		}
		if stored.Email != email || token == "" || stored.EmailVerificationToken != token || stored.VerificationAttemptAt == 0 {
			return errors.New("mail was sent before the atomic reservation")
		}
		started <- token
		<-release
		return nil
	})
	first, second := user, user
	result := make(chan error, 1)
	go func() {
		_, err := attemptVerification(context.Background(), &first)
		result <- err
	}()
	var token string
	select {
	case token = <-started:
	case err := <-result:
		t.Fatalf("first attempt stopped before sending: %v", err)
	case <-time.After(time.Second):
		t.Fatal("verification did not start")
	}
	retryAfter, err := attemptVerification(context.Background(), &second)
	close(release)
	if sendErr := <-result; sendErr != nil {
		t.Fatal(sendErr)
	}
	if err != nil || retryAfter <= 0 || calls.Load() != 1 || second.EmailVerificationToken != token {
		t.Fatalf("concurrent reservation = %v, %v; calls=%d", retryAfter, err, calls.Load())
	}
}

func TestVerificationAttemptMatchesCurrentState(t *testing.T) {
	testMailer(t, true, func(string, string) error { t.Error("stale state sent email"); return nil })
	for _, test := range []struct {
		name    string
		updates map[string]any
	}{
		{"email changed", map[string]any{"email": "changed@example.test"}},
		{"token changed", map[string]any{"email_verification_token": "changed-token"}},
		{"verified", map[string]any{"email_verified": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			user, _ := featureFixture(t)
			user.Email, user.EmailVerificationToken = "original@example.test", "original-token"
			if err := db.Save(&user).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&AdminUser{}).Where("id = ?", user.ID).Updates(test.updates).Error; err != nil {
				t.Fatal(err)
			}
			retryAfter, err := attemptVerification(context.Background(), &user)
			if retryAfter != 0 || !errors.Is(err, ErrVerificationChanged) {
				t.Fatalf("stale attempt = %v, %v", retryAfter, err)
			}
		})
	}
}

func TestVerificationAttemptDisabledAndCanceled(t *testing.T) {
	user, _ := featureFixture(t)
	user.Email = "disabled-verification@example.test"
	if err := db.Save(&user).Error; err != nil {
		t.Fatal(err)
	}
	testMailer(t, false, func(string, string) error { t.Error("disabled send"); return nil })
	if _, err := attemptVerification(context.Background(), &user); !errors.Is(err, ErrMailDisabled) {
		t.Fatalf("disabled attempt = %v", err)
	}
	viper.Set("mailer.mailer_name", "smtp")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := attemptVerification(ctx, &user); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled attempt = %v", err)
	}
	if err := db.First(&user, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if user.EmailVerificationToken != "" || user.VerificationAttemptAt != 0 {
		t.Fatal("unattempted delivery consumed token or cooldown")
	}
}

func TestVerificationAttemptContextDeadline(t *testing.T) {
	user, _ := featureFixture(t)
	user.Email = "bounded-verification@example.test"
	if err := db.Save(&user).Error; err != nil {
		t.Fatal(err)
	}
	testMailer(t, true, func(string, string) error { return nil })
	type contextKey struct{}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), contextKey{}, "request-value"), time.Second)
	defer cancel()
	expectedDeadline, _ := ctx.Deadline()
	sendVerificationEmail = func(sendCtx context.Context, _, _ string) error {
		deadline, ok := sendCtx.Deadline()
		if !ok || deadline.After(expectedDeadline) || sendCtx.Value(contextKey{}) != "request-value" {
			t.Error("verification did not preserve the bounded request context")
		}
		cancel()
		<-sendCtx.Done()
		return sendCtx.Err()
	}
	if _, err := attemptVerification(ctx, &user); !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight cancellation = %v", err)
	}
	if _, err := attemptVerification(context.Background(), &user); err != nil {
		t.Fatal("failed attempt should return a cooldown without trying again")
	}
}

func TestVerificationSettingsShareResendCooldown(t *testing.T) {
	user, _ := featureFixture(t)
	user.Email = "before-change@example.test"
	user.EmailVerificationToken = "before-change-token"
	user.PasswordResetToken = "before-change-reset"
	if err := db.Save(&user).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	testMailer(t, true, func(string, string) error { calls++; return nil })
	router := initRouter()
	requireStatus(t, featureRequest(router, "POST", "/admin/settings/resend-verification", nil, &user, false), 303)
	for index := range 2 {
		email := fmt.Sprintf("after-change-%d@example.test", index)
		response := featureRequest(router, "POST", "/admin/settings",
			url.Values{"settings_section": {"email"}, "email": {email}, "notify": {"on"}}, &user, false)
		requireStatus(t, response, 303)
		if !strings.Contains(response.Header().Get("Location"), "verification=cooldown") || response.Header().Get("Retry-After") == "" {
			t.Fatal("settings did not explain that the changed address was saved without sending")
		}
		if err := db.First(&user, user.ID).Error; err != nil {
			t.Fatal(err)
		}
		if user.Email != email || user.EmailVerified || user.PasswordResetToken != "" || user.EmailVerificationToken == "before-change-token" {
			t.Fatal("cooldown prevented saving the new email or revoking old tokens")
		}
	}
	if calls != 1 {
		t.Fatalf("address changes bypassed cooldown: sends=%d", calls)
	}
	if err := db.Model(&user).Update("verification_attempt_at", time.Now().Add(-time.Minute).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	// A new router models a restart; the limit is persisted, not a route-local counter.
	requireStatus(t, featureRequest(initRouter(), "POST", "/admin/settings/resend-verification", nil, &user, false), 303)
	if calls != 2 {
		t.Fatal("verification did not resume after the shared cooldown")
	}
}
