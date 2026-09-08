package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogoutClearsOnlyTheCurrentBase(t *testing.T) {
	home := t.TempDir()
	staging := "https://staging.placard.example.com"
	prod := "https://placard.example.com"
	cfg := &Config{Tokens: map[string]TokenEntry{
		prod:    {Token: "pl_prod", Base: prod},
		staging: {Token: "pl_staging", Base: staging},
	}}
	if err := SaveConfig(testPaths(home), cfg); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	if err := runCLI(env, "--base", prod, "--json", "logout"); err != nil {
		t.Fatalf("logout: %v", err)
	}
	got, err := LoadConfig(testPaths(home))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Tokens[prod]; ok {
		t.Error("prod entry must be gone")
	}
	if _, ok := got.Tokens[staging]; !ok {
		t.Error("staging entry must survive: logout is per-base")
	}
	var envelope struct {
		Base    string `json:"base"`
		Cleared bool   `json:"cleared"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Base != prod || !envelope.Cleared {
		t.Fatalf("got %+v", envelope)
	}
}

// Logging out of the remembered server must not leave later commands pointing
// at credentials that are gone. One remaining login is an unambiguous
// replacement; several are not, and then nothing is guessed.
func TestLogoutRepointsTheDefaultBase(t *testing.T) {
	const prod, staging, dev = "https://prod.example.com", "https://staging.example.com", "https://dev.example.com"
	for _, tc := range []struct {
		name      string
		remaining []string
		want      string
	}{
		{"one login left becomes the default", []string{staging}, staging},
		{"several left, none is guessed", []string{staging, dev}, ""},
		{"none left clears it", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cfg := &Config{Tokens: map[string]TokenEntry{prod: {Token: "pl_prod", Base: prod}}, DefaultBase: prod}
			for _, b := range tc.remaining {
				cfg.Tokens[b] = TokenEntry{Token: "pl_other", Base: b}
			}
			if err := SaveConfig(testPaths(home), cfg); err != nil {
				t.Fatal(err)
			}

			var stdout, stderr strings.Builder
			env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
				func(string) string { return "" }, testClock, false)
			if err := runCLI(env, "--base", prod, "logout"); err != nil {
				t.Fatalf("logout: %v", err)
			}
			got, err := LoadConfig(testPaths(home))
			if err != nil {
				t.Fatal(err)
			}
			if got.DefaultBase != tc.want {
				t.Fatalf("DefaultBase = %q, want %q", got.DefaultBase, tc.want)
			}
		})
	}
}

// Reporting success while a stored token stays on disk is worse than
// refusing: logout must know which server it is clearing.
func TestLogoutWithoutAServerRefuses(t *testing.T) {
	home := t.TempDir()
	seedConfig(t, home, "https://a.example.com", "pl_a")
	seedConfigEntry(t, home, "https://b.example.com", "pl_b")

	var stdout, stderr strings.Builder
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	err := runCLI(env, "logout")

	var e *Error
	if !asError(err, &e) || e.Code != "usage" {
		t.Fatalf("err = %v, want a usage error", err)
	}
	cfg, lerr := LoadConfig(testPaths(home))
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(cfg.Tokens) != 2 {
		t.Fatalf("nothing may be cleared, tokens = %v", cfg.Tokens)
	}
}

func TestLogoutOnAbsentEntryIsStillSuccess(t *testing.T) {
	home := t.TempDir()
	var stdout, stderr strings.Builder
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	if err := runCLI(env, "--base", "https://placard.example.com", "logout"); err != nil {
		t.Fatalf("logout must be idempotent, got %v", err)
	}
}

func TestLogoutWarnsAboutEnvTokenItCannotClear(t *testing.T) {
	home := t.TempDir()
	var stdout, stderr strings.Builder
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home, func(k string) string {
		if k == "PLACARD_TOKEN" {
			return "pl_env"
		}
		return ""
	}, testClock, false)
	if err := runCLI(env, "--base", "https://placard.example.com", "logout"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "PLACARD_TOKEN") {
		t.Errorf("must warn that the env var still provides a token, stderr = %q", stderr.String())
	}
}

// meBody is a GET /api/me response for a PAT-authenticated caller: the display
// profile is always empty on that channel.
const meBody = `{"display_name":"","avatar_url":"","authz_id":"u_ownerownerowner"}`

func TestWhoamiJSONIsVerbatimPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(meBody))
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--json", "whoami"); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != meBody {
		t.Fatalf("got %q, want verbatim passthrough", stdout.String())
	}
}

func TestWhoamiHumanDoesNotShowAnEmptyName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(meBody))
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--no-color", "whoami"); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, "u_ownerownerowner") {
		t.Fatalf("authz_id must be shown, got %q", out)
	}
	if !strings.Contains(out, "server returned no display profile") {
		t.Fatalf("the empty display_name must be explained, got %q", out)
	}
	if strings.Contains(out, "name  \n") || strings.Contains(out, "display_name: \n") {
		t.Fatalf("must not render an empty name field, got %q", out)
	}
}

func TestWhoamiShowsNameWhenPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"display_name":"Alice","avatar_url":"","authz_id":"u_x"}`))
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--no-color", "whoami"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Alice") {
		t.Fatalf("got %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "server returned no display profile") {
		t.Fatal("the note is only for the empty case")
	}
}

func TestWhoamiWithoutCredentialsIsExit3(t *testing.T) {
	home := t.TempDir()
	var stdout, stderr strings.Builder
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	err := runCLI(env, "--base", "https://placard.example.com", "whoami")
	if ExitCode(err) != 3 {
		t.Fatalf("exit = %d, want 3", ExitCode(err))
	}
}
