package middleware

import (
	"fmt"
	"sync"
	"time"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/logger"
	"github.com/Xm798/placard/internal/userctx"
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

const rateLimitWindow = time.Hour

// RateLimitOptions configures a per-user rate limiter.
type RateLimitOptions struct {
	Counter  Counter
	Limit    int  // max requests per window per user
	FailOpen bool // when the counter is unavailable: true → allow, false → local fallback count
}

// UploadRateLimit enforces a per-user hourly upload cap using a shared counter
// (key placard:ratelimit:upload:{AuthzID}:{hourbucket}). It is a thin
// specialization of UserRateLimit — keyClass "upload", window rateLimitWindow
// (1h) — which reproduces the exact same key shape and behavior (FailOpen
// semantics, local-counter fallback) this function used to implement directly.
// Exceeding the limit returns 429; identity must already be set by the auth
// middleware, absent AuthzID → 401.
func UploadRateLimit(opts RateLimitOptions) fiber.Handler {
	return UserRateLimit(opts, "upload", opts.Limit, rateLimitWindow)
}

// UserRateLimit enforces a per-user rate cap over an arbitrary window, keyed by
// keyClass so independent call sites never share a counter namespace. Key
// placard:ratelimit:<keyClass>:<authzID>:<bucket> (bucket = window-aligned unix
// time), FailOpen governs counter-down behavior, and a per-replica localCounter
// is the last-resort fallback when it doesn't. Identity must already be set by
// upstream auth middleware; absent AuthzID → 401.
func UserRateLimit(opts RateLimitOptions, keyClass string, limit int, window time.Duration) fiber.Handler {
	log := logger.Module("ratelimit")
	local := newLocalCounter()
	// A nil counter becomes an in-process one rather than a panic per request:
	// an unwired limiter must still enforce something.
	if opts.Counter == nil {
		opts.Counter = NewMemoryCounter()
	}

	return func(c *fiber.Ctx) error {
		authzID := userctx.AuthzID(c)
		if authzID == "" {
			return unauthorized()
		}

		bucket := time.Now().Unix() / int64(window.Seconds())
		key := fmt.Sprintf("placard:ratelimit:%s:%s:%d", keyClass, authzID, bucket)

		count, err := opts.Counter.Incr(c.UserContext(), key, window)
		if err != nil {
			log.Warn("rate limit counter unavailable", zap.String("key_class", keyClass), zap.Error(err))
			if opts.FailOpen {
				return c.Next()
			}
			// Local fallback: best-effort per-replica counter.
			if local.incr(authzID, bucket) > int64(limit) {
				return tooManyRequests()
			}
			return c.Next()
		}

		if count > int64(limit) {
			return tooManyRequests()
		}
		return c.Next()
	}
}

// tooManyRequests returns the typed 429 error; the caller returns it so Fiber's
// ErrorHandler renders the unified envelope.
func tooManyRequests() *apperr.Error {
	return apperr.RateLimited()
}

// localCounterMaxKeys caps the number of distinct actors tracked within one
// window — the last resort when a single window alone would overflow the map
// (boundedness wins over counter continuity for a degraded safety net).
const localCounterMaxKeys = 4096

// localCounter is a best-effort per-replica fallback when the shared counter is
// unavailable. It tracks exactly one time bucket: counts are keyed by actor (IP
// or user) within the current window, and a rolled-over bucket discards the map
// — old windows can never be read again, so nothing stale accumulates during a
// prolonged outage. Not shared across replicas; a degraded safety net, not the
// authoritative limit.
type localCounter struct {
	mu     sync.Mutex
	bucket int64
	counts map[string]int64
}

func newLocalCounter() *localCounter {
	return &localCounter{counts: make(map[string]int64)}
}

// incr increments actor's count within bucket, resetting the map when the
// window rolls over; see localCounterMaxKeys for the same-window last resort.
func (l *localCounter) incr(actor string, bucket int64) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if bucket != l.bucket {
		l.bucket = bucket
		l.counts = make(map[string]int64)
	}
	if _, ok := l.counts[actor]; !ok && len(l.counts) >= localCounterMaxKeys {
		l.counts = make(map[string]int64)
	}
	l.counts[actor]++
	return l.counts[actor]
}

// peek returns actor's count within bucket without incrementing; a bucket
// other than the tracked one reads as zero.
func (l *localCounter) peek(actor string, bucket int64) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if bucket != l.bucket {
		return 0
	}
	return l.counts[actor]
}
