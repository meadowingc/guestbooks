package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"guestbook/constants"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/csrf"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type AdminCookieName string

const AdminUserCookieName = AdminCookieName("admin_user")
const AdminTokenCookieName = AdminCookieName("admin_token")

func renderAdminTemplate(w http.ResponseWriter, r *http.Request, tmpl string, data any) {
	templateData := struct {
		templateCommon
		CurrentUser        *AdminUser
		NotificationStatus notificationStatus
		AllowSignups       bool
		Data               any
		CSRFField          template.HTML
		CSRFToken          string
	}{
		templateCommon:     currentTemplateCommon(),
		CurrentUser:        getSignedInAdminUserOrNil(r),
		NotificationStatus: notificationStatusFor(getSignedInAdminUserOrNil(r)),
		AllowSignups:       appConfig.AllowSignups,
		Data:               data,
		CSRFField:          csrf.TemplateField(r),
		CSRFToken:          csrf.Token(r),
	}

	templatesDir := "templates/admin"

	baseTemplate := template.Must(template.ParseFiles(filepath.Join(templatesDir, "layout.html")))
	actualTemplate := template.Must(baseTemplate.ParseFiles(filepath.Join(templatesDir, tmpl+".html")))

	err := actualTemplate.Execute(w, templateData)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func getSignedInAdminUserOrNil(r *http.Request) *AdminUser {
	adminUser, _ := r.Context().Value(AdminUserCookieName).(*AdminUser)
	return adminUser
}

func getSignedInAdminOrFail(r *http.Request) *AdminUser {
	adminUser := getSignedInAdminUserOrNil(r)
	if adminUser == nil {
		panic("authenticated handler called without a user")
	}

	return adminUser
}

func generateAuthToken() (string, error) {
	const tokenLength = 32
	tokenBytes := make([]byte, tokenLength)
	_, err := rand.Read(tokenBytes)
	if err != nil {
		return "", err
	}
	token := base64.URLEncoding.EncodeToString(tokenBytes)
	return token, nil
}

func AdminAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// try to set admin user into context
		cookie, err := r.Cookie(string(AdminTokenCookieName))
		if err != nil || cookie.Value == "" {
			if r.URL.Path != "/admin/signin" && r.URL.Path != "/admin/signup" {
				if r.URL.Path == "/admin/logout" {
					clearSessionCookie(w)
					http.Redirect(w, r, "/admin/signin", http.StatusSeeOther)
					return
				}
				authenticationRequired(w, r)
				return
			} else {
				// then we're already trying to signin or signup, so just let it
				// continue
				next.ServeHTTP(w, r)
				return
			}
		}

		// Validate the token and retrieve the corresponding user
		var user AdminUser
		result := db.Where("session_token = ? AND session_expires_at > ?", cookie.Value, time.Now().Unix()).First(&user)
		if result.Error != nil {
			if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
				log.Printf("Session lookup failed: %v", result.Error)
				http.Error(w, "Authentication is temporarily unavailable", http.StatusInternalServerError)
				return
			}
			clearSessionCookie(w)
			if r.URL.Path == "/admin/signin" || r.URL.Path == "/admin/signup" {
				next.ServeHTTP(w, r)
				return
			}
			authenticationRequired(w, r)
			return
		}

		// Store the admin user in the context
		ctx := context.WithValue(r.Context(), AdminUserCookieName, &user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func AdminSignIn(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		adminUser := getSignedInAdminUserOrNil(r)
		if adminUser == nil {
			renderAdminTemplate(w, r, "signin", nil)
			return
		} else {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}

	} else {
		username := r.FormValue("username")
		password := r.FormValue("password")

		admin, err := lookupUsername(username)
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				recordLookupError(w, err, "account")
				return
			}
			http.Error(w, "Invalid username or password", http.StatusUnauthorized)
			return
		}

		err = bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(password))
		if err != nil {
			http.Error(w, "Invalid password", http.StatusUnauthorized)
			return
		}

		// Generate a new token for the session
		token, err := generateAuthToken()
		if err != nil {
			http.Error(w, "Error signing in", http.StatusInternalServerError)
			return
		}

		result := db.Model(&AdminUser{}).
			Where("id = ? AND password_hash = ?", admin.ID, string(admin.PasswordHash)).
			Updates(map[string]any{"session_token": token, "session_expires_at": time.Now().Add(sessionLifetime).Unix()})
		if result.Error != nil {
			log.Printf("Persist sign-in session: %v", result.Error)
			http.Error(w, "Error saving session", http.StatusInternalServerError)
			return
		}
		if result.RowsAffected != 1 {
			http.Error(w, "Your credentials changed. Please sign in again.", http.StatusConflict)
			return
		}
		setSessionCookie(w, token)

		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}
}

func AdminSignUp(w http.ResponseWriter, r *http.Request) {
	if !appConfig.AllowSignups {
		http.NotFound(w, r)
		return
	}
	if r.Method == "GET" {
		adminUser := getSignedInAdminUserOrNil(r)
		if adminUser == nil {
			renderAdminTemplate(w, r, "signup", nil)
			return
		} else {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}

	} else {
		username := r.FormValue("username")
		password := r.FormValue("password")
		if username == "" || username != strings.TrimSpace(username) || !utf8.ValidString(username) || utf8.RuneCountInString(username) > maxNameCharacters {
			http.Error(w, "Enter a username of at most 200 characters without surrounding whitespace", http.StatusBadRequest)
			return
		}
		if len(password) == 0 || len(password) > 72 {
			http.Error(w, "Password must contain 1 to 72 bytes", http.StatusBadRequest)
			return
		}
		matches, err := normalizedUsernameMatches(username)
		if err != nil {
			recordLookupError(w, err, "account")
			return
		}
		if len(matches) != 0 {
			http.Error(w, "Username is already in use", http.StatusConflict)
			return
		}

		passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "Error creating account: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Create a new token and store it in a cookie
		token, err := generateAuthToken()
		if err != nil {
			http.Error(w, "Error creating account: "+err.Error(), http.StatusInternalServerError)
			return
		}

		newAdmin := AdminUser{Username: username, PasswordHash: passwordHash, SessionToken: token, SessionExpiresAt: time.Now().Add(sessionLifetime).Unix()}

		result := db.Create(&newAdmin)
		if result.Error != nil {
			http.Error(w, "Error creating account: "+result.Error.Error(), http.StatusInternalServerError)
			return
		}

		setSessionCookie(w, token)

		// Redirect to the admin sign-in page after successful sign-up
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}
}

func AdminLogout(w http.ResponseWriter, r *http.Request) {
	user := getSignedInAdminOrFail(r)
	token, err := generateAuthToken()
	if err != nil {
		http.Error(w, "Error revoking session", http.StatusInternalServerError)
		return
	}
	result := db.Model(&AdminUser{}).Where("id = ? AND session_token = ?", user.ID, user.SessionToken).
		Updates(map[string]any{"session_token": token, "session_expires_at": 0})
	if result.Error != nil {
		http.Error(w, "Error revoking session", http.StatusInternalServerError)
		return
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/admin/signin", http.StatusSeeOther)
}

func AdminGuestbookList(w http.ResponseWriter, r *http.Request) {
	adminUser := getSignedInAdminOrFail(r)

	var guestbooks []Guestbook
	result := db.Where(&Guestbook{AdminUserID: adminUser.ID}).Find(&guestbooks)
	if result.Error != nil {
		http.Error(w, "Error fetching guestbooks", http.StatusInternalServerError)
		return
	}

	type GuestbookListItem struct {
		Guestbook       Guestbook
		TotalMessages   int64
		PendingMessages int64
	}

	items := make([]GuestbookListItem, 0, len(guestbooks))
	for _, g := range guestbooks {
		var total int64
		var pending int64
		if err := activeMessagesQuery(db).Where("messages.guestbook_id = ?", g.ID).Count(&total).Error; err != nil {
			recordLookupError(w, err, "message counts")
			return
		}
		if err := activeMessagesQuery(db).Where("messages.guestbook_id = ? AND messages.approved = ?", g.ID, false).Count(&pending).Error; err != nil {
			recordLookupError(w, err, "message counts")
			return
		}

		items = append(items, GuestbookListItem{
			Guestbook:       g,
			TotalMessages:   total,
			PendingMessages: pending,
		})
	}

	renderAdminTemplate(w, r, "guestbook_list", items)
}

func AdminShowGuestbook(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")

	var guestbook Guestbook
	result := activeGuestbooksQuery(db.WithContext(r.Context())).Preload("Messages", func(tx *gorm.DB) *gorm.DB {
		return activeMessagesQuery(tx).Where("messages.guestbook_id = ? AND messages.parent_message_id IS NULL", guestbookID).
			Order("messages.created_at desc, messages.id desc")
	}).Preload("Messages.Replies", func(tx *gorm.DB) *gorm.DB {
		return activeMessagesQuery(tx).Where("messages.guestbook_id = ?", guestbookID).
			Order("messages.created_at asc, messages.id asc")
	}).First(&guestbook, "id = ?", guestbookID)
	if result.Error != nil {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}

	currentUser := getSignedInAdminOrFail(r)
	if guestbook.AdminUserID != currentUser.ID {
		http.Error(w, "You don't own this guestbook", http.StatusUnauthorized)
		return
	}

	renderAdminTemplate(w, r, "show_guestbook", guestbook)
}

func AdminCreateGuestbook(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		renderAdminTemplate(w, r, "create_edit_guestbook", nil)
	} else {
		adminUser := getSignedInAdminOrFail(r)

		websiteURL := r.FormValue("websiteURL")
		challengeQuestion := r.FormValue("challengeQuestion")
		challengeHint := r.FormValue("challengeHint")
		challengeFailedMessage := r.FormValue("challengeFailedMessage")
		challengeAnswer := r.FormValue("challengeAnswer")
		if err := challengeSettingsError(challengeQuestion, challengeAnswer); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requiresApproval := r.FormValue("requiresApproval") == "on"
		powEnabled := r.FormValue("powEnabled") == "on"
		customPageCSS := strings.TrimSpace(r.FormValue("customPageCSS"))

		isCssValid, errorMsg := validateGuestbookCSSInput(customPageCSS)
		if !isCssValid {
			http.Error(w, errorMsg, http.StatusBadRequest)
			return
		}

		// if css is one of our built-in themes, then just store the theme name
		themeName, err := CompareCSSWithThemes(customPageCSS)
		if err != nil {
			http.Error(w, "Error checking provided CSS with built-in themes", http.StatusInternalServerError)
			return
		}

		if themeName != "" {
			customPageCSS = "<<built__in>>" + themeName + "<</built__in>>"
		}

		newGuestbook := Guestbook{
			WebsiteURL:             websiteURL,
			RequiresApproval:       requiresApproval,
			PowEnabled:             powEnabled,
			ChallengeQuestion:      challengeQuestion,
			ChallengeHint:          challengeHint,
			ChallengeFailedMessage: challengeFailedMessage,
			ChallengeAnswer:        challengeAnswer,
			CustomPageCSS:          customPageCSS,
			AdminUserID:            adminUser.ID,
		}
		if err := readGuestbookOptions(r, &newGuestbook); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result := db.Create(&newGuestbook)
		if result.Error != nil {
			http.Error(w, "Error creating guestbook", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}
}

func AdminEmbedGuestbook(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")
	var guestbook Guestbook
	result := db.First(&guestbook, guestbookID)
	if result.Error != nil {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}

	currentUser := getSignedInAdminOrFail(r)
	if guestbook.AdminUserID != currentUser.ID {
		http.Error(w, "You don't own this guestbook", http.StatusUnauthorized)
		return
	}

	hostUrl := PublicURL()
	if constants.DEBUG_MODE {
		hostUrl = "//" + r.Host
	}

	emailTemplate, err := template.ParseFiles("templates/resources/email_field.html")
	if err != nil {
		http.Error(w, "Error loading email field template", http.StatusInternalServerError)
		return
	}
	var emailField bytes.Buffer
	if err := emailTemplate.ExecuteTemplate(&emailField, "email-field", guestbook); err != nil {
		http.Error(w, "Error rendering email field", http.StatusInternalServerError)
		return
	}

	data := struct {
		Guestbook      Guestbook
		PublicHostUrl  string
		EmailFieldHTML string
	}{
		Guestbook:      guestbook,
		PublicHostUrl:  hostUrl,
		EmailFieldHTML: emailField.String(),
	}

	renderAdminTemplate(w, r, "embed_guestbook", data)
}

func AdminDeleteGuestbook(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")

	var guestbook Guestbook
	result := db.First(&guestbook, guestbookID)
	if result.Error != nil {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}

	currentUser := getSignedInAdminOrFail(r)
	if guestbook.AdminUserID != currentUser.ID {
		http.Error(w, "You don't own this guestbook", http.StatusUnauthorized)
		return
	}

	log.Printf("admin=%d username=%q action=delete_guestbook guestbook_id=%d", currentUser.ID, currentUser.Username, guestbook.ID)

	if err := softDeleteGuestbook(guestbook.ID); err != nil {
		log.Printf("Delete guestbook=%d: %v", guestbook.ID, err)
		http.Error(w, "Error deleting guestbook", http.StatusInternalServerError)
		return
	}

	// Invalidate cache for this guestbook since it was deleted
	messageCache.InvalidateGuestbook(guestbook.ID)

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func AdminEditGuestbook(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")
	var guestbook Guestbook
	result := db.First(&guestbook, guestbookID)
	if result.Error != nil {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}

	currentUser := getSignedInAdminOrFail(r)
	if guestbook.AdminUserID != currentUser.ID {
		http.Error(w, "You don't own this guestbook", http.StatusUnauthorized)
		return
	}

	// Determine selected theme; client fetches CSS for built-ins
	themeName := ""
	if strings.HasPrefix(guestbook.CustomPageCSS, "<<built__in>>") {
		themeName = strings.TrimPrefix(guestbook.CustomPageCSS, "<<built__in>>")
		themeName = strings.TrimSuffix(themeName, "<</built__in>>")
	}

	// Build view model embedding Guestbook fields and selected theme URL
	data := struct {
		Guestbook
		SelectedTheme string
	}{
		guestbook,
		"",
	}
	if themeName != "" {
		data.SelectedTheme = "/assets/premade_styles/" + themeName
	}

	renderAdminTemplate(w, r, "create_edit_guestbook", data)
}

func AdminUpdateGuestbook(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")
	websiteURL := r.FormValue("websiteURL")
	challengeQuestion := r.FormValue("challengeQuestion")
	challengeHint := r.FormValue("challengeHint")
	challengeFailedMessage := r.FormValue("challengeFailedMessage")
	challengeAnswer := r.FormValue("challengeAnswer")
	if err := challengeSettingsError(challengeQuestion, challengeAnswer); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	requiresApproval := r.FormValue("requiresApproval") == "on"
	powEnabled := r.FormValue("powEnabled") == "on"
	customPageCSS := strings.TrimSpace(r.FormValue("customPageCSS"))

	isCssValid, errorMsg := validateGuestbookCSSInput(customPageCSS)
	if !isCssValid {
		http.Error(w, errorMsg, http.StatusBadRequest)
		return
	}

	// if css is one of our built-in themes, then just store the theme name
	themeName, err := CompareCSSWithThemes(customPageCSS)
	if err != nil {
		http.Error(w, "Error checking provided CSS with built-in themes", http.StatusInternalServerError)
		return
	}

	if themeName != "" {
		customPageCSS = "<<built__in>>" + themeName + "<</built__in>>"
	}

	var guestbook Guestbook
	result := db.First(&guestbook, guestbookID)
	if result.Error != nil {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}

	currentUser := getSignedInAdminOrFail(r)
	if guestbook.AdminUserID != currentUser.ID {
		http.Error(w, "You don't own this guestbook", http.StatusUnauthorized)
		return
	}

	guestbook.WebsiteURL = websiteURL
	guestbook.RequiresApproval = requiresApproval
	guestbook.PowEnabled = powEnabled
	guestbook.ChallengeQuestion = challengeQuestion
	guestbook.ChallengeHint = challengeHint
	guestbook.ChallengeFailedMessage = challengeFailedMessage
	guestbook.ChallengeAnswer = challengeAnswer
	guestbook.CustomPageCSS = customPageCSS

	if err := readGuestbookOptions(r, &guestbook); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result = db.Model(&Guestbook{}).Where("id = ? AND admin_user_id = ?", guestbook.ID, currentUser.ID).
		Updates(map[string]any{
			"website_url": guestbook.WebsiteURL, "requires_approval": guestbook.RequiresApproval,
			"pow_enabled": guestbook.PowEnabled, "challenge_question": guestbook.ChallengeQuestion,
			"challenge_hint": guestbook.ChallengeHint, "challenge_failed_message": guestbook.ChallengeFailedMessage,
			"challenge_answer": guestbook.ChallengeAnswer, "custom_page_css": guestbook.CustomPageCSS,
			"submission_action": guestbook.SubmissionAction, "submission_message": guestbook.SubmissionMessage,
			"submission_redirect_url": guestbook.SubmissionRedirectURL, "collect_email": guestbook.CollectEmail,
			"email_field_label": guestbook.EmailFieldLabel, "email_field_help": guestbook.EmailFieldHelp,
		})
	if result.Error != nil {
		http.Error(w, "Error updating guestbook", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected != 1 {
		http.Error(w, "The guestbook was deleted or changed. Reload before retrying.", http.StatusConflict)
		return
	}

	http.Redirect(w, r, "/admin/guestbook/"+guestbookID+"/edit", http.StatusSeeOther)
}

func AdminEditMessage(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")
	messageID := chi.URLParam(r, "messageID")

	if r.Method == "GET" {
		var message Message
		result := activeMessagesQuery(db.WithContext(r.Context())).
			Where("messages.id = ? AND messages.guestbook_id = ?", messageID, guestbookID).First(&message)
		if result.Error != nil {
			recordLookupError(w, result.Error, "message")
			return
		}

		var guestbook Guestbook
		result = db.First(&guestbook, message.GuestbookID)
		if result.Error != nil {
			http.Error(w, "Guestbook not found", http.StatusNotFound)
			return
		}

		currentUser := getSignedInAdminOrFail(r)
		if guestbook.AdminUserID != currentUser.ID {
			http.Error(w, "You don't own this guestbook", http.StatusUnauthorized)
			return
		}

		renderAdminTemplate(w, r, "edit_message", message)
	} else if r.Method == "POST" {
		name := strings.TrimSpace(r.FormValue("name"))
		text := normalizedMessageText(r.FormValue("text"))
		website := strings.TrimSpace(r.FormValue("website"))
		if err := messageInputError(name, text, website); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		isApproved := r.FormValue("isApproved") == "on"

		var websitePtr *string
		if website != "" {
			websitePtr = &website
		}

		var message Message
		result := activeMessagesQuery(db.WithContext(r.Context())).
			Where("messages.id = ? AND messages.guestbook_id = ?", messageID, guestbookID).First(&message)
		if result.Error != nil {
			recordLookupError(w, result.Error, "message")
			return
		}

		var guestbook Guestbook
		result = db.First(&guestbook, message.GuestbookID)
		if result.Error != nil {
			http.Error(w, "Guestbook not found", http.StatusNotFound)
			return
		}

		currentUser := getSignedInAdminOrFail(r)
		if guestbook.AdminUserID != currentUser.ID {
			http.Error(w, "You don't own this guestbook", http.StatusUnauthorized)
			return
		}

		result = activeMessagesQuery(db.WithContext(r.Context())).
			Where("messages.id = ? AND messages.guestbook_id = ?", message.ID, guestbook.ID).
			Where("EXISTS (SELECT 1 FROM guestbooks WHERE guestbooks.id = messages.guestbook_id AND guestbooks.admin_user_id = ?)", currentUser.ID).
			Updates(map[string]any{"name": name, "text": text, "website": websitePtr, "approved": isApproved})
		if result.Error != nil {
			http.Error(w, "Error updating message", http.StatusInternalServerError)
			return
		}
		if result.RowsAffected != 1 {
			http.Error(w, "The message was deleted or changed. Reload before retrying.", http.StatusConflict)
			return
		}

		// Invalidate cache for this guestbook since message was edited
		messageCache.InvalidateGuestbook(guestbook.ID)

		http.Redirect(w, r, "/admin/guestbook/"+guestbookID, http.StatusSeeOther)
	}
}

func AdminDeleteMessage(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")
	messageID := chi.URLParam(r, "messageID")

	var guestbook Guestbook
	result := db.First(&guestbook, guestbookID)
	if result.Error != nil {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}

	currentUser := getSignedInAdminOrFail(r)
	if guestbook.AdminUserID != currentUser.ID {
		http.Error(w, "You don't own this guestbook", http.StatusUnauthorized)
		return
	}

	var message Message
	result = db.First(&message, messageID)
	if result.Error != nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	// Ensure the message belongs to the same guestbook scoped in the URL
	if message.GuestbookID != guestbook.ID {
		http.Error(w, "Message does not belong to this guestbook", http.StatusBadRequest)
		return
	}

	log.Printf("admin=%d username=%q action=delete_message guestbook_id=%d message_id=%d", currentUser.ID, currentUser.Username, guestbook.ID, message.ID)

	if err := softDeleteMessages(guestbook.ID, []uint{message.ID}); err != nil {
		log.Printf("Delete message=%d: %v", message.ID, err)
		http.Error(w, "Error deleting message", http.StatusInternalServerError)
		return
	}

	// Invalidate cache for this guestbook since message was deleted
	messageCache.InvalidateGuestbook(guestbook.ID)

	http.Redirect(w, r, "/admin/guestbook/"+guestbookID, http.StatusSeeOther)
}

func AdminReplyToMessage(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")
	messageID := chi.URLParam(r, "messageID")
	replyText := normalizedMessageText(r.FormValue("text"))

	if replyText == "" {
		http.Error(w, "Reply text cannot be empty", http.StatusBadRequest)
		return
	}

	var guestbook Guestbook
	result := db.First(&guestbook, guestbookID)
	if result.Error != nil {
		recordLookupError(w, result.Error, "guestbook")
		return
	}

	currentUser := getSignedInAdminOrFail(r)
	if guestbook.AdminUserID != currentUser.ID {
		http.Error(w, "You don't own this guestbook", http.StatusUnauthorized)
		return
	}

	var parentMessage Message
	result = db.First(&parentMessage, messageID)
	if result.Error != nil {
		recordLookupError(w, result.Error, "message")
		return
	}

	// Ensure the parent message belongs to the same guestbook
	if parentMessage.GuestbookID != guestbook.ID {
		http.Error(w, "Message does not belong to this guestbook", http.StatusBadRequest)
		return
	}

	// Don't allow replies to replies (only one level deep)
	if parentMessage.ParentMessageID != nil {
		http.Error(w, "Cannot reply to a reply", http.StatusBadRequest)
		return
	}

	if err := messageInputError(currentUser.ReplyName(), replyText, ""); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	parentMessageID := parentMessage.ID
	replyMessage := Message{
		Name:            currentUser.ReplyName(),
		Text:            replyText,
		Website:         nil,
		GuestbookID:     guestbook.ID,
		Approved:        true,
		ParentMessageID: &parentMessageID,
	}

	err := writeTransaction(db.WithContext(r.Context()), func(tx *gorm.DB) error {
		var count int64
		if err := activeMessagesQuery(tx).
			Where("messages.id = ? AND messages.guestbook_id = ? AND messages.parent_message_id IS NULL", parentMessage.ID, guestbook.ID).
			Where("EXISTS (SELECT 1 FROM guestbooks WHERE guestbooks.id = messages.guestbook_id AND guestbooks.admin_user_id = ?)", currentUser.ID).
			Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return gorm.ErrRecordNotFound
		}
		return tx.Create(&replyMessage).Error
	})
	if err != nil {
		recordLookupError(w, err, "reply parent")
		return
	}

	// Invalidate cache for this guestbook since a reply was added
	messageCache.InvalidateGuestbook(guestbook.ID)

	http.Redirect(w, r, "/admin/guestbook/"+guestbookID, http.StatusSeeOther)
}

func AdminBulkDeleteMessages(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")

	var guestbook Guestbook
	result := db.First(&guestbook, guestbookID)
	if result.Error != nil {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}

	currentUser := getSignedInAdminOrFail(r)
	if guestbook.AdminUserID != currentUser.ID {
		http.Error(w, "You don't own this guestbook", http.StatusUnauthorized)
		return
	}

	// Parse the request body
	var requestBody struct {
		MessageIDs []string `json:"message_ids"`
	}

	err := json.NewDecoder(r.Body).Decode(&requestBody)
	if err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if len(requestBody.MessageIDs) == 0 {
		http.Error(w, "No messages specified for deletion", http.StatusBadRequest)
		return
	}

	// Convert string IDs to uints and validate all messages belong to this guestbook
	var messageIDs []uint
	seen := make(map[uint]bool)
	for _, idStr := range requestBody.MessageIDs {
		id, err := parsePositiveID(idStr)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid message ID: %s", idStr), http.StatusBadRequest)
			return
		}
		if !seen[id] {
			messageIDs = append(messageIDs, id)
			seen[id] = true
		}
	}

	// Verify all messages belong to this guestbook
	var count int64
	if err := activeMessagesQuery(db).Where("messages.id IN ? AND messages.guestbook_id = ?", messageIDs, guestbook.ID).Count(&count).Error; err != nil {
		recordLookupError(w, err, "message selection")
		return
	}
	if count != int64(len(messageIDs)) {
		http.Error(w, "Some messages do not belong to this guestbook", http.StatusBadRequest)
		return
	}

	log.Printf("admin=%d username=%q action=bulk_delete_messages guestbook_id=%d message_count=%d message_ids=%v",
		currentUser.ID, currentUser.Username, guestbook.ID, len(messageIDs), messageIDs)

	// Delete messages in a transaction
	err = softDeleteMessages(guestbook.ID, messageIDs)

	if err != nil {
		http.Error(w, "Error deleting messages", http.StatusInternalServerError)
		return
	}

	// Invalidate cache for this guestbook since messages were deleted
	messageCache.InvalidateGuestbook(guestbook.ID)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Messages deleted successfully"))
}

func AdminBulkApproveMessages(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")

	var guestbook Guestbook
	result := db.First(&guestbook, guestbookID)
	if result.Error != nil {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}

	currentUser := getSignedInAdminOrFail(r)
	if guestbook.AdminUserID != currentUser.ID {
		http.Error(w, "You don't own this guestbook", http.StatusUnauthorized)
		return
	}

	var requestBody struct {
		MessageIDs []string `json:"message_ids"`
	}

	err := json.NewDecoder(r.Body).Decode(&requestBody)
	if err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if len(requestBody.MessageIDs) == 0 {
		http.Error(w, "No messages specified for approval", http.StatusBadRequest)
		return
	}

	messageIDs := make([]uint, 0, len(requestBody.MessageIDs))
	seen := make(map[uint]bool)
	for _, idStr := range requestBody.MessageIDs {
		id, err := parsePositiveID(idStr)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid message ID: %s", idStr), http.StatusBadRequest)
			return
		}
		if !seen[id] {
			messageIDs = append(messageIDs, id)
			seen[id] = true
		}
	}

	var count int64
	result = activeMessagesQuery(db).Where("messages.id IN ? AND messages.guestbook_id = ?", messageIDs, guestbook.ID).Count(&count)
	if result.Error != nil {
		http.Error(w, "Error validating messages", http.StatusInternalServerError)
		return
	}
	if count != int64(len(messageIDs)) {
		http.Error(w, "Some messages do not belong to this guestbook", http.StatusBadRequest)
		return
	}

	log.Printf("admin=%d username=%q action=bulk_approve_messages guestbook_id=%d message_count=%d message_ids=%v",
		currentUser.ID, currentUser.Username, guestbook.ID, len(messageIDs), messageIDs)

	result = db.Model(&Message{}).
		Where("id IN ? AND guestbook_id = ?", messageIDs, guestbook.ID).
		Update("approved", true)
	if result.Error != nil {
		http.Error(w, "Error approving messages", http.StatusInternalServerError)
		return
	}

	messageCache.InvalidateGuestbook(guestbook.ID)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Messages approved successfully"))
}

func AdminUserSettings(w http.ResponseWriter, r *http.Request) {
	currentUser := getSignedInAdminOrFail(r)

	if r.Method == "GET" {
		notice := ""
		switch r.URL.Query().Get("verification") {
		case "sent":
			notice = "Verification email requested. Check your inbox and follow the link to verify your address."
		case "unavailable":
			notice = "Settings saved, but no verification email was sent because email delivery is disabled on this instance."
		case "cooldown":
			notice = "Settings saved, but no verification email was sent. Wait one minute between verification attempts, then use Resend verification."
		}
		renderUserSettings(w, r, currentUser, notice, "info", http.StatusOK)
		return
	}

	section := r.FormValue("settings_section")
	if section != "" && section != "display_name" && section != "email" {
		http.Error(w, "Unknown settings form", http.StatusBadRequest)
		return
	}
	updatedUser := *currentUser
	updates := map[string]any{}
	if section == "" || section == "display_name" {
		updatedUser.DisplayName = strings.TrimSpace(r.FormValue("display_name"))
		if !utf8.ValidString(updatedUser.DisplayName) || utf8.RuneCountInString(updatedUser.DisplayName) > maxNameCharacters {
			http.Error(w, "Display name must contain at most 200 characters", http.StatusBadRequest)
			return
		}
		updates["display_name"] = updatedUser.DisplayName
	}
	if section == "" || section == "email" {
		updatedUser.Email = strings.TrimSpace(r.FormValue("email"))
		updatedUser.EmailNotifications = r.FormValue("notify") == "on"
		updates["email"] = updatedUser.Email
		updates["email_notifications"] = updatedUser.EmailNotifications
	}
	hasChangedEmail := currentUser.Email != updatedUser.Email
	if hasChangedEmail {
		if _, err := optionalEmail(updatedUser.Email); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		updatedUser.EmailVerificationToken = ""
		if updatedUser.Email != "" {
			token, err := generateAuthToken()
			if err != nil {
				http.Error(w, "Error creating verification token", http.StatusInternalServerError)
				return
			}
			updatedUser.EmailVerificationToken = token
		}
		updatedUser.EmailVerified = false
		updates["email_verification_token"] = updatedUser.EmailVerificationToken
		updates["email_verified"] = false
		updates["password_reset_token"] = ""
		updates["password_reset_expiry"] = 0
		updatedUser.PasswordResetToken = ""
		updatedUser.PasswordResetExpiry = 0
	}
	query := db.Model(&AdminUser{}).Where("id = ?", currentUser.ID)
	if section == "" || section == "email" {
		if currentUser.Email == "" {
			query = query.Where("(email = '' OR email IS NULL)")
		} else {
			query = query.Where("email = ?", currentUser.Email)
		}
	}
	result := query.Updates(updates)
	if result.Error != nil {
		http.Error(w, "Error updating user settings", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected != 1 {
		http.Error(w, "Your settings changed. Reload the page before trying again.", http.StatusConflict)
		return
	}

	displayNameChanged := currentUser.DisplayName != updatedUser.DisplayName
	*currentUser = updatedUser
	if displayNameChanged {
		// Update all existing replies by this user to use the new display name
		newReplyName := currentUser.ReplyName()
		result := db.Model(&Message{}).
			Where("parent_message_id IS NOT NULL AND guestbook_id IN (?)",
				db.Model(&Guestbook{}).Select("id").Where("admin_user_id = ?", currentUser.ID)).
			Update("name", newReplyName)
		if result.Error != nil {
			http.Error(w, "Settings saved, but existing replies could not be updated", http.StatusInternalServerError)
			return
		}

		// Invalidate cache for all guestbooks owned by this user
		var userGuestbooks []Guestbook
		if err := db.Where("admin_user_id = ?", currentUser.ID).Find(&userGuestbooks).Error; err != nil {
			messageCache.Clear()
			http.Error(w, "Settings saved, but guestbook caches could not be refreshed", http.StatusInternalServerError)
			return
		}
		for _, g := range userGuestbooks {
			messageCache.InvalidateGuestbook(g.ID)
		}
	}
	if hasChangedEmail && currentUser.Email != "" {
		if !emailDeliveryEnabled() {
			http.Redirect(w, r, "/admin/settings?verification=unavailable#settings-notice", http.StatusSeeOther)
			return
		}
		retryAfter, err := attemptVerification(r.Context(), currentUser)
		if err != nil {
			log.Printf("Error sending verification for admin=%d: %v", currentUser.ID, safeMailError(err))
			status := http.StatusBadGateway
			if errors.Is(err, ErrVerificationStorage) {
				status = http.StatusInternalServerError
			} else if errors.Is(err, ErrVerificationChanged) {
				status = http.StatusConflict
			}
			renderUserSettings(w, r, currentUser, "Settings saved, but the verification email could not be sent. Reload settings before retrying.", "warning", status)
			return
		}
		if retryAfter > 0 {
			w.Header().Set("Retry-After", fmt.Sprint(int((retryAfter+time.Second-1)/time.Second)))
			http.Redirect(w, r, "/admin/settings?verification=cooldown#settings-notice", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin/settings?verification=sent#settings-notice", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

func AdminChangePassword(w http.ResponseWriter, r *http.Request) {
	currentPassword := r.FormValue("current-password")
	newPassword := r.FormValue("new-password")
	confirmPassword := r.FormValue("confirm-password")

	if newPassword != confirmPassword || len(newPassword) == 0 || len(newPassword) > 72 {
		http.Error(w, "New passwords must match and contain 1 to 72 bytes", http.StatusBadRequest)
		return
	}

	currentUser := getSignedInAdminOrFail(r)
	err := bcrypt.CompareHashAndPassword([]byte(currentUser.PasswordHash), []byte(currentPassword))
	if err != nil {
		http.Error(w, "Current password is incorrect", http.StatusUnauthorized)
		return
	}

	newPasswordHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Error creating account: "+err.Error(), http.StatusInternalServerError)
		return
	}

	token, err := generateAuthToken()
	if err != nil {
		http.Error(w, "Error updating password", http.StatusInternalServerError)
		return
	}
	result := db.Model(&AdminUser{}).
		Where("id = ? AND password_hash = ? AND session_token = ?", currentUser.ID, string(currentUser.PasswordHash), currentUser.SessionToken).
		Updates(map[string]any{
			"password_hash": string(newPasswordHash), "password_reset_token": "", "password_reset_expiry": 0,
			"session_token": token, "session_expires_at": time.Now().Add(sessionLifetime).Unix(),
		})
	if result.Error != nil {
		http.Error(w, "Error updating password", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected != 1 {
		http.Error(w, "Your account changed. Sign in again before changing your password.", http.StatusConflict)
		return
	}
	setSessionCookie(w, token)

	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

func VerifyEmailHandler(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "Token is required", http.StatusBadRequest)
		return
	}

	result := db.Model(&AdminUser{}).
		Where("email_verification_token = ? AND (email_verified = ? OR email_verified IS NULL) AND email IS NOT NULL AND email != ''", token, false).
		Updates(map[string]any{"email_verified": true, "email_verification_token": ""})
	if result.Error != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected != 1 {
		http.Error(w, "This verification link is invalid, already used, or superseded.", http.StatusBadRequest)
		return
	}

	// Redirect to a confirmation page or display a success message
	w.Write([]byte("Email verified successfully!"))
}

func ForgotPasswordHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		renderAdminTemplate(w, r, "forgot_password", nil)
		return
	}

	if !emailDeliveryEnabled() {
		http.Error(w, "Password recovery email is unavailable on this instance. Contact the operator.", http.StatusServiceUnavailable)
		return
	}
	identifier := r.FormValue("username")
	if strings.TrimSpace(identifier) == "" {
		identifier = strings.TrimSpace(r.FormValue("email"))
	}

	// Always show success to avoid user enumeration
	user, err := lookupUsername(identifier)
	if errors.Is(err, gorm.ErrRecordNotFound) && strings.TrimSpace(identifier) != "" {
		err = db.Where("email = ? AND email_verified = ?", strings.TrimSpace(identifier), true).First(&user).Error
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		recordLookupError(w, err, "account")
		return
	}
	if err != nil || user.Email == "" || !user.EmailVerified {
		renderAdminTemplate(w, r, "password_reset_sent", nil)
		return
	}

	token, err := generateAuthToken()
	if err != nil {
		http.Error(w, "Error processing request", http.StatusInternalServerError)
		return
	}

	// Set token expiration (24 hours from now)
	expiryTime := time.Now().Add(24 * time.Hour).Unix()

	result := db.Model(&AdminUser{}).
		Where("id = ? AND email = ? AND email_verified = ? AND password_hash = ?", user.ID, user.Email, true, string(user.PasswordHash)).
		Updates(map[string]any{"password_reset_token": token, "password_reset_expiry": expiryTime})
	if result.Error != nil {
		http.Error(w, "Error processing request", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected == 1 {
		if err := queueMail(fmt.Sprintf("password reset for admin=%d", user.ID), func(ctx context.Context) error {
			return SendPasswordResetEmailContext(ctx, user.Email, token)
		}); err != nil {
			log.Printf("Queue password reset for admin=%d: %v", user.ID, err)
		}
	}

	renderAdminTemplate(w, r, "password_reset_sent", nil)
}

func ResetPasswordFormHandler(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "Invalid reset link", http.StatusBadRequest)
		return
	}

	// Check if token is valid and not expired
	var user AdminUser
	result := db.Where("password_reset_token = ?", token).First(&user)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			http.Error(w, "Invalid reset link", http.StatusBadRequest)
		} else {
			recordLookupError(w, result.Error, "account")
		}
		return
	}

	if user.PasswordResetExpiry <= time.Now().Unix() {
		http.Error(w, "Reset link has expired. Please request a new one.", http.StatusBadRequest)
		return
	}

	data := struct {
		Token string
	}{
		Token: token,
	}

	renderAdminTemplate(w, r, "reset_password", data)
}

func ResetPasswordHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := r.FormValue("token")
	newPassword := r.FormValue("new-password")
	confirmPassword := r.FormValue("confirm-password")

	if token == "" {
		http.Error(w, "Invalid reset link", http.StatusBadRequest)
		return
	}

	if newPassword == "" || confirmPassword == "" || len(newPassword) > 72 {
		http.Error(w, "Password must contain 1 to 72 bytes", http.StatusBadRequest)
		return
	}

	if newPassword != confirmPassword {
		http.Error(w, "Passwords do not match", http.StatusBadRequest)
		return
	}

	var user AdminUser
	result := db.Where("password_reset_token = ?", token).First(&user)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			http.Error(w, "Invalid reset link", http.StatusBadRequest)
		} else {
			recordLookupError(w, result.Error, "account")
		}
		return
	}

	if user.PasswordResetExpiry <= time.Now().Unix() {
		http.Error(w, "Reset link has expired. Please request a new one.", http.StatusBadRequest)
		return
	}

	newPasswordHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Error resetting password: "+err.Error(), http.StatusInternalServerError)
		return
	}

	revokedToken, err := generateAuthToken()
	if err != nil {
		http.Error(w, "Error resetting password", http.StatusInternalServerError)
		return
	}
	result = db.Model(&AdminUser{}).
		Where("id = ? AND password_reset_token = ? AND password_reset_expiry > ?", user.ID, token, time.Now().Unix()).
		Updates(map[string]any{
			"password_hash": string(newPasswordHash), "password_reset_token": "", "password_reset_expiry": 0,
			"session_token": revokedToken, "session_expires_at": 0,
		})
	if result.Error != nil {
		http.Error(w, "Error resetting password", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected != 1 {
		http.Error(w, "This reset link is expired, already used, or superseded.", http.StatusBadRequest)
		return
	}
	clearSessionCookie(w)

	http.Redirect(w, r, "/admin/signin?password_reset=success", http.StatusSeeOther)
}
