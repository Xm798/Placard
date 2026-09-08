package devicecode

import (
	"context"
	"testing"
	"time"

	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/testutil"
)

// Redis drops an expired key on its own; the SQL store leaves the row for the
// cleanup cron, so the sweep has to actually reclaim aged-out flows and
// challenges while leaving live ones alone.
func TestSQLPurgeExpired(t *testing.T) {
	db := testutil.OpenTestDB(t)
	clock := &testClock{t: time.Now().UTC()}
	s := NewSQLStoreWithClock(db, clock.Now)
	ctx := context.Background()

	if _, err := s.Create(ctx, NewFlow{NameHint: "old"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.PutChallenge(ctx, "sid-old", "tok-old"); err != nil {
		t.Fatalf("PutChallenge: %v", err)
	}

	clock.advance(TTL + time.Second)
	live, err := s.Create(ctx, NewFlow{NameHint: "live"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.PutChallenge(ctx, "sid-live", "tok-live"); err != nil {
		t.Fatalf("PutChallenge: %v", err)
	}

	n, err := s.PurgeExpired(ctx, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("purged %d rows, want the aged-out flow and its challenge", n)
	}

	if _, _, err := s.Poll(ctx, live.DeviceCode); err != nil {
		t.Fatalf("live flow must survive the purge: %v", err)
	}
	if ok, err := s.ConsumeChallenge(ctx, "sid-live", "tok-live"); err != nil || !ok {
		t.Fatalf("live challenge must survive the purge: ok=%v err=%v", ok, err)
	}
	if n := count(t, s, &model.DeviceCode{}); n != 1 {
		t.Fatalf("device_code rows = %d, want 1", n)
	}
}

func count(t *testing.T, s *SQLStore, m interface{}) int64 {
	t.Helper()
	var n int64
	if err := s.db.Model(m).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}
