package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/httpx"
)

const testOrigin = "https://placard.example.com"

func newCSRFApp() *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(CSRF(config.CSRFConfig{AllowedOrigins: []string{testOrigin}}))
	ok := func(c *fiber.Ctx) error { return c.SendString("ok") }
	app.Post("/auth/device/code", ok)
	app.Post("/auth/device/token", ok)
	app.Post("/auth/device/approve", ok)
	app.Post("/auth/logout", ok)
	app.Post("/api/tokens", ok)
	return app
}

// TestCSRFExemptDeviceEndpoints: the CLI has no token, no Origin and no
// Sec-Fetch-Site at login time, so without the exemption the very first call
// of the device flow is a hard 403 for every user.
func TestCSRFExemptDeviceEndpoints(t *testing.T) {
	app := newCSRFApp()
	for _, path := range []string{"/auth/device/code", "/auth/device/token"} {
		t.Run(path, func(t *testing.T) {
			resp, err := app.Test(httptest.NewRequest("POST", path, nil))
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			if resp.StatusCode != 200 {
				t.Fatalf("bare POST %s = %d, want 200 (exempt)", path, resp.StatusCode)
			}
		})
	}
}

// TestCSRFExemptApproveWithoutXRequestedWith is the anti-regression guard for
// the blocking conflict in spec §3.3.3: the confirmation page is script-src
// 'none', so its plain <form> POST cannot set X-Requested-With. The middleware
// hard-requires that header, so approve MUST be exempt here — its protection
// lives inside the handler (synchronizer token + Sec-Fetch-Site + Origin).
func TestCSRFExemptApproveWithoutXRequestedWith(t *testing.T) {
	app := newCSRFApp()
	req := httptest.NewRequest("POST", "/auth/device/approve", nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	// deliberately NO X-Requested-With — a plain HTML form cannot send it
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("same-origin form POST to approve = %d, want 200", resp.StatusCode)
	}
}

// TestCSRFExemptionDidNotSpread: the exemption must never widen into a
// /auth/* prefix — that would silently disarm POST /auth/logout.
func TestCSRFExemptionDidNotSpread(t *testing.T) {
	app := newCSRFApp()
	for _, path := range []string{"/auth/logout", "/api/tokens"} {
		t.Run(path, func(t *testing.T) {
			resp, err := app.Test(httptest.NewRequest("POST", path, nil))
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			if resp.StatusCode != 403 {
				t.Fatalf("bare POST %s = %d, want 403 (still protected)", path, resp.StatusCode)
			}
		})
	}
}

// TestCSRFExemptionIsExactPath: trailing-slash and sibling paths must NOT be
// exempt. StrictRouting is off, so /auth/device/code/ reaches the same handler
// — failing closed here is what keeps the exemption from being widened by URL
// shape alone.
func TestCSRFExemptionIsExactPath(t *testing.T) {
	app := newCSRFApp()
	app.Post("/auth/device/code/", func(c *fiber.Ctx) error { return c.SendString("ok") })
	resp, err := app.Test(httptest.NewRequest("POST", "/auth/device/code/", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("POST /auth/device/code/ = %d, want 403 (exact-path exemption)", resp.StatusCode)
	}
}

func TestRequestOriginAndAllowed(t *testing.T) {
	cfg := config.CSRFConfig{AllowedOrigins: []string{testOrigin + "/"}}
	app := fiber.New()
	var gotOrigin string
	var gotAllowed bool
	app.Get("/probe", func(c *fiber.Ctx) error {
		gotOrigin = RequestOrigin(c)
		gotAllowed = OriginAllowed(cfg, gotOrigin)
		return c.SendString("ok")
	})
	req := httptest.NewRequest("GET", "/probe", nil)
	req.Header.Set("Referer", testOrigin+"/auth/device?x=1")
	if _, err := app.Test(req); err != nil {
		t.Fatalf("request: %v", err)
	}
	if gotOrigin != testOrigin {
		t.Fatalf("RequestOrigin = %q, want %q", gotOrigin, testOrigin)
	}
	if !gotAllowed {
		t.Fatal("OriginAllowed = false, want true (allowlist entry has a trailing slash)")
	}
}
