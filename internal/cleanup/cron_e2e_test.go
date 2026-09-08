// End-to-end cron tests against a real database — in-memory SQLite by default,
// and the compose Postgres under -tags=integration, so the behaviors that only
// a real engine can show (clause.OnConflict upsert compilation, 9999 sentinel
// stability across the driver round-trip) are proved on both dialects.
package cleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/repo"
	"github.com/Xm798/placard/internal/storage"
	"github.com/Xm798/placard/internal/testutil"
)

// neverSentinel is the 9999 "never expires" marker (mirrors
// handler.neverSentinel; redeclared here to avoid importing handler in a test).
var neverSentinel = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

// fakeLocker always grants the lock — these tests isolate database behavior,
// not the distributed mutex (lock has its own contract).
type fakeLocker struct{}

func (fakeLocker) Acquire(_ context.Context, _ string, _ time.Duration) (func(context.Context), error) {
	return func(context.Context) {}, nil
}

// failingStorage fails DeleteObject a fixed number of times, then succeeds — drives
// the step2 retry/close loop.
type failingStorage struct {
	storage.Client
	failsLeft int
}

func (f *failingStorage) DeleteObject(context.Context, string) error {
	if f.failsLeft > 0 {
		f.failsLeft--
		return errors.New("simulated storage failure")
	}
	return nil
}

type recordingStorage struct {
	storage.Client
	deleted []string
}

func (f *recordingStorage) DeleteObject(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

func newDeps(db *gorm.DB, objectStore storage.Client) Deps {
	return Deps{
		DB:       db,
		Storage:  objectStore,
		Locker:   fakeLocker{},
		Files:    repo.NewFileRepo(db),
		Views:    repo.NewViewRepo(db),
		Pending:  repo.NewPendingObjectDeleteRepo(db),
		Versions: repo.NewFileVersionRepo(db),
		Audit:    repo.NewAuditRepo(db),
		Cfg:      Config{RetryMax: 5, ViewRecomputeEvery: 1},
	}
}

// TestUpsertOnConflict: two Upserts of the same (file,viewer) must increment
// view_count to 2 — proving clause.OnConflict compiles to the dialect's upsert
// and the uk_file_viewer composite key is matched.
func TestUpsertOnConflict(t *testing.T) {
	db := testutil.OpenTestDB(t)
	files := repo.NewFileRepo(db)
	views := repo.NewViewRepo(db)

	if err := files.DB().Create(&model.File{
		NanoID: "dup00001", Title: "t", ObjectKey: "k", ExpiresAt: neverSentinel,
		CreateUser: "u_a", UpdateUser: "u_a",
	}).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := views.Upsert(context.Background(), "dup00001", "u_viewer", "Viewer"); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	var v model.View
	if err := db.Where("file_nano_id = ? AND viewer = ?", "dup00001", "u_viewer").First(&v).Error; err != nil {
		t.Fatalf("read view: %v", err)
	}
	if v.ViewCount != 2 {
		t.Fatalf("view_count = %d, want 2 (the upsert did not update the conflicting row)", v.ViewCount)
	}
}

// TestSentinelNotExpired: a 9999 file is never returned by ListExpired; an
// already-past file is. Proves the sentinel survives the driver round-trip.
func TestSentinelNotExpired(t *testing.T) {
	db := testutil.OpenTestDB(t)
	files := repo.NewFileRepo(db)

	mustCreate(t, db, &model.File{
		NanoID: "perm0001", ObjectKey: "k1", ExpiresAt: neverSentinel,
		CreateUser: "u_a", UpdateUser: "u_a",
	})
	mustCreate(t, db, &model.File{
		NanoID: "expd0001", ObjectKey: "k2", ExpiresAt: model.Timestamp(time.Now().Add(-time.Hour)),
		CreateUser: "u_a", UpdateUser: "u_a",
	})

	got, err := files.ListExpired(context.Background(), time.Now(), 100)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}
	if len(got) != 1 || got[0].NanoID != "expd0001" {
		t.Fatalf("ListExpired = %+v, want only expd0001 (sentinel must not match)", nanoIDs(got))
	}
}

// TestStep1EndToEnd: an expired multi-version file is reclaimed through the
// queue — is_deleted=1, view rows gone, a system:cron file.delete audit row,
// BOTH version objects deleted by step2 in the same round, queue drained.
func TestStep1EndToEnd(t *testing.T) {
	db := testutil.OpenTestDB(t)
	views := repo.NewViewRepo(db)

	mustCreate(t, db, &model.File{
		NanoID: "exp00001", ObjectKey: "2026/06/exp00001-2.html", LatestVersion: 2,
		ExpiresAt: model.Timestamp(time.Now().Add(-time.Hour)), CreateUser: "u_a", UpdateUser: "u_a",
	})
	for i, key := range []string{"2026/06/exp00001-1.html", "2026/06/exp00001-2.html"} {
		if err := db.Create(&model.FileVersion{
			NanoID: "exp00001", Version: i + 1, ObjectKey: key, SizeBytes: 1,
			Title: "t", CreateUser: "u_a",
		}).Error; err != nil {
			t.Fatalf("seed version %d: %v", i+1, err)
		}
	}
	if err := views.Upsert(context.Background(), "exp00001", "u_viewer", "Viewer"); err != nil {
		t.Fatalf("seed view: %v", err)
	}

	objectStore := &recordingStorage{}
	d := newDeps(db, objectStore)
	Run(context.Background(), d, 1, zap.NewNop())

	var f model.File
	if err := db.Where("nano_id = ?", "exp00001").First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	if f.IsDeleted != 1 {
		t.Fatalf("is_deleted = %d, want 1", f.IsDeleted)
	}
	var viewCount int64
	db.Model(&model.View{}).Where("file_nano_id = ?", "exp00001").Count(&viewCount)
	if viewCount != 0 {
		t.Fatalf("view rows = %d, want 0 (DeleteByFile not run)", viewCount)
	}
	var audit model.AuditLog
	if err := db.Where("action = ? AND file_nano_id = ?", "file.delete", "exp00001").First(&audit).Error; err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if audit.Actor != cronActor {
		t.Fatalf("audit actor = %q, want %q", audit.Actor, cronActor)
	}

	// Both version objects reclaimed via the queue (step1 enqueue → step2 delete).
	sort.Strings(objectStore.deleted)
	want := []string{"2026/06/exp00001-1.html", "2026/06/exp00001-2.html"}
	if !reflect.DeepEqual(objectStore.deleted, want) {
		t.Fatalf("storage deletes = %v, want %v", objectStore.deleted, want)
	}
	var pn int64
	db.Model(&model.PendingObjectDelete{}).Count(&pn)
	if pn != 0 {
		t.Fatalf("pending rows = %d, want 0 (queue not drained)", pn)
	}
}

// TestStep2RetryLoop: a failing storage backend bumps retry_count and leaves the row; once
// it succeeds, the row is deleted.
func TestStep2RetryLoop(t *testing.T) {
	db := testutil.OpenTestDB(t)
	pending := repo.NewPendingObjectDeleteRepo(db)

	if err := pending.Insert(context.Background(), "2026/06/orphan-1.html", "put_replace", "u_a"); err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	// Round 1: storage fails once → retry_count incremented, row retained.
	d := newDeps(db, &failingStorage{failsLeft: 1})
	step2ReconcilePending(context.Background(), d, zap.NewNop())

	var row model.PendingObjectDelete
	if err := db.Where("object_key = ?", "2026/06/orphan-1.html").First(&row).Error; err != nil {
		t.Fatalf("row should still exist after failure: %v", err)
	}
	if row.RetryCount != 1 {
		t.Fatalf("retry_count = %d, want 1", row.RetryCount)
	}

	// Round 2: same fake now succeeds (failsLeft already drained) → row deleted.
	step2ReconcilePending(context.Background(), d, zap.NewNop())
	var n int64
	db.Model(&model.PendingObjectDelete{}).Where("object_key = ?", "2026/06/orphan-1.html").Count(&n)
	if n != 0 {
		t.Fatalf("pending rows = %d, want 0 (row not closed on success)", n)
	}
}

// TestStep2DeletesLocalObjects runs the reclaim round against the on-disk
// backend: the queued key must leave the filesystem, not just the queue table.
func TestStep2DeletesLocalObjects(t *testing.T) {
	db := testutil.OpenTestDB(t)
	pending := repo.NewPendingObjectDeleteRepo(db)

	dir := t.TempDir()
	objectStore, err := storage.NewLocalClient(config.LocalStorageConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewLocalClient: %v", err)
	}
	const key = "2026/06/reclaim-1.html"
	if err := objectStore.PutObject(context.Background(), key, strings.NewReader("<html>bye</html>"), "text/html; charset=utf-8"); err != nil {
		t.Fatalf("seed object: %v", err)
	}
	if err := pending.Insert(context.Background(), key, "expired", "u_a"); err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	step2ReconcilePending(context.Background(), newDeps(db, objectStore), zap.NewNop())

	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(key))); !os.IsNotExist(err) {
		t.Fatalf("object file still present after reclaim (stat err = %v)", err)
	}
	var n int64
	db.Model(&model.PendingObjectDelete{}).Where("object_key = ?", key).Count(&n)
	if n != 0 {
		t.Fatalf("pending rows = %d, want 0", n)
	}
}

func TestStep2RetainsRecentUserDeletes(t *testing.T) {
	db := testutil.OpenTestDB(t)
	pending := repo.NewPendingObjectDeleteRepo(db)

	for _, row := range []struct {
		key    string
		reason string
	}{
		{key: "recent-user-delete.html", reason: "user_delete"},
		{key: "old-user-delete.html", reason: "user_delete"},
		{key: "publish-orphan.html", reason: "publish_rollback"},
	} {
		if err := pending.Insert(context.Background(), row.key, row.reason, "u_a"); err != nil {
			t.Fatalf("seed %s: %v", row.key, err)
		}
	}
	if err := db.Model(&model.PendingObjectDelete{}).
		Where("object_key = ?", "old-user-delete.html").
		Update("create_time", model.Timestamp(time.Now().AddDate(0, 0, -181))).Error; err != nil {
		t.Fatalf("age user delete: %v", err)
	}

	objectStore := &recordingStorage{}
	d := newDeps(db, objectStore)
	d.Cfg.UserDeleteRetention = 180 * 24 * time.Hour
	step2ReconcilePending(context.Background(), d, zap.NewNop())

	wantDeleted := []string{"old-user-delete.html", "publish-orphan.html"}
	if len(objectStore.deleted) != len(wantDeleted) {
		t.Fatalf("deleted keys = %#v, want %#v", objectStore.deleted, wantDeleted)
	}
	for i := range wantDeleted {
		if objectStore.deleted[i] != wantDeleted[i] {
			t.Fatalf("deleted keys = %#v, want %#v", objectStore.deleted, wantDeleted)
		}
	}
	var rows []model.PendingObjectDelete
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("list remaining pending rows: %v", err)
	}
	if len(rows) != 1 || rows[0].ObjectKey != "recent-user-delete.html" {
		t.Fatalf("remaining rows = %#v, want recent user delete only", rows)
	}
}

// TestStep2RetryExhaustion: a row at RetryMax is excluded from ListPending (not
// retried) yet surfaced by CountAtRetryMax for triage (R9).
func TestStep2RetryExhaustion(t *testing.T) {
	db := testutil.OpenTestDB(t)
	pending := repo.NewPendingObjectDeleteRepo(db)

	if err := pending.Insert(context.Background(), "2026/06/poison-1.html", "put_replace", "u_a"); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	// Drive retry_count up to RetryMax (5).
	var row model.PendingObjectDelete
	if err := db.Where("object_key = ?", "2026/06/poison-1.html").First(&row).Error; err != nil {
		t.Fatalf("read seeded row: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := pending.IncrRetry(context.Background(), row.ID); err != nil {
			t.Fatalf("incr retry: %v", err)
		}
	}

	// Exhausted row must NOT be returned by ListPending (maxRetry=5).
	got, err := pending.ListPending(context.Background(), 5, time.Now(), 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListPending returned %d rows, want 0 (exhausted row must be excluded)", len(got))
	}

	// But CountAtRetryMax must surface it for the R9 alert.
	stuck, err := pending.CountAtRetryMax(context.Background(), 5)
	if err != nil {
		t.Fatalf("count at retry max: %v", err)
	}
	if stuck != 1 {
		t.Fatalf("CountAtRetryMax = %d, want 1 (R9 alert would miss the poison row)", stuck)
	}
}

func mustCreate(t *testing.T, db *gorm.DB, f *model.File) {
	t.Helper()
	if err := db.Create(f).Error; err != nil {
		t.Fatalf("create file %s: %v", f.NanoID, err)
	}
}

func nanoIDs(fs []model.File) []string {
	ids := make([]string, len(fs))
	for i, f := range fs {
		ids[i] = f.NanoID
	}
	return ids
}
