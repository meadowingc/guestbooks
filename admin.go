package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"path/filepath"

	"golang.org/x/crypto/bcrypt"
)

func renderAdminTemplate(w http.ResponseWriter, tmpl string, data any) {
	templatesDir := "templates/admin"

	templates, err := template.ParseFiles(
		filepath.Join(templatesDir, tmpl+".html"),
		filepath.Join(templatesDir, "layout.html"),
	)
	if err != nil {
		log.Fatalf("Error parsing templates: %v", err)
	}

	err = templates.ExecuteTemplate(w, tmpl+".html", data)
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
		token, err := generateAuthToken()
		if err != nil {
			http.Error(w, "Error creating account", http.StatusInternalServerError)
			return
		}
		newAdmin.Token = token
		db.Save(&newAdmin)

		http.SetCookie(w, &http.Cookie{
			Name:  "admin_token",
			Value: token,
			Path:  "/",
		})

		// Redirect to the admin sign-in page after successful sign-up
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}
}

func AdminSignIn(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		existingToken := r.Context().Value("admin_user").(*AdminUser)
		if existingToken != nil {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}

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

		// Generate a new token for the session
		token, err := generateAuthToken()
		if err != nil {
			http.Error(w, "Error signing in", http.StatusInternalServerError)
			return
		}
		admin.Token = token
		db.Save(&admin)

		http.SetCookie(w, &http.Cookie{
			Name:  "admin_token",
			Value: token,
			Path:  "/",
		})

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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("admin_token")
		if err != nil {
			if r.URL.Path == "/admin/signin" || r.URL.Path == "/admin/signup" {
				next.ServeHTTP(w, r)
				return
			} else {

				http.Redirect(w, r, "/admin/signin", http.StatusSeeOther)
				return
			}
		}

		// Validate the token and retrieve the corresponding admin user
		var admin AdminUser
		result := db.Where("token = ?", cookie.Value).First(&admin)
		if result.Error != nil {
			http.Error(w, "Unauthorized: "+result.Error.Error(), http.StatusUnauthorized)
			return
		}

		// Store the admin user in the context
		ctx := context.WithValue(r.Context(), "admin_user", &admin)
		next.ServeHTTP(w, r.WithContext(ctx))
	})

}
func generateAuthToken() (string, error) {
	// Implement the token generation logic here
	// This is a placeholder for demonstration purposes
	// In a real-world scenario, you would use a secure method to generate the token
	return "secure-random-token", nil
}
