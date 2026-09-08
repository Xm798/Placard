package main

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Xm798/placard/internal/version"
)

func TestClientSendsBearerAndAcceptsJSON(t *testing.T) {
	var gotAuth, gotAccept, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total":0,"files":[]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "pl_test", srv.Client())
	q := url.Values{"page": {"1"}, "page_size": {"100"}}
	raw, err := c.DoJSON(context.Background(), http.MethodGet, "/api/files", q, nil, nil)
	if err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if gotAuth != "Bearer pl_test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q", gotAccept)
	}
	if gotQuery != "page=1&page_size=100" {
		t.Errorf("query = %q", gotQuery)
	}
	if !json.Valid(raw) {
		t.Errorf("raw body not JSON: %q", raw)
	}
}

func TestClientOmitsAuthorizationWhenNoToken(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", srv.Client())
	if _, err := c.DoJSON(context.Background(), http.MethodPost, "/auth/device/code", nil, nil, nil); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if hadAuth {
		t.Fatal("device-code endpoints must be called without an Authorization header")
	}
}

func TestClientDecodesInto(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"abc123","url":"https://x/s/abc123","title":"T","version":2,"expires_at":null,"create_time":"2026-08-01T00:00:00Z","skill_version":2}`))
	}))
	defer srv.Close()

	var resp struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	c := NewClient(srv.URL, "pl_test", srv.Client())
	if _, err := c.DoJSON(context.Background(), http.MethodPost, "/api/publish", nil, nil, &resp); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if resp.ID != "abc123" || resp.Version != 2 {
		t.Fatalf("decoded = %+v", resp)
	}
}

func TestClient204ReturnsNilBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "pl_test", srv.Client())
	raw, err := c.DoJSON(context.Background(), http.MethodDelete, "/api/files/abc", nil, nil, nil)
	if err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if len(raw) != 0 {
		t.Fatalf("204 must yield an empty body, got %q", raw)
	}
}

func TestErrorEnvelopeMapping(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode string
		wantExit int
		wantMsg  string // substring
	}{
		{"401", 401, `{"code":"unauthorized","message":"unauthorized"}`, "unauthorized", 3, "placard login"},
		{"403", 403, `{"code":"permission_denied","message":"permission denied"}`, "permission_denied", 1, "permission denied"},
		{"400 validation", 400, `{"code":"validation","message":"invalid expiry"}`, "validation", 1, "invalid expiry"},
		{"415 validation", 415, `{"code":"validation","message":"content is not HTML"}`, "validation", 1, "not HTML"},
		{"404 not_found", 404, `{"code":"not_found","message":"not found"}`, "not_found", 1, "does not exist"},
		{"404 route missing", 404, `{"code":"error","message":"Cannot POST /auth/device/code"}`, "server_too_old", 1, "server"},
		{"426 min_cli_version", 426,
			`{"code":"upgrade_required","message":"this Placard server requires placard CLI 2.0.0 or newer, and you are running 1.0.0 — upgrade with: placard update"}`,
			"upgrade_required", 1, "2.0.0"},
		// A server that words the envelope differently must still produce an
		// instruction the user can act on.
		{"426 empty body", 426, ``, "upgrade_required", 1, "placard update"},
		{"429", 429, `{"code":"rate_limited","message":"rate limited"}`, "rate_limited", 1, ""},
		{"502", 502, `{"code":"storage_failed","message":"storage failed"}`, "storage_failed", 1, "storage backend"},
		{"503", 503, `{"code":"unavailable","message":"service unavailable"}`, "unavailable", 1, "temporarily unavailable"},
		{"500", 500, `{"code":"internal","message":"internal"}`, "internal", 1, "internal server"},
		{"413 mapped to 400", 400, `{"code":"validation","message":"file too large"}`, "validation", 1, "file too large"},
		{"empty body", 500, ``, "error", 1, "HTTP 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := parseAPIError(tc.status, []byte(tc.body))
			if e.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", e.Code, tc.wantCode)
			}
			if e.Exit != tc.wantExit {
				t.Errorf("exit = %d, want %d", e.Exit, tc.wantExit)
			}
			if tc.wantMsg != "" && !strings.Contains(e.Message, tc.wantMsg) {
				t.Errorf("message = %q, want substring %q", e.Message, tc.wantMsg)
			}
		})
	}
}

// spec §13.1: the ONLY discriminator between "route missing" and "resource
// missing" is the envelope code — never the message text.
// The server's 426 message already names the command; printing it twice reads
// like two different instructions.
func TestUpgradeInstructionIsNotDuplicated(t *testing.T) {
	e := parseAPIError(426, []byte(`{"code":"upgrade_required","message":"requires placard CLI 2.0.0 or newer — upgrade with: placard update"}`))
	if got := strings.Count(e.Message, "placard update"); got != 1 {
		t.Fatalf("the instruction appears %d times: %q", got, e.Message)
	}
}

func TestServerTooOldNeverMatchesMessageText(t *testing.T) {
	e := parseAPIError(404, []byte(`{"code":"not_found","message":"Cannot POST /auth/device/code"}`))
	if e.Code == "server_too_old" {
		t.Fatal("code not_found must never be treated as a missing route, whatever the message says")
	}
}

func TestClientNetworkFailureIsNetworkCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // nothing is listening any more

	c := NewClient(srv.URL, "pl_test", srv.Client())
	_, err := c.DoJSON(context.Background(), http.MethodGet, "/api/files", nil, nil, nil)
	var e *Error
	if !asError(err, &e) || e.Code != "network" {
		t.Fatalf("want network error, got %v", err)
	}
	if e.Exit != 1 {
		t.Fatalf("exit = %d, want 1", e.Exit)
	}
}

func TestRateLimitHintRewritesOnlyRateLimited(t *testing.T) {
	err := rateLimitHint(parseAPIError(429, []byte(`{"code":"rate_limited","message":"rate limited"}`)),
		"reached the hourly publish limit (default 50/hour); please retry later")
	var e *Error
	if !asError(err, &e) || !strings.Contains(e.Message, "hourly publish limit") {
		t.Fatalf("hint not applied: %v", err)
	}
	other := parseAPIError(404, []byte(`{"code":"not_found","message":"not found"}`))
	if got := rateLimitHint(other, "irrelevant"); got.(*Error).Message != other.Message {
		t.Fatal("non-429 errors must pass through untouched")
	}
}

func TestAsServerTooOldRewritesMessageAndExit(t *testing.T) {
	err := asServerTooOld(parseAPIError(404, []byte(`{"code":"error","message":"Cannot POST /auth/device/code"}`)),
		"This Placard server does not support CLI login (missing device-code endpoint).", 3)
	var e *Error
	if !asError(err, &e) || e.Code != "server_too_old" || e.Exit != 3 {
		t.Fatalf("got %+v", e)
	}
	if strings.Contains(e.Message, "Cannot POST") {
		t.Fatal("Fiber's raw message must not be shown to the user")
	}
	plain := parseAPIError(404, []byte(`{"code":"not_found","message":"not found"}`))
	if got := asServerTooOld(plain, "x", 3); got.(*Error).Code != "not_found" {
		t.Fatal("non server_too_old errors must pass through")
	}
}

func TestDoMultipartShapesPublishRequest(t *testing.T) {
	var gotFields map[string]string
	var gotFile, gotFilename string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("content type: %v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		gotFields = map[string]string{}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("NextPart: %v", err)
			}
			b, _ := io.ReadAll(part)
			if part.FormName() == "file" {
				gotFile, gotFilename = string(b), part.FileName()
				continue
			}
			gotFields[part.FormName()] = string(b)
		}
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "pl_test", srv.Client())
	fields := map[string]string{"title": "Q4 Summary", "expiry": "7d", "visibility": ""}
	if _, err := c.DoMultipart(context.Background(), "/api/publish", fields, "page.html",
		[]byte("<html><title>Q3</title></html>"), nil); err != nil {
		t.Fatalf("DoMultipart: %v", err)
	}
	if gotFilename != "page.html" || !strings.Contains(gotFile, "<title>Q3</title>") {
		t.Fatalf("file part = %q / %q", gotFilename, gotFile)
	}
	if gotFields["title"] != "Q4 Summary" || gotFields["expiry"] != "7d" {
		t.Fatalf("fields = %v", gotFields)
	}
	if _, ok := gotFields["visibility"]; ok {
		t.Fatal("empty fields must be omitted, not sent as empty strings")
	}
}

// The User-Agent is what the server's min_cli_version gate reads, so its shape
// is a contract, not a courtesy: `placard-cli/<version>`.
func TestClientIdentifiesItselfForTheMinVersionGate(t *testing.T) {
	prev := version.Version
	version.Version = "1.4.2"
	t.Cleanup(func() { version.Version = prev })

	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "pl_test", srv.Client())
	if _, err := c.DoJSON(context.Background(), http.MethodGet, "/api/me", nil, nil, nil); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if gotUA != "placard-cli/1.4.2" {
		t.Fatalf("User-Agent = %q, want placard-cli/1.4.2", gotUA)
	}
}

// End to end over HTTP: a server enforcing min_cli_version must leave the user
// with an instruction and exit 1, never a bare status code.
func TestCommandAgainstAServerDemandingANewerCLI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "placard-cli/"+cliVersion() {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"code":"upgrade_required","message":"this Placard server requires placard CLI 2.0.0 or newer, and you are running 1.0.0"}`))
	}))
	defer srv.Close()

	env, _, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	err := runCLI(env, "--base", srv.URL, "ls")

	var e *Error
	if !asError(err, &e) {
		t.Fatalf("err = %v, want a typed CLI error", err)
	}
	if e.Code != "upgrade_required" || e.Exit != 1 {
		t.Fatalf("code/exit = %s/%d, want upgrade_required/1", e.Code, e.Exit)
	}
	for _, want := range []string{"2.0.0", "placard update"} {
		if !strings.Contains(e.Message, want) {
			t.Errorf("message %q must mention %q", e.Message, want)
		}
	}
}
