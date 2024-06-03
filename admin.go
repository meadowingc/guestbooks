package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"html/template"
	"log"
	"net/http"
	"path/filepath"

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

		// Validate the token and retrieve the corresponding admin user
		var admin AdminUser
		result := db.Where("token = ?", cookie.Value).First(&admin)
		if result.Error != nil {
			http.Redirect(w, r, "/admin/signin", http.StatusSeeOther)
			return
		}

		// Store the admin user in the context
		ctx := context.WithValue(r.Context(), AdminUserCookieName, &admin)
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
		result := db.Where("username = ?", username).First(&admin)
		if result.Error != nil {
			http.Error(w, "Invalid username", http.StatusUnauthorized)
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

		admin.Token = token
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

		newAdmin := AdminUser{Username: username, PasswordHash: passwordHash}
		result := db.Create(&newAdmin)
		if result.Error != nil {
			http.Error(w, "Error creating account: "+result.Error.Error(), http.StatusInternalServerError)
			return
		}

		// Create a new token and store it in a cookie
		token, err := generateAuthToken()
		if err != nil {
			http.Error(w, "Error creating account: "+err.Error(), http.StatusInternalServerError)
			return
		}
		newAdmin.Token = token
		db.Save(&newAdmin)

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
	result := db.Where("admin_user_id = ?", adminUser.ID).Find(&guestbooks)
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
		newGuestbook := Guestbook{WebsiteURL: websiteURL, AdminUserID: adminUser.ID}
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

	renderAdminTemplate(w, r, "embed_guestbook", guestbook)
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
	result = db.Save(&guestbook)
	if result.Error != nil {
		http.Error(w, "Error updating guestbook", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
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
	if r.Method == "GET" {
		currentUser := getSignedInAdminOrFail(r)
		renderAdminTemplate(w, r, "user_settings", currentUser)
	} else {
		currentUser := getSignedInAdminOrFail(r)
		email := r.FormValue("email")
		notify := r.FormValue("notify") == "on"

		currentUser.Email = email
		currentUser.Notify = notify

		result := db.Save(&currentUser)
		if result.Error != nil {
			http.Error(w, "Error updating user settings", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
	}
}
