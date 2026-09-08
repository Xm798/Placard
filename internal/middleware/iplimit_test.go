package middleware

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/httpx"
)

func TestIPLimiterHandler(t *testing.T) {
	eachCounter(t, func(t *testing.T, counter Counter) {
		app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
		app.Get("/auth/login", NewIPLimiter(counter, 3, "authroute").Handler(),
			func(c *fiber.Ctx) error { return c.SendString("ok") })

		for i := 0; i < 3; i++ {
			resp, _ := app.Test(httptest.NewRequest("GET", "/auth/login", nil))
			if resp.StatusCode != 200 {
				t.Fatalf("req %d: want 200, got %d", i, resp.StatusCode)
			}
		}
		resp, _ := app.Test(httptest.NewRequest("GET", "/auth/login", nil))
		if resp.StatusCode != 429 {
			t.Fatalf("want 429 after limit, got %d", resp.StatusCode)
		}
	})
}

func TestIPLimiterHitExceeded(t *testing.T) {
	eachCounter(t, func(t *testing.T, counter Counter) {
		l := NewIPLimiter(counter, 2, "authfail")
		ctx := context.Background()
		if l.Exceeded(ctx, "1.2.3.4") {
			t.Fatal("fresh IP must not be exceeded")
		}
		l.Hit(ctx, "1.2.3.4")
		l.Hit(ctx, "1.2.3.4")
		l.Hit(ctx, "1.2.3.4")
		if !l.Exceeded(ctx, "1.2.3.4") {
			t.Fatal("3 hits over limit 2 must be exceeded")
		}
		if l.Exceeded(ctx, "5.6.7.8") {
			t.Fatal("other IP unaffected")
		}
	})
}

// With a counter that never answers (here: a Redis counter built with no
// client), the limiter must degrade to the per-replica local counter — never
// panic, never hard-error, and still enforce the budget.
func TestIPLimiterNilRedisLocalFallback(t *testing.T) {
	l := NewIPLimiter(NewRedisCounter(nil), 2, "authfail")
	ctx := context.Background()
	if l.Exceeded(ctx, "1.2.3.4") {
		t.Fatal("fresh IP on the local fallback must not be exceeded")
	}
	l.Hit(ctx, "1.2.3.4")
	l.Hit(ctx, "1.2.3.4")
	if l.Exceeded(ctx, "1.2.3.4") {
		t.Fatal("2 hits at limit 2 must not be exceeded yet")
	}
	l.Hit(ctx, "1.2.3.4")
	if !l.Exceeded(ctx, "1.2.3.4") {
		t.Fatal("3 hits over limit 2 must be exceeded via local fallback")
	}
	if l.Exceeded(ctx, "5.6.7.8") {
		t.Fatal("other IP unaffected")
	}
}
