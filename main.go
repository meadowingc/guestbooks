package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
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
	// Setup a channel to listen for termination signals
	signals := make(chan os.Signal, 1)
	// Notify signals channel on SIGINT and SIGTERM
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	r := initRouter()

	const portNum = ":6235"
	go func() {
		log.Printf("Running on http://localhost%s", portNum)
		if err := http.ListenAndServe(portNum, r); err != nil {
			log.Printf("HTTP server stopped: %v", err)
		}
	}()

	// Block until a signal is received
	<-signals
	log.Println("Shutting down gracefully...")

	// Close the database connection
	sqlDB, err := db.DB()
	if err != nil {
		log.Printf("Error on closing database connection: %v", err)
	} else {
		if err := sqlDB.Close(); err != nil {
			log.Printf("Error on closing database connection: %v", err)
		}
	}
}

func initDatabase() {
	var err error
	db, err = gorm.Open(sqlite.Open("file:guestbook.db?cache=shared&mode=rwc&_journal_mode=WAL"), &gorm.Config{})
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}

	// Migrate the schema
	err = db.AutoMigrate(&Guestbook{}, &Message{}, &AdminUser{})
	if err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}
}

func initRouter() *chi.Mux {

	r := chi.NewRouter()

	CORSMiddleware := cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	})

	r.Use(CORSMiddleware.Handler)
	r.Use(RealIPMiddleware)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

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

	fileServer := http.FileServer(http.Dir("./assets"))
	r.Handle("/assets/*", http.StripPrefix("/assets", fileServer))

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
				guestbookIDUint, err := strconv.ParseUint(guestbookID, 10, 32)
				if err != nil {
					log.Fatal(err)
				}

				var messages []Message
				result := db.Where(&Message{GuestbookID: uint(guestbookIDUint), Approved: true}).Find(&messages)
				if result.Error != nil {
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(messages)
			})
		})
	})

	return r
}

// RealIPMiddleware extracts the client's real IP address from the
// X-Forwarded-For header and sets it on the request's RemoteAddr field. Useful
// for when the app is running behind a reverse proxy
func RealIPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// This assumes the first IP in the X-Forwarded-For list is the client's real IP
			// This may need to be adjusted depending on your reverse proxy setup
			i := strings.Index(xff, ", ")
			if i == -1 {
				i = len(xff)
			}
			r.RemoteAddr = xff[:i]
		}
		next.ServeHTTP(w, r)
	})
}
