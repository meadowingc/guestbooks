package main

import (
	"html/template"
	"log"
	"net/http"
	"path/filepath"

	"golang.org/x/crypto/bcrypt"
)

var adminTemplates = parseAdminTemplates()

func parseAdminTemplates() *template.Template {
	templatesDir := "templates/admin"

	layoutPattern := filepath.Join(templatesDir, "layout.html")
	contentPattern := filepath.Join(templatesDir, "*.html")
	templates, err := template.ParseFiles(layoutPattern)

	if err != nil {
		log.Fatalf("Error parsing admin templates: %v", err)
	}
	templates, err = templates.ParseGlob(contentPattern)
	if err != nil {
		log.Fatalf("Error parsing content templates: %v", err)
	}

	return templates
}

func renderAdminTemplate(w http.ResponseWriter, tmpl string, data interface{}) {
	err := adminTemplates.ExecuteTemplate(w, tmpl+".html", data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func AdminGuestbookList(w http.ResponseWriter, r *http.Request) {
	// TODO: Fetch the list of guestbooks for the signed-in admin and pass it to the template
	renderAdminTemplate(w, "guestbook_list", nil)
}

func AdminSignUp(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		renderAdminTemplate(w, "signup", nil)

	} else {
		username := r.FormValue("username")
		password := r.FormValue("password")

		// Here you should hash the password before storing it
		// For example, using bcrypt to generate a password hash
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "Error creating account", http.StatusInternalServerError)
			return
		}

		newAdmin := AdminUser{Username: username, PasswordHash: passwordHash}
		result := db.Create(&newAdmin)
		if result.Error != nil {
			http.Error(w, "Error creating account", http.StatusInternalServerError)
			return
		}

		// Create a new token and store it in a cookie
		token := newAdmin.ID // Replace this with your token generation logic
		http.SetCookie(w, &http.Cookie{
			Name:  "admin_token",
			Value: token,
			Path:  "/",
		})

		// Redirect to the admin sign-in page after successful sign-up
		http.Redirect(w, r, "/admin/signin", http.StatusSeeOther)
	}
}

func AdminSignIn(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		renderAdminTemplate(w, "signin", nil)

	} else {
		username := r.FormValue("username")
		password := r.FormValue("password")

		var admin AdminUser
		result := db.Where("username = ?", username).First(&admin)
		if result.Error != nil {
			http.Error(w, "Invalid credentials", http.StatusUnauthorized)
			return
		}

		// Here you should compare the password with the stored hash.
		// This is just a placeholder for the actual password check.
		// You should use a secure method like bcrypt to compare the password.
		if password != string(admin.PasswordHash) {
			http.Error(w, "Invalid credentials", http.StatusUnauthorized)
			return
		}

		// Here you should set up the session for the authenticated user.
		// This is just a placeholder for session management.

		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}
}

func AdminManageGuestbook(w http.ResponseWriter, r *http.Request) {
	renderAdminTemplate(w, "manage_guestbook", nil)
}

func AdminEditMessage(w http.ResponseWriter, r *http.Request) {
	renderAdminTemplate(w, "edit_message", nil)
}

func AdminDeleteMessage(w http.ResponseWriter, r *http.Request) {
	// This route will handle deletion and then redirect, no template needed
}

func AdminAuthMiddleware(next http.Handler) http.Handler {
	// TODO: Implement the admin authentication middleware
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Perform authentication check here

		// Pass through to the next handler if authentication is successful
		next.ServeHTTP(w, r)
	})

}
