package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeRunner(goos string, env map[string]string, missing map[string]bool, runErr error, log *[]string) browserRunner {
	return browserRunner{
		Getenv: func(k string) string { return env[k] },
		GOOS:   goos,
		Look: func(name string) (string, error) {
			if missing[name] {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + name, nil
		},
		Run: func(name string, args ...string) error {
			*log = append(*log, name+" "+strings.Join(args, " "))
			return runErr
		},
	}
}

func TestBrowserEnvVarWinsFirst(t *testing.T) {
	var log []string
	b := fakeRunner("linux", map[string]string{"BROWSER": "firefox", "DISPLAY": ":0"}, nil, nil, &log)
	opened, err := b.Open("https://x/s/abc")
	if err != nil || !opened {
		t.Fatalf("opened=%v err=%v", opened, err)
	}
	if len(log) != 1 || !strings.HasPrefix(log[0], "firefox ") {
		t.Fatalf("log = %v, want $BROWSER first", log)
	}
}

func TestBrowserPlatformDefaults(t *testing.T) {
	cases := []struct{ goos, wantPrefix string }{
		{"darwin", "open "},
		{"windows", "rundll32 "},
		{"linux", "xdg-open "},
	}
	for _, tc := range cases {
		var log []string
		env := map[string]string{}
		if tc.goos == "linux" {
			env["DISPLAY"] = ":0"
		}
		b := fakeRunner(tc.goos, env, nil, nil, &log)
		opened, err := b.Open("https://x/s/abc")
		if err != nil || !opened {
			t.Fatalf("%s: opened=%v err=%v", tc.goos, opened, err)
		}
		if len(log) != 1 || !strings.HasPrefix(log[0], tc.wantPrefix) {
			t.Fatalf("%s: log = %v, want prefix %q", tc.goos, log, tc.wantPrefix)
		}
	}
}

func TestBrowserHeadlessDetection(t *testing.T) {
	cases := []struct {
		name string
		goos string
		env  map[string]string
	}{
		{"linux without display", "linux", map[string]string{}},
		{"ssh session", "darwin", map[string]string{"SSH_CONNECTION": "**.*.*.* 22 **.*.*.* 22"}},
	}
	for _, tc := range cases {
		var log []string
		b := fakeRunner(tc.goos, tc.env, nil, nil, &log)
		opened, err := b.Open("https://x/s/abc")
		if err != nil {
			t.Fatalf("%s: headless must not be an error, got %v", tc.name, err)
		}
		if opened {
			t.Fatalf("%s: must degrade, not open", tc.name)
		}
		if len(log) != 0 {
			t.Fatalf("%s: nothing should be executed, got %v", tc.name, log)
		}
	}
}

func TestBrowserWaylandCountsAsGraphical(t *testing.T) {
	var log []string
	b := fakeRunner("linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, nil, nil, &log)
	opened, _ := b.Open("https://x/s/abc")
	if !opened {
		t.Fatal("WAYLAND_DISPLAY alone must count as a graphical session")
	}
}

func TestBrowserMissingCommandDegrades(t *testing.T) {
	var log []string
	b := fakeRunner("linux", map[string]string{"DISPLAY": ":0"}, map[string]bool{"xdg-open": true}, nil, &log)
	opened, err := b.Open("https://x/s/abc")
	if err != nil || opened {
		t.Fatalf("opened=%v err=%v, want graceful degradation", opened, err)
	}
}

func TestBrowserNonZeroExitDegrades(t *testing.T) {
	var log []string
	b := fakeRunner("darwin", map[string]string{}, nil, errors.New("exit status 1"), &log)
	opened, err := b.Open("https://x/s/abc")
	if err != nil || opened {
		t.Fatalf("opened=%v err=%v, want graceful degradation", opened, err)
	}
}

func TestOpenCommandPrintsURLAndExitsZeroWhenHeadless(t *testing.T) {
	env, stdout, stderr := newTestEnv(t, "https://placard.example.com")
	env.Getenv = func(k string) string {
		if k == "SSH_CONNECTION" {
			return "**.*.*.* 22 **.*.*.* 22"
		}
		return ""
	}
	if err := runCLI(env, "--base", "https://placard.example.com", "open", "abc123"); err != nil {
		t.Fatalf("headless open must exit 0, got %v", err)
	}
	if !strings.Contains(stdout.String(), "https://placard.example.com/s/abc123") {
		t.Fatalf("stdout must carry the URL, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "No browser available") {
		t.Fatalf("stderr must explain the degradation, got %q", stderr.String())
	}
}

func TestOpenJSONReportsOpenedFalse(t *testing.T) {
	env, stdout, _ := newTestEnv(t, "https://placard.example.com")
	env.Getenv = func(k string) string {
		if k == "SSH_CONNECTION" {
			return "x"
		}
		return ""
	}
	if err := runCLI(env, "--base", "https://placard.example.com", "--json", "open", "abc123"); err != nil {
		t.Fatal(err)
	}
	var got struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Opened bool   `json:"opened"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v (%q)", err, stdout.String())
	}
	if got.ID != "abc123" || got.Opened || got.URL != "https://placard.example.com/s/abc123" {
		t.Fatalf("got %+v", got)
	}
}

// Every command that opens a URL must go through Env.OpenURL, not the
// package-level openURL: that field is the only seam a test can stub, and a
// direct call spawns a real browser on the developer's machine during
// `make gates`. TestMain's default stub hides the symptom, so this test guards
// the invariant itself — it fails if `open` or `publish --open` is ever wired
// back to openURL directly.
func TestCommandsOpenThroughTheInjectedSeam(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		env, _, _ := newTestEnv(t, "https://placard.example.com")
		var got []string
		env.OpenURL = func(_ *Env, u string) (bool, error) { got = append(got, u); return true, nil }

		if err := runCLI(env, "--base", "https://placard.example.com", "open", "abc123"); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0] != "https://placard.example.com/s/abc123" {
			t.Fatalf("env.OpenURL calls = %v, want the share URL exactly once", got)
		}
	})

	t.Run("publish --open", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(publishOK))
		}))
		defer srv.Close()

		env, _, _ := newTestEnv(t, srv.URL)
		env.HTTP = srv.Client()
		var got []string
		env.OpenURL = func(_ *Env, u string) (bool, error) { got = append(got, u); return true, nil }
		file := writeTempHTML(t, "<html><body>x</body></html>")

		if err := runCLI(env, "--base", srv.URL, "publish", file, "--open"); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0] != "https://placard.example.com/s/abc123" {
			t.Fatalf("env.OpenURL calls = %v, want the published URL exactly once", got)
		}
	})
}

// spec §11.5: /s/* is BrowserOnly, so open must never try to fetch the page.
func TestOpenMakesNoHTTPRequest(t *testing.T) {
	env, _, _ := newTestEnv(t, "https://placard.example.com")
	env.Getenv = func(k string) string {
		if k == "SSH_CONNECTION" {
			return "x"
		}
		return ""
	}
	env.HTTP = nil // any HTTP attempt would nil-panic
	if err := runCLI(env, "--base", "https://placard.example.com", "open", "abc123"); err != nil {
		t.Fatalf("open must be purely local, got %v", err)
	}
}
