package lock

import (
	"context"
	"sync"
	"time"
)

// MemoryLocker implements Locker inside one process, for a deployment running
// without Redis. It serializes the cron against itself and knows nothing of
// other replicas — running several replicas against one database without Redis
// means several concurrent crons.
type MemoryLocker struct {
	mu   sync.Mutex
	held map[string]hold
	now  func() time.Time
}

type hold struct {
	token string
	until time.Time
}

func NewMemoryLocker() *MemoryLocker {
	return &MemoryLocker{held: make(map[string]hold), now: time.Now}
}

// Acquire takes key for ttl, honoring the same TTL backstop as RedisLocker: a
// holder that never releases (a panicking round, a killed goroutine) frees the
// key once its ttl lapses. Release is CAS'd on the holder's token so a slow
// predecessor cannot delete its successor's lock.
func (l *MemoryLocker) Acquire(_ context.Context, key string, ttl time.Duration) (func(context.Context), error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if h, ok := l.held[key]; ok && now.Before(h.until) {
		return nil, ErrNotAcquired
	}
	token := randToken()
	l.held[key] = hold{token: token, until: now.Add(ttl)}

	var once sync.Once
	return func(context.Context) {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if h, ok := l.held[key]; ok && h.token == token {
				delete(l.held, key)
			}
		})
	}, nil
}
