package main

import (
	"log"
	"net/http"

	"github.com/spf13/viper"
)

var sendVerificationEmail = SendVerificationEmail
var sendMessageNotification = notifyGuestbookOwner

type notificationStatus struct {
	State      string
	Message    string
	NeedsSetup bool
	CanResend  bool
}

func emailDeliveryEnabled() bool {
	switch viper.GetString("mailer.mailer_name") {
	case "smtp", "azure_communication_service":
		return true
	default:
		return false
	}
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
	if !emailDeliveryEnabled() {
		renderUserSettings(w, r, user, "No email was sent: email delivery is disabled on this instance.", "warning", http.StatusServiceUnavailable)
		return
	}
	if user.EmailVerificationToken == "" {
		token, err := generateAuthToken()
		if err != nil {
			http.Error(w, "Error creating verification token", http.StatusInternalServerError)
			return
		}
		result := db.Model(&AdminUser{}).
			Where("id = ? AND email = ? AND (email_verified = ? OR email_verified IS NULL) AND (email_verification_token = '' OR email_verification_token IS NULL)", user.ID, user.Email, false).
			Update("email_verification_token", token)
		if result.Error != nil {
			http.Error(w, "Error saving verification token", http.StatusInternalServerError)
			return
		}
		if result.RowsAffected != 1 {
			renderUserSettings(w, r, user, "Your email settings changed. Reload settings before trying again.", "warning", http.StatusConflict)
			return
		}
		user.EmailVerificationToken = token
	}
	if err := sendVerificationEmail(user.Email, user.EmailVerificationToken); err != nil {
		log.Printf("Error resending verification for admin=%d: %v", user.ID, err)
		renderUserSettings(w, r, user, "The verification email could not be sent. Please try again after the resend cooldown.", "warning", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/admin/settings?verification=sent#settings-notice", http.StatusSeeOther)
}

func AdminVerificationRateLimited(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "60")
	renderUserSettings(w, r, getSignedInAdminOrFail(r),
		"Please wait one minute between verification email requests.", "warning", http.StatusTooManyRequests)
}
