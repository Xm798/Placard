package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/Xm798/placard/internal/logger"
	"github.com/Xm798/placard/internal/userctx"
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

const ipLimitWindow = time.Minute

// IPLimiter is a per-subject fixed-window counter for pre-auth surfaces. The
// auth routes (/auth/login, /auth/callback) count every request via Handler();
// failed authn attempts count via Hit()+Exceeded(). A counter outage falls back
// to a per-replica local counter (fail-closed enough without a shared view).
//
// Handler() always keys on the client IP. Hit/Exceeded take the subject
// explicitly, so a caller that needs a narrower budget than "this address"
// composes one — the share-code endpoint passes "<file>|<ip>" to meter guesses
// per page rather than across every page an address touches.
type IPLimiter struct {
	counter  Counter
	limit    int
	keyClass string // key segment, e.g. "authroute" / "authfail"
	local    *localCounter
	log      *zap.Logger
}

// NewIPLimiter builds a limiter over counter. A nil counter becomes an
// in-process one rather than a panic on the auth path: an unwired limiter must
// still enforce something.
func NewIPLimiter(counter Counter, limit int, keyClass string) *IPLimiter {
	if counter == nil {
		counter = NewMemoryCounter()
	}
	return &IPLimiter{counter: counter, limit: limit, keyClass: keyClass,
		local: newLocalCounter(), log: logger.Module("iplimit")}
}

// bucket returns the current fixed-window bucket index. Computed once per
// operation so the shared key and the local-counter fallback share one window.
func (l *IPLimiter) bucket() int64 {
	return time.Now().Unix() / int64(ipLimitWindow.Seconds())
}

func (l *IPLimiter) key(subject string, bucket int64) string {
	return fmt.Sprintf("placard:ratelimit:%s:%s:%d", l.keyClass, subject, bucket)
}

// incr bumps the window counter for subject, falling back to the per-replica
// local counter (with a warn) when the shared counter is unavailable. Returns
// the resulting count.
func (l *IPLimiter) incr(ctx context.Context, subject string) int64 {
	bucket := l.bucket()
	count, err := l.counter.Incr(ctx, l.key(subject, bucket), ipLimitWindow)
	if err != nil {
		l.log.Warn("ip limit counter unavailable", zap.Error(err))
		return l.local.incr(subject, bucket)
	}
	return count
}

// Handler counts every request and rejects with 429 above the limit.
func (l *IPLimiter) Handler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if l.incr(c.UserContext(), c.IP()) > int64(l.limit) {
			return tooManyRequests()
		}
		return c.Next()
	}
}

// Hit records one failed attempt for subject (best-effort).
func (l *IPLimiter) Hit(ctx context.Context, subject string) {
	l.incr(ctx, subject)
}

// Exceeded reports whether subject is over the failure budget for this window. A
// counter that is down (or was never configured) degrades to the per-replica
// local counter — the limiter must never turn into a hard error (or a panic) on
// the auth path.
//
// Both counts are consulted even when the shared one answers: hits that landed
// locally during an outage would otherwise stop counting the moment the shared
// counter came back mid-window, handing the attacker a fresh budget for the
// remainder of it.
func (l *IPLimiter) Exceeded(ctx context.Context, subject string) bool {
	bucket := l.bucket()
	local := l.local.peek(subject, bucket)
	n, err := l.counter.Peek(ctx, l.key(subject, bucket))
	if err != nil {
		l.log.Warn("ip limit counter unavailable", zap.Error(err))
		return local > int64(l.limit)
	}
	return max(n, local) > int64(l.limit)
}

// AnonOnly runs next only for requests the auth middleware left anonymous,
// letting an authenticated one straight through. It is how the share-link
// routes carry a per-IP budget for visitors without also metering a signed-in
// owner, whose traffic is already attributable and bounded per account.
func AnonOnly(next fiber.Handler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if userctx.AuthzID(c) != "" {
			return c.Next()
		}
		return next(c)
	}
}
