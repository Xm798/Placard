package middleware

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/httpx"
	"github.com/Xm798/placard/internal/session"
	"github.com/Xm798/placard/internal/userctx"
)

type fakeSessions struct {
	data map[string]session.Data
	err  error
}

func (f *fakeSessions) Get(_ context.Context, id string) (session.Data, error) {
	if f.err != nil {
		return session.Data{}, f.err
	}
	d, ok := f.data[id]
	if !ok {
		return session.Data{}, session.ErrNotFound
	}
	return d, nil
}

func newSessionApp(sess *fakeSessions) *fiber.App {
	// Use httpx.ErrorHandler (not fiber's default) so 401/503 typed errors
	// render with the real status code instead of fiber's plain-text 500.
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(NewAuth(AuthOptions{
		Sessions:   sess,
		CookieName: "__Host-placard_session",
	}))
	app.Get("/api/files", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"user": userctx.AuthzID(c)})
	})
	app.Get("/files", func(c *fiber.Ctx) error { return c.SendString("app") })
	app.Get("/api/health", func(c *fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/login", func(c *fiber.Ctx) error { return c.SendString("landing") })
	app.Get("/s/:id", func(c *fiber.Ctx) error { return c.SendString("share") })
	app.Get("/", func(c *fiber.Ctx) error { return c.SendString("root") })
	app.Get("/skill.md", func(c *fiber.Ctx) error { return c.SendString("skill") })
	app.Get("/install.md", func(c *fiber.Ctx) error { return c.SendString("install") })
	// The device-code confirmation page: registered WITHOUT StrictRouting, so
	// "/auth/device/" hits this same handler — exactly like production.
	app.Get("/auth/device", func(c *fiber.Ctx) error { return c.SendString("confirm") })
	app.Post("/auth/device/approve", func(c *fiber.Ctx) error { return c.SendString("approved") })
	app.Post("/auth/device/code", func(c *fiber.Ctx) error { return c.SendString("code") })
	app.Get("/install.sh", func(c *fiber.Ctx) error { return c.SendString("#!/bin/sh") })
	// The two SSO legs. They are what an unauthenticated visitor navigates to,
	// so gating either one makes single sign-on unreachable.
	app.Get("/auth/oidc/start", func(c *fiber.Ctx) error { return c.SendString("start") })
	app.Get("/auth/oidc/callback", func(c *fiber.Ctx) error { return c.SendString("callback") })
	return app
}

func TestValidSession(t *testing.T) {
	app := newSessionApp(&fakeSessions{data: map[string]session.Data{
		"sid1": {AuthzID: "u_abc", DisplayName: "沈", CreatedAt: time.Now()},
	}})
	req := httptest.NewRequest("GET", "/api/files", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-placard_session", Value: "sid1"})
	resp, _ := app.Test(req)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "u_abc") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

func TestUnauthAPIGets401(t *testing.T) {
	app := newSessionApp(&fakeSessions{})
	// XHR-style request: Sec-Fetch-Mode: cors → 401 JSON, never a redirect.
	req := httptest.NewRequest("GET", "/api/files", nil)
	req.Header.Set("Sec-Fetch-Mode", "cors")
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestUnauthNavigationRedirects(t *testing.T) {
	app := newSessionApp(&fakeSessions{})
	req := httptest.NewRequest("GET", "/files", nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	resp, _ := app.Test(req)
	if resp.StatusCode != 302 {
		t.Fatalf("want 302, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login?redirect=%2Ffiles" {
		t.Fatalf("Location=%q", loc)
	}
}

func TestUnauthRootNavigationShowsLoginPage(t *testing.T) {
	app := newSessionApp(&fakeSessions{})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	resp, _ := app.Test(req)
	if resp.StatusCode != 302 {
		t.Fatalf("want 302, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Fatalf("Location=%q", loc)
	}
}

// Accept-based fallback when Sec-Fetch-Mode is absent (older webviews).
func TestUnauthAcceptHTMLFallback(t *testing.T) {
	app := newSessionApp(&fakeSessions{})
	req := httptest.NewRequest("GET", "/files", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, _ := app.Test(req)
	if resp.StatusCode != 302 {
		t.Fatalf("want 302, got %d", resp.StatusCode)
	}
}

// Cookie present but the session id is unknown to the store (expired/revoked)
// → ErrNotFound path → 401 for XHR, NOT 503.
func TestUnknownSessionIs401(t *testing.T) {
	app := newSessionApp(&fakeSessions{})
	req := httptest.NewRequest("GET", "/api/files", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-placard_session", Value: "does-not-exist"})
	req.Header.Set("Sec-Fetch-Mode", "cors")
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

// A nil Sessions store is a misconfiguration → 503 envelope, never a
// nil-interface panic eaten by recover as a bare 500.
func TestNilSessionsIs503(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(NewAuth(AuthOptions{
		Sessions:   nil,
		CookieName: "__Host-placard_session",
	}))
	app.Get("/api/files", func(c *fiber.Ctx) error { return c.SendString("ok") })
	req := httptest.NewRequest("GET", "/api/files", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-placard_session", Value: "sid1"})
	resp, _ := app.Test(req)
	if resp.StatusCode != 503 {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

// Session data with an empty AuthzID fails closed as unauthenticated (401 for
// XHR) — an empty identity must never be injected.
func TestEmptyAuthzIDFailsClosed(t *testing.T) {
	app := newSessionApp(&fakeSessions{data: map[string]session.Data{
		"sid1": {AuthzID: "", DisplayName: "沈", CreatedAt: time.Now()},
	}})
	req := httptest.NewRequest("GET", "/api/files", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-placard_session", Value: "sid1"})
	req.Header.Set("Sec-Fetch-Mode", "cors")
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

// Redis outage is 503, NOT 401/302 — never dump live users into a login loop.
func TestRedisErrorIs503(t *testing.T) {
	app := newSessionApp(&fakeSessions{err: errors.New("redis: connection refused")})
	req := httptest.NewRequest("GET", "/api/files", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-placard_session", Value: "sid1"})
	resp, _ := app.Test(req)
	if resp.StatusCode != 503 {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

// A failing PAT (bogus Bearer token) records a FailLimiter hit; once the
// per-IP failure budget is exhausted, further attempts get 429 without even
// reaching the token validator.
func TestFailedAuthBudget(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(NewAuth(AuthOptions{
		Sessions:       &fakeSessions{},
		CookieName:     "__Host-placard_session",
		FailLimiter:    NewIPLimiter(NewMemoryCounter(), 2, "authfail"),
		TokenValidator: func(string) (userctx.Identity, bool) { return userctx.Identity{}, false },
	}))
	app.Get("/api/files", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := func() *http.Request {
		r := httptest.NewRequest("GET", "/api/files", nil)
		r.Header.Set("Authorization", "Bearer wrong")
		r.Header.Set("Sec-Fetch-Mode", "cors")
		return r
	}
	for i := 0; i < 3; i++ {
		resp, _ := app.Test(req())
		if resp.StatusCode != 401 {
			t.Fatalf("attempt %d: want 401, got %d", i, resp.StatusCode)
		}
	}
	resp, _ := app.Test(req())
	if resp.StatusCode != 429 {
		t.Fatalf("over budget: want 429, got %d", resp.StatusCode)
	}
}

// PATs authenticate the API surface only. A Bearer header on a browser route
// must fall through to the cookie channel rather than resolving a CLI identity
// there.
func TestBearerOnlyAcceptedUnderAPI(t *testing.T) {
	validator := func(token string) (userctx.Identity, bool) {
		return userctx.Identity{AuthzID: "u_pat", AuthChannel: userctx.ChannelPAT}, token == "good"
	}
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(NewAuth(AuthOptions{
		Sessions:       &fakeSessions{},
		CookieName:     "__Host-placard_session",
		TokenValidator: validator,
	}))
	app.Get("/api/files", func(c *fiber.Ctx) error { return c.SendString(userctx.AuthChannel(c)) })
	app.Get("/s/:id", func(c *fiber.Ctx) error { return c.SendString(userctx.AuthChannel(c)) })

	req := httptest.NewRequest("GET", "/api/files", nil)
	req.Header.Set("Authorization", "Bearer good")
	resp, _ := app.Test(req)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != userctx.ChannelPAT {
		t.Fatalf("/api/files: status=%d channel=%q, want 200 pat", resp.StatusCode, body)
	}

	// A share link is served anonymously rather than as the token's owner: the
	// route is open to visitors with no account, and a PAT must never widen
	// what its holder can read there.
	req = httptest.NewRequest("GET", "/s/abc12345", nil)
	req.Header.Set("Authorization", "Bearer good")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	resp, _ = app.Test(req)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "" {
		t.Fatalf("/s/:id with a PAT: status=%d channel=%q, want 200 anonymous", resp.StatusCode, body)
	}
}

// A successful session auth invokes AuthOptions.TouchActive with the resolved
// AuthzID — the wiring point ActiveToucher hangs off.
func TestSessionCallsTouchActive(t *testing.T) {
	var got []string
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(NewAuth(AuthOptions{
		Sessions:   &fakeSessions{data: map[string]session.Data{"sid1": {AuthzID: "u_abc", DisplayName: "沈"}}},
		CookieName: "__Host-placard_session",
		TouchActive: func(authzID string) {
			got = append(got, authzID)
		},
	}))
	app.Get("/api/files", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "/api/files", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-placard_session", Value: "sid1"})
	resp, _ := app.Test(req)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if len(got) != 1 || got[0] != "u_abc" {
		t.Fatalf("TouchActive calls = %v, want [u_abc]", got)
	}
}

// A failed session auth (unknown session) must NOT invoke TouchActive.
func TestFailedSessionDoesNotCallTouchActive(t *testing.T) {
	called := false
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(NewAuth(AuthOptions{
		Sessions:    &fakeSessions{},
		CookieName:  "__Host-placard_session",
		TouchActive: func(string) { called = true },
	}))
	app.Get("/api/files", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "/api/files", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-placard_session", Value: "does-not-exist"})
	req.Header.Set("Sec-Fetch-Mode", "cors")
	app.Test(req)
	if called {
		t.Fatal("TouchActive must not fire on failed session auth")
	}
}

// A successful PAT (Bearer) auth invokes TouchActive with the validator's
// resolved AuthzID.
func TestPATCallsTouchActive(t *testing.T) {
	var got []string
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(NewAuth(AuthOptions{
		Sessions: &fakeSessions{},
		TokenValidator: func(token string) (userctx.Identity, bool) {
			if token != "good" {
				return userctx.Identity{}, false
			}
			return userctx.Identity{AuthzID: "u_pat", AuthChannel: userctx.ChannelPAT}, true
		},
		TouchActive: func(authzID string) {
			got = append(got, authzID)
		},
	}))
	app.Get("/api/files", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "/api/files", nil)
	req.Header.Set("Authorization", "Bearer good")
	resp, _ := app.Test(req)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if len(got) != 1 || got[0] != "u_pat" {
		t.Fatalf("TouchActive calls = %v, want [u_pat]", got)
	}
}

// A rejected PAT must not invoke TouchActive.
func TestFailedPATDoesNotCallTouchActive(t *testing.T) {
	called := false
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(NewAuth(AuthOptions{
		Sessions:       &fakeSessions{},
		TokenValidator: func(string) (userctx.Identity, bool) { return userctx.Identity{}, false },
		TouchActive:    func(string) { called = true },
	}))
	app.Get("/api/files", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "/api/files", nil)
	req.Header.Set("Authorization", "Bearer bad")
	app.Test(req)
	if called {
		t.Fatal("TouchActive must not fire on a rejected PAT")
	}
}

// A nil TouchActive (unset) must never panic across every channel — the
// zero-value AuthOptions is the common case in tests/config that don't care.
func TestNilTouchActiveDoesNotPanic(t *testing.T) {
	app := newSessionApp(&fakeSessions{data: map[string]session.Data{"sid1": {AuthzID: "u_abc"}}})
	req := httptest.NewRequest("GET", "/api/files", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-placard_session", Value: "sid1"})
	resp, _ := app.Test(req)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

// The dev_mock channel also invokes TouchActive.
func TestDevMockCallsTouchActive(t *testing.T) {
	var got []string
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(NewAuth(AuthOptions{
		DevMock: config.DevMockConfig{Enabled: true, UID: "u_dev"},
		TouchActive: func(authzID string) {
			got = append(got, authzID)
		},
	}))
	app.Get("/api/files", func(c *fiber.Ctx) error { return c.SendString("ok") })

	resp, _ := app.Test(httptest.NewRequest("GET", "/api/files", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if len(got) != 1 || got[0] != "u_dev" {
		t.Fatalf("TouchActive calls = %v, want [u_dev]", got)
	}
}

// Idle-expired sessions (cookie MaxAge=absolute TTL outlives the store's idle
// TTL) are a benign, common returning-user event and must NOT count against
// the per-IP failure budget — otherwise a shared office NAT would trip the
// budget and 429 everyone's logins. Every request stays 401, never 429.
func TestIdleExpiredSessionDoesNotHitFailBudget(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(NewAuth(AuthOptions{
		Sessions:    &fakeSessions{}, // empty store → every lookup is ErrNotFound
		CookieName:  "__Host-placard_session",
		FailLimiter: NewIPLimiter(NewMemoryCounter(), 2, "authfail"),
	}))
	app.Get("/api/files", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := func() *http.Request {
		r := httptest.NewRequest("GET", "/api/files", nil)
		r.AddCookie(&http.Cookie{Name: "__Host-placard_session", Value: "idle-expired"})
		r.Header.Set("Sec-Fetch-Mode", "cors")
		return r
	}
	for i := 0; i < 6; i++ { // well over the budget of 2
		resp, _ := app.Test(req())
		if resp.StatusCode != 401 {
			t.Fatalf("attempt %d: want 401 (never 429), got %d", i, resp.StatusCode)
		}
	}
}

// TestAuthDeviceRequiresAuthInAllThreeForms is the regression guard for the
// builtinSkips whitelist. Testing only "/auth/device" would give a false
// "protected" signal: under a "/auth/*" wildcard the bare path could be
// excluded by hand while the trailing-slash variant still slipped through the
// prefix matcher (StrictRouting is off, so both reach the same handler).
func TestAuthDeviceRequiresAuthInAllThreeForms(t *testing.T) {
	for _, path := range []string{"/auth/device", "/auth/device/", "/auth/device?user_code=ABCD1234"} {
		t.Run(path, func(t *testing.T) {
			app := newSessionApp(&fakeSessions{})
			req := httptest.NewRequest("GET", path, nil)
			req.Header.Set("Sec-Fetch-Mode", "navigate")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			if resp.StatusCode == 200 {
				t.Fatalf("%s returned 200 — the confirmation page is reachable unauthenticated", path)
			}
			if resp.StatusCode != 302 && resp.StatusCode != 401 {
				t.Fatalf("%s status = %d, want 302 or 401", path, resp.StatusCode)
			}
		})
	}
}

// TestApproveRequiresAuth: the authorization action must bind a REAL identity,
// so /auth/device/approve must be absent from the whitelist. It needs no code
// of its own — being absent is the whole mechanism, which is exactly why it
// needs a test.
func TestApproveRequiresAuth(t *testing.T) {
	app := newSessionApp(&fakeSessions{})
	resp, err := app.Test(httptest.NewRequest("POST", "/auth/device/approve", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated approve = %d, want 401", resp.StatusCode)
	}
	// ...while the CLI's own two endpoints must stay reachable without auth.
	resp, err = app.Test(httptest.NewRequest("POST", "/auth/device/code", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("POST /auth/device/code = %d, want 200 (whitelisted)", resp.StatusCode)
	}
}

// TestBuiltinSkipsWhitelist pins every path that MUST stay unauthenticated.
// A missing entry here is a hard login regression.
func TestBuiltinSkipsWhitelist(t *testing.T) {
	app := newSessionApp(&fakeSessions{})
	for _, path := range []string{
		"/api/health", "/login", "/skill.md", "/install.md", "/install.sh",
		// The SSO legs: a visitor with no session is exactly who reaches
		// them, so gating either one makes single sign-on unreachable.
		"/auth/oidc/start", "/auth/oidc/callback",
	} {
		t.Run(path, func(t *testing.T) {
			resp, err := app.Test(httptest.NewRequest("GET", path, nil))
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			if resp.StatusCode != 200 {
				t.Fatalf("%s status = %d, want 200 (must stay unauthenticated)", path, resp.StatusCode)
			}
		})
	}
}

// TestUnauthenticatedRedirectKeepsQuery: unauthenticated() used c.Path(), which
// drops the query wholesale. A user opening /auth/device?user_code=ABCD1234
// while logged out came back to a bare page with no context. This is a public
// middleware, so the fix restores deep-link query for EVERY route.
func TestUnauthenticatedRedirectKeepsQuery(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"device confirm", "/auth/device?user_code=ABCD1234",
			"/login?redirect=" + url.QueryEscape("/auth/device?user_code=ABCD1234")},
		{"settings deep link", "/settings?tab=tokens",
			"/login?redirect=" + url.QueryEscape("/settings?tab=tokens")},
		{"no query is unchanged", "/files",
			"/login?redirect=" + url.QueryEscape("/files")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newSessionApp(&fakeSessions{})
			req := httptest.NewRequest("GET", tt.path, nil)
			req.Header.Set("Sec-Fetch-Mode", "navigate")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			if resp.StatusCode != 302 {
				t.Fatalf("status = %d, want 302", resp.StatusCode)
			}
			if got := resp.Header.Get("Location"); got != tt.want {
				t.Fatalf("Location = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUnauthenticatedRootStillGoesToLanding pins that the root special case is
// untouched: "/" shows the static landing page with no redirect parameter.
func TestUnauthenticatedRootStillGoesToLanding(t *testing.T) {
	app := newSessionApp(&fakeSessions{})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/login" {
		t.Fatalf("status=%d location=%q, want 302 /login", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// newAccountStatusApp mounts the auth middleware with an AccountStatus seam and
// a single protected route, so each verdict can be driven directly.
func newAccountStatusApp(status func(ctx context.Context, userID string) (bool, error)) *fiber.App {
	sess := &fakeSessions{data: map[string]session.Data{
		"sid1": {AuthzID: "u_abc", DisplayName: "Alice", CreatedAt: time.Now()},
	}}
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(NewAuth(AuthOptions{
		Sessions:      sess,
		CookieName:    "__Host-placard_session",
		AccountStatus: status,
		TokenValidator: func(token string) (userctx.Identity, bool) {
			return userctx.Identity{AuthzID: "u_abc", AuthChannel: userctx.ChannelPAT}, token == "good"
		},
	}))
	app.Get("/api/files", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"user": userctx.AuthzID(c)})
	})
	return app
}

// Disabling an account has to reach the session cookies and PATs it already
// issued — they are long-lived and nothing else re-checks them.
func TestDisabledAccountRejectedOnBothChannels(t *testing.T) {
	app := newAccountStatusApp(func(context.Context, string) (bool, error) { return false, nil })

	req := httptest.NewRequest("GET", "/api/files", nil)
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.AddCookie(&http.Cookie{Name: "__Host-placard_session", Value: "sid1"})
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Errorf("session channel status = %d, want 401", resp.StatusCode)
	}

	req = httptest.NewRequest("GET", "/api/files", nil)
	req.Header.Set("Authorization", "Bearer good")
	resp, _ = app.Test(req)
	if resp.StatusCode != 401 {
		t.Errorf("PAT channel status = %d, want 401", resp.StatusCode)
	}
}

// A database blip is not a credential verdict: answering 401 would log every
// live user out at once.
func TestAccountStatusErrorIs503(t *testing.T) {
	app := newAccountStatusApp(func(context.Context, string) (bool, error) {
		return false, errors.New("database unreachable")
	})

	req := httptest.NewRequest("GET", "/api/files", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-placard_session", Value: "sid1"})
	resp, _ := app.Test(req)
	if resp.StatusCode != 503 {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

// The SPA bundle backing the login page must load without a session — gating it
// would leave an unauthenticated visitor a blank page with no way in.
func TestAssetsAreUnauthenticated(t *testing.T) {
	app := newSessionApp(&fakeSessions{})
	app.Get("/assets/*", func(c *fiber.Ctx) error { return c.SendString("bundle") })

	req := httptest.NewRequest("GET", "/assets/index-abc123.js", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// --- share links: optional authentication ---

// shareApp exposes the identity the middleware resolved for a share route, so
// the tests below can tell "anonymous" apart from "the session's owner".
func shareApp(opts AuthOptions) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	opts.CookieName = "__Host-placard_session"
	app.Use(NewAuth(opts))
	app.Get("/s/:id", func(c *fiber.Ctx) error { return c.SendString(userctx.AuthzID(c)) })
	app.Get("/s/:id/render", func(c *fiber.Ctx) error { return c.SendString(userctx.AuthzID(c)) })
	return app
}

func shareGet(t *testing.T, app *fiber.App, path string, cookie string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "__Host-placard_session", Value: cookie})
	}
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request %s: %v", path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestShareLinkServedAnonymously: a browser navigation to a share link with no
// cookie is served, not redirected to /login — the visitor may have no account.
func TestShareLinkServedAnonymously(t *testing.T) {
	app := shareApp(AuthOptions{Sessions: &fakeSessions{}})
	for _, path := range []string{"/s/abc12345", "/s/abc12345/render"} {
		code, body := shareGet(t, app, path, "")
		if code != 200 || body != "" {
			t.Errorf("%s = %d %q, want 200 with no identity", path, code, body)
		}
	}
}

// TestShareLinkPrefixMatchesRouterCasing: Fiber routes case-insensitively by
// default, so /S/abc12345 reaches ViewShell. The prefix test has to agree, or
// that request would fall through to the login gate and redirect a visitor away
// from a page they are entitled to see.
func TestShareLinkPrefixMatchesRouterCasing(t *testing.T) {
	app := shareApp(AuthOptions{Sessions: &fakeSessions{}})
	if code, body := shareGet(t, app, "/S/abc12345", ""); code != 200 || body != "" {
		t.Fatalf("/S/abc12345 = %d %q, want 200 anonymous", code, body)
	}
}

// TestShareLinkResolvesSession: the owner's cookie still produces an identity,
// which is what lets them reach their own private page.
func TestShareLinkResolvesSession(t *testing.T) {
	app := shareApp(AuthOptions{
		Sessions: &fakeSessions{data: map[string]session.Data{"sid1": {AuthzID: "u_abc"}}},
	})
	if code, body := shareGet(t, app, "/s/abc12345", "sid1"); code != 200 || body != "u_abc" {
		t.Fatalf("share with session = %d %q, want 200 u_abc", code, body)
	}
}

// TestShareLinkDegradesToAnonymous: every "credential present but unusable"
// verdict leaves the request anonymous rather than refusing it — an expired
// session, a disabled account, and a session store that is down alike.
func TestShareLinkDegradesToAnonymous(t *testing.T) {
	live := map[string]session.Data{"sid1": {AuthzID: "u_abc"}}
	tests := []struct {
		name string
		opts AuthOptions
	}{
		{"unknown session", AuthOptions{Sessions: &fakeSessions{}}},
		{"store unavailable", AuthOptions{Sessions: &fakeSessions{err: errors.New("redis down")}}},
		{"disabled account", AuthOptions{
			Sessions:      &fakeSessions{data: live},
			AccountStatus: func(context.Context, string) (bool, error) { return false, nil },
		}},
		{"account lookup failed", AuthOptions{
			Sessions:      &fakeSessions{data: live},
			AccountStatus: func(context.Context, string) (bool, error) { return false, errors.New("db down") },
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := shareGet(t, shareApp(tt.opts), "/s/abc12345", "sid1")
			if code != 200 || body != "" {
				t.Fatalf("= %d %q, want 200 anonymous", code, body)
			}
		})
	}
}

// TestShareLinkSkipsFailureBudget: an anonymous share view is not a failed
// authentication attempt, so it must neither be refused by a tripped per-IP
// budget nor feed one — otherwise one probing visitor would 429 a popular page
// for everyone behind the same NAT.
func TestShareLinkSkipsFailureBudget(t *testing.T) {
	limiter := NewIPLimiter(NewMemoryCounter(), 1, "authfail")
	app := shareApp(AuthOptions{Sessions: &fakeSessions{}, FailLimiter: limiter})
	for i := 0; i < 5; i++ {
		if code, _ := shareGet(t, app, "/s/abc12345", "sid-unknown"); code != 200 {
			t.Fatalf("request %d = %d, want 200", i, code)
		}
	}
	if limiter.Exceeded(context.Background(), "0.0.0.0") {
		t.Fatal("share views fed the failed-authn budget")
	}
}
