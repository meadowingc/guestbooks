package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMessageCacheRejectsStaleFills(t *testing.T) {
	for _, event := range []string{"invalidate", "clear", "evict"} {
		t.Run(event, func(t *testing.T) {
			cache, err := NewMessageCache(1, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			started, resume := make(chan struct{}), make(chan struct{})
			result := make(chan bool, 1)
			go func() {
				generation := cache.beginRead(1)
				close(started)
				<-resume
				messages := cache.publishMessages(1, generation, []Message{{Text: "stale"}})
				page := cache.publishPage(1, generation, 1, 20, 99, map[string]any{
					"messages": []Message{{Text: "stale"}},
				})
				result <- messages || page
			}()
			<-started
			switch event {
			case "invalidate":
				cache.InvalidateGuestbook(1)
			case "clear":
				cache.Clear()
			case "evict":
				cache.beginRead(2)
			}
			generation := cache.beginRead(1)
			if !cache.publishMessages(1, generation, []Message{{Text: "fresh"}}) ||
				!cache.publishPage(1, generation, 1, 20, 1, map[string]any{"messages": []Message{{Text: "fresh"}}}) {
				t.Fatal("current fill rejected")
			}
			close(resume)
			if <-result {
				t.Fatal("obsolete reader published after invalidation/clear/eviction")
			}
			messages, hit := cache.GetMessages(1)
			if !hit || messages[0].Text != "fresh" {
				t.Fatalf("fresh messages overwritten: %+v, hit=%v", messages, hit)
			}
			page, hit := cache.GetPaginatedResponse(1, 1, 20)
			if !hit || page["messages"].([]Message)[0].Text != "fresh" {
				t.Fatalf("fresh page overwritten: %+v, hit=%v", page, hit)
			}
			if count, hit := cache.GetCount(1); !hit || count != 1 {
				t.Fatalf("fresh count overwritten: %d, hit=%v", count, hit)
			}
		})
	}
}

func TestMessageCacheBoundedIndependentAndCopied(t *testing.T) {
	cache, err := NewMessageCache(2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	website, email := "https://example.test", "private@example.test"
	source := []Message{{Text: "original", Website: &website, Email: &email, Replies: []Message{{Text: "reply"}}}}
	cache.SetMessages(1, source)
	cache.SetPaginatedResponse(1, 1, 20, map[string]any{
		"messages": source, "pagination": map[string]any{"total": int64(1)},
	})
	cache.SetCount(1, 1)
	source[0].Text, source[0].Replies[0].Text, website = "mutated", "mutated", "mutated"
	messages, _ := cache.GetMessages(1)
	if messages[0].Text != "original" || messages[0].Replies[0].Text != "reply" ||
		*messages[0].Website != "https://example.test" || messages[0].Email != nil {
		t.Fatal("cache did not make an isolated public copy")
	}
	messages[0].Replies[0].Text = "caller mutation"
	page, _ := cache.GetPaginatedResponse(1, 1, 20)
	page["pagination"].(map[string]any)["total"] = int64(100)
	page["messages"].([]Message)[0].Text = "caller mutation"
	messages, _ = cache.GetMessages(1)
	page, _ = cache.GetPaginatedResponse(1, 1, 20)
	if messages[0].Replies[0].Text != "reply" || page["messages"].([]Message)[0].Text != "original" ||
		page["pagination"].(map[string]any)["total"] != int64(1) {
		t.Fatal("cache getter leaked a mutable alias")
	}
	cache.InvalidateGuestbook(10)
	if _, hit := cache.GetMessages(1); !hit {
		t.Fatal("invalidation removed another book")
	}
	for id := uint(2); id < 100; id++ {
		cache.beginRead(id)
		cache.SetMessages(id, nil)
		cache.SetCount(id, 0)
		cache.SetPaginatedResponse(id, 1, 20, nil)
	}
	if cache.generations.Len() > 2 || cache.messagesCache.Len() > 2 ||
		cache.countsCache.Len() > 2 || cache.paginatedCache.Len() > 2 {
		t.Fatal("cache bookkeeping exceeded its configured bound")
	}
	if _, hit := cache.GetMessages(1); hit {
		t.Fatal("old message entry was not evicted")
	}
}

func TestMessageCacheExpiryAndConcurrentOperations(t *testing.T) {
	cache, err := NewMessageCache(8, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cache.SetMessages(1, []Message{{Text: "expired"}})
	cache.SetCount(1, 99)
	cache.SetPaginatedResponse(1, 1, 20, map[string]any{"expired": true})
	past := time.Now().Add(-2 * time.Minute)
	cache.mu.Lock()
	messages, _ := cache.messagesCache.Peek("all_messages_1")
	messages.Timestamp = past
	cache.messagesCache.Add("all_messages_1", messages)
	count, _ := cache.countsCache.Peek(1)
	count.Timestamp = past
	cache.countsCache.Add(1, count)
	page, _ := cache.paginatedCache.Peek(pageCacheKey(1, 1, 20))
	page.Timestamp = past
	cache.paginatedCache.Add(pageCacheKey(1, 1, 20), page)
	cache.mu.Unlock()
	if _, hit := cache.GetMessages(1); hit {
		t.Fatal("expired messages returned")
	}
	if _, hit := cache.GetCount(1); hit {
		t.Fatal("expired count returned")
	}
	if _, hit := cache.GetPaginatedResponse(1, 1, 20); hit {
		t.Fatal("expired page returned")
	}

	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := range 100 {
				id := uint(worker + 1)
				generation := cache.beginRead(id)
				cache.publishMessages(id, generation, []Message{{Text: fmt.Sprint(iteration)}})
				cache.publishPage(id, generation, 1, 20, 1, map[string]any{"messages": []Message{{Text: "public"}}})
				cache.GetMessages(id)
				cache.GetCount(id)
				cache.GetPaginatedResponse(id, 1, 20)
				cache.InvalidateGuestbook(id)
				if iteration%10 == 0 {
					cache.Clear()
				}
			}
		}()
	}
	workers.Wait()
}
