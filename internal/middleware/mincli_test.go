package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/httpx"
)

// minCLIApp mounts the gate behind the production ErrorHandler, so the
// assertions cover the wire shape the CLI actually parses.
func minCLIApp(t *testing.T, min string) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(MinCLIVersion(min))
	app.Get("/api/files", func(c *fiber.Ctx) error { return c.SendString("ok") })
	return app
}

func requestWithUA(t *testing.T, app *fiber.App, ua string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	return resp
}

func TestMinCLIVersionRejectsOlderCLI(t *testing.T) {
	app := minCLIApp(t, "1.2.0")
	resp := requestWithUA(t, app, "placard-cli/0.9.0")
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want 426", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var env struct{ Code, Message string }
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("body is not the error envelope: %s", body)
	}
	if env.Code != "upgrade_required" {
		t.Errorf("code = %q, want upgrade_required", env.Code)
	}
	// The rejected CLI predates this code and can only print what it is handed,
	// so the message has to carry both versions and the fix.
	for _, want := range []string{"1.2.0", "0.9.0", "placard update"} {
		if !strings.Contains(env.Message, want) {
			t.Errorf("message %q must mention %q", env.Message, want)
		}
	}
}

func TestMinCLIVersionAdmitsEqualAndNewer(t *testing.T) {
	app := minCLIApp(t, "1.2.0")
	for _, ua := range []string{"placard-cli/1.2.0", "placard-cli/1.2.1", "placard-cli/2.0.0"} {
		if got := requestWithUA(t, app, ua).StatusCode; got != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", ua, got)
		}
	}
}

// A prerelease of the minimum is still older than the minimum.
func TestMinCLIVersionRejectsPrereleaseOfTheMinimum(t *testing.T) {
	app := minCLIApp(t, "1.2.0")
	if got := requestWithUA(t, app, "placard-cli/1.2.0-rc.1").StatusCode; got != http.StatusUpgradeRequired {
		t.Errorf("status = %d, want 426", got)
	}
}

// Everything that is not a released CLI passes: browsers, curl, and local CLI
// builds, whose version strings are not comparable at all.
func TestMinCLIVersionAdmitsNonReleaseCallers(t *testing.T) {
	app := minCLIApp(t, "1.2.0")
	uas := []string{
		"",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
		"curl/8.7.1",
		"placard-cli/dev",
		"placard-cli/master-3577643",
		"placard-cli/v0.6.3-2-g3577643",
		"placard-cli",
		"placard-cli/",
	}
	for _, ua := range uas {
		if got := requestWithUA(t, app, ua).StatusCode; got != http.StatusOK {
			t.Errorf("UA %q: status = %d, want 200", ua, got)
		}
	}
}

// An empty minimum is the default: the gate must then check nothing at all.
func TestMinCLIVersionUnsetChecksNothing(t *testing.T) {
	app := minCLIApp(t, "")
	if got := requestWithUA(t, app, "placard-cli/0.0.1").StatusCode; got != http.StatusOK {
		t.Errorf("status = %d, want 200", got)
	}
}
