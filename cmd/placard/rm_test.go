package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRmSendsDeleteAndSynthesizesJSON(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--json", "rm", "-y", "abc123"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/files/abc123" {
		t.Fatalf("%s %s", gotMethod, gotPath)
	}
	var got struct {
		ID      string `json:"id"`
		Deleted bool   `json:"deleted"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("204 must be synthesized into a minimal object: %v (%q)", err, stdout.String())
	}
	if got.ID != "abc123" || !got.Deleted {
		t.Fatalf("got %+v", got)
	}
}

func TestRmHumanOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--no-color", "rm", "-y", "abc123"); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "Deleted: abc123\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRm404IsIndistinguishableMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"code":"not_found","message":"not found"}`))
	}))
	defer srv.Close()

	env, _, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	err := runCLI(env, "--base", srv.URL, "rm", "-y", "abc123")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("got %v", err)
	}
	if ExitCode(err) != 1 {
		t.Fatalf("exit = %d, want 1", ExitCode(err))
	}
}

// rmConfirmServer answers the prompt's version lookup and records whether a
// DELETE was ever sent.
func rmConfirmServer(t *testing.T, title string, versions int) (*httptest.Server, *bool) {
	t.Helper()
	deleted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		items := make([]map[string]any, 0, versions)
		for i := versions; i >= 1; i-- {
			items = append(items, map[string]any{"version": i, "title": title, "size_bytes": 10})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"latest_version": versions, "shared_version": 0, "versions": items,
		})
	}))
	return srv, &deleted
}

// Deletion is unrecoverable, so no terminal must mean refusal, not assumed
// consent — otherwise -y would be meaningless in the mode it exists for.
func TestRmWithoutTTYRefusesWithoutYes(t *testing.T) {
	srv, deleted := rmConfirmServer(t, "Report", 1)
	defer srv.Close()

	env, _, _ := newTestEnv(t, srv.URL) // newTestEnv is non-TTY
	env.HTTP = srv.Client()
	err := runCLI(env, "--base", srv.URL, "rm", "abc123")
	if err == nil || !strings.Contains(err.Error(), "-y") {
		t.Fatalf("want an error naming -y, got %v", err)
	}
	if ExitCode(err) != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", ExitCode(err))
	}
	if *deleted {
		t.Fatal("no DELETE may be sent when confirmation was never given")
	}
}

// Consent must come from a person, not a pipe. `echo y | placard rm <id>`
// leaves stdout a terminal, so gating on stdout's terminal-ness would read the
// "y" straight off the pipe and delete unprompted.
func TestRmPipedStdinIsNotConsent(t *testing.T) {
	srv, deleted := rmConfirmServer(t, "Report", 1)
	defer srv.Close()

	env, _, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	env.IsTTY = true // stdout is a terminal (colour)...
	env.StdinTTY = false
	env.Stdin = strings.NewReader("y\n") // ...but the answer comes from a pipe
	err := runCLI(env, "--base", srv.URL, "rm", "abc123")
	if ExitCode(err) != 2 {
		t.Fatalf("exit = %d, want 2: piped stdin must not count as confirmation", ExitCode(err))
	}
	if *deleted {
		t.Fatal("a pipe must never satisfy the confirmation prompt")
	}
}

// --json must never block on stdin, so it needs -y even on a TTY.
func TestRmJSONOnTTYStillRequiresYes(t *testing.T) {
	srv, deleted := rmConfirmServer(t, "Report", 1)
	defer srv.Close()

	env, _, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	env.StdinTTY = true
	env.Stdin = strings.NewReader("y\n")
	if err := runCLI(env, "--base", srv.URL, "--json", "rm", "abc123"); ExitCode(err) != 2 {
		t.Fatalf("exit = %d, want 2", ExitCode(err))
	}
	if *deleted {
		t.Fatal("--json must not delete on an unconfirmed run")
	}
}

func TestRmPromptAcceptedDeletesAndNamesThePage(t *testing.T) {
	srv, deleted := rmConfirmServer(t, "Q3 Report", 3)
	defer srv.Close()

	env, stdout, stderr := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	env.StdinTTY = true
	env.Stdin = strings.NewReader("y\n")
	if err := runCLI(env, "--base", srv.URL, "--no-color", "rm", "abc123"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if !*deleted {
		t.Fatal("a confirmed rm must send DELETE")
	}
	// The prompt must identify the page, not echo back the id alone.
	if p := stderr.String(); !strings.Contains(p, `"Q3 Report"`) || !strings.Contains(p, "3 versions") {
		t.Fatalf("prompt did not describe the page: %q", p)
	}
	if stdout.String() != "Deleted: abc123\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRmPromptDeclinedSendsNoDelete(t *testing.T) {
	srv, deleted := rmConfirmServer(t, "Report", 1)
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	env.StdinTTY = true
	env.Stdin = strings.NewReader("n\n")
	err := runCLI(env, "--base", srv.URL, "rm", "abc123")
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("want an abort error, got %v", err)
	}
	if *deleted {
		t.Fatal("declining the prompt must send no DELETE")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

// Empty input (bare Enter) is a decline: the prompt is [y/N].
func TestRmPromptDefaultsToNo(t *testing.T) {
	srv, deleted := rmConfirmServer(t, "Report", 1)
	defer srv.Close()

	env, _, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	env.StdinTTY = true
	env.Stdin = strings.NewReader("\n")
	if err := runCLI(env, "--base", srv.URL, "rm", "abc123"); err == nil {
		t.Fatal("bare Enter must not delete")
	}
	if *deleted {
		t.Fatal("bare Enter must send no DELETE")
	}
}

// -y must not spend a request on the prompt's lookup.
func TestRmWithYesSkipsVersionLookup(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	env, _, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "rm", "-y", "abc123"); err != nil {
		t.Fatalf("rm -y: %v", err)
	}
	if len(methods) != 1 || methods[0] != "DELETE /api/files/abc123" {
		t.Fatalf("requests = %v, want exactly one DELETE", methods)
	}
}

// A prompt lookup that fails must not abort: DELETE stays authoritative.
func TestRmPromptSurvivesVersionLookupFailure(t *testing.T) {
	deleted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(500)
	}))
	defer srv.Close()

	env, _, stderr := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	env.StdinTTY = true
	env.Stdin = strings.NewReader("y\n")
	if err := runCLI(env, "--base", srv.URL, "rm", "abc123"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if !deleted {
		t.Fatal("a confirmed rm must still delete when the lookup failed")
	}
	if !strings.Contains(stderr.String(), "abc123") {
		t.Fatalf("prompt should fall back to the bare id: %q", stderr.String())
	}
}

func TestRmRequiresExactlyOneArg(t *testing.T) {
	env, _, _ := newTestEnv(t, "https://placard.example.com")
	if got := ExitCode(runCLI(env, "rm")); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
	if got := ExitCode(runCLI(env, "rm", "a", "b")); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
}
