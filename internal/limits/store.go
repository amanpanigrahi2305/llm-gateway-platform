package limits

import (
	"context"
	"sync"
	"time"
)

// Store holds counters that expire. Redis is the real store; MemoryStore is
// for tests and for a single gateway replica.
type Store interface {
	// IncrBy adds n to the counter at key and returns the new value. A new
	// counter expires after ttl.
	IncrBy(ctx context.Context, key string, n int64, ttl time.Duration) (int64, error)
	// Get returns the values of the counters, in order. A missing counter is 0.
	Get(ctx context.Context, keys ...string) ([]int64, error)
}

// MemoryStore keeps counters in this process. With more than one gateway
// replica it is wrong: each replica counts only its own requests, so a key
// could use N times its limit with N replicas. Use Redis there.
type MemoryStore struct {
	mu     sync.Mutex
	now    func() time.Time
	values map[string]memoryValue
	writes int
}

type memoryValue struct {
	n       int64
	expires time.Time
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{now: time.Now, values: map[string]memoryValue{}}
}

// IncrBy adds n to the counter at key.
func (m *MemoryStore) IncrBy(_ context.Context, key string, n int64, ttl time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if m.writes++; m.writes%1000 == 0 {
		m.sweep(now) // drop old counters now and then, so memory does not grow forever
	}
	v, ok := m.values[key]
	if !ok || !now.Before(v.expires) {
		v = memoryValue{expires: now.Add(ttl)}
	}
	v.n += n
	m.values[key] = v
	return v.n, nil
}

// Get returns the counters' values.
func (m *MemoryStore) Get(_ context.Context, keys ...string) ([]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := make([]int64, len(keys))
	for i, k := range keys {
		if v, ok := m.values[k]; ok && now.Before(v.expires) {
			out[i] = v.n
		}
	}
	return out, nil
}

func (m *MemoryStore) sweep(now time.Time) {
	for k, v := range m.values {
		if !now.Before(v.expires) {
			delete(m.values, k)
		}
	}
}
