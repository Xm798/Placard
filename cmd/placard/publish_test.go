package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempHTML(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const publishOK = `{"id":"abc123","url":"https://placard.example.com/s/abc123","title":"Q3 Report","version":1,"expires_at":null,"create_time":"2026-08-01T12:00:00Z","skill_version":2}`

const publishWithCodeOK = `{"id":"abc123","url":"https://placard.example.com/s/abc123","title":"Q3 Report","version":1,"expires_at":null,"share_code":"042195","create_time":"2026-08-01T12:00:00Z","skill_version":2}`

func TestPublishSendsMultipartFields(t *testing.T) {
	var gotPath string
	var gotTitle, gotExpiry, gotVisibility, gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		gotTitle = r.FormValue("title")
		gotExpiry = r.FormValue("expiry")
		gotVisibility = r.FormValue("visibility")
		gotID = r.FormValue("id")
		_, _ = w.Write([]byte(publishOK))
	}))
	defer srv.Close()

	env, _, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	file := writeTempHTML(t, "<html><body>no title here</body></html>")

	if err := runCLI(env, "--base", srv.URL, "publish", file,
		"--title", "Q4 Summary", "--expiry", "7d", "--visibility", "link"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if gotPath != "/api/publish" {
		t.Errorf("path = %q", gotPath)
	}
	if gotTitle != "Q4 Summary" || gotExpiry != "7d" || gotVisibility != "link" || gotID != "" {
		t.Errorf("fields: title=%q expiry=%q visibility=%q id=%q", gotTitle, gotExpiry, gotVisibility, gotID)
	}
}

// spec §2.1(1): the page's own <title> always beats --title. The warning goes
// to stderr (in --json mode too), never blocks, never changes the exit code.
func TestPublishWarnsWhenPageTitleOverridesFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(publishOK))
	}))
	defer srv.Close()

	env, stdout, stderr := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	file := writeTempHTML(t, "<html><head><title>Q3 Report</title></head></html>")

	err := runCLI(env, "--base", srv.URL, "--json", "publish", file, "--title", "Q4 Summary")
	if err != nil {
		t.Fatalf("warning must not block publish, got %v", err)
	}
	if !strings.Contains(stderr.String(), "Q3 Report") || !strings.Contains(stderr.String(), "Q4 Summary") {
		t.Fatalf("stderr must name both titles, got %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "warning") {
		t.Fatalf("warning leaked into --json stdout: %q", stdout.String())
	}
	if !json.Valid(stdout.Bytes()) {
		t.Fatalf("stdout must remain valid JSON: %q", stdout.String())
	}
}

func TestPublishNoWarningWhenTitlesAgree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(publishOK))
	}))
	defer srv.Close()

	env, _, stderr := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	file := writeTempHTML(t, "<html><head><title>Q4 Summary</title></head></html>")

	if err := runCLI(env, "--base", srv.URL, "publish", file, "--title", "Q4 Summary"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "warning") {
		t.Fatalf("identical titles must not warn, got %q", stderr.String())
	}
}

func TestPublishNoWarningWithoutTitleFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(publishOK))
	}))
	defer srv.Close()

	env, _, stderr := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	file := writeTempHTML(t, "<html><head><title>Q3 Report</title></head></html>")

	if err := runCLI(env, "--base", srv.URL, "publish", file); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "warning") {
		t.Fatalf("no --title means no conflict, got %q", stderr.String())
	}
}

func TestPublishJSONIsVerbatimPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(publishOK))
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	file := writeTempHTML(t, "<html><body>x</body></html>")

	if err := runCLI(env, "--base", srv.URL, "--json", "publish", file); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != publishOK {
		t.Fatalf("got %q, want byte-identical passthrough", stdout.String())
	}
}

func TestPublishHumanOutputGolden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(publishOK))
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	file := writeTempHTML(t, "<html><body>x</body></html>")

	if err := runCLI(env, "--base", srv.URL, "--no-color", "publish", file); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "publish_human.golden", stdout.Bytes())
}

// --password auto: the field reaches the server and the minted code reaches the
// user. It is the only response that ever carries the code, so a golden pins
// the line that prints it.
func TestPublishWithPasswordAutoGolden(t *testing.T) {
	var gotPassword string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		gotPassword = r.FormValue("password")
		_, _ = w.Write([]byte(publishWithCodeOK))
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	file := writeTempHTML(t, "<html><body>x</body></html>")

	if err := runCLI(env, "--base", srv.URL, "--no-color", "publish", file, "--password", "auto"); err != nil {
		t.Fatal(err)
	}
	if gotPassword != "auto" {
		t.Errorf("password field = %q, want auto", gotPassword)
	}
	assertGolden(t, "publish_password_human.golden", stdout.Bytes())
}

// Without the flag the field is absent and the code line never appears.
func TestPublishWithoutPasswordPrintsNoCode(t *testing.T) {
	var sawPassword bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		_, sawPassword = r.MultipartForm.Value["password"]
		_, _ = w.Write([]byte(publishOK))
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	file := writeTempHTML(t, "<html><body>x</body></html>")

	if err := runCLI(env, "--base", srv.URL, "--no-color", "publish", file); err != nil {
		t.Fatal(err)
	}
	if sawPassword {
		t.Error("password field sent without --password")
	}
	if strings.Contains(stdout.String(), "code") {
		t.Errorf("printed a code line for a page without one: %q", stdout.String())
	}
}

// --id makes the server ignore --password, so the CLI says so before the page
// silently keeps whatever code it already had.
func TestPublishWarnsPasswordIgnoredWithID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(publishOK))
	}))
	defer srv.Close()

	env, _, stderr := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	file := writeTempHTML(t, "<html><body>x</body></html>")

	if err := runCLI(env, "--base", srv.URL, "publish", file, "--id", "abc123", "--password", "auto"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "--password") {
		t.Errorf("stderr = %q, want a warning naming --password", stderr.String())
	}
}

func TestPublishMissingFileIsLocalIO(t *testing.T) {
	env, _, _ := newTestEnv(t, "https://placard.example.com")
	err := runCLI(env, "--base", "https://placard.example.com", "publish", "/nope/missing.html")
	var e *Error
	if !asError(err, &e) || e.Code != "local_io" {
		t.Fatalf("want local_io, got %v", err)
	}
	if ExitCode(err) != 1 {
		t.Fatalf("exit = %d, want 1", ExitCode(err))
	}
}

func TestPublishNoArgsIsUsageError(t *testing.T) {
	env, _, _ := newTestEnv(t, "https://placard.example.com")
	if got := ExitCode(runCLI(env, "publish")); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
}

func TestPublish415Message(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(415)
		_, _ = w.Write([]byte(`{"code":"validation","message":"content is not HTML"}`))
	}))
	defer srv.Close()

	env, _, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	file := writeTempHTML(t, "not html at all")
	err := runCLI(env, "--base", srv.URL, "publish", file)
	if err == nil || !strings.Contains(err.Error(), "not HTML") {
		t.Fatalf("got %v", err)
	}
}

func TestPublish429UsesPublishSpecificWording(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"code":"rate_limited","message":"rate limited"}`))
	}))
	defer srv.Close()

	env, _, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	file := writeTempHTML(t, "<html>x</html>")
	err := runCLI(env, "--base", srv.URL, "publish", file)
	if err == nil || !strings.Contains(err.Error(), "hourly publish limit") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "user resolve") {
		t.Fatal("must not borrow the resolve endpoint's wording")
	}
}

var _ = context.Background
