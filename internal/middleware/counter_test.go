package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// eachCounter runs fn against every Counter implementation. Which one a
// deployment gets depends only on whether redis.addr is set, so the limiters
// behave identically on both — the properties below are what "identically"
// means.
func eachCounter(t *testing.T, fn func(*testing.T, Counter)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { fn(t, NewMemoryCounter()) })
	t.Run("redis", func(t *testing.T) { fn(t, newTestRedisCounter(t)) })
}

func newTestRedisCounter(t *testing.T) *RedisCounter {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewRedisCounter(rdb)
}

func TestCounterIncrCountsPerKey(t *testing.T) {
	eachCounter(t, func(t *testing.T, c Counter) {
		ctx := context.Background()
		for want := int64(1); want <= 3; want++ {
			got, err := c.Incr(ctx, "k", time.Minute)
			if err != nil {
				t.Fatalf("Incr: %v", err)
			}
			if got != want {
				t.Fatalf("Incr = %d, want %d", got, want)
			}
		}
		if got, err := c.Incr(ctx, "other", time.Minute); err != nil || got != 1 {
			t.Fatalf("a second key = %d (err %v), want 1 — keys must not share a count", got, err)
		}
	})
}

// Peek must read a fresh key as 0 rather than as an error: an untouched window
// is the ordinary case, and Exceeded turns an error into its degraded local
// fallback.
func TestCounterPeekFreshKeyIsZeroNotError(t *testing.T) {
	eachCounter(t, func(t *testing.T, c Counter) {
		got, err := c.Peek(context.Background(), "never-touched")
		if err != nil {
			t.Fatalf("Peek on a fresh key: %v", err)
		}
		if got != 0 {
			t.Fatalf("Peek = %d, want 0", got)
		}
	})
}

func TestCounterPeekDoesNotIncrement(t *testing.T) {
	eachCounter(t, func(t *testing.T, c Counter) {
		ctx := context.Background()
		if _, err := c.Incr(ctx, "k", time.Minute); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if got, _ := c.Peek(ctx, "k"); got != 1 {
				t.Fatalf("Peek #%d = %d, want 1", i, got)
			}
		}
	})
}

// A counter that is not configured at all must surface an error, which is what
// routes the limiters to their local fallback instead of panicking on the auth
// path.
func TestRedisCounterWithoutClientErrors(t *testing.T) {
	c := NewRedisCounter(nil)
	ctx := context.Background()
	if _, err := c.Incr(ctx, "k", time.Minute); err == nil {
		t.Fatal("Incr without a client must error")
	}
	if _, err := c.Peek(ctx, "k"); err == nil {
		t.Fatal("Peek without a client must error")
	}
}

// The window is what makes the counter self-limiting: once it passes, the key
// counts from scratch and its memory goes back.
func TestMemoryCounterForgetsThePastWindow(t *testing.T) {
	c := NewMemoryCounter()
	now := time.Now()
	c.now = func() time.Time { return now }
	ctx := context.Background()

	if _, err := c.Incr(ctx, "k", time.Minute); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute + time.Second)
	if got, _ := c.Peek(ctx, "k"); got != 0 {
		t.Fatalf("Peek past the window = %d, want 0", got)
	}
	if got, _ := c.Incr(ctx, "k", time.Minute); got != 1 {
		t.Fatalf("Incr past the window = %d, want 1", got)
	}

	// The sweep is what returns the memory; it runs at most once per interval,
	// so reach past that too.
	now = now.Add(memorySweepInterval + time.Minute + time.Second)
	if _, err := c.Incr(ctx, "other", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, held := c.entries["k"]; held {
		t.Fatal("an expired key must not be held past a sweep")
	}
}

// Even a flood of distinct keys inside one sweep interval must leave the map
// bounded — a per-IP counter is fed by whoever shows up.
func TestMemoryCounterBoundedOnOverflow(t *testing.T) {
	c := NewMemoryCounter()
	ctx := context.Background()
	for i := 0; i < memoryCounterMaxKeys+10; i++ {
		if _, err := c.Incr(ctx, string(rune(i))+"-k", time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.entries) > memoryCounterMaxKeys {
		t.Fatalf("map size = %d, want at most %d", len(c.entries), memoryCounterMaxKeys)
	}
}
