package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"
)

var sendVerificationEmail = SendVerificationEmailContext
var sendMessageNotification = notifyGuestbookOwner

var (
	ErrVerificationChanged = errors.New("email verification settings changed")
	ErrVerificationStorage = errors.New("email verification could not be saved")
)

type notificationStatus struct {
	State      string
	Message    string
	NeedsSetup bool
	CanResend  bool
}

func emailDeliveryEnabled() bool {
	config, err := readMailerConfig()
	return err == nil && config.provider != "none"
}

func notificationStatusFor(user *AdminUser) notificationStatus {
	if user == nil {
		return notificationStatus{}
	}
	if !emailDeliveryEnabled() {
		return notificationStatus{
			State:   "unavailable",
			Message: "Email delivery is disabled on this instance. Notifications and verification emails cannot be sent.",
		}
	}
	if user.Email == "" {
		return notificationStatus{
			State: "missing-email", NeedsSetup: true,
			Message: "Add and verify your email address, then enable notifications to hear about new messages.",
		}
	}
	if !user.EmailVerified {
		message := "Verify your email address before notifications can be sent."
		if !user.EmailNotifications {
			message += " Email notifications are also turned off."
		}
		return notificationStatus{
			State: "unverified", Message: message, NeedsSetup: true, CanResend: true,
		}
	}
	if !user.EmailNotifications {
		return notificationStatus{
			State: "disabled", NeedsSetup: true,
			Message: "Email notifications are turned off. Enable them so messages do not wait unnoticed.",
		}
	}
	return notificationStatus{
		State:   "enabled",
		Message: "Email notifications are enabled and your email address is verified.",
	}
}

func renderUserSettings(w http.ResponseWriter, r *http.Request, user *AdminUser, notice, kind string, status int) {
	data := struct {
		AdminUser
		Notice     string
		NoticeKind string
	}{*user, notice, kind}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	renderAdminTemplate(w, r, "user_settings", data)
}

func AdminResendVerification(w http.ResponseWriter, r *http.Request) {
	user := getSignedInAdminOrFail(r)
	if user.Email == "" {
		renderUserSettings(w, r, user, "Save an email address before requesting verification.", "warning", http.StatusBadRequest)
		return
	}
	if user.EmailVerified {
		renderUserSettings(w, r, user, "Your email address is already verified.", "info", http.StatusConflict)
		return
	}
	retryAfter, err := attemptVerification(r.Context(), user)
	if errors.Is(err, ErrMailDisabled) {
		renderUserSettings(w, r, user, "No email was sent: email delivery is disabled on this instance.", "warning", http.StatusServiceUnavailable)
		return
	}
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
		renderUserSettings(w, r, user, "Please wait one minute between verification email requests.", "warning", http.StatusTooManyRequests)
		return
	}
	if errors.Is(err, ErrVerificationChanged) {
		renderUserSettings(w, r, user, "Your email settings changed. Reload settings before trying again.", "warning", http.StatusConflict)
		return
	}
	if errors.Is(err, ErrVerificationStorage) {
		log.Printf("Error reserving verification attempt for admin=%d", user.ID)
		http.Error(w, "Error saving verification attempt", http.StatusInternalServerError)
		return
	}
	if err != nil {
		log.Printf("Error resending verification for admin=%d: %v", user.ID, safeMailError(err))
		renderUserSettings(w, r, user, "The verification email could not be sent. Please try again after the resend cooldown.", "warning", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/admin/settings?verification=sent#settings-notice", http.StatusSeeOther)
}

// Reservation survives failed sends, process restarts, and changes of address.
func attemptVerification(ctx context.Context, user *AdminUser) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if user == nil || user.ID == 0 || user.Email == "" || user.EmailVerified {
		return 0, ErrVerificationChanged
	}
	snapshot, err := mailSnapshotForContext(ctx)
	if err != nil {
		return 0, err
	}
	if snapshot.config.provider == "none" {
		return 0, ErrMailDisabled
	}
	token := user.EmailVerificationToken
	if token == "" {
		token, err = generateAuthToken()
		if err != nil {
			return 0, fmt.Errorf("%w: token creation failed", ErrVerificationStorage)
		}
	}
	now := time.Now()
	query := db.WithContext(ctx).Model(&AdminUser{}).
		Where("id = ? AND email = ? AND (email_verified = ? OR email_verified IS NULL)", user.ID, user.Email, false).
		Where("(verification_attempt_at IS NULL OR verification_attempt_at <= ?)", now.Add(-time.Minute).Unix())
	if user.EmailVerificationToken == "" {
		query = query.Where("(email_verification_token = '' OR email_verification_token IS NULL)")
	} else {
		query = query.Where("email_verification_token = ?", user.EmailVerificationToken)
	}
	result := query.Updates(map[string]any{
		"email_verification_token": token,
		"verification_attempt_at":  now.Unix(),
	})
	if result.Error != nil {
		return 0, ErrVerificationStorage
	}
	if result.RowsAffected != 1 {
		var current AdminUser
		if err := db.WithContext(ctx).First(&current, user.ID).Error; err != nil {
			return 0, ErrVerificationStorage
		}
		if current.Email != user.Email || current.EmailVerified {
			return 0, ErrVerificationChanged
		}
		retryAfter := time.Unix(current.VerificationAttemptAt, 0).Add(time.Minute).Sub(now)
		if retryAfter > 0 {
			*user = current
			return retryAfter, nil
		}
		return 0, ErrVerificationChanged
	}
	user.EmailVerificationToken = token
	user.VerificationAttemptAt = now.Unix()
	sendCtx, cancel := context.WithTimeout(context.WithValue(ctx, mailSnapshotKey{}, snapshot), mailSendTimeout)
	defer cancel()
	return 0, sendVerificationEmail(sendCtx, user.Email, token)
}

func AdminVerificationRateLimited(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "60")
	renderUserSettings(w, r, getSignedInAdminOrFail(r),
		"Please wait one minute between verification email requests.", "warning", http.StatusTooManyRequests)
}
