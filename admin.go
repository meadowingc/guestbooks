package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"guestbook/constants"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type AdminCookieName string

const AdminUserCookieName = AdminCookieName("admin_user")
const AdminTokenCookieName = AdminCookieName("admin_token")

func renderAdminTemplate(w http.ResponseWriter, r *http.Request, tmpl string, data interface{}) {
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
			Name:  string(AdminTokenCookieName),
			Value: token,
			Path:  "/",
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
			Name:  string(AdminTokenCookieName),
			Value: token,
			Path:  "/",
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

	renderAdminTemplate(w, r, "guestbook_list", guestbooks)
}

func AdminShowGuestbook(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")

	var guestbook Guestbook
	result := db.Preload("Messages", func(db *gorm.DB) *gorm.DB {
		return db.Order("created_at desc")
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

	result = db.Delete(&guestbook, guestbookID)
	if result.Error != nil {
		http.Error(w, "Error deleting guestbook", http.StatusInternalServerError)
		return
	}

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

	renderAdminTemplate(w, r, "create_edit_guestbook", guestbook)
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

	result = db.Delete(&message, messageID)
	if result.Error != nil {
		http.Error(w, "Error deleting message", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/guestbook/"+guestbookID, http.StatusSeeOther)
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
