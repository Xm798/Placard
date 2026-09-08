// Package lock provides the mutex that keeps exactly one cleanup cron running
// at a time. RedisLocker spans replicas and is what a multi-replica deployment
// needs; MemoryLocker covers a single binary running without Redis, where the
// only cron to exclude is this process's own.
package lock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotAcquired signals the lock is held elsewhere — the caller should skip
// this round, not error.
var ErrNotAcquired = errors.New("lock: not acquired")

// Locker is the mutex surface cleanup depends on.
type Locker interface {
	// Acquire grabs key for ttl. On success it returns an idempotent release
	// closure that deletes ONLY the lock it still owns; on contention it returns
	// ErrNotAcquired.
	Acquire(ctx context.Context, key string, ttl time.Duration) (release func(context.Context), err error)
}

// RedisLocker implements Locker over a shared *redis.Client — the only
// implementation that excludes OTHER replicas.
type RedisLocker struct{ rdb *redis.Client }

// NewRedisLocker builds a RedisLocker over rdb. It reuses the caller's existing
// client (main.go) — never opens its own connection.
func NewRedisLocker(rdb *redis.Client) *RedisLocker { return &RedisLocker{rdb: rdb} }

// releaseScript deletes the lock only when its value still equals our token
// (CAS). This prevents the classic mis-release: A acquires → A's TTL lapses → B
// acquires → A finishes and would otherwise DEL B's lock.
var releaseScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("del", KEYS[1])
else
  return 0
end`)

// Acquire takes key via SET NX PX with a unique token.
//
// Pitfalls baked in:
//   - Unique token + Lua CAS release (releaseScript) so we never delete a lock
//     a slower successor now holds.
//   - TTL is the backstop: it MUST exceed the worst-case single round, or the
//     lock lapses mid-cleanup and two replicas run concurrently. Callers pass a
//     TTL comfortably above measured round time; batchLimit bounds round size.
func (l *RedisLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (func(context.Context), error) {
	token := randToken()
	ok, err := l.rdb.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotAcquired
	}
	var once sync.Once
	return func(rctx context.Context) {
		once.Do(func() {
			_ = releaseScript.Run(rctx, l.rdb, []string{key}, token).Err()
		})
	}, nil
}

func randToken() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }
