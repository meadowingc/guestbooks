package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	texttemplate "text/template"
	"time"

	"html/template"

	"guestbook/constants"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

var (
	guestbookTemplate     *template.Template
	guestbookTemplateErr  error
	guestbookTemplateOnce sync.Once
)

func formatDate(t time.Time) string {
	return t.Format("Jan 2, 2006")
}

func loadGuestbookTemplate() (*template.Template, error) {
	return template.New("guestbook_page.html").Funcs(template.FuncMap{
		"formatDate": formatDate,
	}).ParseFiles("templates/guestbook_page.html", "templates/resources/email_field.html")
}

func GuestbookPage(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")

	type GuestbookPageData struct {
		WebsiteURL        string
		CustomPageCSS     string
		PowEnabled        bool
		SubmissionAction  SubmissionAction
		SubmissionMessage string
		CollectEmail      bool
		EmailFieldLabel   string
		EmailFieldHelp    string
	}

	var guestbookData GuestbookPageData
	result := activeGuestbooksQuery(db.WithContext(r.Context())).
		Select("website_url, custom_page_css, pow_enabled, submission_action, submission_message, collect_email, email_field_label, email_field_help").
		Where("id = ?", guestbookID).
		Scan(&guestbookData)

	if result.Error != nil {
		http.Error(w, "Error querying the database", http.StatusInternalServerError)
		return
	}

	if result.RowsAffected == 0 {
		http.Error(w, "Guestbook not found. It may have been deleted or the URL is incorrect.", http.StatusNotFound)
		return
	}

	var pageTemplate *template.Template
	var templateErr error
	if constants.DEBUG_MODE {
		pageTemplate, templateErr = loadGuestbookTemplate()
	} else {
		guestbookTemplateOnce.Do(func() {
			guestbookTemplate, guestbookTemplateErr = loadGuestbookTemplate()
		})
		pageTemplate, templateErr = guestbookTemplate, guestbookTemplateErr
	}
	if templateErr != nil {
		log.Printf("Load guestbook template: %v", templateErr)
		http.Error(w, "Guestbook page temporarily unavailable", http.StatusInternalServerError)
		return
	}

	selectedBuiltInTheme, _ := validBuiltInTheme(guestbookData.CustomPageCSS)

	data := struct {
		templateCommon
		ID                   string
		WebsiteURL           string
		CustomPageCSS        string
		SelectedBuiltInTheme string
		PowEnabled           bool
		CollectEmail         bool
		EmailFieldLabel      string
		EmailFieldHelp       string
		SubmissionAction     SubmissionAction
		ConfirmationMessage  string
	}{
		templateCommon:       currentTemplateCommon(),
		ID:                   guestbookID,
		WebsiteURL:           guestbookData.WebsiteURL,
		CustomPageCSS:        guestbookData.CustomPageCSS,
		SelectedBuiltInTheme: selectedBuiltInTheme,
		PowEnabled:           guestbookData.PowEnabled,
		CollectEmail:         guestbookData.CollectEmail,
		EmailFieldLabel:      guestbookData.EmailFieldLabel,
		EmailFieldHelp:       guestbookData.EmailFieldHelp,
		SubmissionAction:     guestbookData.SubmissionAction,
	}
	if r.URL.Query().Get("submitted") == "1" && guestbookData.SubmissionAction == SubmissionMessage {
		data.ConfirmationMessage = guestbookData.SubmissionMessage
	}

	err := pageTemplate.Execute(w, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func GuestbookSubmit(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")

	var guestbook Guestbook
	result := activeGuestbooksQuery(db.WithContext(r.Context())).First(&guestbook, "guestbooks.id = ?", guestbookID)
	if result.Error != nil {
		recordLookupError(w, result.Error, "Guestbook")
		return
	}

	limitedBody := http.MaxBytesReader(w, r.Body, maxSubmissionBytes)
	defer limitedBody.Close()
	body, readErr := io.ReadAll(limitedBody)
	var bodyLimit *http.MaxBytesError
	if errors.As(readErr, &bodyLimit) {
		http.Error(w, "Submission body exceeds 64 KiB", http.StatusRequestEntityTooLarge)
		return
	}
	if readErr != nil {
		http.Error(w, "Unable to read submission body", http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	parseErr := r.ParseForm()
	if parseErr == nil {
		parseErr = r.ParseMultipartForm(maxSubmissionBytes)
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if errors.As(parseErr, &bodyLimit) {
		http.Error(w, "Submission body exceeds 64 KiB", http.StatusRequestEntityTooLarge)
		return
	}
	if parseErr != nil && !errors.Is(parseErr, http.ErrNotMultipart) {
		http.Error(w, "Invalid submission form", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil && len(r.MultipartForm.File) != 0 {
		http.Error(w, "File uploads are not supported", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	text := normalizedMessageText(r.FormValue("text"))
	website := strings.TrimSpace(r.FormValue("website"))
	if err := messageInputError(name, text, website); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var websitePtr *string
	if website != "" {
		websitePtr = &website
	}
	var email *string
	if guestbook.CollectEmail {
		var err error
		email, err = optionalEmail(r.FormValue("email"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := challengeSettingsError(guestbook.ChallengeQuestion, guestbook.ChallengeAnswer); err != nil {
		log.Printf("Invalid challenge configuration for guestbook=%d", guestbook.ID)
		http.Error(w, "This guestbook's verification settings need to be repaired by its owner.", http.StatusServiceUnavailable)
		return
	}

	feedback := submissionFeedback{Success: true}
	redirectToURL := strings.TrimSpace(r.FormValue("redirect_to_url"))
	allowRelative := redirectToURL != ""
	if redirectToURL == "" {
		switch guestbook.SubmissionAction {
		case SubmissionUnchanged:
		case SubmissionMessage:
			feedback.Message = guestbook.SubmissionMessage
		case SubmissionRedirect:
			redirectToURL = guestbook.SubmissionRedirectURL
		default:
			http.Error(w, "Invalid guestbook submission settings", http.StatusInternalServerError)
			return
		}
	}
	if redirectToURL != "" || guestbook.SubmissionAction == SubmissionRedirect {
		var err error
		feedback.RedirectURL, err = validatedRedirect(r, redirectToURL, allowRelative)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	var adminUser AdminUser
	if err := db.First(&adminUser, "id = ?", guestbook.AdminUserID).Error; err != nil {
		http.Error(w, "Error loading guestbook owner", http.StatusInternalServerError)
		return
	}

	// check that the form has the expected challenge if necesary
	if strings.TrimSpace(guestbook.ChallengeQuestion) != "" {
		challengeQuestionAnswer := strings.TrimSpace(r.FormValue("challengeQuestionAnswer"))
		challengeQuestionAnswer = strings.ToLower(challengeQuestionAnswer)

		expectedChallengeAnswer := strings.TrimSpace(guestbook.ChallengeAnswer)
		expectedChallengeAnswer = strings.ToLower(expectedChallengeAnswer)

		if expectedChallengeAnswer != challengeQuestionAnswer {
			http.Error(w, "The provided answer to the challenge question is invalid!", http.StatusUnauthorized)
			return
		}
	}

	// Verify proof-of-work if enabled for this guestbook
	if guestbook.PowEnabled {
		powChallenge := strings.TrimSpace(r.FormValue("powChallenge"))
		powNonce := strings.TrimSpace(r.FormValue("powNonce"))
		if powChallenge == "" || powNonce == "" || !powChallengeStore.VerifyPow(powChallenge, powNonce, guestbook.ID) {
			http.Error(w, "Proof of work verification failed. Please reload the page and try again.", http.StatusForbidden)
			return
		}
	}

	message := Message{
		Name:        name,
		Text:        text,
		Website:     websitePtr,
		Email:       email,
		GuestbookID: guestbook.ID,
		Approved:    !guestbook.RequiresApproval,
	}
	saveErr := writeTransaction(db.WithContext(r.Context()), func(tx *gorm.DB) error {
		var count int64
		if err := activeGuestbooksQuery(tx).Where("id = ?", guestbook.ID).
			Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return gorm.ErrRecordNotFound
		}
		return tx.Create(&message).Error
	})
	if saveErr != nil {
		log.Printf("Save submission for guestbook=%d: %v", guestbook.ID, saveErr)
		if guestbook.PowEnabled {
			w.Header().Set("X-Guestbooks-Fresh-Proof", "true")
			http.Error(w, "Error submitting message. Complete a fresh verification before retrying.", http.StatusInternalServerError)
		} else {
			http.Error(w, "Error submitting message", http.StatusInternalServerError)
		}
		return
	}

	// Invalidate cache for this guestbook since we added a new message
	messageCache.InvalidateGuestbook(guestbook.ID)

	if err := sendMessageNotification(guestbook, message, adminUser); err != nil {
		log.Printf("Error preparing notification for guestbook=%d message=%d: %v", guestbook.ID, message.ID, err)
	}

	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(feedback); err != nil {
			log.Printf("Error writing submission response for message=%d: %v", message.ID, err)
		}
		return
	}
	destination := feedback.RedirectURL
	if destination == "" {
		destination = "/guestbook/" + guestbookID
		if guestbook.SubmissionAction == SubmissionMessage {
			destination += "?submitted=1"
		}
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}

type submissionFeedback struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	RedirectURL string `json:"redirectUrl"`
}

func notifyGuestbookOwner(guestbook Guestbook, message Message, adminUser AdminUser) error {
	if !adminUser.EmailNotifications || !adminUser.EmailVerified || adminUser.Email == "" {
		return nil
	}
	body, err := messageNotificationBody(guestbook, message)
	if err != nil {
		return err
	}
	if constants.DEBUG_MODE {
		log.Printf("Debug mode: notification for guestbook=%d message=%d not sent", guestbook.ID, message.ID)
	} else {
		return queueMail(fmt.Sprintf("notification for guestbook=%d message=%d", guestbook.ID, message.ID), func(ctx context.Context) error {
			return SendMailContext(ctx, []string{adminUser.Email}, "[Guestbooks] New message on guestbook '"+guestbook.WebsiteURL+"'", body)
		})
	}
	return nil
}

func messageNotificationBody(guestbook Guestbook, message Message) (string, error) {
	submitterText := ""
	if message.Website != nil {
		submitterText = "[Website: " + *message.Website + "]"
	}

	data := struct {
		ApplicationURL       string
		GuestbookID          string
		GuestbookURL         string
		MessageID            uint
		MessageName          string
		MessageNeedsApproval bool
		MessageText          string
		SubmitterText        string
		SupportURL           string
	}{
		ApplicationURL:       PublicURL(),
		GuestbookID:          fmt.Sprint(guestbook.ID),
		GuestbookURL:         guestbook.WebsiteURL,
		MessageID:            message.ID,
		MessageName:          message.Name,
		MessageNeedsApproval: guestbook.RequiresApproval && !message.Approved,
		MessageText:          message.Text,
		SubmitterText:        submitterText,
		SupportURL:           appConfig.SupportURL,
	}

	// Define your template string
	tmpl := `
Hi! Someone has just submitted a new message on your guestbook '{{.GuestbookURL}}'.

From: {{.MessageName}} {{.SubmitterText}}
===BEGIN MESSAGE===
{{.MessageText}}
===END MESSAGE===

You can view the messages on your guestbook here {{.ApplicationURL}}/admin/guestbook/{{.GuestbookID}}

{{if .MessageNeedsApproval}}
This message needs approval before it is shown on your guestbook.

Please go here to approve or reject the message: {{.ApplicationURL}}/admin/guestbook/{{.GuestbookID}}/message/{{.MessageID}}/edit
{{end}}

This is an autogenerated message from {{.ApplicationURL}} . Please don't answer since this mailbox is not monitored. {{if .SupportURL}}
If you do need some help then please reach out through here {{.SupportURL}}{{end}}
		`

	// Parse and execute the template
	t, err := texttemplate.New("email").Parse(tmpl)
	if err != nil {
		return "", err
	}

	var tpl bytes.Buffer
	if err := t.Execute(&tpl, data); err != nil {
		return "", err
	}
	return tpl.String(), nil
}
