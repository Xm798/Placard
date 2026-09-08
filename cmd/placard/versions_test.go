package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const versionsOK = `{"latest_version":3,"shared_version":0,"versions":[` +
	`{"version":3,"title":"Q3 Report v3","size_bytes":20480,"create_time":"2026-08-01T12:00:00Z"},` +
	`{"version":2,"title":"Q3 Report v2","size_bytes":10240,"create_time":"2026-07-31T12:00:00Z"},` +
	`{"version":1,"title":"Q3 Report","size_bytes":5120,"create_time":"2026-07-30T12:00:00Z"}]}`

func TestVersionLsPassesThroughVerbatim(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(versionsOK))
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--json", "version", "ls", "abc123"); err != nil {
		t.Fatalf("version ls: %v", err)
	}
	if gotPath != "/api/files/abc123/versions" {
		t.Fatalf("path = %q", gotPath)
	}
	if strings.TrimSpace(stdout.String()) != versionsOK {
		t.Fatalf("must be byte-identical passthrough, got %q", stdout.String())
	}
}

func TestVersionLsHumanGolden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(versionsOK))
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--no-color", "version", "ls", "abc123"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "shared_version 0") {
		t.Fatal("the 0 magic number must never surface in human output")
	}
	assertGolden(t, "version_ls_human.golden", stdout.Bytes())
}

func TestParsePinTarget(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"latest", 0, true},
		{"LATEST", 0, true},
		{"1", 1, true},
		{"42", 42, true},
		{"0", 0, false}, // 0 is the internal magic number, not user input
		{"-1", 0, false},
		{"v3", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, err := parsePinTarget(tc.in)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("parsePinTarget(%q) = %d, %v; want %d, nil", tc.in, got, err, tc.want)
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("parsePinTarget(%q) must fail", tc.in)
			} else if ExitCode(err) != 2 {
				t.Errorf("parsePinTarget(%q) exit = %d, want 2", tc.in, ExitCode(err))
			}
		}
	}
}

func TestVersionPinLatestSendsZero(t *testing.T) {
	var gotBody map[string]any
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--json", "version", "pin", "abc123", "latest"); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Fatalf("method = %q, want PATCH", gotMethod)
	}
	if gotBody["shared_version"] != float64(0) {
		t.Fatalf("body = %v, want shared_version 0", gotBody)
	}
	if _, ok := gotBody["visibility"]; ok {
		t.Fatal("pin must not send visibility")
	}
	var got struct {
		ID            string `json:"id"`
		SharedVersion int    `json:"shared_version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("204 must be synthesized: %v (%q)", err, stdout.String())
	}
	if got.ID != "abc123" || got.SharedVersion != 0 {
		t.Fatalf("got %+v; --json keeps the raw 0", got)
	}
}

func TestVersionPinNumberHumanOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--no-color", "version", "pin", "abc123", "2"); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "abc123 pinned to version 2\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestVersionPinLatestHumanSaysLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--no-color", "version", "pin", "abc123", "latest"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "latest") || strings.Contains(stdout.String(), "version 0") {
		t.Fatalf("stdout = %q; humans must never see 0", stdout.String())
	}
}

func TestVersionPinBadTargetIsExit2(t *testing.T) {
	env, _, _ := newTestEnv(t, "https://placard.example.com")
	if got := ExitCode(runCLI(env, "version", "pin", "abc123", "v3")); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
}

func TestVersionRestorePassesThroughPublishShape(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_, _ = w.Write([]byte(publishOK))
	}))
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--json", "version", "restore", "abc123", "2"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/files/abc123/versions/2/restore" {
		t.Fatalf("%s %s", gotMethod, gotPath)
	}
	if strings.TrimSpace(stdout.String()) != publishOK {
		t.Fatalf("restore responses are publish-shaped and must pass through verbatim: %q", stdout.String())
	}
}

func TestVersionRestore429SharesPublishBudgetWording(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"code":"rate_limited","message":"rate limited"}`))
	}))
	defer srv.Close()

	env, _, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	err := runCLI(env, "--base", srv.URL, "version", "restore", "abc123", "2")
	if err == nil || !strings.Contains(err.Error(), "hourly publish limit") {
		t.Fatalf("restore shares publish's hourly budget, got %v", err)
	}
}

func TestVersionRestoreRejectsNonPositive(t *testing.T) {
	env, _, _ := newTestEnv(t, "https://placard.example.com")
	if got := ExitCode(runCLI(env, "version", "restore", "abc123", "0")); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
	if got := ExitCode(runCLI(env, "version", "restore", "abc123", "latest")); got != 2 {
		t.Fatalf("restore takes a concrete version number only, exit = %d, want 2", got)
	}
}
