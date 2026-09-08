package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type deviceServer struct {
	codeStatus    int
	codeBody      string
	tokenBodies   []string
	meBody        string
	tokenCalls    int
	sawAuthOnCode bool
	gotHostname   string
	sawCodeBody   bool
}

func (d *deviceServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	if d.codeStatus == 0 {
		d.codeStatus = 200
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/device/code":
			if _, ok := r.Header["Authorization"]; ok {
				d.sawAuthOnCode = true
			}
			var req struct {
				Hostname string `json:"hostname"`
			}
			raw, _ := io.ReadAll(r.Body)
			d.sawCodeBody = len(raw) > 0
			_ = json.Unmarshal(raw, &req)
			d.gotHostname = req.Hostname
			w.WriteHeader(d.codeStatus)
			_, _ = w.Write([]byte(d.codeBody))
		case "/auth/device/token":
			body := d.tokenBodies[len(d.tokenBodies)-1]
			if d.tokenCalls < len(d.tokenBodies) {
				body = d.tokenBodies[d.tokenCalls]
			}
			d.tokenCalls++
			_, _ = w.Write([]byte(body))
		case "/api/me":
			if got := r.Header.Get("Authorization"); got != "Bearer pl_newtoken" {
				t.Errorf("whoami confirmation must use the NEW token, got %q", got)
			}
			_, _ = w.Write([]byte(d.meBody))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
}

const deviceCodeOK = `{"device_code":"dc_secret","user_code":"K7M2X9PQ","verification_uri":"https://placard.example.com/auth/device","verification_uri_complete":"https://placard.example.com/auth/device?user_code=K7M2X9PQ","expires_in":180,"interval":5}`

func TestLoginStoresTokenBoundToBase(t *testing.T) {
	d := &deviceServer{
		codeBody:    deviceCodeOK,
		tokenBodies: []string{`{"status":"approved","token":"pl_newtoken","name":"CLI on mac"}`},
		meBody:      meBody,
	}
	srv := d.start(t)
	defer srv.Close()

	home := t.TempDir()
	var stdout, stderr strings.Builder
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	env.HTTP = srv.Client()
	env.Sleep = func(d time.Duration) {} // no real waiting in tests

	if err := runCLI(env, "--base", srv.URL, "login"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if d.sawAuthOnCode {
		t.Error("POST /auth/device/code must be called without an Authorization header")
	}

	base, _ := NormalizeBase(srv.URL)
	cfg, err := LoadConfig(testPaths(home))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	entry, ok := cfg.Tokens[base]
	if !ok {
		t.Fatalf("no entry for %s, config = %+v", base, cfg.Tokens)
	}
	if entry.Token != "pl_newtoken" {
		t.Errorf("token = %q", entry.Token)
	}
	if entry.Base != base {
		t.Errorf("entry.Base = %q, want %q (the binding is what makes rule 1 work)", entry.Base, base)
	}
	if entry.CreatedAt != "2026-08-01T12:00:00Z" {
		t.Errorf("CreatedAt = %q, want the injected clock's value", entry.CreatedAt)
	}
	// Without this every later command would need --base again: Placard has no
	// default instance to fall back on.
	if cfg.DefaultBase != base {
		t.Errorf("DefaultBase = %q, want %q", cfg.DefaultBase, base)
	}
}

// The server names the token "CLI on " + sanitize(hostname). Sending no body
// would make every machine's token show up as "CLI on unknown" in /settings —
// and no other test would notice, because the fake server hands back a
// hard-coded name.
func TestLoginSendsHostname(t *testing.T) {
	d := &deviceServer{
		codeBody:    deviceCodeOK,
		tokenBodies: []string{`{"status":"approved","token":"pl_newtoken","name":"CLI on mac"}`},
		meBody:      meBody,
	}
	srv := d.start(t)
	defer srv.Close()

	home := t.TempDir()
	var stdout, stderr strings.Builder
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	env.HTTP = srv.Client()
	env.Sleep = func(d time.Duration) {}

	if err := runCLI(env, "--base", srv.URL, "login"); err != nil {
		t.Fatal(err)
	}
	if !d.sawCodeBody {
		t.Fatal("POST /auth/device/code must carry a JSON body")
	}
	want, err := os.Hostname()
	if err != nil {
		want = ""
	}
	if d.gotHostname != want {
		t.Fatalf("hostname = %q, want %q (report it as-is; the server sanitizes)", d.gotHostname, want)
	}
}

// TestLoginPrintsTheExpiry: the lifetime is chosen in the BROWSER, so the
// terminal is the only place the user can check what they actually approved.
// A server that sends no expires_at (one predating the field) must print
// nothing rather than imply the token never expires.
func TestLoginPrintsTheExpiry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		tokenBody  string
		wantOutput string
		absent     string
	}{
		{
			name:       "expiry present",
			tokenBody:  `{"status":"approved","token":"pl_newtoken","name":"CLI on mac","expires_at":"2026-08-08T12:00:00Z"}`,
			wantOutput: "2026-08-08 12:00:00Z",
		},
		{
			name:      "no expiry field",
			tokenBody: `{"status":"approved","token":"pl_newtoken","name":"CLI on mac"}`,
			absent:    "expires at",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &deviceServer{codeBody: deviceCodeOK, tokenBodies: []string{tc.tokenBody}, meBody: meBody}
			srv := d.start(t)
			defer srv.Close()

			var stdout, stderr strings.Builder
			env := NewEnv(&stdout, &stderr, strings.NewReader(""), t.TempDir(),
				func(string) string { return "" }, testClock, false)
			env.HTTP = srv.Client()
			env.Sleep = func(d time.Duration) {}

			if err := runCLI(env, "--base", srv.URL, "login"); err != nil {
				t.Fatal(err)
			}
			all := stdout.String() + stderr.String()
			if tc.wantOutput != "" && !strings.Contains(all, tc.wantOutput) {
				t.Fatalf("output %q does not contain the expiry %q", all, tc.wantOutput)
			}
			if tc.absent != "" && strings.Contains(all, tc.absent) {
				t.Fatalf("output %q must not claim an expiry the server did not send", all)
			}
		})
	}
}

func TestLoginPrintsUserCodeAndNeverTheToken(t *testing.T) {
	d := &deviceServer{
		codeBody:    deviceCodeOK,
		tokenBodies: []string{`{"status":"approved","token":"pl_newtoken","name":"CLI on mac"}`},
		meBody:      meBody,
	}
	srv := d.start(t)
	defer srv.Close()

	home := t.TempDir()
	var stdout, stderr strings.Builder
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	env.HTTP = srv.Client()
	env.Sleep = func(d time.Duration) {}

	if err := runCLI(env, "--base", srv.URL, "login"); err != nil {
		t.Fatal(err)
	}
	all := stdout.String() + stderr.String()
	if !strings.Contains(all, "K7M2X9PQ") {
		t.Fatalf("the user_code must be shown prominently, got %q", all)
	}
	if !strings.Contains(all, "Verify") {
		t.Fatalf("must tell the user to compare the code with the browser, got %q", all)
	}
	if strings.Contains(all, "pl_newtoken") {
		t.Fatal("the PAT plaintext must never be printed")
	}
	if strings.Contains(all, "dc_secret") {
		t.Fatal("the device_code must never be printed: whoever holds it can take the token")
	}
}

func TestLoginJSONShape(t *testing.T) {
	d := &deviceServer{
		codeBody:    deviceCodeOK,
		tokenBodies: []string{`{"status":"approved","token":"pl_newtoken","name":"CLI on mac"}`},
		meBody:      meBody,
	}
	srv := d.start(t)
	defer srv.Close()

	home := t.TempDir()
	var stdout, stderr strings.Builder
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	env.HTTP = srv.Client()
	env.Sleep = func(d time.Duration) {}

	if err := runCLI(env, "--base", srv.URL, "--json", "login"); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &got); err != nil {
		t.Fatalf("unmarshal: %v (%q)", err, stdout.String())
	}
	if got["authz_id"] != "u_ownerownerowner" || got["token_name"] != "CLI on mac" {
		t.Fatalf("got %v", got)
	}
	if _, ok := got["token"]; ok {
		t.Fatal("the JSON envelope must never carry the token")
	}
}

func TestLoginServerTooOldExit3(t *testing.T) {
	d := &deviceServer{
		codeStatus: 404,
		codeBody:   `{"code":"error","message":"Cannot POST /auth/device/code"}`,
	}
	srv := d.start(t)
	defer srv.Close()

	home := t.TempDir()
	var stdout, stderr strings.Builder
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	env.HTTP = srv.Client()
	env.Sleep = func(d time.Duration) {}

	err := runCLI(env, "--base", srv.URL, "login")
	var e *Error
	if !asError(err, &e) || e.Code != "server_too_old" {
		t.Fatalf("want server_too_old, got %v", err)
	}
	if e.Exit != 3 {
		t.Fatalf("exit = %d, want 3 for login", e.Exit)
	}
	if !strings.Contains(e.Message, "PLACARD_TOKEN") || !strings.Contains(e.Message, "/settings") {
		t.Fatalf("message must offer the PAT fallback, got %q", e.Message)
	}
	if strings.Contains(e.Message, "Cannot POST") {
		t.Fatal("Fiber's raw message must not be surfaced")
	}
}

func TestLoginPermissionDeniedIdentifiesServerSideFailure(t *testing.T) {
	d := &deviceServer{
		codeStatus: 403,
		codeBody:   `{"code":"permission_denied","message":"permission denied"}`,
	}
	srv := d.start(t)
	defer srv.Close()

	home := t.TempDir()
	var stdout, stderr strings.Builder
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	env.HTTP = srv.Client()

	err := runCLI(env, "--base", srv.URL, "login")
	var e *Error
	if !asError(err, &e) || e.Code != "permission_denied" {
		t.Fatalf("want permission_denied, got %v", err)
	}
	if !strings.Contains(e.Message, "Placard server") ||
		!strings.Contains(e.Message, "not a local file permission error") {
		t.Fatalf("message must identify a server-side failure, got %q", e.Message)
	}
	if strings.TrimSpace(e.Message) == "permission denied" {
		t.Fatal("ambiguous server message must not pass through unchanged")
	}
}

// runLoginCapturingOpenURL runs a full login against a fake server serving
// codeBody, and reports which URL the CLI handed to the browser plus everything
// it printed.
func runLoginCapturingOpenURL(t *testing.T, codeBody string) (openedURL, output string) {
	t.Helper()
	d := &deviceServer{
		codeBody:    codeBody,
		tokenBodies: []string{`{"status":"approved","token":"pl_newtoken"}`},
		meBody:      meBody,
	}
	srv := d.start(t)
	defer srv.Close()

	var stdout, stderr strings.Builder
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), t.TempDir(),
		func(string) string { return "" }, testClock, false)
	env.HTTP = srv.Client()
	env.Sleep = func(time.Duration) {}
	env.OpenURL = func(_ *Env, u string) (bool, error) { openedURL = u; return true, nil }

	if err := runCLI(env, "--base", srv.URL, "login"); err != nil {
		t.Fatal(err)
	}
	return openedURL, stdout.String() + stderr.String()
}

// TestLoginOpensTheURITheServerChose pins both directions of dto's OpenURI rule.
//
// Opening the bare URI when a complete one was offered still logs in fine, and
// synthesizing a complete one the server withheld also "works" — both regress
// silently, so these assertions are the only thing standing between the
// intended shape and either mistake.
func TestLoginOpensTheURITheServerChose(t *testing.T) {
	const bare = "https://placard.example.com/auth/device"
	// Same response as deviceCodeOK minus verification_uri_complete: a server
	// that omits the field is asking for the manual-entry page.
	const noComplete = `{"device_code":"dc_secret","user_code":"K7M2X9PQ","verification_uri":"` +
		bare + `","expires_in":180,"interval":5}`

	tests := []struct {
		name     string
		codeBody string
		wantURL  string
		wantText string
	}{
		{"complete URI offered", deviceCodeOK, bare + "?user_code=K7M2X9PQ", "Confirm in your browser"},
		{"complete URI withheld", noComplete, bare, "type the code above"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			openedURL, output := runLoginCapturingOpenURL(t, tt.codeBody)
			if openedURL != tt.wantURL {
				t.Fatalf("opened %q, want %q", openedURL, tt.wantURL)
			}
			if !strings.Contains(output, tt.wantText) {
				t.Fatalf("output must contain %q, got %q", tt.wantText, output)
			}
		})
	}
}
