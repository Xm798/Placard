package middleware

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Counter is the fixed-window counter both rate limiters run on: keys already
// carry their window bucket, so an implementation only has to increment a key
// and forget it once the window has passed.
//
// Incr returns the count INCLUDING this call. Peek returns the count so far and
// 0 for a key nobody has touched — never an error for a missing key, which is
// an ordinary fresh window rather than a fault. Both report real backend
// failures as errors, which the limiters answer with their local fallback.
type Counter interface {
	Incr(ctx context.Context, key string, window time.Duration) (int64, error)
	Peek(ctx context.Context, key string) (int64, error)
}

// RedisCounter is the shared-across-replicas Counter, and the only one that can
// enforce a limit for a deployment running more than one replica.
type RedisCounter struct{ rdb *redis.Client }

func NewRedisCounter(rdb *redis.Client) *RedisCounter { return &RedisCounter{rdb: rdb} }

// errNoRedis is what a limiter sees when it was built without a client at all;
// it takes the same local-fallback path as an outage rather than panicking on
// the auth path.
var errNoRedis = errors.New("ratelimit: redis client is nil")

// Incr increments the counter and sets the window TTL on the first increment.
func (c *RedisCounter) Incr(ctx context.Context, key string, window time.Duration) (int64, error) {
	if c.rdb == nil {
		return 0, errNoRedis
	}
	count, err := c.rdb.Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	if count == 1 {
		// Best-effort TTL; a missed expire only widens the window slightly.
		_ = c.rdb.Expire(ctx, key, window).Err()
	}
	return count, nil
}

func (c *RedisCounter) Peek(ctx context.Context, key string) (int64, error) {
	if c.rdb == nil {
		return 0, errNoRedis
	}
	n, err := c.rdb.Get(ctx, key).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil // fresh window, not an outage
	}
	if err != nil {
		return 0, err
	}
	return n, nil
}

// memoryCounterMaxKeys caps the distinct keys held between sweeps. Reaching it
// means arrivals outran the sweep interval; discarding the map keeps the
// limiter bounded at the cost of forgiving the counts in flight, the same
// trade localCounter makes.
const memoryCounterMaxKeys = 16384

// memorySweepInterval is how often expired entries are collected. Entries are
// also checked on read, so the sweep only reclaims memory for keys nobody
// touches again — which, since a key names its own window, is all of them.
const memorySweepInterval = time.Minute

// MemoryCounter is the single-process Counter, for a deployment running without
// Redis. It is exact within one replica and blind to every other one: a
// multi-replica deployment must configure Redis, or each replica hands out the
// full budget.
type MemoryCounter struct {
	mu        sync.Mutex
	entries   map[string]memoryEntry
	nextSweep time.Time
	now       func() time.Time
}

type memoryEntry struct {
	count     int64
	expiresAt time.Time
}

func NewMemoryCounter() *MemoryCounter {
	return &MemoryCounter{entries: make(map[string]memoryEntry), now: time.Now}
}

func (c *MemoryCounter) Incr(_ context.Context, key string, window time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	c.sweep(now)

	e, ok := c.entries[key]
	if !ok || !now.Before(e.expiresAt) {
		if len(c.entries) >= memoryCounterMaxKeys {
			c.entries = make(map[string]memoryEntry)
		}
		e = memoryEntry{expiresAt: now.Add(window)}
	}
	e.count++
	c.entries[key] = e
	return e.count, nil
}

func (c *MemoryCounter) Peek(_ context.Context, key string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[key]
	if !ok || !c.now().Before(e.expiresAt) {
		return 0, nil
	}
	return e.count, nil
}

// sweep drops expired entries, at most once per memorySweepInterval. Callers
// hold the mutex.
func (c *MemoryCounter) sweep(now time.Time) {
	if now.Before(c.nextSweep) {
		return
	}
	c.nextSweep = now.Add(memorySweepInterval)
	for k, e := range c.entries {
		if !now.Before(e.expiresAt) {
			delete(c.entries, k)
		}
	}
}
