package limits

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
)

// RedisStore keeps counters in Redis, so every gateway replica sees the same
// numbers. It needs Redis 7 or newer (EXPIRE ... NX).
type RedisStore struct {
	client *redis.Client
}

// NewRedisStore connects to the Redis URL, for example redis://host:6379/0.
// Timeouts are short and retries are off: when Redis is down, a request
// should learn it in milliseconds, then the fail-open or fail-closed rule applies.
func NewRedisStore(url string) (*RedisStore, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redis url: %w", err)
	}
	opt.DialTimeout = 200 * time.Millisecond
	opt.ReadTimeout = 200 * time.Millisecond
	opt.WriteTimeout = 200 * time.Millisecond
	opt.DialerRetries = 1
	opt.MaxRetries = -1 // -1 turns command retries off
	opt.MaintNotificationsConfig = &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}
	return &RedisStore{client: redis.NewClient(opt)}, nil
}

// IncrBy adds n and sets the expiry of a new counter in one transaction
// (MULTI/EXEC), so two replicas can never lose each other's updates.
func (s *RedisStore) IncrBy(ctx context.Context, key string, n int64, ttl time.Duration) (int64, error) {
	var incr *redis.IntCmd
	_, err := s.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		incr = p.IncrBy(ctx, key, n)
		p.ExpireNX(ctx, key, ttl) // only a new counter gets an expiry
		return nil
	})
	if err != nil {
		return 0, err
	}
	return incr.Val(), nil
}

// Get reads several counters in one round trip (MGET).
func (s *RedisStore) Get(ctx context.Context, keys ...string) ([]int64, error) {
	vals, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(keys))
	for i, v := range vals {
		str, ok := v.(string)
		if !ok {
			continue // missing key
		}
		if out[i], err = strconv.ParseInt(str, 10, 64); err != nil {
			return nil, fmt.Errorf("counter %s: %w", keys[i], err)
		}
	}
	return out, nil
}

// Ping checks the connection.
func (s *RedisStore) Ping(ctx context.Context) error { return s.client.Ping(ctx).Err() }

// Close closes the connection pool.
func (s *RedisStore) Close() error { return s.client.Close() }
