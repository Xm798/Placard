package handler

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// TestViewShellVersionUI: the shell ships the owner version dropdown skeleton
// and still admits its inline blocks by hash (never 'unsafe-inline').
func TestViewShellVersionUI(t *testing.T) {
	app, _ := newTestApp(t)
	id := publishOne(t, app, "never")

	req := httptest.NewRequest("GET", "/s/"+id, nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	body := string(b)

	if !strings.Contains(body, `id="version-select"`) {
		t.Errorf("shell missing version dropdown")
	}
	if !strings.Contains(body, "/versions") {
		t.Errorf("shell script does not probe the owner versions endpoint")
	}
	if !strings.Contains(body, "location.search") && !strings.Contains(body, "URLSearchParams") {
		t.Errorf("shell script does not forward the ?v= preview param")
	}

	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'sha256-") || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("shell CSP must hash-admit inline script, got %q", csp)
	}
}
