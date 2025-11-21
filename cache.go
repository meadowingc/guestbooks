package main

import (
	"fmt"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// CachedMessages stores cached message data with timestamp
type CachedMessages struct {
	Messages  []Message
	Timestamp time.Time
}

// CachedCount stores cached count data with timestamp
type CachedCount struct {
	Count     int64
	Timestamp time.Time
}

// CachedPaginatedResponse stores the full paginated API response
type CachedPaginatedResponse struct {
	Response  map[string]any
	Timestamp time.Time
}

// MessageCache manages caching for guestbook messages
type MessageCache struct {
	messagesCache  *lru.Cache[string, CachedMessages]
	countsCache    *lru.Cache[uint, CachedCount]
	paginatedCache *lru.Cache[string, CachedPaginatedResponse]
	ttl            time.Duration
	mu             sync.RWMutex
}

// NewMessageCache creates a new message cache with specified size and TTL
func NewMessageCache(size int, ttl time.Duration) (*MessageCache, error) {
	messagesCache, err := lru.New[string, CachedMessages](size)
	if err != nil {
		return nil, err
	}

	countsCache, err := lru.New[uint, CachedCount](size)
	if err != nil {
		return nil, err
	}

	paginatedCache, err := lru.New[string, CachedPaginatedResponse](size)
	if err != nil {
		return nil, err
	}

	return &MessageCache{
		messagesCache:  messagesCache,
		countsCache:    countsCache,
		paginatedCache: paginatedCache,
		ttl:            ttl,
	}, nil
}

// GetMessages retrieves cached messages for a guestbook (v1 API - all messages)
func (c *MessageCache) GetMessages(guestbookID uint) ([]Message, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := fmt.Sprintf("all_messages_%d", guestbookID)
	cached, ok := c.messagesCache.Get(key)
	if !ok {
		return nil, false
	}

	// Check if cache entry has expired
	if time.Since(cached.Timestamp) > c.ttl {
		c.mu.RUnlock()
		c.mu.Lock()
		c.messagesCache.Remove(key)
		c.mu.Unlock()
		c.mu.RLock()
		return nil, false
	}

	return cached.Messages, true
}

// SetMessages stores messages in cache for a guestbook (v1 API)
func (c *MessageCache) SetMessages(guestbookID uint, messages []Message) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := fmt.Sprintf("all_messages_%d", guestbookID)
	c.messagesCache.Add(key, CachedMessages{
		Messages:  messages,
		Timestamp: time.Now(),
	})
}

// GetPaginatedResponse retrieves cached paginated response (v2 API)
func (c *MessageCache) GetPaginatedResponse(guestbookID uint, page, limit int) (map[string]any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := fmt.Sprintf("paginated_messages_%d_p%d_l%d", guestbookID, page, limit)
	cached, ok := c.paginatedCache.Get(key)
	if !ok {
		return nil, false
	}

	// Check if cache entry has expired
	if time.Since(cached.Timestamp) > c.ttl {
		c.mu.RUnlock()
		c.mu.Lock()
		c.paginatedCache.Remove(key)
		c.mu.Unlock()
		c.mu.RLock()
		return nil, false
	}

	return cached.Response, true
}

// SetPaginatedResponse stores paginated response in cache (v2 API)
func (c *MessageCache) SetPaginatedResponse(guestbookID uint, page, limit int, response map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := fmt.Sprintf("paginated_messages_%d_p%d_l%d", guestbookID, page, limit)
	c.paginatedCache.Add(key, CachedPaginatedResponse{
		Response:  response,
		Timestamp: time.Now(),
	})
}

// GetCount retrieves cached message count for a guestbook
func (c *MessageCache) GetCount(guestbookID uint) (int64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cached, ok := c.countsCache.Get(guestbookID)
	if !ok {
		return 0, false
	}

	// Check if cache entry has expired
	if time.Since(cached.Timestamp) > c.ttl {
		c.mu.RUnlock()
		c.mu.Lock()
		c.countsCache.Remove(guestbookID)
		c.mu.Unlock()
		c.mu.RLock()
		return 0, false
	}

	return cached.Count, true
}

// SetCount stores message count in cache for a guestbook
func (c *MessageCache) SetCount(guestbookID uint, count int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.countsCache.Add(guestbookID, CachedCount{
		Count:     count,
		Timestamp: time.Now(),
	})
}

// InvalidateGuestbook clears all cached data for a specific guestbook
func (c *MessageCache) InvalidateGuestbook(guestbookID uint) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Remove all messages cache
	allMessagesKey := fmt.Sprintf("all_messages_%d", guestbookID)
	c.messagesCache.Remove(allMessagesKey)

	// Remove count cache
	c.countsCache.Remove(guestbookID)

	// Remove all paginated caches for this guestbook
	// We iterate through all keys and remove ones matching this guestbook
	keys := c.paginatedCache.Keys()
	prefix := fmt.Sprintf("paginated_messages_%d_", guestbookID)
	for _, key := range keys {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			c.paginatedCache.Remove(key)
		}
	}
}

// Clear removes all entries from the cache
func (c *MessageCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.messagesCache.Purge()
	c.countsCache.Purge()
	c.paginatedCache.Purge()
}
