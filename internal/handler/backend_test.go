//go:build !integration

package handler

import (
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/devicecode"
	"github.com/Xm798/placard/internal/session"
)

// ephemeral is the login state the handler contract runs on: the session store
// behind the cookie channel and the device-code store behind /auth/device*.
//
// Both have two interchangeable backends, and which one a deployment gets
// depends only on whether redis.addr is set — so the whole contract has to hold
// on both. The default build wires the SQL stores (no external service, which
// is what `go test ./...` promises); -tags=integration wires the Redis ones and
// runs the same tests against miniredis. See backend_redis_test.go.
type ephemeral struct {
	sessions    session.Store
	deviceCodes devicecode.Store
	// advance reaches the far side of a TTL — a device flow's 180s window, its
	// poll gate — without the test sleeping through it.
	advance func(time.Duration)
}

// testClock drives the SQL stores, which read wall time rather than letting a
// server expire keys for them.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newEphemeral(t *testing.T, db *gorm.DB, idleTTL, absoluteTTL time.Duration) ephemeral {
	t.Helper()
	clock := &testClock{t: time.Now().UTC()}
	return ephemeral{
		sessions:    session.NewSQLStoreWithClock(db, idleTTL, absoluteTTL, clock.Now),
		deviceCodes: devicecode.NewSQLStoreWithClock(db, clock.Now),
		advance:     clock.advance,
	}
}
