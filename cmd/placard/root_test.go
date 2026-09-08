package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Xm798/placard/internal/version"
)

// TestMain neutralises the browser for the WHOLE package before any test runs.
// Without it, every test that reaches `login` / `open` / `publish --open` with
// an unstubbed Env.OpenURL shells out to the real `open`/`xdg-open` and pops a
// browser tab on the developer's machine — `make gates` opened 8 of them
// (4 per test binary, and test-all builds the package twice: once plain, once
// with -tags=integration).
//
// This is deliberately a whitelist: the default is "no browser", and a test
// that actually wants to observe the call assigns its own env.OpenURL (see
// TestLoginNeverBuildsAVerificationURIWithTheCode). Returning false — the
// headless outcome — keeps the degradation branch the printed-URL tests assert
// on; the real degradation matrix is covered by browser_test.go's fakeRunner,
// which never touches this seam.
func TestMain(m *testing.M) {
	openURL = func(*Env, string) (bool, error) { return false, nil }
	os.Exit(m.Run())
}

// testClock is the pinned clock every golden test uses (spec §12.2 item 1).
var testClock = func() time.Time {
	return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
}

// newTestEnv builds an Env whose home is a temp dir pre-seeded with a token
// bound to srvURL, so `--base <srvURL>` (strict mode) resolves credentials.
func newTestEnv(t *testing.T, srvURL string) (*Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	if srvURL != "" {
		base, err := NormalizeBase(srvURL)
		if err != nil {
			t.Fatalf("NormalizeBase(%q): %v", srvURL, err)
		}
		seedConfig(t, home, base, "pl_test")
	}
	var stdout, stderr bytes.Buffer
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	env.HTTP = &http.Client{Timeout: 5 * time.Second}
	return env, &stdout, &stderr
}

func runCLI(env *Env, args ...string) error {
	return Run(env, args)
}

func TestVersionFlagPrintsRawVersion(t *testing.T) {
	env, stdout, _ := newTestEnv(t, "")
	if err := runCLI(env, "--version"); err != nil {
		t.Fatalf("--version: %v", err)
	}
	if !strings.Contains(stdout.String(), version.Version) {
		t.Fatalf("stdout = %q, want it to contain %q", stdout.String(), version.Version)
	}
	// version.Public() adds a master-<commit> form that is neither semver nor a
	// local-build marker; the CLI must never use it (spec §8.2).
	if strings.Contains(stdout.String(), "master-") && !strings.Contains(version.Version, "master-") {
		t.Fatal("CLI must print version.Version, not version.Public()")
	}
}

func TestUnknownCommandIsExit2(t *testing.T) {
	env, _, _ := newTestEnv(t, "")
	err := runCLI(env, "frobnicate")
	if ExitCode(err) != 2 {
		t.Fatalf("exit = %d, want 2 (usage), err = %v", ExitCode(err), err)
	}
}

func TestBadBaseIsExit2(t *testing.T) {
	env, _, _ := newTestEnv(t, "")
	err := runCLI(env, "--base", "placard.example.com", "ls")
	if ExitCode(err) != 2 {
		t.Fatalf("exit = %d, want 2, err = %v", ExitCode(err), err)
	}
}

func TestExplicitBaseIsDetected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[],"total":0,"page":1,"page_size":100}`))
	}))
	defer srv.Close()

	// No config entry, but PLACARD_TOKEN is set: with an explicit --base the
	// unbound env token must NOT be used (spec §4.1 rule 2).
	home := t.TempDir()
	var stdout, stderr bytes.Buffer
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home, func(k string) string {
		if k == "PLACARD_TOKEN" {
			return "pl_prod_from_env"
		}
		return ""
	}, testClock, false)
	env.HTTP = srv.Client()

	err := runCLI(env, "--base", srv.URL, "ls")
	if ExitCode(err) != 3 {
		t.Fatalf("exit = %d, want 3 (no_credentials), err = %v", ExitCode(err), err)
	}
}

func TestPlacardURLEnvDoesNotTriggerStrictMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer pl_env" {
			t.Errorf("Authorization = %q, want Bearer pl_env", got)
		}
		_, _ = w.Write([]byte(`{"files":[],"total":0,"page":1,"page_size":100}`))
	}))
	defer srv.Close()

	home := t.TempDir()
	var stdout, stderr bytes.Buffer
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home, func(k string) string {
		switch k {
		case "PLACARD_URL":
			return srv.URL
		case "PLACARD_TOKEN":
			return "pl_env"
		}
		return ""
	}, testClock, false)
	env.HTTP = srv.Client()

	if err := runCLI(env, "ls"); err != nil {
		t.Fatalf("PLACARD_URL alone must keep the env token usable, got %v", err)
	}
}

func TestJSONErrorGoesToStdoutAndKeepsExitCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthorized","message":"unauthorized"}`))
	}))
	defer srv.Close()

	env, stdout, stderr := newTestEnv(t, srv.URL)
	err := runCLI(env, "--base", srv.URL, "--json", "ls")
	if ExitCode(err) != 3 {
		t.Fatalf("exit = %d, want 3", ExitCode(err))
	}
	env.Out.EmitError(err) // main.go does this; assert the shape here
	if !json.Valid(stdout.Bytes()) {
		t.Fatalf("json-mode stdout must stay valid JSON on the error path: %q", stdout.String())
	}
	var got struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error.Code != "unauthorized" {
		t.Fatalf("code = %q", got.Error.Code)
	}
	_ = stderr
}

// applyGlobals runs as PersistentPreRunE, i.e. only when a real command
// executes — --version and --help short-circuit inside cobra before it. So this
// must drive a real command, otherwise it would pass vacuously.
func TestNoColorEnvIsHonored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[],"total":0,"page":1,"page_size":100}`))
	}))
	defer srv.Close()

	base, err := NormalizeBase(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	seedConfig(t, home, base, "pl_test")

	var stdout, stderr bytes.Buffer
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home, func(k string) string {
		if k == "NO_COLOR" {
			return "1"
		}
		return ""
	}, testClock, true /* isTTY */)
	env.HTTP = srv.Client()

	if err := runCLI(env, "--base", srv.URL, "ls"); err != nil {
		t.Fatal(err)
	}
	if env.Out.Color {
		t.Fatal("NO_COLOR must win over TTY detection")
	}

	// And a TTY without NO_COLOR does enable colour, so the assertion above is
	// not passing for the wrong reason.
	var out2, err2 bytes.Buffer
	env2 := NewEnv(&out2, &err2, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, true)
	env2.HTTP = srv.Client()
	if err := runCLI(env2, "--base", srv.URL, "ls"); err != nil {
		t.Fatal(err)
	}
	if !env2.Out.Color {
		t.Fatal("a TTY without NO_COLOR must enable colour")
	}
}

func TestAnonClientHasNoToken(t *testing.T) {
	env, _, _ := newTestEnv(t, "")
	env.Base = "https://placard.example.com"
	c, err := env.AnonClient()
	if err != nil {
		t.Fatalf("AnonClient: %v", err)
	}
	if c.Token != "" {
		t.Fatalf("AnonClient token = %q, want empty", c.Token)
	}
}

var _ io.Reader = (*bytes.Buffer)(nil)

// Placard has no default instance, so a command that needs a server and was
// given none must say so — and say how to fix it — rather than reach for
// somebody else's deployment.
func TestNoConfiguredServerIsAUsageError(t *testing.T) {
	env, _, _ := newTestEnv(t, "")
	err := runCLI(env, "ls")

	var e *Error
	if !asError(err, &e) || e.Code != "usage" || e.Exit != 2 {
		t.Fatalf("err = %v, want a usage error (exit 2)", err)
	}
	for _, want := range []string{"--base", "PLACARD_URL", "placard login"} {
		if !strings.Contains(e.Message, want) {
			t.Errorf("message %q must mention %q", e.Message, want)
		}
	}
}

// Commands that never talk to a server keep working without one.
func TestUpdateNeedsNoConfiguredServer(t *testing.T) {
	env, stdout, _ := newTestEnv(t, "")
	prevNew, prevVer := newUpdater, currentVersion
	newUpdater = func(*Env) Updater { return &fakeUpdater{latest: "9.9.9"} }
	currentVersion = func() string { return "1.0.0" }
	t.Cleanup(func() { newUpdater, currentVersion = prevNew, prevVer })

	if err := runCLI(env, "--json", "update"); err != nil {
		t.Fatalf("update must not require a base, got %v", err)
	}
	if !strings.Contains(stdout.String(), "9.9.9") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

// The server of the last login is what a bare command talks to; --base and
// PLACARD_URL still outrank it.
func TestStoredDefaultBaseIsUsedWhenNoneIsGiven(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer pl_test" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"files":[],"total":0,"page":1,"page_size":100}`))
	}))
	defer srv.Close()

	base, err := NormalizeBase(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	seedConfig(t, home, base, "pl_test")
	cfg, err := LoadConfig(testPaths(home))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultBase = base
	if err := SaveConfig(testPaths(home), cfg); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	env.HTTP = srv.Client()

	if err := runCLI(env, "ls"); err != nil {
		t.Fatalf("ls with a stored default base: %v", err)
	}
	if env.Base != base {
		t.Fatalf("env.Base = %q, want %q", env.Base, base)
	}
}

// A config written before default_base existed carries only tokens. One login
// is an unambiguous answer, so an upgrade must not force everyone to log in
// again; two are ambiguous and the CLI asks rather than guesses.
func TestSingleStoredLoginIsTheFallbackBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[],"total":0,"page":1,"page_size":100}`))
	}))
	defer srv.Close()
	base, err := NormalizeBase(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	seedConfig(t, home, base, "pl_test") // no default_base, as an older CLI wrote it
	var stdout, stderr bytes.Buffer
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	env.HTTP = srv.Client()
	if err := runCLI(env, "ls"); err != nil {
		t.Fatalf("a single stored login must be enough, got %v", err)
	}

	// A second login makes it ambiguous, and ambiguity is a question, not a coin flip.
	seedConfigEntry(t, home, "https://other.example.com", "pl_other")
	var out2, err2 bytes.Buffer
	env2 := NewEnv(&out2, &err2, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	env2.HTTP = srv.Client()
	var e *Error
	if lerr := runCLI(env2, "ls"); !asError(lerr, &e) || e.Code != "usage" {
		t.Fatalf("err = %v, want a usage error naming how to pick a server", lerr)
	}
}
