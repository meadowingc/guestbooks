package main

import (
	"log"
	"net/http"
	"time"

	"html/template"

	"codeberg.org/meadowingc/guestbook/constants"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

var guestbookTemplate *template.Template = loadGuestbookTemplate()

func formatDate(t time.Time) string {
	return t.Format("Jan 2, 2006")
}

func loadGuestbookTemplate() *template.Template {
	tmpl, err := template.New("guestbook_page.html").Funcs(template.FuncMap{
		"formatDate": formatDate,
	}).ParseFiles("templates/guestbook_page.html")

	if err != nil {
		log.Fatal(err)
	}

	return tmpl

	// templates, err := template.ParseFiles(filepath.Join("templates", "guestbook_page.html"))
	// if err != nil {
	// 	log.Fatalf("Error parsing guestbook page template: %v", err)
	// }
	// return templates

}

func GuestbookPage(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")
	var guestbook Guestbook
	result := db.Preload("Messages", func(db *gorm.DB) *gorm.DB {
		return db.Order("created_at desc")
	}).First(&guestbook, "id = ?", guestbookID)
	if result.Error != nil {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}

	if constants.DEBUG_MODE {
		guestbookTemplate = loadGuestbookTemplate()
	}

	err := guestbookTemplate.Execute(w, guestbook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func GuestbookSubmit(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")
	var guestbook Guestbook
	result := db.First(&guestbook, "id = ?", guestbookID)
	if result.Error != nil {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}

	name := r.FormValue("name")
	text := r.FormValue("text")
	website := r.FormValue("website")
	var websitePtr *string
	if website != "" {
		websitePtr = &website
	}
	message := Message{Name: name, Text: text, Website: websitePtr, GuestbookID: guestbook.ID}
	result = db.Create(&message)
	if result.Error != nil {
		http.Error(w, "Error submitting message", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/guestbook/"+guestbookID, http.StatusSeeOther)
}
