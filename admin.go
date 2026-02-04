package main

import (
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

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type AdminCookieName string

const AdminUserCookieName = AdminCookieName("admin_user")
const AdminTokenCookieName = AdminCookieName("admin_token")

func renderAdminTemplate(w http.ResponseWriter, r *http.Request, tmpl string, data any) {
	templateData := struct {
		CurrentUser *AdminUser
		Data        any
	}{
		CurrentUser: getSignedInAdminUserOrNil(r),
		Data:        data,
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
		log.Fatalf("Expected user to be signed in but it wasn't")
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
		// if logout then just continue
		if r.URL.Path == "/admin/logout" {
			next.ServeHTTP(w, r)
			return
		}

		// try to set admin user into context
		cookie, err := r.Cookie(string(AdminTokenCookieName))
		if err != nil || cookie.Value == "" {
			if r.URL.Path != "/admin/signin" && r.URL.Path != "/admin/signup" {
				http.Redirect(w, r, "/admin/signin", http.StatusSeeOther)
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
		result := db.Where(&AdminUser{SessionToken: cookie.Value}).First(&user)
		if result.Error != nil {
			// Clear the invalid cookie
			http.SetCookie(w, &http.Cookie{
				Name:   string(AdminTokenCookieName),
				Value:  "",
				Path:   "/",
				MaxAge: -1,
			})

			http.Redirect(w, r, "/admin/signin", http.StatusSeeOther)
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

		var admin AdminUser
		result := db.Where(&AdminUser{Username: username}).First(&admin)
		if result.Error != nil {
			http.Error(w, "Invalid username. You're trying to sign in, but perhaps you still need to sign up?", http.StatusUnauthorized)
			return
		}

		err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(password))
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

		admin.SessionToken = token
		db.Save(&admin)

		http.SetCookie(w, &http.Cookie{
			Name:     string(AdminTokenCookieName),
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}
}

func AdminSignUp(w http.ResponseWriter, r *http.Request) {
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

		newAdmin := AdminUser{Username: username, PasswordHash: passwordHash, SessionToken: token}

		result := db.Create(&newAdmin)
		if result.Error != nil {
			http.Error(w, "Error creating account: "+result.Error.Error(), http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     string(AdminTokenCookieName),
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		// Redirect to the admin sign-in page after successful sign-up
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}
}

func AdminLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:   string(AdminTokenCookieName),
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
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
		db.Model(&Message{}).Where("guestbook_id = ?", g.ID).Count(&total)
		db.Model(&Message{}).Where("guestbook_id = ? AND approved = ?", g.ID, false).Count(&pending)

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
	result := db.Preload("Messages", func(db *gorm.DB) *gorm.DB {
		return db.Where("parent_message_id IS NULL").Order("created_at desc")
	}).Preload("Messages.Replies", func(db *gorm.DB) *gorm.DB {
		return db.Order("created_at asc")
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
		requiresApproval := r.FormValue("requiresApproval") == "on"
		customPageCSS := strings.TrimSpace(r.FormValue("customPageCSS"))

		isCssValid, errorMsg := validateCSS(customPageCSS)
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
			ChallengeQuestion:      challengeQuestion,
			ChallengeHint:          challengeHint,
			ChallengeFailedMessage: challengeFailedMessage,
			ChallengeAnswer:        challengeAnswer,
			CustomPageCSS:          customPageCSS,
			AdminUserID:            adminUser.ID,
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

	hostUrl := constants.PUBLIC_URL
	if constants.DEBUG_MODE {
		hostUrl = "//" + r.Host
	}

	data := struct {
		Guestbook     Guestbook
		PublicHostUrl string
	}{
		Guestbook:     guestbook,
		PublicHostUrl: hostUrl,
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

	log.Printf("admin=%d username=%q ip=%s action=delete_guestbook guestbook_id=%d", currentUser.ID, currentUser.Username, r.RemoteAddr, guestbook.ID)

	result = db.Delete(&guestbook, guestbookID)
	if result.Error != nil {
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
	requiresApproval := r.FormValue("requiresApproval") == "on"
	customPageCSS := strings.TrimSpace(r.FormValue("customPageCSS"))

	isCssValid, errorMsg := validateCSS(customPageCSS)
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
	guestbook.ChallengeQuestion = challengeQuestion
	guestbook.ChallengeHint = challengeHint
	guestbook.ChallengeFailedMessage = challengeFailedMessage
	guestbook.ChallengeAnswer = challengeAnswer
	guestbook.CustomPageCSS = customPageCSS

	result = db.Save(&guestbook)
	if result.Error != nil {
		http.Error(w, "Error updating guestbook", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/guestbook/"+guestbookID+"/edit", http.StatusSeeOther)
}

func AdminEditMessage(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")
	messageID := chi.URLParam(r, "messageID")

	if r.Method == "GET" {
		var message Message
		result := db.First(&message, messageID)
		if result.Error != nil {
			http.Error(w, "Message not found", http.StatusNotFound)
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
		name := r.FormValue("name")
		text := r.FormValue("text")
		website := r.FormValue("website")
		isApproved := r.FormValue("isApproved") == "on"

		var websitePtr *string
		if website != "" {
			websitePtr = &website
		}

		var message Message
		result := db.First(&message, messageID)
		if result.Error != nil {
			http.Error(w, "Message not found", http.StatusNotFound)
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

		message.Name = name
		message.Text = text
		message.Website = websitePtr
		message.Approved = isApproved

		result = db.Save(&message)
		if result.Error != nil {
			http.Error(w, "Error updating message", http.StatusInternalServerError)
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

	log.Printf("admin=%d username=%q ip=%s action=delete_message guestbook_id=%d message_id=%d", currentUser.ID, currentUser.Username, r.RemoteAddr, guestbook.ID, message.ID)

	result = db.Delete(&message, messageID)
	if result.Error != nil {
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
	replyText := strings.TrimSpace(r.FormValue("text"))

	if replyText == "" {
		http.Error(w, "Reply text cannot be empty", http.StatusBadRequest)
		return
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

	var parentMessage Message
	result = db.First(&parentMessage, messageID)
	if result.Error != nil {
		http.Error(w, "Message not found", http.StatusNotFound)
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

	if len(replyText) > constants.MAX_MESSAGE_LENGTH {
		http.Error(w, "Reply is too long, maximum length is "+fmt.Sprint(constants.MAX_MESSAGE_LENGTH)+" characters", http.StatusBadRequest)
		return
	}

	parentMessageID := parentMessage.ID
	replyMessage := Message{
		Name:            currentUser.Username,
		Text:            replyText,
		Website:         nil,
		GuestbookID:     guestbook.ID,
		Approved:        true,
		ParentMessageID: &parentMessageID,
	}

	result = db.Create(&replyMessage)
	if result.Error != nil {
		http.Error(w, "Error creating reply", http.StatusInternalServerError)
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
	for _, idStr := range requestBody.MessageIDs {
		var id int
		_, err := fmt.Sscanf(idStr, "%d", &id)
		if err != nil || id <= 0 {
			http.Error(w, fmt.Sprintf("Invalid message ID: %s", idStr), http.StatusBadRequest)
			return
		}
		messageIDs = append(messageIDs, uint(id))
	}

	// Verify all messages belong to this guestbook
	var count int64
	db.Model(&Message{}).Where("id IN ? AND guestbook_id = ?", messageIDs, guestbook.ID).Count(&count)
	if count != int64(len(messageIDs)) {
		http.Error(w, "Some messages do not belong to this guestbook", http.StatusBadRequest)
		return
	}

	log.Printf("admin=%d username=%q ip=%s action=bulk_delete_messages guestbook_id=%d message_count=%d message_ids=%v",
		currentUser.ID, currentUser.Username, r.RemoteAddr, guestbook.ID, len(messageIDs), messageIDs)

	// Delete messages in a transaction
	err = db.Transaction(func(tx *gorm.DB) error {
		result := tx.Where("id IN ? AND guestbook_id = ?", messageIDs, guestbook.ID).Delete(&Message{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(messageIDs)) {
			return fmt.Errorf("expected to delete %d messages but only deleted %d", len(messageIDs), result.RowsAffected)
		}
		return nil
	})

	if err != nil {
		http.Error(w, "Error deleting messages", http.StatusInternalServerError)
		return
	}

	// Invalidate cache for this guestbook since messages were deleted
	messageCache.InvalidateGuestbook(guestbook.ID)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Messages deleted successfully"))
}

func AdminUserSettings(w http.ResponseWriter, r *http.Request) {
	currentUser := getSignedInAdminOrFail(r)

	if r.Method == "GET" {
		renderAdminTemplate(w, r, "user_settings", currentUser)
	} else {
		email := strings.TrimSpace(r.FormValue("email"))
		notify := r.FormValue("notify") == "on"

		hasChangedEmail := currentUser.Email != email

		currentUser.Email = email
		currentUser.EmailNotifications = notify

		if hasChangedEmail {
			newToken, err := generateAuthToken()
			if err != nil {
				http.Error(w, "Error updating user settings", http.StatusInternalServerError)
				return
			}

			currentUser.EmailVerificationToken = newToken
			currentUser.EmailVerified = false

			go SendVerificationEmail(currentUser.Email, newToken)
		}

		result := db.Save(&currentUser)
		if result.Error != nil {
			http.Error(w, "Error updating user settings", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
	}
}

func AdminChangePassword(w http.ResponseWriter, r *http.Request) {
	currentPassword := r.FormValue("current-password")
	newPassword := r.FormValue("new-password")
	confirmPassword := r.FormValue("confirm-password")

	if newPassword != confirmPassword {
		http.Error(w, "New passwords do not match", http.StatusBadRequest)
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

	currentUser.PasswordHash = newPasswordHash
	result := db.Save(&currentUser)
	if result.Error != nil {
		http.Error(w, "Error updating password", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

func VerifyEmailHandler(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "Token is required", http.StatusBadRequest)
		return
	}

	var user AdminUser
	result := db.Where(&AdminUser{EmailVerificationToken: token}).First(&user)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			http.Error(w, "Invalid token. It could be that the token is mispelled or, more likely, you've already confirmed your email. Verification tokens are single use!", http.StatusBadRequest)
		} else {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	user.EmailVerified = true
	user.EmailVerificationToken = "" // Clear the token after verification
	result = db.Save(&user)
	if result.Error != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
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

	identifier := strings.TrimSpace(r.FormValue("username"))
	if identifier == "" {
		identifier = strings.TrimSpace(r.FormValue("email"))
	}

	// Always show success to avoid user enumeration
	var user AdminUser
	var result *gorm.DB

	if identifier != "" {
		// Try lookup by username first
		result = db.Where(&AdminUser{Username: identifier}).First(&user)
		if result.Error != nil {
			// Fallback to email lookup (only if verified)
			result = db.Where(&AdminUser{Email: identifier, EmailVerified: true}).First(&user)
		}
	}

	if result == nil || result.Error != nil || user.Email == "" || !user.EmailVerified {
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

	user.PasswordResetToken = token
	user.PasswordResetExpiry = expiryTime
	result = db.Save(&user)
	if result.Error != nil {
		http.Error(w, "Error processing request", http.StatusInternalServerError)
		return
	}

	go SendPasswordResetEmail(user.Email, token)

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
	result := db.Where(&AdminUser{PasswordResetToken: token}).First(&user)
	if result.Error != nil {
		http.Error(w, "Invalid reset link", http.StatusBadRequest)
		return
	}

	if user.PasswordResetExpiry < time.Now().Unix() {
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

	if newPassword == "" || confirmPassword == "" {
		http.Error(w, "Passwords cannot be empty", http.StatusBadRequest)
		return
	}

	if newPassword != confirmPassword {
		http.Error(w, "Passwords do not match", http.StatusBadRequest)
		return
	}

	var user AdminUser
	result := db.Where(&AdminUser{PasswordResetToken: token}).First(&user)
	if result.Error != nil {
		http.Error(w, "Invalid reset link", http.StatusBadRequest)
		return
	}

	if user.PasswordResetExpiry < time.Now().Unix() {
		http.Error(w, "Reset link has expired. Please request a new one.", http.StatusBadRequest)
		return
	}

	newPasswordHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Error resetting password: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Update user's password and clear reset token
	user.PasswordHash = newPasswordHash
	user.PasswordResetToken = ""
	user.PasswordResetExpiry = 0

	result = db.Save(&user)
	if result.Error != nil {
		http.Error(w, "Error resetting password", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/signin?password_reset=success", http.StatusSeeOther)
}
