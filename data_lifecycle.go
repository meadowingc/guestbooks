package main

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

var ErrInvalidMessageSelection = errors.New("invalid message selection")

func activeGuestbooksQuery(tx *gorm.DB) *gorm.DB {
	return tx.Model(&Guestbook{}).Where(`EXISTS (
		SELECT 1 FROM admin_users AS owner
		WHERE owner.id = guestbooks.admin_user_id AND owner.deleted_at IS NULL
	)`)
}

// Pending messages remain visible to moderation; only ancestor visibility is scoped.
func activeMessagesQuery(tx *gorm.DB) *gorm.DB {
	return tx.Model(&Message{}).
		Where(`EXISTS (
			SELECT 1 FROM guestbooks AS book
			JOIN admin_users AS owner ON owner.id = book.admin_user_id AND owner.deleted_at IS NULL
			WHERE book.id = messages.guestbook_id AND book.deleted_at IS NULL
		)`).
		Where(`(messages.parent_message_id IS NULL OR EXISTS (
			SELECT 1 FROM messages AS root
			WHERE root.id = messages.parent_message_id
			AND root.guestbook_id = messages.guestbook_id
			AND root.parent_message_id IS NULL AND root.deleted_at IS NULL
		))`)
}

func requireActiveGuestbook(tx *gorm.DB, guestbookID uint) error {
	if guestbookID == 0 {
		return gorm.ErrRecordNotFound
	}
	var book Guestbook
	return activeGuestbooksQuery(tx).Select("guestbooks.id").
		Where("guestbooks.id = ?", guestbookID).First(&book).Error
}

func softDeleteMessages(guestbookID uint, messageIDs []uint) error {
	if len(messageIDs) == 0 {
		return ErrInvalidMessageSelection
	}
	selected := make(map[uint]bool, len(messageIDs))
	ids := make([]uint, 0, len(messageIDs))
	for _, id := range messageIDs {
		if id == 0 {
			return ErrInvalidMessageSelection
		}
		if !selected[id] {
			selected[id] = true
			ids = append(ids, id)
		}
	}
	return writeTransaction(db, func(tx *gorm.DB) error {
		if err := requireActiveGuestbook(tx, guestbookID); err != nil {
			return err
		}
		for start := 0; start < len(ids); start += 500 {
			batch := ids[start:min(start+500, len(ids))]
			var count int64
			if err := activeMessagesQuery(tx).Where("messages.guestbook_id = ? AND messages.id IN ?", guestbookID, batch).
				Count(&count).Error; err != nil {
				return err
			}
			if count != int64(len(batch)) {
				return ErrInvalidMessageSelection
			}
		}

		// Include deleted intermediate ancestors so even historical deep replies cascade.
		var messages []Message
		if err := tx.Unscoped().Select("id", "parent_message_id", "deleted_at").
			Where("guestbook_id = ?", guestbookID).Find(&messages).Error; err != nil {
			return err
		}
		children := make(map[uint][]uint)
		active := make(map[uint]bool, len(messages))
		for _, message := range messages {
			active[message.ID] = !message.DeletedAt.Valid
			if message.ParentMessageID != nil {
				children[*message.ParentMessageID] = append(children[*message.ParentMessageID], message.ID)
			}
		}
		for i := 0; i < len(ids); i++ {
			for _, child := range children[ids[i]] {
				if !selected[child] {
					selected[child] = true
					ids = append(ids, child)
				}
			}
		}
		deletions := make([]uint, 0, len(ids))
		for _, id := range ids {
			if active[id] {
				deletions = append(deletions, id)
			}
		}
		for start := 0; start < len(deletions); start += 500 {
			batch := deletions[start:min(start+500, len(deletions))]
			result := tx.Where("guestbook_id = ? AND id IN ?", guestbookID, batch).Delete(&Message{})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != int64(len(batch)) {
				return fmt.Errorf("message deletion affected %d rows, expected %d", result.RowsAffected, len(batch))
			}
		}
		return nil
	})
}

func softDeleteGuestbook(guestbookID uint) error {
	return writeTransaction(db, func(tx *gorm.DB) error {
		if err := requireActiveGuestbook(tx, guestbookID); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&Message{}).Where("guestbook_id = ?", guestbookID).Count(&count).Error; err != nil {
			return err
		}
		result := tx.Where("guestbook_id = ?", guestbookID).Delete(&Message{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != count {
			return fmt.Errorf("guestbook message deletion affected %d rows, expected %d", result.RowsAffected, count)
		}
		result = tx.Where("id = ?", guestbookID).Delete(&Guestbook{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}
