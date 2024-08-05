package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"html/template"

	"guestbook/constants"

	"github.com/go-chi/chi/v5"
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
}

func GuestbookPage(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")
	var websiteURL string
	result := db.Model(&Guestbook{}).Select("website_url").Where("id = ?", guestbookID).Scan(&websiteURL)
	if result.Error != nil {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}

	if constants.DEBUG_MODE {
		guestbookTemplate = loadGuestbookTemplate()
	}

	data := struct {
		ID         string
		WebsiteURL string
	}{
		ID:         guestbookID,
		WebsiteURL: websiteURL,
	}

	err := guestbookTemplate.Execute(w, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func GuestbookSubmit(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")

	var guestbook Guestbook
	result := db.First(&guestbook, guestbookID)
	if result.Error != nil {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}

	// check that the form has the expected challenge if necesary
	if strings.TrimSpace(guestbook.ChallengeQuestion) != "" {
		challengeQuestionAnswer := strings.TrimSpace(r.FormValue("challengeQuestionAnswer"))
		challengeQuestionAnswer = strings.ToLower(challengeQuestionAnswer)

		expectedChallengeAnswer := strings.TrimSpace(guestbook.ChallengeAnswer)
		expectedChallengeAnswer = strings.ToLower(expectedChallengeAnswer)

		if expectedChallengeAnswer != "" && expectedChallengeAnswer != challengeQuestionAnswer {
			http.Error(w, "The provided answer to the challenge question is invalid!", http.StatusUnauthorized)
			return
		}
	}

	name := strings.TrimSpace(r.FormValue("name"))
	text := strings.TrimSpace(r.FormValue("text"))
	website := strings.TrimSpace(r.FormValue("website"))
	var websitePtr *string
	if website != "" {
		websitePtr = &website
	}

	message := Message{
		Name:        name,
		Text:        text,
		Website:     websitePtr,
		GuestbookID: guestbook.ID,
		Approved:    !guestbook.RequiresApproval,
	}
	result = db.Create(&message)
	if result.Error != nil {
		http.Error(w, "Error submitting message", http.StatusInternalServerError)
		return
	}

	// now send an email to the user if necessary
	var adminUser AdminUser
	result = db.First(&adminUser, "id = ?", guestbook.AdminUserID)
	if result.Error != nil {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}

	if adminUser.EmailNotifications && adminUser.EmailVerified && adminUser.Email != "" {
		submitterText := ""
		if website != "" {
			submitterText = "[Website: " + website + "]"
		}

		data := struct {
			ApplicationURL       string
			GuestbookID          string
			GuestbookURL         string
			MessageID            uint
			MessageName          string
			MessageNeedsApproval bool
			MessageText          string
			SubmitterText        string
		}{
			ApplicationURL:       constants.PUBLIC_URL,
			GuestbookID:          guestbookID,
			GuestbookURL:         guestbook.WebsiteURL,
			MessageID:            message.ID,
			MessageName:          message.Name,
			MessageNeedsApproval: guestbook.RequiresApproval && !message.Approved,
			MessageText:          message.Text,
			SubmitterText:        submitterText,
		}

		// Define your template string
		tmpl := `
Hi! Someone has just submitted a new message on your guestbook '{{.GuestbookURL}}'.

From: {{.MessageName}} {{.SubmitterText}}
===BEGIN MESSAGE===
{{.MessageText}}
===END MESSAGE===

You can view the messages on your guestbook here {{.ApplicationURL}}/admin/guestbook/{{.GuestbookID}}

{{if .MessageNeedsApproval}}
This message needs approval before it is shown on your guestbook.

Please go here to approve or reject the message: {{.ApplicationURL}}/admin/guestbook/{{.GuestbookID}}/message/{{.MessageID}}/edit
{{end}}

This is an autogenerated message from {{.ApplicationURL}} . Please don't answer since this mailbox is not monitored. 
If you do need some help then please reach out through here https://meadow.bearblog.dev/mailbox/
		`

		// Parse and execute the template
		t, err := template.New("email").Parse(tmpl)
		if err != nil {
			fmt.Println("Error parsing template:", err)
			return
		}

		var tpl bytes.Buffer
		if err := t.Execute(&tpl, data); err != nil {
			fmt.Println("Error executing template:", err)
			return
		}

		if constants.DEBUG_MODE {
			fmt.Println("In debug mode, not sending email:")
			fmt.Println(tpl.String())
		} else {
			go SendMail([]string{adminUser.Email}, "[Guestbooks] New message on guestbook '"+guestbook.WebsiteURL+"'", tpl.String())
		}
	}

	http.Redirect(w, r, "/guestbook/"+guestbookID, http.StatusSeeOther)
}
