package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func dataAPIRequest(version, id, query string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	if version == "v1" {
		router.Get("/messages/{guestbookID}", PublicMessagesV1)
	} else {
		router.Get("/messages/{guestbookID}", PublicMessagesV2)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/messages/"+id+query, nil))
	return response
}

func decodePublicResponse(t *testing.T, response *httptest.ResponseRecorder, version string) ([]publicMessage, map[string]any) {
	t.Helper()
	requireStatus(t, response, http.StatusOK)
	var messages []publicMessage
	var pagination map[string]any
	if version == "v1" {
		if err := json.Unmarshal(response.Body.Bytes(), &messages); err != nil {
			t.Fatal(err)
		}
	} else {
		var decoded struct {
			Messages   []publicMessage
			Pagination map[string]any
		}
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		messages, pagination = decoded.Messages, decoded.Pagination
	}
	return messages, pagination
}

func TestPublicMessageWireContract(t *testing.T) {
	fixed := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	website, email := "https://example.test", "private@example.test"
	parent := uint(1)
	models := []Message{{
		Model: gorm.Model{ID: 1, CreatedAt: fixed, UpdatedAt: fixed},
		Name:  "Visitor", Text: "Root", Email: &email, Website: nil,
		Approved: true, GuestbookID: 2, Replies: []Message{{
			Model: gorm.Model{ID: 3, CreatedAt: fixed, UpdatedAt: fixed},
			Name:  "Owner", Text: "Reply", Email: &email, Website: &website,
			Approved: true, GuestbookID: 2, ParentMessageID: &parent,
		}},
	}, {Model: gorm.Model{ID: 4}, Replies: []Message{}}}
	legacy, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	models[0].Guestbook = Guestbook{AdminUserID: 99, ChallengeAnswer: "PRIVATE ANSWER", CustomPageCSS: "PRIVATE CSS"}
	actual, err := json.Marshal(publicMessages(models))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacy, actual) {
		t.Fatalf("wire contract changed:\nlegacy=%s\nactual=%s", legacy, actual)
	}
	if bytes.Contains(actual, []byte("private")) || bytes.Contains(actual, []byte("PRIVATE")) || bytes.Contains(actual, []byte("Email")) {
		t.Fatal("private data exposed")
	}
	for _, empty := range [][]Message{nil, {}} {
		want, _ := json.Marshal(empty)
		got, _ := json.Marshal(publicMessages(empty))
		if !bytes.Equal(got, want) {
			t.Fatalf("null/empty contract changed: %s != %s", got, want)
		}
	}
}

func TestPublicAPIRootsRepliesPaginationAndCache(t *testing.T) {
	_, book := featureFixture(t)
	_, other := featureFixture(t)
	root := dataMessage(t, book, nil, true)
	second := dataMessage(t, book, nil, true)
	pending := dataMessage(t, book, nil, false)
	firstReply := dataMessage(t, book, &root.ID, true)
	secondReply := dataMessage(t, book, &root.ID, true)
	dataMessage(t, book, &root.ID, false)
	dataMessage(t, book, &pending.ID, true)
	dataMessage(t, other, &root.ID, true)
	dataMessage(t, book, &firstReply.ID, true)
	deleted := dataMessage(t, book, nil, true)
	dataMessage(t, book, &deleted.ID, true)
	if err := db.Delete(&deleted).Error; err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := db.Model(&Message{}).Where("id IN ?", []uint{root.ID, second.ID, firstReply.ID, secondReply.ID}).
		Update("created_at", fixed).Error; err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatUint(uint64(book.ID), 10)
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			messageCache.InvalidateGuestbook(book.ID)
			// A page must not reuse a count from a different database snapshot.
			messageCache.SetCount(book.ID, 999)
			query := ""
			if version == "v2" {
				query = "?page=2&limit=1"
			}
			miss := dataAPIRequest(version, id, query)
			messages, pagination := decodePublicResponse(t, miss, version)
			if miss.Header().Get("X-Cache") != "MISS" {
				t.Fatal("first response was not a miss")
			}
			if version == "v1" {
				if len(messages) != 2 || messages[0].ID != second.ID || messages[1].ID != root.ID {
					t.Fatalf("roots/sort incorrect: %+v", messages)
				}
				messages = messages[1:]
			} else if len(messages) != 1 || messages[0].ID != root.ID ||
				!reflect.DeepEqual(pagination, map[string]any{
					"page": float64(2), "limit": float64(1), "total": float64(2),
					"totalPages": float64(2), "hasNext": false, "hasPrevious": true,
				}) {
				t.Fatalf("incorrect root pagination: %+v, %+v", messages, pagination)
			}
			if len(messages[0].Replies) != 2 || messages[0].Replies[0].ID != firstReply.ID ||
				messages[0].Replies[1].ID != secondReply.ID {
				t.Fatalf("replies not scoped/ordered: %+v", messages[0].Replies)
			}
			if strings.Contains(miss.Body.String(), "private-visitor") || strings.Contains(miss.Body.String(), `"Email"`) {
				t.Fatal("private email leaked")
			}
			hit := dataAPIRequest(version, "00"+id, query)
			requireStatus(t, hit, http.StatusOK)
			if hit.Header().Get("X-Cache") != "HIT" || hit.Body.String() != miss.Body.String() {
				t.Fatalf("cache changed response: %s", hit.Body.String())
			}
		})
	}
	page := dataAPIRequest("v2", id, "?page=1&limit=1")
	messages, pagination := decodePublicResponse(t, page, "v2")
	if len(messages) != 1 || messages[0].ID != second.ID || pagination["total"] != float64(2) {
		t.Fatal("replies displaced a page-one root")
	}
}

func TestPublicAPIInvalidIDsAndPageArithmetic(t *testing.T) {
	_, book := featureFixture(t)
	id := strconv.FormatUint(uint64(book.ID), 10)
	for _, version := range []string{"v1", "v2"} {
		for _, invalid := range []string{"0", "000", "-1", "1x", "18446744073709551616"} {
			requireStatus(t, dataAPIRequest(version, invalid, ""), http.StatusBadRequest)
		}
		requireStatus(t, dataAPIRequest(version, "987654321", ""), http.StatusNotFound)
		response := dataAPIRequest(version, id, "")
		messages, pagination := decodePublicResponse(t, response, version)
		if messages == nil || len(messages) != 0 {
			t.Fatalf("%s empty collection must be [], got %+v", version, messages)
		}
		if version == "v2" && (pagination["total"] != float64(0) || pagination["totalPages"] != float64(0)) {
			t.Fatal("empty root count incorrect")
		}
	}
	for _, query := range []string{"?page=184467440737095516160", "?page=" + strconv.Itoa(int(^uint(0)>>1)) + "&limit=100"} {
		requireStatus(t, dataAPIRequest("v2", id, query), http.StatusBadRequest)
	}
	for _, query := range []string{"?page=garbage&limit=101", "?page=0&limit=-1"} {
		_, pagination := decodePublicResponse(t, dataAPIRequest("v2", id, query), "v2")
		if pagination["page"] != float64(1) || pagination["limit"] != float64(20) {
			t.Fatalf("legacy defaults changed: %+v", pagination)
		}
	}
	messages, _ := decodePublicResponse(t, dataAPIRequest("v2", id, "?page="+strconv.Itoa(int(^uint(0)>>1))+"&limit=1"), "v2")
	if messages == nil || len(messages) != 0 {
		t.Fatal("representable page beyond end must return []")
	}
}

func TestPublicAPIRejectsDeletedAncestorsBeforeHits(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		for _, ancestor := range []string{"owner", "book", "parent", "missing owner", "missing book", "missing parent", "unapproved parent"} {
			t.Run(version+"/"+ancestor, func(t *testing.T) {
				owner, book := featureFixture(t)
				root := dataMessage(t, book, nil, true)
				dataMessage(t, book, &root.ID, true)
				id := strconv.FormatUint(uint64(book.ID), 10)
				requireStatus(t, dataAPIRequest(version, id, ""), http.StatusOK)
				requireStatus(t, dataAPIRequest(version, id, ""), http.StatusOK)
				var err error
				switch ancestor {
				case "owner":
					err = db.Delete(&owner).Error
				case "book":
					err = db.Delete(&book).Error
				case "parent":
					err = db.Delete(&root).Error
				case "missing owner":
					err = db.Unscoped().Delete(&owner).Error
				case "missing book":
					err = db.Unscoped().Delete(&book).Error
				case "missing parent":
					err = db.Unscoped().Delete(&root).Error
				case "unapproved parent":
					err = db.Model(&root).Update("approved", false).Error
				}
				if err != nil {
					t.Fatal(err)
				}
				response := dataAPIRequest(version, id, "")
				if strings.Contains(ancestor, "parent") {
					messages, pagination := decodePublicResponse(t, response, version)
					if len(messages) != 0 || response.Header().Get("X-Cache") != "MISS" {
						t.Fatalf("deleted/unapproved parent exposed via cache: %s", response.Body.String())
					}
					if version == "v2" && pagination["total"] != float64(0) {
						t.Fatal("hidden parent still counted")
					}
				} else {
					requireStatus(t, response, http.StatusNotFound)
				}
			})
		}
	}
}

func TestPublicAPICachedReplyVisibility(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		for _, mutation := range []string{"unapprove", "soft delete", "hard delete", "pending parent", "deleted parent", "missing parent", "cross book"} {
			t.Run(version+"/"+mutation, func(t *testing.T) {
				_, book := featureFixture(t)
				_, other := featureFixture(t)
				root := dataMessage(t, book, nil, true)
				reply := dataMessage(t, book, &root.ID, true)
				sibling := dataMessage(t, book, &root.ID, true)
				pending := dataMessage(t, book, nil, false)
				deleted := dataMessage(t, book, nil, true)
				if err := db.Delete(&deleted).Error; err != nil {
					t.Fatal(err)
				}
				id := fmt.Sprint(book.ID)
				messages, _ := decodePublicResponse(t, dataAPIRequest(version, id, ""), version)
				if len(messages) != 1 || len(messages[0].Replies) != 2 {
					t.Fatal("initial root/reply fixture is incorrect")
				}
				hit := dataAPIRequest(version, id, "")
				requireStatus(t, hit, http.StatusOK)
				if hit.Header().Get("X-Cache") != "HIT" {
					t.Fatal("initial response was not cached")
				}

				var err error
				target := db.Model(&Message{}).Where("id = ?", reply.ID)
				switch mutation {
				case "unapprove":
					err = target.Update("approved", false).Error
				case "soft delete":
					err = db.Delete(&reply).Error
				case "hard delete":
					err = db.Unscoped().Delete(&reply).Error
				case "pending parent":
					err = target.Update("parent_message_id", pending.ID).Error
				case "deleted parent":
					err = target.Update("parent_message_id", deleted.ID).Error
				case "missing parent":
					err = target.Update("parent_message_id", 987654321).Error
				case "cross book":
					err = target.Update("guestbook_id", other.ID).Error
				}
				if err != nil {
					t.Fatal(err)
				}
				// Deliberately omit invalidation to exercise the pre-hit visibility guard.
				response := dataAPIRequest(version, id, "")
				messages, pagination := decodePublicResponse(t, response, version)
				if response.Header().Get("X-Cache") != "MISS" || len(messages) != 1 ||
					messages[0].ID != root.ID || len(messages[0].Replies) != 1 ||
					messages[0].Replies[0].ID != sibling.ID {
					t.Fatalf("ineligible reply exposed: %s", response.Body.String())
				}
				if version == "v2" && pagination["total"] != float64(1) {
					t.Fatal("reply visibility changed the root count")
				}
				refilled := dataAPIRequest(version, id, "")
				if refilled.Header().Get("X-Cache") != "HIT" || refilled.Body.String() != response.Body.String() {
					t.Fatal("corrected reply scope was not cached")
				}
			})
		}
	}
}

func TestPublicAPIStorageFailureIsNotEmptySuccess(t *testing.T) {
	_, book := featureFixture(t)
	dataMessage(t, book, nil, true)
	id := fmt.Sprint(book.ID)
	for _, version := range []string{"v1", "v2"} {
		requireStatus(t, dataAPIRequest(version, id, ""), http.StatusOK)
	}
	callback := "test:public_storage_failure"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "messages" {
			tx.AddError(errors.New("injected public query failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Query().Remove(callback); err != nil {
			t.Error(err)
		}
	})
	for _, cached := range []bool{true, false} {
		if !cached {
			messageCache.InvalidateGuestbook(book.ID)
		}
		for _, version := range []string{"v1", "v2"} {
			requireStatus(t, dataAPIRequest(version, id, ""), http.StatusInternalServerError)
		}
	}
}

func TestPublicAPICachedRootCannotBecomeTopLevelReply(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			_, book := featureFixture(t)
			first := dataMessage(t, book, nil, true)
			second := dataMessage(t, book, nil, true)
			id := fmt.Sprint(book.ID)
			requireStatus(t, dataAPIRequest(version, id, ""), http.StatusOK)
			if err := db.Model(&first).Update("parent_message_id", second.ID).Error; err != nil {
				t.Fatal(err)
			}
			response := dataAPIRequest(version, id, "")
			messages, pagination := decodePublicResponse(t, response, version)
			if response.Header().Get("X-Cache") != "MISS" || len(messages) != 1 ||
				messages[0].ID != second.ID || len(messages[0].Replies) != 1 || messages[0].Replies[0].ID != first.ID {
				t.Fatalf("obsolete parent scope returned: %s", response.Body.String())
			}
			if version == "v2" && pagination["total"] != float64(1) {
				t.Fatal("reply counted as a root")
			}
		})
	}
}

func isolatedDataDatabase(t *testing.T) {
	t.Helper()
	previousDB, previousCache := db, messageCache
	isolated, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "snapshot.sqlite")+"?_journal_mode=WAL&_busy_timeout=5000"), &gorm.Config{Logger: databaseLogger})
	if err != nil {
		t.Fatal(err)
	}
	if err := isolated.AutoMigrate(&Guestbook{}, &Message{}, &AdminUser{}); err != nil {
		t.Fatal(err)
	}
	connection, err := isolated.DB()
	if err != nil {
		t.Fatal(err)
	}
	db = isolated
	messageCache, err = NewMessageCache(10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db, messageCache = previousDB, previousCache
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
}

func TestPublicAPIDeterministicStaleFill(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			isolatedDataDatabase(t)
			_, book := featureFixture(t)
			root := dataMessage(t, book, nil, true)
			captured, resume := make(chan struct{}), make(chan struct{})
			var blocked atomic.Bool
			callback := "test:data_stale_fill"
			if err := db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table != "messages" {
					return
				}
				_, countQuery := tx.Statement.Dest.(*int64)
				isBarrier := (version == "v1" && len(tx.Statement.Preloads) > 0) ||
					(version == "v2" && countQuery)
				if isBarrier && blocked.CompareAndSwap(false, true) {
					close(captured)
					<-resume
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.Callback().Query().Remove(callback); err != nil {
					t.Error(err)
				}
			})
			responses := make(chan *httptest.ResponseRecorder, 1)
			go func() { responses <- dataAPIRequest(version, fmt.Sprint(book.ID), "") }()
			select {
			case <-captured:
			case <-time.After(5 * time.Second):
				close(resume)
				<-responses
				t.Fatal("stale-fill barrier was not reached")
			}
			mutationErr := db.Model(&root).Update("text", "fresh committed text").Error
			if mutationErr == nil {
				newRoot := Message{Name: "New root", Text: "new", Approved: true, GuestbookID: book.ID}
				mutationErr = db.Create(&newRoot).Error
			}
			messageCache.InvalidateGuestbook(book.ID)
			close(resume)
			response := <-responses
			if mutationErr != nil {
				t.Fatal(mutationErr)
			}
			messages, pagination := decodePublicResponse(t, response, version)
			if len(messages) != 2 || messages[1].ID != root.ID || messages[1].Text != "fresh committed text" {
				t.Fatalf("superseded fill returned: %s", response.Body.String())
			}
			if version == "v2" {
				if pagination["total"] != float64(2) {
					t.Fatalf("mixed count/message snapshots: %s", response.Body.String())
				}
				if count, ok := messageCache.GetCount(book.ID); !ok || count != 2 {
					t.Fatalf("stale count published: count=%d, hit=%v", count, ok)
				}
			}
			hit := dataAPIRequest(version, fmt.Sprint(book.ID), "")
			if hit.Header().Get("X-Cache") != "HIT" || response.Body.String() != hit.Body.String() {
				t.Fatal("stale data was cached after invalidation")
			}
		})
	}
}
