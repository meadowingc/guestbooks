package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

type CachedMessages struct {
	Messages  []Message
	Timestamp time.Time
}

type CachedCount struct {
	Count     int64
	Timestamp time.Time
}

type CachedPaginatedResponse struct {
	Response  map[string]any
	Timestamp time.Time
}

// A nonzero-sized identity cannot be reused while an old reader still holds it.
type cacheGeneration byte

type MessageCache struct {
	messagesCache  *lru.Cache[string, CachedMessages]
	countsCache    *lru.Cache[uint, CachedCount]
	paginatedCache *lru.Cache[string, CachedPaginatedResponse]
	generations    *lru.Cache[uint, *cacheGeneration]
	ttl            time.Duration
	mu             sync.Mutex
}

func NewMessageCache(size int, ttl time.Duration) (*MessageCache, error) {
	messages, err := lru.New[string, CachedMessages](size)
	if err != nil {
		return nil, err
	}
	counts, err := lru.New[uint, CachedCount](size)
	if err != nil {
		return nil, err
	}
	pages, err := lru.New[string, CachedPaginatedResponse](size)
	if err != nil {
		return nil, err
	}
	generations, err := lru.New[uint, *cacheGeneration](size)
	if err != nil {
		return nil, err
	}
	return &MessageCache{
		messagesCache: messages, countsCache: counts, paginatedCache: pages,
		generations: generations, ttl: ttl,
	}, nil
}

// beginRead must precede every database/cache read contributing to a fill.
// Evicting a generation rejects its in-flight fills rather than reusing a token.
func (c *MessageCache) beginRead(guestbookID uint) *cacheGeneration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation, ok := c.generations.Get(guestbookID); ok {
		return generation
	}
	generation := new(cacheGeneration)
	c.generations.Add(guestbookID, generation)
	return generation
}

func (c *MessageCache) currentLocked(guestbookID uint, generation *cacheGeneration) bool {
	current, ok := c.generations.Peek(guestbookID)
	return ok && current == generation
}

func (c *MessageCache) getMessagesLocked(guestbookID uint) ([]Message, bool) {
	key := fmt.Sprintf("all_messages_%d", guestbookID)
	cached, ok := c.messagesCache.Get(key)
	if !ok {
		return nil, false
	}
	if time.Since(cached.Timestamp) > c.ttl {
		c.messagesCache.Remove(key)
		return nil, false
	}
	return cloneCachedMessages(cached.Messages), true
}

func (c *MessageCache) GetMessages(guestbookID uint) ([]Message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getMessagesLocked(guestbookID)
}

func (c *MessageCache) messagesAtGeneration(guestbookID uint, generation *cacheGeneration) ([]Message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.currentLocked(guestbookID, generation) {
		return nil, false
	}
	return c.getMessagesLocked(guestbookID)
}

func (c *MessageCache) setMessagesLocked(guestbookID uint, messages []Message) {
	c.messagesCache.Add(fmt.Sprintf("all_messages_%d", guestbookID), CachedMessages{
		Messages: cloneCachedMessages(messages), Timestamp: time.Now(),
	})
}

// SetMessages is retained for callers publishing already-current data.
// Read-through fills must use publishMessages with a token acquired before reading.
func (c *MessageCache) SetMessages(guestbookID uint, messages []Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setMessagesLocked(guestbookID, messages)
}

func (c *MessageCache) publishMessages(guestbookID uint, generation *cacheGeneration, messages []Message) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.currentLocked(guestbookID, generation) {
		return false
	}
	c.setMessagesLocked(guestbookID, messages)
	return true
}

func pageCacheKey(guestbookID uint, page, limit int) string {
	return fmt.Sprintf("paginated_messages_%d_p%d_l%d", guestbookID, page, limit)
}

func (c *MessageCache) getPageLocked(guestbookID uint, page, limit int) (map[string]any, bool) {
	key := pageCacheKey(guestbookID, page, limit)
	cached, ok := c.paginatedCache.Get(key)
	if !ok {
		return nil, false
	}
	if time.Since(cached.Timestamp) > c.ttl {
		c.paginatedCache.Remove(key)
		return nil, false
	}
	return cloneCachedMap(cached.Response), true
}

func (c *MessageCache) GetPaginatedResponse(guestbookID uint, page, limit int) (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getPageLocked(guestbookID, page, limit)
}

func (c *MessageCache) pageAtGeneration(guestbookID uint, generation *cacheGeneration, page, limit int) (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.currentLocked(guestbookID, generation) {
		return nil, false
	}
	return c.getPageLocked(guestbookID, page, limit)
}

func (c *MessageCache) SetPaginatedResponse(guestbookID uint, page, limit int, response map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paginatedCache.Add(pageCacheKey(guestbookID, page, limit), CachedPaginatedResponse{
		Response: cloneCachedMap(response), Timestamp: time.Now(),
	})
}

// The count and page come from one database snapshot and are published together.
func (c *MessageCache) publishPage(guestbookID uint, generation *cacheGeneration, page, limit int, count int64, response map[string]any) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.currentLocked(guestbookID, generation) {
		return false
	}
	now := time.Now()
	c.countsCache.Add(guestbookID, CachedCount{Count: count, Timestamp: now})
	c.paginatedCache.Add(pageCacheKey(guestbookID, page, limit), CachedPaginatedResponse{
		Response: cloneCachedMap(response), Timestamp: now,
	})
	return true
}

func (c *MessageCache) GetCount(guestbookID uint) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cached, ok := c.countsCache.Get(guestbookID)
	if !ok {
		return 0, false
	}
	if time.Since(cached.Timestamp) > c.ttl {
		c.countsCache.Remove(guestbookID)
		return 0, false
	}
	return cached.Count, true
}

func (c *MessageCache) SetCount(guestbookID uint, count int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.countsCache.Add(guestbookID, CachedCount{Count: count, Timestamp: time.Now()})
}

func (c *MessageCache) InvalidateGuestbook(guestbookID uint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generations.Remove(guestbookID)
	c.messagesCache.Remove(fmt.Sprintf("all_messages_%d", guestbookID))
	c.countsCache.Remove(guestbookID)
	prefix := fmt.Sprintf("paginated_messages_%d_", guestbookID)
	for _, key := range c.paginatedCache.Keys() {
		if strings.HasPrefix(key, prefix) {
			c.paginatedCache.Remove(key)
		}
	}
}

func (c *MessageCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generations.Purge()
	c.messagesCache.Purge()
	c.countsCache.Purge()
	c.paginatedCache.Purge()
}

func cloneCachedMessages(messages []Message) []Message {
	if messages == nil {
		return nil
	}
	result := make([]Message, len(messages))
	for i, message := range messages {
		result[i] = message
		// The cache is public-only; never retain private or loaded associations.
		result[i].Email = nil
		result[i].Guestbook = Guestbook{}
		if message.Website != nil {
			website := *message.Website
			result[i].Website = &website
		}
		if message.ParentMessageID != nil {
			parent := *message.ParentMessageID
			result[i].ParentMessageID = &parent
		}
		result[i].Replies = cloneCachedMessages(message.Replies)
	}
	return result
}

func cloneCachedMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		switch value := value.(type) {
		case map[string]any:
			result[key] = cloneCachedMap(value)
		case []Message:
			result[key] = cloneCachedMessages(value)
		default:
			result[key] = value
		}
	}
	return result
}
