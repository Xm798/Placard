package cleanup

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/devicecode"
	"github.com/Xm798/placard/internal/lock"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/session"
	"github.com/Xm798/placard/internal/testutil"
)

// backdated is how the SQL stores are made to write rows that are already past
// their expiry: they stamp expires_at off this clock, while step4 sweeps
// against wall time.
func backdated(d time.Duration) func() time.Time {
	t := time.Now().UTC().Add(-d)
	return func() time.Time { return t }
}

// The zero-dependency deployment gets the in-process lock and the SQL stores,
// so one round has to do all four steps with no Redis anywhere: reclaim an
// expired file (step1), drain its objects (step2), recompute the view cache
// (step3), and sweep the expired session and device flow (step4).
func TestRoundWithoutRedis(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ctx := context.Background()
	clock := backdated(3 * time.Hour)

	sessions := session.NewSQLStoreWithClock(db, time.Hour, 2*time.Hour, clock)
	deviceCodes := devicecode.NewSQLStoreWithClock(db, clock)

	expiredSession, err := sessions.Create(ctx, session.Data{AuthzID: "u_a", CreatedAt: clock()})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := deviceCodes.Create(ctx, devicecode.NewFlow{NameHint: "mac"}); err != nil {
		t.Fatalf("seed device flow: %v", err)
	}

	mustCreate(t, db, &model.File{
		NanoID: "norediss", ObjectKey: "2026/06/norediss-1.html", LatestVersion: 1,
		ExpiresAt: model.Timestamp(time.Now().Add(-time.Hour)), CreateUser: "u_a", UpdateUser: "u_a",
	})
	if err := db.Create(&model.FileVersion{
		NanoID: "norediss", Version: 1, ObjectKey: "2026/06/norediss-1.html", SizeBytes: 1,
		Title: "t", CreateUser: "u_a",
	}).Error; err != nil {
		t.Fatalf("seed version: %v", err)
	}

	objectStore := &recordingStorage{}
	d := newDeps(db, objectStore)
	d.Locker = lock.NewMemoryLocker()
	d.Purgers = []Purger{sessions, deviceCodes}

	Run(ctx, d, 1, zap.NewNop())

	var f model.File
	if err := db.Where("nano_id = ?", "norediss").First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	if f.IsDeleted != 1 {
		t.Fatalf("step1: is_deleted = %d, want 1", f.IsDeleted)
	}
	if len(objectStore.deleted) != 1 || objectStore.deleted[0] != "2026/06/norediss-1.html" {
		t.Fatalf("step2: storage deletes = %v", objectStore.deleted)
	}
	if n := countRows(t, db, &model.Session{}); n != 0 {
		t.Fatalf("step4: session rows = %d, want 0", n)
	}
	if n := countRows(t, db, &model.DeviceCode{}); n != 0 {
		t.Fatalf("step4: device_code rows = %d, want 0", n)
	}
	if _, err := sessions.Get(ctx, expiredSession); err != session.ErrNotFound {
		t.Fatalf("purged session = %v, want ErrNotFound", err)
	}

	// The lock was released, so the next round runs rather than skipping.
	Run(ctx, d, 2, zap.NewNop())
}

// A round that cannot take the lock does nothing at all — the property that
// keeps two overlapping rounds from reclaiming the same objects twice.
func TestRoundSkipsWhenLockHeld(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ctx := context.Background()

	mustCreate(t, db, &model.File{
		NanoID: "lockheld", ObjectKey: "2026/06/lockheld-1.html", LatestVersion: 1,
		ExpiresAt: model.Timestamp(time.Now().Add(-time.Hour)), CreateUser: "u_a", UpdateUser: "u_a",
	})

	locker := lock.NewMemoryLocker()
	if _, err := locker.Acquire(ctx, lockKey, time.Minute); err != nil {
		t.Fatalf("pre-acquire: %v", err)
	}

	d := newDeps(db, &recordingStorage{})
	d.Locker = locker
	Run(ctx, d, 1, zap.NewNop())

	var f model.File
	if err := db.Where("nano_id = ?", "lockheld").First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	if f.IsDeleted != 0 {
		t.Fatal("a round that lost the lock must not touch anything")
	}
}

func countRows(t *testing.T, db *gorm.DB, m interface{}) int64 {
	t.Helper()
	var n int64
	if err := db.Model(m).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}
