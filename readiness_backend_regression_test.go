package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"
)

func TestAdminHiddenDescendantsCannotBeEdited(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-parent-%v", missing), func(t *testing.T) {
			user, book := featureFixture(t)
			root := dataMessage(t, book, nil, true)
			reply := dataMessage(t, book, &root.ID, false)
			query := db
			if missing {
				query = query.Unscoped()
			}
			if err := query.Delete(&root).Error; err != nil {
				t.Fatal(err)
			}
			router := initRouter()
			path := fmt.Sprintf("/admin/guestbook/%d/message/%d/edit", book.ID, reply.ID)
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				response := featureRequest(router, method, path,
					url.Values{"name": {"Owner"}, "text": {"Changed hidden descendant"}, "isApproved": {"on"}}, &user, false)
				requireStatus(t, response, http.StatusNotFound)
				if strings.Contains(response.Body.String(), *reply.Email) {
					t.Fatal("hidden reply email rendered")
				}
			}
			var saved Message
			if err := db.First(&saved, reply.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.Text != reply.Text || saved.Approved || saved.DeletedAt.Valid {
				t.Fatal("hidden descendant was modified or automatically repaired")
			}
		})
	}
}

func TestAdminReplyPreloadsRespectAncestry(t *testing.T) {
	for _, deleted := range []string{"none", "book", "owner"} {
		t.Run(deleted, func(t *testing.T) {
			user, book := featureFixture(t)
			otherUser, otherBook := featureFixture(t)
			root := dataMessage(t, book, nil, false)
			local := dataMessage(t, book, &root.ID, false)
			if err := db.Model(&local).Update("text", "LOCAL-PENDING-REPLY").Error; err != nil {
				t.Fatal(err)
			}
			foreign := Message{Name: "Foreign owner", Text: "FOREIGN-PENDING-CONTENT", Approved: false,
				GuestbookID: otherBook.ID, ParentMessageID: &root.ID}
			if err := db.Create(&foreign).Error; err != nil {
				t.Fatal(err)
			}
			var err error
			switch deleted {
			case "book":
				err = db.Delete(&otherBook).Error
			case "owner":
				err = db.Delete(&otherUser).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			response := featureRequest(initRouter(), "GET", fmt.Sprintf("/admin/guestbook/%d", book.ID), nil, &user, false)
			requireStatus(t, response, http.StatusOK)
			if strings.Contains(response.Body.String(), foreign.Text) {
				t.Fatal("foreign reply rendered under another owner's root")
			}
			if !strings.Contains(response.Body.String(), "LOCAL-PENDING-REPLY") {
				t.Fatal("legitimate pending reply hidden from moderation")
			}
		})
	}
}

func TestAdminEditRechecksAncestryAtWrite(t *testing.T) {
	user, book := featureFixture(t)
	root := dataMessage(t, book, nil, true)
	reply := dataMessage(t, book, &root.ID, true)
	callback := "readiness:edit-ancestor-deleted"
	var once sync.Once
	if err := db.Callback().Update().Before("gorm:begin_transaction").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "messages" {
			once.Do(func() {
				if err := db.Delete(&root).Error; err != nil {
					t.Error(err)
				}
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Update().Remove(callback) })
	response := featureRequest(initRouter(), "POST",
		fmt.Sprintf("/admin/guestbook/%d/message/%d/edit", book.ID, reply.ID),
		url.Values{"name": {"Owner"}, "text": {"Must not overwrite hidden content"}}, &user, false)
	requireStatus(t, response, http.StatusConflict)
	var saved Message
	if err := db.First(&saved, reply.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Text != reply.Text {
		t.Fatal("write predicate did not recheck active ancestry")
	}
}

func TestOrphanedGuestbookResourcesAreHidden(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-owner-%v", missing), func(t *testing.T) {
			user, book := featureFixture(t)
			if err := db.Model(&book).Updates(map[string]any{
				"pow_enabled": true, "challenge_question": "retained-private-configuration", "challenge_answer": "answer",
			}).Error; err != nil {
				t.Fatal(err)
			}
			router := initRouter()
			paths := []string{
				fmt.Sprintf("/guestbook/%d", book.ID),
				fmt.Sprintf("/resources/js/embed_script/%d/script.js", book.ID),
				fmt.Sprintf("/resources/css/guestbook/%d.css", book.ID),
				fmt.Sprintf("/api/pow-challenge/%d", book.ID),
			}
			for _, path := range paths {
				requireStatus(t, featureRequest(router, "GET", path, nil, nil, false), 200)
			}
			query := db
			if missing {
				query = query.Unscoped()
			}
			if err := query.Delete(&user).Error; err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				response := featureRequest(router, "GET", path, nil, nil, false)
				requireStatus(t, response, http.StatusNotFound)
				if strings.Contains(response.Body.String(), "retained-private-configuration") {
					t.Fatal("orphaned configuration was rendered")
				}
			}
			requireStatus(t, featureRequest(router, "POST", fmt.Sprintf("/guestbook/%d/submit", book.ID),
				url.Values{"name": {"Visitor"}, "text": {"Hidden"}}, nil, true), http.StatusNotFound)
			if err := db.First(&Guestbook{}, book.ID).Error; err != nil {
				t.Fatal("resource visibility must not modify historical records:", err)
			}
		})
	}
}

func TestSubmissionBodyCapRegardlessOfParser(t *testing.T) {
	for _, contentType := range []string{"text/plain", "application/json", "", "application/x-www-form-urlencoded", "multipart/form-data; boundary=fixture"} {
		for _, unknownLength := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/chunked=%v", contentType, unknownLength), func(t *testing.T) {
				_, book := featureFixture(t)
				target := fmt.Sprintf("/guestbook/%d/submit?name=Visitor&text=Query+submission", book.ID)
				request := httptest.NewRequest("POST", target, strings.NewReader(strings.Repeat("x", maxSubmissionBytes+1)))
				request.Header.Set("Content-Type", contentType)
				request.Header.Set("Accept", "application/json")
				if unknownLength {
					request.ContentLength = -1
				}
				response := httptest.NewRecorder()
				initRouter().ServeHTTP(response, request)
				requireStatus(t, response, http.StatusRequestEntityTooLarge)
				var count int64
				if err := db.Model(&Message{}).Where("guestbook_id = ?", book.ID).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("oversized body was stored: count=%d error=%v", count, err)
				}
			})
		}
	}
	_, book := featureFixture(t)
	request := httptest.NewRequest("POST", fmt.Sprintf("/guestbook/%d/submit?name=Visitor&text=Boundary", book.ID),
		strings.NewReader(strings.Repeat("x", maxSubmissionBytes)))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()
	initRouter().ServeHTTP(response, request)
	requireStatus(t, response, http.StatusCreated)
}

type failingSubmissionBody struct{}

func (failingSubmissionBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failingSubmissionBody) Close() error             { return nil }

func TestSubmissionBodyReadFailure(t *testing.T) {
	_, book := featureFixture(t)
	request := httptest.NewRequest("POST", fmt.Sprintf("/guestbook/%d/submit?name=Visitor&text=Incomplete", book.ID), nil)
	request.Body = failingSubmissionBody{}
	response := httptest.NewRecorder()
	initRouter().ServeHTTP(response, request)
	requireStatus(t, response, http.StatusBadRequest)
}

func TestMessageNewlinesHaveOneCanonicalLength(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		t.Run(fmt.Sprintf("%q", newline), func(t *testing.T) {
			user, book := featureFixture(t)
			text := strings.Repeat("a", 2498) + newline + "b"
			want := strings.Repeat("a", 2498) + "\nb"
			router := initRouter()
			response := featureRequest(router, "POST", fmt.Sprintf("/guestbook/%d/submit", book.ID),
				url.Values{"name": {"Visitor"}, "text": {text}}, nil, true)
			requireStatus(t, response, http.StatusCreated)
			var root Message
			if err := db.Where("guestbook_id = ?", book.ID).First(&root).Error; err != nil || root.Text != want {
				t.Fatalf("visitor newline normalization failed: %v", err)
			}
			requireStatus(t, featureRequest(router, "POST", fmt.Sprintf("/admin/guestbook/%d/message/%d/reply", book.ID, root.ID),
				url.Values{"text": {text}}, &user, false), http.StatusSeeOther)
			var reply Message
			if err := db.Where("parent_message_id = ?", root.ID).First(&reply).Error; err != nil || reply.Text != want {
				t.Fatalf("reply newline normalization failed: %v", err)
			}
			editPath := fmt.Sprintf("/admin/guestbook/%d/message/%d/edit", book.ID, reply.ID)
			requireStatus(t, featureRequest(router, "POST", editPath,
				url.Values{"name": {"Owner"}, "text": {text}}, &user, false), http.StatusSeeOther)
			if err := db.First(&reply, reply.ID).Error; err != nil || reply.Text != want {
				t.Fatalf("edit newline normalization failed: %v", err)
			}
			requireStatus(t, featureRequest(router, "POST", editPath,
				url.Values{"name": {"Owner"}, "text": {text + "c"}}, &user, false), http.StatusBadRequest)
		})
	}
	if got := normalizedMessageText(" \r\nfirst\rsecond\n "); got != "first\nsecond" {
		t.Fatalf("unexpected normalization: %q", got)
	}
	if err := messageInputError("Visitor", normalizedMessageText(string([]byte{0xff})), ""); err == nil {
		t.Fatal("invalid UTF-8 must still fail validation")
	}
}

func TestAdminBuiltInMarkersSaveWithoutHydration(t *testing.T) {
	const marker = "<<built__in>>gray-bear.css<</built__in>>"
	for _, action := range []string{"create", "update"} {
		for _, test := range []struct {
			name, css string
			valid     bool
		}{
			{"allowlisted", marker, true},
			{"surrounding-whitespace", " \n" + marker + "\t ", true},
			{"unknown", "<<built__in>>unknown.css<</built__in>>", false},
			{"traversal", "<<built__in>>../gray-bear.css<</built__in>>", false},
			{"appended-css", marker + "body{color:red}", false},
		} {
			t.Run(action+"/"+test.name, func(t *testing.T) {
				user, book := featureFixture(t)
				path := "/admin/guestbook/new"
				if action == "update" {
					path = fmt.Sprintf("/admin/guestbook/%d/edit", book.ID)
				}
				response := featureRequest(initRouter(), "POST", path, url.Values{
					"websiteURL": {"https://marker-save.example.test"}, "customPageCSS": {test.css},
				}, &user, false)
				expected := http.StatusBadRequest
				if test.valid {
					expected = http.StatusSeeOther
				}
				requireStatus(t, response, expected)
				var books []Guestbook
				if err := db.Where("admin_user_id = ?", user.ID).Order("id").Find(&books).Error; err != nil {
					t.Fatal(err)
				}
				expectedBooks := 1
				if action == "create" && test.valid {
					expectedBooks = 2
				}
				if len(books) != expectedBooks {
					t.Fatalf("saved %d books, expected %d", len(books), expectedBooks)
				}
				saved := books[len(books)-1]
				if test.valid {
					if saved.CustomPageCSS != marker || saved.WebsiteURL != "https://marker-save.example.test" {
						t.Fatal("unhydrated marker or unrelated settings were not saved")
					}
				} else if saved.CustomPageCSS != book.CustomPageCSS || saved.WebsiteURL != book.WebsiteURL {
					t.Fatal("invalid marker partially changed settings")
				}
			})
		}
	}
}
