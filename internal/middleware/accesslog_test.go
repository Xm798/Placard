package middleware

import (
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/httpx"
	"github.com/Xm798/placard/internal/userctx"
)

// A downstream apperr must be rendered with its real status AND logged with that
// same status at Warn — the historic bug logged every error as 200/Info because
// the status was read before fiber.Config.ErrorHandler ran. The ErrorHandler
// must also run exactly once (the middleware handles the error and returns nil).
func TestAccessLogRendersAndLogsErrorStatus(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)

	var ehCalls int32
	eh := func(c *fiber.Ctx, err error) error {
		atomic.AddInt32(&ehCalls, 1)
		return httpx.ErrorHandler(c, err)
	}
	app := fiber.New(fiber.Config{ErrorHandler: eh})
	app.Use(accessLog(zap.New(core)))
	app.Get("/boom", func(c *fiber.Ctx) error { return apperr.Unauthorized() })

	resp, err := app.Test(httptest.NewRequest("GET", "/boom", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("client status: want 401, got %d", resp.StatusCode)
	}
	if n := atomic.LoadInt32(&ehCalls); n != 1 {
		t.Fatalf("ErrorHandler invoked %d times, want exactly 1 (double-invoke bug)", n)
	}

	entries := logs.FilterMessage("access").All()
	if len(entries) != 1 {
		t.Fatalf("want 1 access log entry, got %d", len(entries))
	}
	e := entries[0]
	if got := e.ContextMap()["status"]; got != int64(401) {
		t.Fatalf("logged status = %v, want 401", got)
	}
	if e.Level != zapcore.WarnLevel {
		t.Fatalf("logged level = %v, want Warn", e.Level)
	}
}

// A 2xx response is logged at Info with the real status.
func TestAccessLogSuccessLoggedInfo(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(accessLog(zap.New(core)))
	app.Get("/ok", func(c *fiber.Ctx) error { return c.SendString("ok") })

	resp, _ := app.Test(httptest.NewRequest("GET", "/ok", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	entries := logs.FilterMessage("access").All()
	if len(entries) != 1 {
		t.Fatalf("want 1 access log entry, got %d", len(entries))
	}
	if entries[0].Level != zapcore.InfoLevel {
		t.Fatalf("logged level = %v, want Info", entries[0].Level)
	}
	if got := entries[0].ContextMap()["status"]; got != int64(200) {
		t.Fatalf("logged status = %v, want 200", got)
	}
}

func TestAccessLogIncludesAuthenticatedActorAndChannel(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(accessLog(zap.New(core)))
	app.Get("/ok", func(c *fiber.Ctx) error {
		userctx.Set(c, userctx.Identity{AuthzID: "on_x", AuthChannel: "session"})
		return c.SendString("ok")
	})

	_, _ = app.Test(httptest.NewRequest("GET", "/ok?filter=mine", nil))
	context := logs.FilterMessage("access").All()[0].ContextMap()
	if context["actor"] != "on_x" || context["auth_channel"] != "session" || context["query"] != "filter=mine" {
		t.Fatalf("access identity fields = %v", context)
	}
}

// duration_ms replaces latency: an Int64 count of milliseconds, not a
// zap.Duration serializing as float seconds (design §10). The rename is
// breaking on purpose, so the old field must be gone rather than dual-written.
func TestAccessLogEmitsDurationMSNotLatency(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(RequestID())
	app.Use(accessLog(zap.New(core)))
	app.Get("/ok", func(c *fiber.Ctx) error { return c.SendString("ok") })

	_, _ = app.Test(httptest.NewRequest("GET", "/ok", nil))
	ctx := logs.FilterMessage("access").All()[0].ContextMap()

	if _, ok := ctx["latency"]; ok {
		t.Errorf("latency still emitted: %v", ctx)
	}
	if _, ok := ctx["duration_ms"].(int64); !ok {
		t.Errorf("duration_ms = %T (%v), want int64 milliseconds", ctx["duration_ms"], ctx["duration_ms"])
	}
	// entrypoint joins the correlation pair; RequestID() is what puts it there.
	if ctx["entrypoint"] != ctxlog.EntrypointHTTP {
		t.Errorf("entrypoint = %v, want %q", ctx["entrypoint"], ctxlog.EntrypointHTTP)
	}
	if rid, _ := ctx["request_id"].(string); rid == "" {
		t.Errorf("request_id missing: %v", ctx)
	}
}

// The OAuth callback's authorization code must not reach the log, but the key
// names must survive so the shape of the request stays debuggable.
func TestAccessLogRedactsSensitiveQueryKeys(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(accessLog(zap.New(core)))
	app.Get("/auth/callback", func(c *fiber.Ctx) error { return c.SendString("ok") })

	raw := "code=4%2Fsecret&state=st1&ticket=tkt&access_key=AK&token=pl_abc&Authorization=Bearer+x&page=2"
	_, _ = app.Test(httptest.NewRequest("GET", "/auth/callback?"+raw, nil))
	got, _ := logs.FilterMessage("access").All()[0].ContextMap()["query"].(string)

	want := "code=REDACTED&state=st1&ticket=REDACTED&access_key=REDACTED&token=REDACTED&Authorization=REDACTED&page=2"
	if got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	for _, secret := range []string{"secret", "AK", "pl_abc", "Bearer"} {
		if strings.Contains(got, secret) {
			t.Errorf("redacted query still contains %q: %s", secret, got)
		}
	}
}

// A query with no sensitive keys is logged byte-for-byte, including the
// original escaping and parameter order.
func TestAccessLogLeavesBenignQueryUntouched(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(accessLog(zap.New(core)))
	app.Get("/api/files", func(c *fiber.Ctx) error { return c.SendString("ok") })

	raw := "page=2&page_size=20&q=hello%20world&flag"
	_, _ = app.Test(httptest.NewRequest("GET", "/api/files?"+raw, nil))
	if got := logs.FilterMessage("access").All()[0].ContextMap()["query"]; got != raw {
		t.Fatalf("query = %v, want it untouched (%q)", got, raw)
	}
}

// access_token must NOT be mistaken for token: a regex over the raw string
// would redact its value, a keyed parse does not.
func TestRedactQueryMatchesWholeKeysOnly(t *testing.T) {
	if got := redactQuery("access_token=keepme&token=dropme"); got != "access_token=keepme&token=REDACTED" {
		t.Fatalf("redactQuery = %q", got)
	}
	if got := redactQuery(""); got != "" {
		t.Fatalf("redactQuery(empty) = %q", got)
	}
	if got := redactQuery("bare"); got != "bare" {
		t.Fatalf("redactQuery(valueless) = %q", got)
	}
	// Percent-encoded key: decoded before matching, so it cannot smuggle past.
	if got := redactQuery("%74oken=dropme"); got != "%74oken=REDACTED" {
		t.Fatalf("redactQuery(encoded key) = %q", got)
	}
}
