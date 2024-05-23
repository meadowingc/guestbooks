package main

import (
	"log"
	"net/http"

	"html/template"
	"path/filepath"

	"codeberg.org/meadowingc/guestbook/constants"
	"github.com/go-chi/chi/v5"
)

var guestbookTemplate *template.Template = loadGuestbookTemplate()

func loadGuestbookTemplate() *template.Template {
	templates, err := template.ParseFiles(filepath.Join("templates", "guestbook_page.html"))
	if err != nil {
		log.Fatalf("Error parsing guestbook page template: %v", err)
	}
	return templates
}

func GuestbookPage(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")
	var guestbook Guestbook
	result := db.Preload("Messages").First(&guestbook, "id = ?", guestbookID)
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
