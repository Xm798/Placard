package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/testutil"
)

// The production defaults, spelled out because the two-tier expiry below is
// only meaningful in terms of them: a week of inactivity ends a session, and a
// month ends it however active it was.
const (
	testIdleTTL     = 168 * time.Hour
	testAbsoluteTTL = 720 * time.Hour
)

// testClock stands in for miniredis.FastForward: the SQL store reads wall time
// rather than letting a server expire keys for it, so the tests move the clock
// instead of the data.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Now().UTC()} }

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

func newSQLTestStore(t *testing.T) (*SQLStore, *gorm.DB, *testClock) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	clock := newTestClock()
	return NewSQLStoreWithClock(db, testIdleTTL, testAbsoluteTTL, clock.Now), db, clock
}

// create mints a session whose CreatedAt is the clock's current instant, which
// is what the auth handler does.
func (c *testClock) create(t *testing.T, s *SQLStore, authzID string) string {
	t.Helper()
	id, err := s.Create(context.Background(), Data{AuthzID: authzID, DisplayName: "沈", CreatedAt: c.Now()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return id
}

func TestSQLCreateGetDelete(t *testing.T) {
	s, _, clock := newSQLTestStore(t)
	ctx := context.Background()

	id := clock.create(t, s, "on_abc")
	if len(id) < 40 { // 32 random bytes base64url ≈ 43 chars
		t.Fatalf("session id too short: %q", id)
	}

	d, err := s.Get(ctx, id)
	if err != nil || d.AuthzID != "on_abc" || d.DisplayName != "沈" {
		t.Fatalf("get: %+v err=%v", d, err)
	}

	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, id); err != ErrNotFound {
		t.Fatalf("after delete want ErrNotFound, got %v", err)
	}
}

func TestSQLGetUnknownIsNotFound(t *testing.T) {
	s, _, _ := newSQLTestStore(t)
	if _, err := s.Get(context.Background(), "nope"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// A session untouched for the full idle window is gone, and the row goes with
// it rather than waiting for the cleanup cron.
func TestSQLIdleExpiry(t *testing.T) {
	s, db, clock := newSQLTestStore(t)
	ctx := context.Background()
	id := clock.create(t, s, "u")

	clock.advance(testIdleTTL + time.Minute)
	if _, err := s.Get(ctx, id); err != ErrNotFound {
		t.Fatalf("want ErrNotFound after %v idle, got %v", testIdleTTL, err)
	}
	if n := countSessions(t, db); n != 0 {
		t.Fatalf("expired row must be dropped on the read that finds it, %d left", n)
	}
}

// The idle window slides: reading before it lapses buys another full window,
// which is what keeps a daily user logged in for the whole month.
func TestSQLIdleWindowSlides(t *testing.T) {
	s, _, clock := newSQLTestStore(t)
	ctx := context.Background()
	id := clock.create(t, s, "u")

	for elapsed := time.Duration(0); elapsed < testAbsoluteTTL-24*time.Hour; elapsed += 24 * time.Hour {
		clock.advance(24 * time.Hour)
		if _, err := s.Get(ctx, id); err != nil {
			t.Fatalf("daily use at +%v: %v", elapsed+24*time.Hour, err)
		}
	}
}

// Renewal only fires once the remaining window dropped below idle/2, so an
// active session costs one UPDATE per half-window rather than one per request.
func TestSQLSlidingRenewalThreshold(t *testing.T) {
	s, db, clock := newSQLTestStore(t)
	ctx := context.Background()
	id := clock.create(t, s, "u")
	created := expiresAt(t, db, id)

	// Just under half the window elapsed: more than idle/2 remains → no write.
	clock.advance(testIdleTTL/2 - time.Hour)
	if _, err := s.Get(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := expiresAt(t, db, id); !got.Equal(created) {
		t.Fatalf("expires_at should be untouched, %v → %v", created, got)
	}

	// Past half: renewed to a full window from now.
	clock.advance(2 * time.Hour)
	if _, err := s.Get(ctx, id); err != nil {
		t.Fatal(err)
	}
	want := model.Timestamp(clock.Now().Add(testIdleTTL))
	if got := expiresAt(t, db, id); !got.Equal(want) {
		t.Fatalf("expires_at = %v, want renewed to %v", got, want)
	}
}

// The absolute cap outranks the sliding window: a session in constant use still
// ends 720h after it was created.
func TestSQLAbsoluteCap(t *testing.T) {
	s, db, clock := newSQLTestStore(t)
	ctx := context.Background()
	id := clock.create(t, s, "u")

	// Keep it alive across the whole month, one read per day.
	for elapsed := time.Duration(0); elapsed < testAbsoluteTTL-24*time.Hour; elapsed += 24 * time.Hour {
		clock.advance(24 * time.Hour)
		if _, err := s.Get(ctx, id); err != nil {
			t.Fatalf("daily use at +%v: %v", elapsed+24*time.Hour, err)
		}
	}

	clock.advance(24*time.Hour + time.Minute)
	if _, err := s.Get(ctx, id); err != ErrNotFound {
		t.Fatalf("want ErrNotFound past the absolute cap, got %v", err)
	}
	if n := countSessions(t, db); n != 0 {
		t.Fatalf("capped row must be dropped on read, %d left", n)
	}
}

// Update rewrites Data and leaves the idle deadline where it was — a profile
// refresh must not re-extend (or shrink) the expiry clock.
func TestSQLUpdateKeepsExpiry(t *testing.T) {
	s, db, clock := newSQLTestStore(t)
	ctx := context.Background()
	id := clock.create(t, s, "u1")
	before := expiresAt(t, db, id)

	clock.advance(time.Hour)
	if err := s.Update(ctx, id, Data{AuthzID: "u1", DisplayName: "renamed", CreatedAt: clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := expiresAt(t, db, id); !got.Equal(before) {
		t.Fatalf("Update must not change expires_at, %v → %v", before, got)
	}

	d, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.DisplayName != "renamed" {
		t.Fatalf("Update did not persist new fields: %+v", d)
	}
}

// Update against an unknown or expired id must report ErrNotFound rather than
// insert a row — it must never be the point that mints a session.
func TestSQLUpdateUnknownIsNotFound(t *testing.T) {
	s, db, clock := newSQLTestStore(t)
	ctx := context.Background()

	if err := s.Update(ctx, "nope", Data{AuthzID: "x"}); err != ErrNotFound {
		t.Fatalf("unknown id: want ErrNotFound, got %v", err)
	}

	id := clock.create(t, s, "u")
	clock.advance(testIdleTTL + time.Minute)
	if err := s.Update(ctx, id, Data{AuthzID: "u", DisplayName: "renamed"}); err != ErrNotFound {
		t.Fatalf("expired id: want ErrNotFound, got %v", err)
	}
	if n := countSessions(t, db); n != 1 {
		t.Fatalf("Update must neither insert nor delete, %d rows", n)
	}
}

// PurgeExpired is space reclamation for sessions nobody comes back for; it must
// leave live ones alone.
func TestSQLPurgeExpired(t *testing.T) {
	s, _, clock := newSQLTestStore(t)
	ctx := context.Background()

	clock.create(t, s, "idle") // never read again: dies of the idle window
	capped := clock.create(t, s, "capped")

	// Read `capped` daily right up to the cap, then stop: its idle deadline is
	// still in the future, so only the absolute clause can reclaim it.
	for elapsed := time.Duration(0); elapsed < testAbsoluteTTL-24*time.Hour; elapsed += 24 * time.Hour {
		clock.advance(24 * time.Hour)
		if _, err := s.Get(ctx, capped); err != nil {
			t.Fatalf("daily use at +%v: %v", elapsed+24*time.Hour, err)
		}
	}
	clock.advance(48 * time.Hour)

	n, err := s.PurgeExpired(ctx, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("purged %d rows, want both the idle-expired and the capped one", n)
	}

	live := clock.create(t, s, "live")
	if n, err := s.PurgeExpired(ctx, clock.Now()); err != nil || n != 0 {
		t.Fatalf("purge of a live-only table: n=%d err=%v", n, err)
	}
	if _, err := s.Get(ctx, live); err != nil {
		t.Fatalf("live session must survive the purge: %v", err)
	}
}

func expiresAt(t *testing.T, db *gorm.DB, id string) time.Time {
	t.Helper()
	var row model.Session
	if err := db.Where("id = ?", id).Take(&row).Error; err != nil {
		t.Fatalf("read session %q: %v", id, err)
	}
	return row.ExpiresAt
}

func countSessions(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.Session{}).Count(&n).Error; err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return n
}

// A pending OIDC flow survives a create/get round trip and is cleared by an
// update that carries none — the callback's single-use consumption depends on
// both halves.
func TestSQLOIDCFlowRoundTrip(t *testing.T) {
	s, _, clock := newSQLTestStore(t)
	ctx := context.Background()

	flow := &OIDCFlow{Provider: "corp", State: "st", Nonce: "nc", Redirect: "/settings"}
	id, err := s.Create(ctx, Data{CreatedAt: clock.Now(), OIDC: flow})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	d, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if d.AuthzID != "" {
		t.Errorf("authz_id = %q, want empty on a pending session", d.AuthzID)
	}
	if d.OIDC == nil || *d.OIDC != *flow {
		t.Fatalf("flow = %+v, want %+v", d.OIDC, flow)
	}

	d.OIDC = nil
	if err := s.Update(ctx, id, d); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if after.OIDC != nil {
		t.Errorf("flow = %+v after clearing, want nil", after.OIDC)
	}
}
