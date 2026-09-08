package repo

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/testutil"
)

// seedVersionedFile creates a file row (head=latest, pin=shared; serving cache
// left on v1's fields) plus version rows v1..latest.
func seedVersionedFile(t *testing.T, db *gorm.DB, nano string, latest, shared int) {
	t.Helper()
	never := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	f := &model.File{NanoID: nano, Title: "t1", ObjectKey: fmt.Sprintf("2026/07/%s-1.html", nano),
		SizeBytes: 1, LatestVersion: latest, SharedVersion: shared,
		ExpiresAt: never, CreateUser: "u_a", UpdateUser: "u_a"}
	if err := db.Create(f).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	vr := NewFileVersionRepo(db)
	for i := 1; i <= latest; i++ {
		h := fmt.Sprintf("%064d", i)
		v := &model.FileVersion{NanoID: nano, Version: i,
			ObjectKey: fmt.Sprintf("2026/07/%s-%d.html", nano, i), SizeBytes: int64(i),
			ContentHash: h, Title: fmt.Sprintf("t%d", i), CreateUser: "u_a"}
		if err := db.Transaction(func(tx *gorm.DB) error { return vr.Insert(tx, v) }); err != nil {
			t.Fatalf("seed version %d: %v", i, err)
		}
	}
}

func TestFileVersionRepoReadPaths(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedVersionedFile(t, db, "ver00001", 3, 0)
	r := NewFileVersionRepo(db)

	vs, err := r.ListByNanoID(context.Background(), "ver00001")
	if err != nil {
		t.Fatalf("ListByNanoID: %v", err)
	}
	if len(vs) != 3 || vs[0].Version != 3 || vs[2].Version != 1 {
		t.Fatalf("ListByNanoID = %+v, want v3..v1 descending", vs)
	}

	v2, err := r.GetByVersion(context.Background(), "ver00001", 2)
	if err != nil || v2.ObjectKey != "2026/07/ver00001-2.html" {
		t.Fatalf("GetByVersion(2) = %+v, %v", v2, err)
	}
	if _, err := r.GetByVersion(context.Background(), "ver00001", 9); err != gorm.ErrRecordNotFound {
		t.Fatalf("GetByVersion(miss) err = %v, want ErrRecordNotFound", err)
	}

	latest, err := r.GetLatest(context.Background(), "ver00001")
	if err != nil || latest.Version != 3 {
		t.Fatalf("GetLatest = %+v, %v, want v3", latest, err)
	}
}

func TestFileVersionUniqueNanoVersion(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedVersionedFile(t, db, "ver00002", 1, 0)
	r := NewFileVersionRepo(db)
	dup := &model.FileVersion{NanoID: "ver00002", Version: 1, ObjectKey: "x", CreateUser: "u_a"}
	if err := db.Transaction(func(tx *gorm.DB) error { return r.Insert(tx, dup) }); err == nil {
		t.Fatal("duplicate (nano_id, version) insert must fail on uk_nano_version")
	}
}

func TestAdvanceHeadCASAndServingCache(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedVersionedFile(t, db, "cas00001", 1, 0)
	files := NewFileRepo(db)
	h := fmt.Sprintf("%064d", 2)
	v2 := &model.FileVersion{NanoID: "cas00001", Version: 2,
		ObjectKey: "2026/07/cas00001-2.html", SizeBytes: 2, ContentHash: h, Title: "t2", CreateUser: "u_a"}

	var n int64
	err := db.Transaction(func(tx *gorm.DB) error {
		var e error
		n, e = files.AdvanceHead(tx, "cas00001", 2, v2, "u_a")
		return e
	})
	if err != nil || n != 1 {
		t.Fatalf("AdvanceHead = (%d, %v), want (1, nil)", n, err)
	}
	var f model.File
	if err := db.Where("nano_id = ?", "cas00001").First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	if f.LatestVersion != 2 || f.ObjectKey != v2.ObjectKey || f.SizeBytes != 2 || f.Title != "t2" {
		t.Fatalf("head/serving cache not updated: %+v", f)
	}

	// Stale prev (latest already 2, WHERE latest_version=1 misses) → 0 rows, no error.
	err = db.Transaction(func(tx *gorm.DB) error {
		var e error
		n, e = files.AdvanceHead(tx, "cas00001", 2, v2, "u_a")
		return e
	})
	if err != nil || n != 0 {
		t.Fatalf("stale AdvanceHead = (%d, %v), want (0, nil)", n, err)
	}
}

func TestAdvanceHeadPinnedKeepsServingCache(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedVersionedFile(t, db, "cas00002", 1, 1) // pinned to v1
	files := NewFileRepo(db)
	h := fmt.Sprintf("%064d", 2)
	v2 := &model.FileVersion{NanoID: "cas00002", Version: 2,
		ObjectKey: "2026/07/cas00002-2.html", SizeBytes: 2, ContentHash: h, Title: "t2", CreateUser: "u_a"}

	var n int64
	err := db.Transaction(func(tx *gorm.DB) error {
		var e error
		n, e = files.AdvanceHead(tx, "cas00002", 2, v2, "u_a")
		return e
	})
	if err != nil || n != 1 {
		t.Fatalf("AdvanceHead = (%d, %v), want (1, nil)", n, err)
	}
	var f model.File
	if err := db.Where("nano_id = ?", "cas00002").First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	if f.LatestVersion != 2 {
		t.Fatalf("latest_version = %d, want 2", f.LatestVersion)
	}
	if f.ObjectKey != "2026/07/cas00002-1.html" || f.Title != "t1" || f.SizeBytes != 1 {
		t.Fatalf("pinned serving cache must not move: %+v", f)
	}
}

func TestSetSharedVersionRefreshesCache(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedVersionedFile(t, db, "pin00001", 2, 0)
	files := NewFileRepo(db)
	vr := NewFileVersionRepo(db)
	v2, err := vr.GetByVersion(context.Background(), "pin00001", 2)
	if err != nil {
		t.Fatalf("GetByVersion(2): %v", err)
	}

	// Pin to v2: pointer + cache move together.
	if err := db.Transaction(func(tx *gorm.DB) error {
		return files.SetSharedVersion(tx, "pin00001", 2, v2, "u_a")
	}); err != nil {
		t.Fatalf("SetSharedVersion(2): %v", err)
	}
	var f model.File
	if err := db.Where("nano_id = ?", "pin00001").First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	if f.SharedVersion != 2 || f.ObjectKey != v2.ObjectKey || f.Title != "t2" || f.SizeBytes != 2 {
		t.Fatalf("pin did not refresh cache: %+v", f)
	}

	// Unpin (0 = follow latest): caller resolves latest row (v2 here) and passes it.
	if err := db.Transaction(func(tx *gorm.DB) error {
		return files.SetSharedVersion(tx, "pin00001", 0, v2, "u_a")
	}); err != nil {
		t.Fatalf("SetSharedVersion(0): %v", err)
	}
	if err := db.Where("nano_id = ?", "pin00001").First(&f).Error; err != nil {
		t.Fatalf("re-read file: %v", err)
	}
	if f.SharedVersion != 0 || f.ObjectKey != v2.ObjectKey {
		t.Fatalf("unpin wrong: %+v", f)
	}
}

func TestInsertBatchIdempotent(t *testing.T) {
	db := testutil.OpenTestDB(t)
	r := NewPendingObjectDeleteRepo(db)
	keys := []string{"2026/07/bat-1.html", "2026/07/bat-2.html", "2026/07/bat-3.html"}

	if err := db.Transaction(func(tx *gorm.DB) error {
		return r.InsertBatch(tx, keys, model.ReasonExpired, "system:cron")
	}); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}
	// Overlapping re-insert is a silent no-op (uk_object_key).
	if err := db.Transaction(func(tx *gorm.DB) error {
		return r.InsertBatch(tx, keys[:2], model.ReasonUserDelete, "u_a")
	}); err != nil {
		t.Fatalf("InsertBatch re-run: %v", err)
	}
	var n int64
	db.Model(&model.PendingObjectDelete{}).Count(&n)
	if n != 3 {
		t.Fatalf("rows = %d, want 3", n)
	}
	// Empty key set is a no-op, not an SQL error.
	if err := db.Transaction(func(tx *gorm.DB) error {
		return r.InsertBatch(tx, nil, model.ReasonExpired, "system:cron")
	}); err != nil {
		t.Fatalf("InsertBatch(nil): %v", err)
	}
}
