package lock

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryLockerExcludesWhileHeld(t *testing.T) {
	l := NewMemoryLocker()
	ctx := context.Background()

	release, err := l.Acquire(ctx, "k", time.Minute)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if _, err := l.Acquire(ctx, "k", time.Minute); !errors.Is(err, ErrNotAcquired) {
		t.Fatalf("Acquire while held = %v, want ErrNotAcquired", err)
	}
	if _, err := l.Acquire(ctx, "other", time.Minute); err != nil {
		t.Fatalf("a different key must be free: %v", err)
	}

	release(ctx)
	if _, err := l.Acquire(ctx, "k", time.Minute); err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
}

// The TTL is the backstop for a holder that never releases — a round that
// panicked, or a process that was killed mid-cleanup.
func TestMemoryLockerTTLReleases(t *testing.T) {
	l := NewMemoryLocker()
	now := time.Now()
	l.now = func() time.Time { return now }
	ctx := context.Background()

	if _, err := l.Acquire(ctx, "k", time.Minute); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Second)
	if _, err := l.Acquire(ctx, "k", time.Minute); !errors.Is(err, ErrNotAcquired) {
		t.Fatalf("Acquire inside the TTL = %v, want ErrNotAcquired", err)
	}
	now = now.Add(31 * time.Second)
	if _, err := l.Acquire(ctx, "k", time.Minute); err != nil {
		t.Fatalf("Acquire past the TTL: %v", err)
	}
}

// A holder whose TTL lapsed must not delete the lock its successor now holds —
// the mis-release RedisLocker guards with a Lua CAS.
func TestMemoryLockerReleaseDoesNotStealSuccessor(t *testing.T) {
	l := NewMemoryLocker()
	now := time.Now()
	l.now = func() time.Time { return now }
	ctx := context.Background()

	stale, err := l.Acquire(ctx, "k", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := l.Acquire(ctx, "k", time.Minute); err != nil {
		t.Fatalf("successor Acquire: %v", err)
	}

	stale(ctx) // the slow predecessor finally finishes
	if _, err := l.Acquire(ctx, "k", time.Minute); !errors.Is(err, ErrNotAcquired) {
		t.Fatalf("successor's lock was released by its predecessor: %v", err)
	}
}

func TestMemoryLockerReleaseIsIdempotent(t *testing.T) {
	l := NewMemoryLocker()
	ctx := context.Background()

	release, err := l.Acquire(ctx, "k", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	release(ctx)

	other, err := l.Acquire(ctx, "k", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	release(ctx) // second call on the first holder's closure

	if _, err := l.Acquire(ctx, "k", time.Minute); !errors.Is(err, ErrNotAcquired) {
		t.Fatalf("a repeated release freed someone else's lock: %v", err)
	}
	other(ctx)
}
