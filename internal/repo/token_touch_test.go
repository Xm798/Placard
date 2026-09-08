package repo

import (
	"context"
	"testing"
	"time"

	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/testutil"
)

// TestTouchLastUsedUpdatesOnlyLastUsed asserts TouchLastUsed stamps
// last_used_at and does NOT bump the autoUpdateTime update_time column.
func TestTouchLastUsedUpdatesOnlyLastUsed(t *testing.T) {
	db := testutil.OpenTestDB(t)
	r := NewTokenRepo(db)

	tok := &model.Token{
		TokenHash:  "hash-touch-1",
		UserID:     "u_alice",
		CreateUser: "u_alice",
	}
	if err := r.Insert(context.Background(), tok); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Pin update_time / last_used_at to known values, bypassing GORM tracking.
	past := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	sentinel := time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := db.Exec("UPDATE token SET update_time = ?, last_used_at = ? WHERE id = ?",
		past, sentinel, tok.ID).Error; err != nil {
		t.Fatalf("pin columns: %v", err)
	}

	at := time.Now().UTC().Truncate(time.Second)
	if err := r.TouchLastUsed(context.Background(), tok.ID, at); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}

	var got model.Token
	if err := db.First(&got, tok.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.LastUsedAt.Unix() != at.Unix() {
		t.Errorf("last_used_at = %v, want %v", got.LastUsedAt, at)
	}
	if got.UpdateTime.Unix() != past.Unix() {
		t.Errorf("update_time bumped to %v, must stay %v (UpdateColumn must not track update time)",
			got.UpdateTime, past)
	}
}
