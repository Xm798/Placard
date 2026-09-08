package middleware

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/httpx"
	"github.com/Xm798/placard/internal/userctx"
)

// identityInjector stands in for the session/dev_mock auth middleware
// in tests below — sets a fixed AuthzID so UserRateLimit has something to key
// on, without pulling in the real auth stack.
func identityInjector(authzID string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if authzID != "" {
			userctx.Set(c, userctx.Identity{AuthzID: authzID, AuthChannel: "session"})
		}
		return c.Next()
	}
}

// TestUserRateLimitEnforcesPerKeyClass asserts UserRateLimit caps requests at
// `limit` within the window and 429s the next one, using its own key namespace
// (placard:ratelimit:<keyClass>:...).
func TestUserRateLimitEnforcesPerKeyClass(t *testing.T) {
	eachCounter(t, func(t *testing.T, counter Counter) {
		app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
		app.Get("/x", identityInjector("u1"),
			UserRateLimit(RateLimitOptions{Counter: counter, FailOpen: false}, "testclass", 2, time.Minute),
			func(c *fiber.Ctx) error { return c.SendString("ok") })

		for i := 0; i < 2; i++ {
			resp, _ := app.Test(httptest.NewRequest("GET", "/x", nil))
			if resp.StatusCode != fiber.StatusOK {
				t.Fatalf("req %d: want 200, got %d", i, resp.StatusCode)
			}
		}
		resp, _ := app.Test(httptest.NewRequest("GET", "/x", nil))
		if resp.StatusCode != fiber.StatusTooManyRequests {
			t.Fatalf("3rd request: want 429, got %d", resp.StatusCode)
		}
	})
}

// TestUserRateLimitKeyClassIsolation asserts two keyClasses for the SAME
// user never share a counter — exhausting "upload"'s budget must not affect
// "usersearch".
func TestUserRateLimitKeyClassIsolation(t *testing.T) {
	eachCounter(t, func(t *testing.T, counter Counter) {
		app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
		app.Use(identityInjector("u1"))
		app.Get("/a", UserRateLimit(RateLimitOptions{Counter: counter, FailOpen: false}, "classA", 1, time.Minute),
			func(c *fiber.Ctx) error { return c.SendString("ok") })
		app.Get("/b", UserRateLimit(RateLimitOptions{Counter: counter, FailOpen: false}, "classB", 1, time.Minute),
			func(c *fiber.Ctx) error { return c.SendString("ok") })

		if resp, _ := app.Test(httptest.NewRequest("GET", "/a", nil)); resp.StatusCode != fiber.StatusOK {
			t.Fatalf("classA 1st: want 200, got %d", resp.StatusCode)
		}
		if resp, _ := app.Test(httptest.NewRequest("GET", "/a", nil)); resp.StatusCode != fiber.StatusTooManyRequests {
			t.Fatalf("classA 2nd: want 429, got %d", resp.StatusCode)
		}
		// classB for the same user is untouched by classA's exhaustion.
		if resp, _ := app.Test(httptest.NewRequest("GET", "/b", nil)); resp.StatusCode != fiber.StatusOK {
			t.Fatalf("classB 1st: want 200, got %d", resp.StatusCode)
		}
	})
}

// TestUserRateLimitNoIdentityIs401 asserts a request with no AuthzID set on
// the context (auth middleware not run, or ran and left it empty) is
// rejected 401 before ever touching the counter.
func TestUserRateLimitNoIdentityIs401(t *testing.T) {
	eachCounter(t, func(t *testing.T, counter Counter) {
		app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
		app.Get("/x", UserRateLimit(RateLimitOptions{Counter: counter, FailOpen: false}, "testclass", 5, time.Minute),
			func(c *fiber.Ctx) error { return c.SendString("ok") })

		resp, _ := app.Test(httptest.NewRequest("GET", "/x", nil))
		if resp.StatusCode != fiber.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})
}

// The degraded-mode fallback map must stay bounded even when a single window
// alone would overflow it: 4096 distinct actors fill the current bucket, then a
// brand-new actor in that SAME bucket trips the last-resort full reset — the map
// ends at size 1.
func TestLocalCounterBoundedOnOverflow(t *testing.T) {
	l := newLocalCounter()
	const bucket = 100
	for i := 0; i < localCounterMaxKeys; i++ {
		l.incr(fmt.Sprintf("k-%d", i), bucket)
	}
	if got := len(l.counts); got != localCounterMaxKeys {
		t.Fatalf("map size = %d, want %d", got, localCounterMaxKeys)
	}

	// A new distinct actor at capacity in the same bucket triggers the reset.
	if got := l.incr("overflow", bucket); got != 1 {
		t.Fatalf("overflow actor count = %d, want 1", got)
	}
	if got := len(l.counts); got != 1 {
		t.Fatalf("after overflow map size = %d, want 1 (reset)", got)
	}
}

// Re-incrementing an existing actor at capacity within the same bucket must NOT
// reset the map — only a brand-new actor over the cap does.
func TestLocalCounterExistingKeyNoReset(t *testing.T) {
	l := newLocalCounter()
	const bucket = 100
	for i := 0; i < localCounterMaxKeys; i++ {
		l.incr(fmt.Sprintf("k-%d", i), bucket)
	}
	if got := l.incr("k-0", bucket); got != 2 {
		t.Fatalf("existing actor incr = %d, want 2", got)
	}
	if got := len(l.counts); got != localCounterMaxKeys {
		t.Fatalf("map size = %d, want unchanged %d", got, localCounterMaxKeys)
	}
}

// A window rollover discards the previous bucket wholesale: the new bucket's
// counts start fresh and the old bucket can never be read again, so nothing
// stale accumulates across a prolonged outage.
func TestLocalCounterWindowRolloverResets(t *testing.T) {
	l := newLocalCounter()
	l.incr("A", 100)
	l.incr("A", 100)

	// A different actor in a later bucket rolls the window over.
	if got := l.incr("B", 200); got != 1 {
		t.Fatalf("new-bucket actor count = %d, want 1", got)
	}
	if got := len(l.counts); got != 1 {
		t.Fatalf("after rollover map size = %d, want 1 (bucket-200 only)", got)
	}
	// A never appears in bucket 200; the old bucket 100 is unreadable now too.
	if got := l.peek("A", 200); got != 0 {
		t.Fatalf("peek(A, 200) = %d, want 0", got)
	}
	if got := l.peek("A", 100); got != 0 {
		t.Fatalf("peek(A, 100) after rollover = %d, want 0 (tracked bucket moved on)", got)
	}
}
