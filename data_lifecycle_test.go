package main

import (
	"errors"
	"fmt"
	"testing"

	"gorm.io/gorm"
)

func dataMessage(t *testing.T, book Guestbook, parent *uint, approved bool) Message {
	t.Helper()
	email := "private-visitor@example.test"
	message := Message{Name: "Visitor", Text: "Public message", Email: &email,
		GuestbookID: book.ID, ParentMessageID: parent, Approved: approved}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	return message
}

func requireMessageDeleted(t *testing.T, message Message, deleted bool) {
	t.Helper()
	var stored Message
	if err := db.Unscoped().First(&stored, message.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.DeletedAt.Valid != deleted {
		t.Fatalf("message %d deleted=%v, want %v", message.ID, stored.DeletedAt.Valid, deleted)
	}
	if stored.Email == nil || *stored.Email != *message.Email {
		t.Fatal("soft deletion unexpectedly removed private data")
	}
}

func TestSoftDeleteMessageSelections(t *testing.T) {
	_, book := featureFixture(t)
	_, otherBook := featureFixture(t)
	root := dataMessage(t, book, nil, false)
	reply := dataMessage(t, book, &root.ID, false)
	sibling := dataMessage(t, book, &root.ID, true)
	deep := dataMessage(t, book, &reply.ID, true)
	unrelated := dataMessage(t, book, nil, true)
	foreign := dataMessage(t, otherBook, nil, true)
	for _, selection := range [][]uint{nil, {0}, {root.ID, foreign.ID}, {root.ID, ^uint(0) >> 1}} {
		if err := softDeleteMessages(book.ID, selection); !errors.Is(err, ErrInvalidMessageSelection) {
			t.Fatalf("selection %v: got %v, want invalid selection", selection, err)
		}
		requireMessageDeleted(t, root, false)
	}
	if err := softDeleteMessages(book.ID, []uint{reply.ID}); err != nil {
		t.Fatal(err)
	}
	requireMessageDeleted(t, root, false)
	requireMessageDeleted(t, reply, true)
	requireMessageDeleted(t, deep, true)
	requireMessageDeleted(t, sibling, false)

	// A historical descendant behind an already-deleted intermediate must cascade.
	historical := dataMessage(t, book, &reply.ID, true)
	if err := softDeleteMessages(book.ID, []uint{root.ID, root.ID, sibling.ID}); err != nil {
		t.Fatal(err)
	}
	for _, message := range []Message{root, sibling, historical} {
		requireMessageDeleted(t, message, true)
	}
	requireMessageDeleted(t, unrelated, false)
	requireMessageDeleted(t, foreign, false)
}

func TestSoftDeleteTransactionsRollback(t *testing.T) {
	for _, kind := range []string{"messages", "guestbook"} {
		t.Run(kind, func(t *testing.T) {
			_, book := featureFixture(t)
			root := dataMessage(t, book, nil, true)
			reply := dataMessage(t, book, &root.ID, true)
			trigger := fmt.Sprintf("reject_data_delete_%d", book.ID)
			statement := fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE OF deleted_at ON messages
				WHEN NEW.id = %d BEGIN SELECT RAISE(ABORT, 'injected deletion failure'); END`, trigger, reply.ID)
			if kind == "guestbook" {
				statement = fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE OF deleted_at ON guestbooks
					WHEN NEW.id = %d BEGIN SELECT RAISE(ABORT, 'injected deletion failure'); END`, trigger, book.ID)
			}
			if err := db.Exec(statement).Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.Exec("DROP TRIGGER " + trigger).Error; err != nil {
					t.Error(err)
				}
			})
			var err error
			if kind == "messages" {
				err = softDeleteMessages(book.ID, []uint{root.ID})
			} else {
				err = softDeleteGuestbook(book.ID)
			}
			if err == nil {
				t.Fatal("injected storage error was swallowed")
			}
			requireMessageDeleted(t, root, false)
			requireMessageDeleted(t, reply, false)
			if err := db.First(&Guestbook{}, book.ID).Error; err != nil {
				t.Fatalf("guestbook deleted despite rollback: %v", err)
			}
		})
	}
}

func TestSoftDeleteChecksAffectedRows(t *testing.T) {
	_, book := featureFixture(t)
	root := dataMessage(t, book, nil, true)
	reply := dataMessage(t, book, &root.ID, true)
	trigger := fmt.Sprintf("ignore_data_delete_%d", book.ID)
	if err := db.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE OF deleted_at ON messages
		WHEN NEW.id = %d BEGIN SELECT RAISE(IGNORE); END`, trigger, reply.ID)).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Exec("DROP TRIGGER " + trigger).Error; err != nil {
			t.Error(err)
		}
	})
	if err := softDeleteMessages(book.ID, []uint{root.ID}); err == nil {
		t.Fatal("partial deletion reported success")
	}
	requireMessageDeleted(t, root, false)
	requireMessageDeleted(t, reply, false)
}

func TestSoftDeleteGuestbookScope(t *testing.T) {
	_, book := featureFixture(t)
	_, other := featureFixture(t)
	root := dataMessage(t, book, nil, false)
	reply := dataMessage(t, book, &root.ID, true)
	orphan := dataMessage(t, book, nil, true)
	missing := uint(987654321)
	if err := db.Model(&orphan).Update("parent_message_id", missing).Error; err != nil {
		t.Fatal(err)
	}
	unrelated := dataMessage(t, other, nil, true)
	if err := softDeleteGuestbook(book.ID); err != nil {
		t.Fatal(err)
	}
	for _, message := range []Message{root, reply, orphan} {
		requireMessageDeleted(t, message, true)
	}
	requireMessageDeleted(t, unrelated, false)
	if err := softDeleteGuestbook(book.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("repeat deletion = %v", err)
	}
	if err := softDeleteGuestbook(0); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("zero deletion = %v", err)
	}
}

func TestActiveMessagesAncestorScope(t *testing.T) {
	owner, book := featureFixture(t)
	root := dataMessage(t, book, nil, false)
	reply := dataMessage(t, book, &root.ID, false)
	deep := dataMessage(t, book, &reply.ID, true)
	_, other := featureFixture(t)
	crossBook := dataMessage(t, other, &root.ID, true)
	count := func(bookID uint, expected int64) {
		t.Helper()
		var actual int64
		if err := activeMessagesQuery(db).Where("messages.guestbook_id = ?", bookID).Count(&actual).Error; err != nil {
			t.Fatal(err)
		}
		if actual != expected {
			t.Fatalf("active count = %d, want %d", actual, expected)
		}
	}
	count(book.ID, 2)
	count(other.ID, 0)
	if err := softDeleteMessages(book.ID, []uint{deep.ID}); !errors.Is(err, ErrInvalidMessageSelection) {
		t.Fatalf("hidden nested selection = %v", err)
	}
	if err := softDeleteMessages(other.ID, []uint{crossBook.ID}); !errors.Is(err, ErrInvalidMessageSelection) {
		t.Fatalf("cross-book parent selection = %v", err)
	}
	if err := db.Delete(&root).Error; err != nil {
		t.Fatal(err)
	}
	count(book.ID, 0)
	root2 := dataMessage(t, book, nil, true)
	if err := db.Delete(&owner).Error; err != nil {
		t.Fatal(err)
	}
	count(book.ID, 0)
	if err := softDeleteMessages(book.ID, []uint{root2.ID}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deleted owner mutation = %v", err)
	}
	requireMessageDeleted(t, root2, false)
}
