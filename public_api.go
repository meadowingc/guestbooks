package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// Keep the legacy model-shaped wire format without serializing database associations.
type publicMessage struct {
	ID              uint
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
	Name            string
	Text            string
	Website         *string
	Approved        bool
	GuestbookID     uint
	Guestbook       publicGuestbook
	ParentMessageID *uint
	Replies         []publicMessage
}

type publicGuestbook struct {
	ID                     uint
	CreatedAt              time.Time
	UpdatedAt              time.Time
	DeletedAt              *time.Time
	WebsiteURL             string
	AdminUserID            uint
	RequiresApproval       bool
	PowEnabled             bool
	ChallengeQuestion      string
	ChallengeAnswer        string
	ChallengeHint          string
	ChallengeFailedMessage string
	CustomPageCSS          string
	Messages               []publicMessage
}

func publicMessages(messages []Message) []publicMessage {
	if messages == nil {
		return nil
	}
	result := make([]publicMessage, len(messages))
	for i, message := range messages {
		result[i] = publicMessage{
			ID: message.ID, CreatedAt: message.CreatedAt, UpdatedAt: message.UpdatedAt,
			Name: message.Name, Text: message.Text, Website: message.Website,
			Approved: message.Approved, GuestbookID: message.GuestbookID,
			ParentMessageID: message.ParentMessageID, Replies: publicMessages(message.Replies),
		}
		if message.DeletedAt.Valid {
			deletedAt := message.DeletedAt.Time
			result[i].DeletedAt = &deletedAt
		}
	}
	return result
}

func approvedMessagesQuery(tx *gorm.DB, guestbookID uint) *gorm.DB {
	return activeMessagesQuery(tx).
		Where("messages.guestbook_id = ? AND messages.approved = ?", guestbookID, true).
		Where(`(messages.parent_message_id IS NULL OR EXISTS (
			SELECT 1 FROM messages AS root
			WHERE root.id = messages.parent_message_id AND root.approved = ?
		))`, true)
}

func publicRootsQuery(tx *gorm.DB, guestbookID uint) *gorm.DB {
	return approvedMessagesQuery(tx, guestbookID).Where("messages.parent_message_id IS NULL")
}

func readPublicMessages(tx *gorm.DB, guestbookID uint, page, limit int) ([]Message, error) {
	query := publicRootsQuery(tx, guestbookID).
		Select("messages.id", "messages.created_at", "messages.updated_at", "messages.deleted_at",
			"messages.name", "messages.text", "messages.website", "messages.approved",
			"messages.guestbook_id", "messages.parent_message_id").
		Order("messages.created_at DESC, messages.id DESC").
		Preload("Replies", func(tx *gorm.DB) *gorm.DB {
			return tx.Select("id", "created_at", "updated_at", "deleted_at", "name", "text",
				"website", "approved", "guestbook_id", "parent_message_id").
				Where("guestbook_id = ? AND approved = ?", guestbookID, true).
				Order("created_at ASC, id ASC")
		})
	if limit > 0 {
		query = query.Offset((page - 1) * limit).Limit(limit)
	}
	var messages []Message
	err := query.Find(&messages).Error
	return messages, err
}

// A hit still verifies ancestry, including historical deletions made outside this process.
func cachedMessagesVisible(tx *gorm.DB, guestbookID uint, messages []Message) (bool, error) {
	ids := make([]uint, 0, len(messages))
	parents := make(map[uint]uint)
	for _, message := range messages {
		if message.GuestbookID != guestbookID || message.ParentMessageID != nil {
			return false, nil
		}
		if _, duplicate := parents[message.ID]; duplicate {
			return false, nil
		}
		ids = append(ids, message.ID)
		parents[message.ID] = 0
		for _, reply := range message.Replies {
			if reply.GuestbookID != guestbookID || reply.ParentMessageID == nil ||
				*reply.ParentMessageID != message.ID || len(reply.Replies) != 0 {
				return false, nil
			}
			if _, duplicate := parents[reply.ID]; duplicate {
				return false, nil
			}
			ids = append(ids, reply.ID)
			parents[reply.ID] = message.ID
		}
	}
	for start := 0; start < len(ids); start += 500 {
		batch := ids[start:min(start+500, len(ids))]
		var rows []struct {
			ID              uint
			ParentMessageID *uint
		}
		if err := approvedMessagesQuery(tx, guestbookID).Select("messages.id", "messages.parent_message_id").
			Where("messages.id IN ?", batch).Find(&rows).Error; err != nil {
			return false, err
		}
		if len(rows) != len(batch) {
			return false, nil
		}
		for _, row := range rows {
			var parent uint
			if row.ParentMessageID != nil {
				parent = *row.ParentMessageID
			}
			if parents[row.ID] != parent {
				return false, nil
			}
		}
	}
	return true, nil
}

func publicAPIError(w http.ResponseWriter, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}
	log.Print("Public guestbook query failed")
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func writePublicJSON(w http.ResponseWriter, value any, hit bool) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "MISS")
	if hit {
		w.Header().Set("X-Cache", "HIT")
	}
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Print("Public guestbook response write failed")
	}
}

func PublicMessagesV1(w http.ResponseWriter, r *http.Request) {
	guestbookID, err := parsePositiveID(chi.URLParam(r, "guestbookID"))
	if err != nil {
		http.Error(w, "Invalid guestbook ID", http.StatusBadRequest)
		return
	}
	for attempt := 0; attempt < 2; attempt++ {
		generation := messageCache.beginRead(guestbookID)
		var messages []Message
		var hit bool
		err = db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
			if err := requireActiveGuestbook(tx, guestbookID); err != nil {
				return err
			}
			if cached, ok := messageCache.messagesAtGeneration(guestbookID, generation); ok {
				visible, err := cachedMessagesVisible(tx, guestbookID, cached)
				if err != nil {
					return err
				}
				if visible {
					messages, hit = cached, true
					return nil
				}
				messageCache.InvalidateGuestbook(guestbookID)
			}
			var err error
			messages, err = readPublicMessages(tx, guestbookID, 0, 0)
			return err
		})
		if err != nil {
			publicAPIError(w, err)
			return
		}
		if hit || messageCache.publishMessages(guestbookID, generation, messages) || attempt == 1 {
			writePublicJSON(w, publicMessages(messages), hit)
			return
		}
	}
}

func publicPagination(r *http.Request) (page, limit int, err error) {
	page, limit = 1, 20
	if value := r.URL.Query().Get("page"); value != "" {
		parsed, parseErr := strconv.Atoi(value)
		if errors.Is(parseErr, strconv.ErrRange) {
			return 0, 0, parseErr
		}
		if parseErr == nil && parsed > 0 {
			page = parsed
		}
	}
	if parsed, parseErr := strconv.Atoi(r.URL.Query().Get("limit")); parseErr == nil && parsed > 0 && parsed <= 100 {
		limit = parsed
	}
	if page-1 > int(^uint(0)>>1)/limit {
		return 0, 0, errors.New("page offset overflow")
	}
	return page, limit, nil
}

func PublicMessagesV2(w http.ResponseWriter, r *http.Request) {
	guestbookID, err := parsePositiveID(chi.URLParam(r, "guestbookID"))
	if err != nil {
		http.Error(w, "Invalid guestbook ID", http.StatusBadRequest)
		return
	}
	page, limit, err := publicPagination(r)
	if err != nil {
		http.Error(w, "Invalid page offset", http.StatusBadRequest)
		return
	}
	for attempt := 0; attempt < 2; attempt++ {
		generation := messageCache.beginRead(guestbookID)
		var response map[string]any
		var total int64
		var hit bool
		err = db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
			if err := requireActiveGuestbook(tx, guestbookID); err != nil {
				return err
			}
			if cached, ok := messageCache.pageAtGeneration(guestbookID, generation, page, limit); ok {
				if messages, ok := cached["messages"].([]Message); ok {
					visible, err := cachedMessagesVisible(tx, guestbookID, messages)
					if err != nil {
						return err
					}
					if visible {
						response, hit = cached, true
						return nil
					}
					messageCache.InvalidateGuestbook(guestbookID)
				}
			}
			// Do not combine a cached count with a new database snapshot.
			if err := publicRootsQuery(tx, guestbookID).Count(&total).Error; err != nil {
				return err
			}
			messages, err := readPublicMessages(tx, guestbookID, page, limit)
			if err != nil {
				return err
			}
			totalPages := total / int64(limit)
			if total%int64(limit) != 0 {
				totalPages++
			}
			response = map[string]any{
				"messages": messages,
				"pagination": map[string]any{
					"page": page, "limit": limit, "total": total, "totalPages": totalPages,
					"hasNext": int64(page) < totalPages, "hasPrevious": page > 1,
				},
			}
			return nil
		})
		if err != nil {
			publicAPIError(w, err)
			return
		}
		if hit || messageCache.publishPage(guestbookID, generation, page, limit, total, response) || attempt == 1 {
			response["messages"] = publicMessages(response["messages"].([]Message))
			writePublicJSON(w, response, hit)
			return
		}
	}
}
