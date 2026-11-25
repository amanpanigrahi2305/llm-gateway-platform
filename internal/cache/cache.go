// Package cache is an exact-match response cache with a TTL.
//
// Safety rules:
//   - Only requests with temperature 0 are cached. With a higher temperature
//     the client asks for a different answer each time; a cached copy would
//     break that. A missing temperature means the provider default (often 1).
//   - Only normal (non-streaming) requests are cached.
//   - The cache key includes the client key's ID, so one key never gets an
//     answer that was produced for another key.
//
// Privacy: the cache is off by default, because it keeps answer text. When it
// is on, answers stay in this process's memory only, never on disk, and the
// key is a SHA-256 hash, so prompts are not stored. Each replica has its own
// cache: more replicas mean a lower hit ratio, but answers stay correct.
package cache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/providers"
)

// Entry is one cached answer.
type Entry struct {
	Content      string
	FinishReason string
	Usage        providers.Usage
	Provider     string // who produced the answer, to report the cost saved
	Model        string
}

// Cache is a least-recently-used cache with a time to live.
type Cache struct {
	ttl time.Duration
	max int
	now func() time.Time // replaced in tests

	mu    sync.Mutex
	order *list.List               // front = most recently used
	items map[string]*list.Element // key -> element holding *item
}

type item struct {
	key     string
	entry   Entry
	expires time.Time
}

// New returns a cache that keeps up to maxEntries answers for ttl each.
func New(ttl time.Duration, maxEntries int) *Cache {
	return &Cache{ttl: ttl, max: max(maxEntries, 1), now: time.Now,
		order: list.New(), items: map[string]*list.Element{}}
}

// Key returns the cache key for a request, or "" when it must not be cached.
func Key(keyID, alias string, req providers.ChatRequest) string {
	if req.Temperature == nil || *req.Temperature != 0 {
		return ""
	}
	h := sha256.New()
	_ = json.NewEncoder(h).Encode(struct {
		KeyID     string
		Alias     string
		Messages  []providers.Message
		TopP      *float64
		MaxTokens *int
		Stop      []string
	}{keyID, alias, req.Messages, req.TopP, req.MaxTokens, req.Stop})
	return hex.EncodeToString(h.Sum(nil))
}

// Get returns a fresh entry and marks it as recently used.
func (c *Cache) Get(key string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return Entry{}, false
	}
	it := el.Value.(*item)
	if !c.now().Before(it.expires) {
		c.order.Remove(el)
		delete(c.items, key)
		return Entry{}, false
	}
	c.order.MoveToFront(el)
	return it.entry, true
}

// Put stores an entry. When the cache is full, the least recently used entry goes.
func (c *Cache) Put(key string, e Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value = &item{key: key, entry: e, expires: c.now().Add(c.ttl)}
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&item{key: key, entry: e, expires: c.now().Add(c.ttl)})
	if c.order.Len() > c.max {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*item).key)
	}
}

// Len returns the number of entries, fresh or not yet removed.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
