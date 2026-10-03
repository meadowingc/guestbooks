package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"guestbook/constants"

	"gorm.io/gorm"
)

func TestConcurrentWriteRoutesReserveBeforeReading(t *testing.T) {
	for _, kind := range []string{"submission", "reply", "message-delete", "book-delete"} {
		t.Run(kind, func(t *testing.T) {
			isolatedDataDatabase(t)
			_, firstBook := featureFixture(t)
			secondOwner, secondBook := featureFixture(t)
			root := dataMessage(t, secondBook, nil, true)
			if err := db.Model(&firstBook).Update("pow_enabled", true).Error; err != nil {
				t.Fatal(err)
			}
			challenge, err := powChallengeStore.GenerateChallenge(firstBook.ID)
			if err != nil {
				t.Fatal(err)
			}
			var nonce string
			for i := 0; ; i++ {
				nonce = strconv.Itoa(i)
				hash := sha256.Sum256([]byte(challenge + nonce))
				if hasLeadingZeroBits(hash[:], constants.POW_DIFFICULTY) {
					break
				}
			}

			read, secondBegin, resume := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(resume) })
			var begins atomic.Int32
			var paused atomic.Bool
			queryCallback, rawCallback := "readiness:reserved-snapshot", "readiness:second-reservation"
			if err := db.Callback().Query().After("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
				_, count := tx.Statement.Dest.(*int64)
				if count && tx.Statement.Table == "guestbooks" && paused.CompareAndSwap(false, true) {
					close(read)
					<-resume
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Raw().Before("gorm:raw").Register(rawCallback, func(tx *gorm.DB) {
				if tx.Statement.SQL.String() == "BEGIN IMMEDIATE" && begins.Add(1) == 2 {
					close(secondBegin)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				db.Callback().Query().Remove(queryCallback)
				db.Callback().Raw().Remove(rawCallback)
			})
			router := initRouter()
			firstResponse, secondResponse := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
			go func() {
				firstResponse <- featureRequest(router, "POST", fmt.Sprintf("/guestbook/%d/submit", firstBook.ID),
					url.Values{"name": {"First visitor"}, "text": {"Verified submission"},
						"powChallenge": {challenge}, "powNonce": {nonce}}, nil, true)
			}()
			select {
			case <-read:
			case <-time.After(5 * time.Second):
				once.Do(func() { close(resume) })
				<-firstResponse
				t.Fatal("submission did not reach its ancestry read")
			}
			expected := 303
			path := fmt.Sprintf("/admin/guestbook/%d/message/%d/", secondBook.ID, root.ID)
			form := url.Values{"text": {"Owner reply"}}
			switch kind {
			case "submission":
				expected = 201
				path = fmt.Sprintf("/guestbook/%d/submit", secondBook.ID)
				form = url.Values{"name": {"Second visitor"}, "text": {"Independent submission"}}
			case "reply":
				path += "reply"
			case "message-delete":
				path += "delete"
			case "book-delete":
				path = fmt.Sprintf("/admin/guestbook/%d/delete", secondBook.ID)
			}
			go func() {
				secondResponse <- featureRequest(router, "POST", path, form, &secondOwner, true)
			}()
			select {
			case <-secondBegin:
			case <-time.After(5 * time.Second):
				once.Do(func() { close(resume) })
				<-firstResponse
				<-secondResponse
				t.Fatal("concurrent route did not reserve its writer before reading")
			}
			once.Do(func() { close(resume) })
			first, second := <-firstResponse, <-secondResponse
			requireStatus(t, first, 201)
			requireStatus(t, second, expected)
			if first.Header().Get("X-Guestbooks-Fresh-Proof") != "" {
				t.Fatal("successful contention handling required a fresh proof")
			}
			if powChallengeStore.VerifyPow(challenge, nonce, firstBook.ID) {
				t.Fatal("successful proof was not consumed exactly once")
			}
			var count int64
			if err := db.Model(&Message{}).Where("guestbook_id = ?", firstBook.ID).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("verified message was lost or duplicated: count=%d error=%v", count, err)
			}
		})
	}
}

func TestWriteTransactionRollbackAndConnectionReuse(t *testing.T) {
	for _, failure := range []string{"begin", "begin-result", "operation", "commit", "rollback", "cancellation", "panic"} {
		t.Run(failure, func(t *testing.T) {
			isolatedDataDatabase(t)
			_, book := featureFixture(t)
			connection, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			connection.SetMaxOpenConns(1)
			injected := errors.New("injected transaction failure")
			rollbackFailure := errors.New("injected rollback failure")
			beginFailure := failure == "begin" || failure == "begin-result"
			callback := "readiness:transaction-failure"
			if beginFailure || failure == "commit" || failure == "rollback" {
				registration := db.Callback().Raw().Before("gorm:raw")
				if failure == "begin-result" {
					registration = db.Callback().Raw().After("gorm:raw")
				}
				if err := registration.Register(callback, func(tx *gorm.DB) {
					statement := tx.Statement.SQL.String()
					if (failure == "commit" && statement == "COMMIT") || (beginFailure && statement == "BEGIN IMMEDIATE") {
						tx.AddError(injected)
					} else if failure == "rollback" && statement == "ROLLBACK" {
						tx.AddError(rollbackFailure)
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			func() {
				defer func() {
					recovered := recover()
					if failure == "panic" {
						if recovered != injected {
							t.Errorf("panic was not preserved: %v", recovered)
						}
					} else if recovered != nil {
						panic(recovered)
					}
				}()
				err = writeTransaction(db.WithContext(ctx), func(tx *gorm.DB) error {
					if beginFailure {
						t.Error("operation ran after failed BEGIN")
					}
					if err := tx.Create(&Message{Name: "Visitor", Text: "Must roll back", GuestbookID: book.ID}).Error; err != nil {
						return err
					}
					switch failure {
					case "operation", "rollback":
						return injected
					case "cancellation":
						cancel()
						return ctx.Err()
					case "panic":
						panic(injected)
					}
					return nil
				})
				if failure == "cancellation" {
					if !errors.Is(err, context.Canceled) {
						t.Errorf("cancellation was not preserved: %v", err)
					}
				} else if failure != "panic" && !errors.Is(err, injected) {
					t.Errorf("transaction failure was not preserved: %v", err)
				}
				if failure == "rollback" && !errors.Is(err, rollbackFailure) {
					t.Errorf("rollback failure was not preserved: %v", err)
				}
			}()
			if beginFailure || failure == "commit" || failure == "rollback" {
				db.Callback().Raw().Remove(callback)
			}
			if (beginFailure || failure == "rollback") && connection.Stats().OpenConnections != 0 {
				t.Fatal("uncertain transaction connection was returned to the pool")
			}
			var count int64
			if err := db.Model(&Message{}).Where("guestbook_id = ?", book.ID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("failed transaction persisted data: count=%d error=%v", count, err)
			}
			if err := writeTransaction(db, func(tx *gorm.DB) error {
				return tx.Create(&Message{Name: "Visitor", Text: "Committed after rollback", GuestbookID: book.ID}).Error
			}); err != nil {
				t.Fatal("pooled connection was left in a transaction:", err)
			}
		})
	}
}
