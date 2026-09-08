package handler

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

func serveInstallScripts(t *testing.T, lookup func(context.Context) (string, error)) *fiber.App {
	t.Helper()
	h := &Handlers{cliVersion: newCLIVersionResolver(lookup)}
	app := fiber.New()
	app.Get("/install.sh", h.InstallScript)
	app.Get("/install.ps1", h.InstallScriptPS)
	return app
}

func getBody(t *testing.T, app *fiber.App, path string) string {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest("GET", path, nil), -1)
	if err != nil {
		t.Fatalf("request %s: %v", path, err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func TestInstallScriptsCarryTheResolvedCLIVersion(t *testing.T) {
	app := serveInstallScripts(t, func(context.Context) (string, error) { return "1.4.2", nil })

	if got := getBody(t, app, "/install.sh"); !strings.Contains(got, `DEFAULT_CLI_VERSION="1.4.2"`) {
		t.Errorf("install.sh is not pinned to the resolved release")
	}
	if got := getBody(t, app, "/install.ps1"); !strings.Contains(got, `$DefaultCliVersion = '1.4.2'`) {
		t.Errorf("install.ps1 is not pinned to the resolved release")
	}
}

// An instance that cannot reach GitHub still has to serve a working installer:
// unpinned, the script resolves the release itself.
func TestInstallScriptsAreServedUnpinnedWhenTheLookupFails(t *testing.T) {
	app := serveInstallScripts(t, func(context.Context) (string, error) {
		return "", errors.New("no network")
	})

	if got := getBody(t, app, "/install.sh"); !strings.Contains(got, `DEFAULT_CLI_VERSION=""`) {
		t.Errorf("install.sh must keep its empty version line when nothing was resolved")
	}
	if got := getBody(t, app, "/install.ps1"); !strings.Contains(got, `$DefaultCliVersion = ''`) {
		t.Errorf("install.ps1 must keep its empty version line when nothing was resolved")
	}
}

func TestInstallScriptsAreServedUnpinnedWithoutALookup(t *testing.T) {
	app := serveInstallScripts(t, nil)
	if got := getBody(t, app, "/install.sh"); !strings.Contains(got, `DEFAULT_CLI_VERSION=""`) {
		t.Errorf("a Deps without CLIRelease must serve the script untouched")
	}
}

// The endpoint is unauthenticated, so every hit must not become an outbound
// request — neither while a version is known nor while GitHub is failing.
func TestCLIVersionResolverCachesBothOutcomes(t *testing.T) {
	calls := 0
	fail := false
	now := time.Now()
	r := newCLIVersionResolver(func(context.Context) (string, error) {
		calls++
		if fail {
			return "", errors.New("rate limited")
		}
		return "1.4.2", nil
	})
	r.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if got := r.Current(context.Background()); got != "1.4.2" {
			t.Fatalf("Current = %q, want 1.4.2", got)
		}
	}
	if calls != 1 {
		t.Fatalf("lookups = %d, want 1 within the TTL", calls)
	}

	// Past the TTL the release is re-checked; a failure keeps the last known
	// version and is itself cached, so a broken GitHub cannot be turned into
	// one outbound request per visitor.
	now = now.Add(cliVersionTTL + time.Second)
	fail = true
	if got := r.Current(context.Background()); got != "1.4.2" {
		t.Fatalf("Current after a failed refresh = %q, want the last known 1.4.2", got)
	}
	if calls != 2 {
		t.Fatalf("lookups = %d, want a refresh past the TTL", calls)
	}
	if got := r.Current(context.Background()); got != "1.4.2" || calls != 2 {
		t.Fatalf("a failed lookup must be cached too: version %q after %d lookups", got, calls)
	}
	now = now.Add(cliVersionRetryTTL + time.Second)
	if r.Current(context.Background()); calls != 3 {
		t.Fatalf("lookups = %d, want a retry past the negative TTL", calls)
	}
}
