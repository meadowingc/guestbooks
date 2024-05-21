package main

import (
	"log"
	"net/http"

	"gorm.io/gorm"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"

	"gorm.io/driver/sqlite"
)

var db *gorm.DB

func main() {
	initDatabase()
	r := chi.NewRouter()
	r.Use(middleware.Logger)

	r.With(AdminAuthMiddleware).Route("/admin", func(r chi.Router) {
		r.Get("/", AdminGuestbookList)

		r.Get("/signin", AdminSignUp)
		r.Post("/signin", AdminSignIn)

		r.Get("/signup", AdminSignUp)
		r.Post("/signup", AdminSignUp)

		r.Route("/{websiteURL}", func(r chi.Router) {
			r.Get("/", AdminManageGuestbook)
			r.Post("/edit/{messageID}", AdminEditMessage)
			r.Post("/delete/{messageID}", AdminDeleteMessage)
		})
	})

	r.Route("/guestbook", func(r chi.Router) {
		r.Get("/{websiteURL}", GuestbookForm)
		r.Post("/{websiteURL}", GuestbookSubmit)
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
