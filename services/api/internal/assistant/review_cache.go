package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

const reviewCacheLimit = 64
const reviewCacheTTL = 10 * time.Minute

// Providers must identify the actual model and endpoint, not just a mutable alias.
type reviewCacheModel interface {
	ReviewCacheIdentity(context.Context, string) (string, error)
}

type reviewCacheEntry struct {
	content string
	expires time.Time
}
type reviewCache struct {
	mu      sync.Mutex
	entries map[string]reviewCacheEntry
	order   []string
}

func (c *reviewCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !time.Now().Before(entry.expires) {
		return "", false
	}
	return entry.content, true
}
func (c *reviewCache) put(key, content string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]reviewCacheEntry{}
	}
	// FIFO bounded storage; replace an expired entry without duplicating its key.
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	if len(c.order) >= reviewCacheLimit {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
	c.entries[key] = reviewCacheEntry{content: content, expires: time.Now().Add(reviewCacheTTL)}
	c.order = append(c.order, key)
}

func actionReviewCacheKey(identity string, input Context, candidates []Suggestion, request ModelRequest) (string, error) {
	// Suggestion IDs belong to the current run; no semantic input is discarded.
	canonical := append([]Suggestion(nil), candidates...)
	for i := range canonical {
		canonical[i].ID = ""
	}
	payload, err := json.Marshal(struct {
		Context    Context      `json:"context"`
		Candidates []Suggestion `json:"candidates"`
	}{input, canonical})
	if err != nil {
		return "", err
	}
	request.Messages = append([]ChatMessage(nil), request.Messages...)
	request.Messages[1].Content = string(payload)
	encoded, err := json.Marshal(struct {
		Identity string       `json:"identity"`
		Request  ModelRequest `json:"request"`
	}{identity, request})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}
