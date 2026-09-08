package repo

import (
	"context"
	"testing"
	"time"

	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/testutil"
)

func TestListOwnedIsolationAndOrder(t *testing.T) {
	db := testutil.OpenTestDB(t)
	r := NewFileRepo(db)
	never := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

	// alice: 2 files, bob: 1 file, plus one soft-deleted alice file.
	mk := func(nano, user string, deleted int8) {
		f := &model.File{NanoID: nano, Title: nano, ObjectKey: nano + "-1.html",
			ExpiresAt: never, CreateUser: user, UpdateUser: user, IsDeleted: deleted}
		if err := db.Create(f).Error; err != nil {
			t.Fatalf("seed %s: %v", nano, err)
		}
	}
	mk("a1aaaaaa", "u_alice", 0)
	time.Sleep(10 * time.Millisecond) // distinct create_time; id DESC also tiebreaks
	mk("a2aaaaaa", "u_alice", 0)
	mk("b1bbbbbb", "u_bob", 0)
	mk("a3aaaaaa", "u_alice", 1) // soft-deleted, must be excluded

	got, err := r.ListOwned(context.Background(), "u_alice", 0, 20)
	if err != nil {
		t.Fatalf("ListOwned: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 alice files, got %d", len(got))
	}
	// create_time DESC: newest (a2) first.
	if got[0].NanoID != "a2aaaaaa" || got[1].NanoID != "a1aaaaaa" {
		t.Errorf("order wrong: %s, %s", got[0].NanoID, got[1].NanoID)
	}

	n, err := r.CountOwned(context.Background(), "u_alice")
	if err != nil {
		t.Fatalf("CountOwned: %v", err)
	}
	if n != 2 {
		t.Errorf("CountOwned = %d, want 2", n)
	}

	// Pagination: page_size 1, offset 1 → second item (a1).
	page2, err := r.ListOwned(context.Background(), "u_alice", 1, 1)
	if err != nil {
		t.Fatalf("ListOwned page2: %v", err)
	}
	if len(page2) != 1 || page2[0].NanoID != "a1aaaaaa" {
		t.Errorf("pagination wrong: %+v", page2)
	}
}
