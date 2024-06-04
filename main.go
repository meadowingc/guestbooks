package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/cors"
	"github.com/spf13/viper"

	"gorm.io/gorm"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"

	"gorm.io/driver/sqlite"
)

var db *gorm.DB

func main() {
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	err := viper.ReadInConfig()
	if err != nil {
		panic(fmt.Errorf("fatal error config file: %w", err))
	}

	initDatabase()
	r := chi.NewRouter()

	CORSMiddleware := cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	})

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(CORSMiddleware.Handler)

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		renderAdminTemplate(w, r, "landing_page", nil)
	})

	r.Get("/verify-email", VerifyEmailHandler)

	r.Get("/terms-and-conditions", func(w http.ResponseWriter, r *http.Request) {
		renderAdminTemplate(w, r, "terms_and_conditions", nil)
	})

	r.With(AdminAuthMiddleware).Route("/admin", func(r chi.Router) {
		r.Get("/", AdminGuestbookList)
		r.Get("/settings", AdminUserSettings)
		r.Post("/settings", AdminUserSettings)

		r.Get("/signin", AdminSignIn)
		r.Post("/signin", AdminSignIn)

		r.Get("/signup", AdminSignUp)
		r.Post("/signup", AdminSignUp)

		r.Post("/logout", AdminLogout)

		r.Get("/guestbook/new", AdminCreateGuestbook)
		r.Post("/guestbook/new", AdminCreateGuestbook)

		r.Route("/guestbook/{guestbookID}", func(r chi.Router) {
			r.Get("/", AdminShowGuestbook)
			r.Get("/embed", AdminEmbedGuestbook)

			r.Get("/edit", AdminEditGuestbook)
			r.Post("/edit", AdminUpdateGuestbook)

			r.Post("/delete", AdminDeleteGuestbook)

			r.Route("/message/{messageID}", func(r chi.Router) {
				r.Get("/edit", AdminEditMessage)
				r.Post("/edit", AdminEditMessage)
				r.Post("/delete", AdminDeleteMessage)
			})
		})
	})

	r.Route("/guestbook", func(r chi.Router) {
		r.Get("/{guestbookID}", GuestbookPage)
		r.Post("/{guestbookID}/submit", GuestbookSubmit)
	})

	r.Route("/resources", func(r chi.Router) {
		r.Route("/js", func(r chi.Router) {
			r.Get("/embed_script/{guestbookID}/script.js", func(w http.ResponseWriter, r *http.Request) {
				guestbookID := chi.URLParam(r, "guestbookID")
				template, err := template.ParseFiles("templates/resources/embed_javascript.js")
				if err != nil {
					log.Fatalf("Error parsing guestbook page template: %v", err)
				}

				templateData := struct {
					GuestbookID string
				}{
					GuestbookID: guestbookID,
				}

				w.Header().Set("Cache-Control", "public, max-age=259200") // 3 days
				w.Header().Set("Expires", time.Now().Add(72*time.Hour).Format(http.TimeFormat))

				template.Execute(w, templateData)
			})
		})
	})

	r.Route("/api", func(r chi.Router) {
		r.Route("/v1", func(r chi.Router) {
			r.Get("/get-guestbook-messages/{guestbookID}", func(w http.ResponseWriter, r *http.Request) {
				guestbookID := chi.URLParam(r, "guestbookID")
				var messages []Message
				result := db.Where("guestbook_id = ?", guestbookID).Find(&messages)
				if result.Error != nil {
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(messages)
			})
		})
	})

	const portNum = ":6235"
	log.Printf("Running on http://localhost%s", portNum)
	log.Fatal(http.ListenAndServe(portNum, r))
}

func initDatabase() {
	var err error
	db, err = gorm.Open(sqlite.Open("guestbook.db"), &gorm.Config{})
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}

	// Migrate the schema
	err = db.AutoMigrate(&Guestbook{}, &Message{}, &AdminUser{})
	if err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}
}
