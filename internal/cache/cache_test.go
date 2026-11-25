package cache

import (
	"testing"
	"time"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/providers"
)

func ptr[T any](v T) *T { return &v }

func request(content string, temperature *float64) providers.ChatRequest {
	return providers.ChatRequest{Messages: []providers.Message{{Role: "user", Content: content}}, Temperature: temperature}
}

func TestKeyRules(t *testing.T) {
	zero := ptr(0.0)
	if Key("dev", "fast", request("hi", nil)) != "" {
		t.Error("no temperature (provider default, often 1) must not be cached")
	}
	if Key("dev", "fast", request("hi", ptr(0.7))) != "" {
		t.Error("temperature 0.7 must not be cached")
	}
	k := Key("dev", "fast", request("hi", zero))
	if len(k) != 64 {
		t.Fatalf("key = %q, want a SHA-256 hex hash", k)
	}
	if Key("dev", "fast", request("hi", zero)) != k {
		t.Error("the same request gave a different key")
	}
	for name, other := range map[string]string{
		"other client key": Key("ops", "fast", request("hi", zero)),
		"other alias":      Key("dev", "smart", request("hi", zero)),
		"other prompt":     Key("dev", "fast", request("hello", zero)),
	} {
		if other == k {
			t.Errorf("%s gave the same key", name)
		}
	}
	withMax := request("hi", zero)
	withMax.MaxTokens = ptr(5)
	if Key("dev", "fast", withMax) == k {
		t.Error("a different max_tokens gave the same key")
	}
}

func TestGetPutAndTTL(t *testing.T) {
	c := New(time.Minute, 10)
	now := time.Unix(1_000_000, 0)
	c.now = func() time.Time { return now }
	c.Put("k", Entry{Content: "cached answer"})
	if e, ok := c.Get("k"); !ok || e.Content != "cached answer" {
		t.Fatalf("Get = %+v, %v", e, ok)
	}
	now = now.Add(time.Minute)
	if _, ok := c.Get("k"); ok {
		t.Fatal("entry still served after its TTL")
	}
	if c.Len() != 0 {
		t.Errorf("Len = %d; an expired entry should be removed when read", c.Len())
	}
}

func TestLeastRecentlyUsedIsEvicted(t *testing.T) {
	c := New(time.Minute, 2)
	c.Put("a", Entry{Content: "A"})
	c.Put("b", Entry{Content: "B"})
	c.Get("a")                      // a is now the most recently used
	c.Put("c", Entry{Content: "C"}) // the cache is full: b goes
	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a was evicted, but it was used recently")
	}
	if c.Len() != 2 {
		t.Errorf("Len = %d, want 2", c.Len())
	}
}
