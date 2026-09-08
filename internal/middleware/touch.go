package middleware

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Xm798/placard/internal/logger"
)

// ActiveTouchThrottle bounds how often last_active_at is (re)written per
// authz_id — a per-pod, in-memory window (spec: 5 minutes). Unlike
// handler.lastUsedToucher (which judges staleness off the token row it just
// read), the auth middleware never has a user row in hand — session/PAT
// resolution only proves an authz_id — so the throttle here is judged off an
// in-process map instead of a query. A var (not const) so tests can shrink it.
var ActiveTouchThrottle = 5 * time.Minute

// activeTouchTimeout bounds the detached async last_active_at write.
const activeTouchTimeout = 5 * time.Second

// ActiveToucher stamps user.last_active_at after successful authentication on
// any channel, throttled per-pod in memory so it never becomes a per-request
// DB write. The write itself runs detached from the request context, exactly
// like lastUsedToucher (internal/handler/token.go) — a slow or failed write
// can never affect the auth result. Touch is nil-safe on the receiver so
// AuthOptions.TouchActive can be left unset without a nil check at call sites.
type ActiveToucher struct {
	mu    sync.Mutex
	seen  map[string]time.Time
	touch func(ctx context.Context, authzID string, at time.Time) error
	log   *zap.Logger
}

// NewActiveToucher builds an ActiveToucher around a TouchLastActive-shaped
// write (e.g. UserRepo.TouchLastActive). touch is update-only by contract — a
// missing user row is a silent no-op there, never a new-row build point.
func NewActiveToucher(touch func(ctx context.Context, authzID string, at time.Time) error) *ActiveToucher {
	return &ActiveToucher{
		seen:  make(map[string]time.Time),
		touch: touch,
		log:   logger.Module("activetouch"),
	}
}

// Touch throttles per authzID at ActiveTouchThrottle. Within the window it is
// a pure in-memory check-and-return (no I/O at all); once the window has
// elapsed it records the new timestamp synchronously (so concurrent callers
// don't double-fire) and spawns a detached goroutine to perform the actual
// write, bounded by activeTouchTimeout. Failures are logged only — this must
// never affect the caller's request.
func (a *ActiveToucher) Touch(authzID string) {
	if a == nil || authzID == "" || a.touch == nil {
		return
	}

	now := time.Now()
	a.mu.Lock()
	if last, ok := a.seen[authzID]; ok && now.Sub(last) < ActiveTouchThrottle {
		a.mu.Unlock()
		return
	}
	a.seen[authzID] = now
	a.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), activeTouchTimeout)
		defer cancel()
		if err := a.touch(ctx, authzID, now); err != nil {
			a.log.Warn("touch last_active_at failed",
				zap.String("authz_id", authzID), zap.Error(err))
		}
	}()
}
