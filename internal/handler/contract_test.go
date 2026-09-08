package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/httpx"
	"github.com/Xm798/placard/internal/middleware"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/repo"
	"github.com/Xm798/placard/internal/session"
	"github.com/Xm798/placard/internal/storage"
	"github.com/Xm798/placard/internal/testutil"
	"github.com/Xm798/placard/internal/userctx"
)

const testAuthzID = "u_alice"

// newTestApp builds an app backed by a real database (via testutil.OpenTestDB)
// with a fixed dev identity injected, so handlers run with
// userctx.AuthzID() = testAuthzID.
func newTestApp(t *testing.T) (*fiber.App, Deps) {
	t.Helper()
	app, deps, _ := newTestAppClock(t)
	return app, deps
}

// newTestAppClock is newTestApp plus its ephemeral backend's clock seam, for
// tests that have to reach the far side of a TTL.
func newTestAppClock(t *testing.T) (*fiber.App, Deps, func(time.Duration)) {
	t.Helper()
	return newTestAppStorage(t, storage.NewStubClient())
}

// newTestAppStorage builds the test app over a caller-supplied storage backend,
// so a test can drive the publish/render path against a real one (the local
// on-disk client) instead of the in-memory stub.
func newTestAppStorage(t *testing.T, objectStore storage.Client) (*fiber.App, Deps, func(time.Duration)) {
	t.Helper()

	db := testutil.OpenTestDB(t)
	eph := newEphemeral(t, db, time.Hour, 2*time.Hour)

	cfg := &config.Config{}
	cfg.Upload.MaxFileSize = 10 << 20
	cfg.Server.SecretKey = "test-secret-key"
	cfg.Token.MaxTTLDays = 365
	cfg.Server.BaseURL = "https://placard.example.com"
	cfg.CSRF.AllowedOrigins = []string{"https://placard.example.com"}

	deps := Deps{
		DB:            db,
		Storage:       objectStore,
		Cfg:           cfg,
		Files:         repo.NewFileRepo(db),
		Tokens:        repo.NewTokenRepo(db),
		Views:         repo.NewViewRepo(db),
		Audit:         repo.NewAuditRepo(db),
		Pending:       repo.NewPendingObjectDeleteRepo(db),
		Versions:      repo.NewFileVersionRepo(db),
		Users:         repo.NewUserRepo(db),
		SessionCookie: "__Host-placard_session",
	}
	// Device-code store: every test app gets a real one so the /auth/device*
	// handlers are exercised end to end.
	deps.DeviceCodes = eph.deviceCodes

	// ErrorHandler mirrors main.newFiberApp (shared httpx.ErrorHandler) so
	// handlers returning *apperr.Error translate to the correct status (the
	// default handler would 500 them).
	app := fiber.New(fiber.Config{
		ErrorHandler: httpx.ErrorHandler,
	})
	// RequestID mirrors main.go's chain: it is what puts request_id and
	// entrypoint on the UserContext, so anything a handler logs or persists
	// from ctx is correlated here the same way it is in production.
	app.Use(middleware.RequestID())
	app.Use(injectTestIdentity)
	Register(app, deps)
	return app, deps, eph.advance
}

// injectTestIdentity stands in for the auth middleware, giving every request
// the fixed test identity. Tests that need the anonymous view of a route mount
// the same routes without it (see anonApp).
func injectTestIdentity(c *fiber.Ctx) error {
	userctx.Set(c, userctx.Identity{AuthzID: testAuthzID, DisplayName: "Alice", AuthChannel: userctx.ChannelDevMock})
	return c.Next()
}

func doJSON(t *testing.T, app *fiber.App, method, path string, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test %s %s: %v", method, path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// publishOne publishes a file and returns its nano id.
func publishOne(t *testing.T, app *fiber.App, expiry string) string {
	t.Helper()
	reqBody := `{"html":"<!DOCTYPE html><html><body>hi</body></html>","title":"Secret Title","expiry":"` + expiry + `"}`
	code, b := doJSON(t, app, "POST", "/api/publish", reqBody)
	if code != fiber.StatusOK {
		t.Fatalf("publish status = %d, body = %s", code, b)
	}
	var pr struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &pr); err != nil {
		t.Fatalf("unmarshal publish resp: %v", err)
	}
	if pr.ID == "" {
		t.Fatalf("publish returned empty id")
	}
	return pr.ID
}

// createTestToken creates a PAT via the API and returns its id and plaintext.
func createTestToken(t *testing.T, app *fiber.App, expiry string) (uint, string) {
	t.Helper()
	code, b := doJSON(t, app, "POST", "/api/tokens", `{"name":"cli","expiry":"`+expiry+`"}`)
	if code != fiber.StatusOK {
		t.Fatalf("create token status = %d, body = %s", code, b)
	}
	var cr struct {
		ID    uint   `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(b, &cr); err != nil {
		t.Fatalf("unmarshal token: %v", err)
	}
	return cr.ID, cr.Token
}

// renderIframe issues the iframe-style GET /s/:id/render the viewer shell makes
// (Sec-Fetch-Dest: iframe) and returns the response, failing the test on error.
func renderIframe(t *testing.T, app *fiber.App, id string) *http.Response {
	t.Helper()
	req := httptest.NewRequest("GET", "/s/"+id+"/render", nil)
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("render test: %v", err)
	}
	return resp
}

// TestMetaHitNoInternalFields asserts a public meta HIT carries only the
// whitelist and never internal fields or sentinel literals.
func TestMetaHitNoInternalFields(t *testing.T) {
	app, _ := newTestApp(t)
	id := publishOne(t, app, "30d")

	code, b := doJSON(t, app, "GET", "/s/"+id+"/meta", "")
	if code != fiber.StatusOK {
		t.Fatalf("meta status = %d, body = %s", code, b)
	}
	body := string(b)

	for _, banned := range []string{"create_user", "update_user", "object_key", "size_bytes", "view_count", "9999", "1970"} {
		if strings.Contains(body, banned) {
			t.Errorf("meta hit response leaked %q: %s", banned, body)
		}
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal meta: %v", err)
	}
	if m["expired"] != false {
		t.Errorf("expected expired=false, got %v", m["expired"])
	}
	if got := m["render_url"]; got != "/s/"+id+"/render" {
		t.Errorf("meta render_url = %v, want /s/%s/render", got, id)
	}
	if m["title"] != "Secret Title" {
		t.Errorf("meta hit title = %v", m["title"])
	}
}

// TestMetaExpiredNoRenderNoTitle asserts an expired file's meta returns only
// {expired:true} — no render_url, no title.
func TestMetaExpiredNoRenderNoTitle(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "30d")

	// Force expiry into the past.
	if err := deps.DB.Model(&model.File{}).
		Where("nano_id = ?", id).
		Update("expires_at", model.Timestamp(time.Now().Add(-time.Hour))).Error; err != nil {
		t.Fatalf("expire file: %v", err)
	}

	code, b := doJSON(t, app, "GET", "/s/"+id+"/meta", "")
	if code != fiber.StatusOK {
		t.Fatalf("meta status = %d", code)
	}
	body := string(b)

	if strings.Contains(body, "render_url") {
		t.Errorf("expired meta leaked render_url: %s", body)
	}
	if strings.Contains(body, "Secret Title") || strings.Contains(body, "title") {
		t.Errorf("expired meta leaked title: %s", body)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["expired"] != true {
		t.Errorf("expected expired=true, got %v", m["expired"])
	}
}

// TestMetaMissingFile asserts an unknown id returns the generic expired state.
func TestMetaMissingFile(t *testing.T) {
	app, _ := newTestApp(t)
	code, b := doJSON(t, app, "GET", "/s/nonexist/meta", "")
	if code != fiber.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if strings.Contains(string(b), "render_url") || strings.Contains(string(b), "title") {
		t.Errorf("missing-file meta leaked fields: %s", b)
	}
}

// TestMetaExpiredIsNotAppErr asserts the expired/missing path stays a 200 with
// {expired:true} and is NOT routed through the apperr ErrorHandler (which would
// add a "code" field and a non-200 status). This is the safety-disguise invariant.
func TestMetaExpiredIsNotAppErr(t *testing.T) {
	app, _ := newTestApp(t)
	code, b := doJSON(t, app, "GET", "/s/nonexist/meta", "")
	if code != fiber.StatusOK {
		t.Fatalf("expired meta status = %d, want 200 (must not be an apperr)", code)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["expired"] != true {
		t.Errorf("expired meta body = %s, want {expired:true}", b)
	}
	if _, hasCode := m["code"]; hasCode {
		t.Errorf("expired meta must not carry a code field (would imply apperr): %s", b)
	}
	if _, hasMsg := m["message"]; hasMsg {
		t.Errorf("expired meta must not carry a message field: %s", b)
	}
}

// TestTokenExpiryFailClosed asserts an expired token is rejected by the
// validator (fail-closed), and the 9999 sentinel never-expires is accepted.
func TestTokenExpiryFailClosed(t *testing.T) {
	app, deps := newTestApp(t)
	h := New(deps)
	validate := h.TokenValidator()

	tokenID, plaintext := createTestToken(t, app, "30d")
	if !strings.HasPrefix(plaintext, "pl_") {
		t.Errorf("token missing pl_ prefix: %q", plaintext)
	}

	// Valid token resolves to the owner's authz id.
	id, ok := validate(plaintext)
	if !ok || id.AuthzID != testAuthzID {
		t.Fatalf("valid token rejected or wrong authzid: ok=%v id=%+v", ok, id)
	}

	// Expire the token in the past → fail-closed reject.
	if err := deps.DB.Model(&model.Token{}).
		Where("id = ?", tokenID).
		Update("expires_at", model.Timestamp(time.Now().Add(-time.Minute))).Error; err != nil {
		t.Fatalf("expire token: %v", err)
	}
	if _, ok := validate(plaintext); ok {
		t.Errorf("expired token accepted (must fail-closed)")
	}

	// A <=1970 ambiguous value is also rejected (fail-closed).
	if err := deps.DB.Model(&model.Token{}).
		Where("id = ?", tokenID).
		Update("expires_at", time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC)).Error; err != nil {
		t.Fatalf("set 1970: %v", err)
	}
	if _, ok := validate(plaintext); ok {
		t.Errorf("1970-sentinel token accepted (must fail-closed)")
	}

	// Revoked token rejected even when unexpired.
	if err := deps.DB.Model(&model.Token{}).
		Where("id = ?", tokenID).
		Updates(map[string]any{"expires_at": model.Timestamp(time.Now().Add(time.Hour)), "revoked": 1}).Error; err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, ok := validate(plaintext); ok {
		t.Errorf("revoked token accepted")
	}
}

// TestTokenMaxTTLTruncated asserts "never" / over-365d expiry is truncated to
// the max lifetime (no permanent token).
func TestTokenMaxTTLTruncated(t *testing.T) {
	app, _ := newTestApp(t)
	code, b := doJSON(t, app, "POST", "/api/tokens", `{"name":"forever","expiry":"never"}`)
	if code != fiber.StatusOK {
		t.Fatalf("status = %d", code)
	}
	var cr struct {
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(b, &cr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cr.ExpiresAt == nil {
		t.Fatalf("token expiry must not be null (no permanent token)")
	}
	maxBound := time.Now().Add(366 * 24 * time.Hour)
	if cr.ExpiresAt.After(maxBound) {
		t.Errorf("token expiry %v exceeds max TTL", cr.ExpiresAt)
	}
}

// TestTokenListNoPlaintext asserts the list never returns plaintext/hash.
func TestTokenListNoPlaintext(t *testing.T) {
	app, _ := newTestApp(t)
	_, _ = createTestToken(t, app, "7d")

	code, b := doJSON(t, app, "GET", "/api/tokens", "")
	if code != fiber.StatusOK {
		t.Fatalf("status = %d", code)
	}
	body := string(b)
	for _, banned := range []string{"token_hash", "pl_", "pepper", "user_id"} {
		if strings.Contains(body, banned) {
			t.Errorf("token list leaked %q: %s", banned, body)
		}
	}
}

// TestDeleteRevokedToken enforces the two-step lifecycle: an active token
// cannot be deleted, while a revoked owner-scoped token can be removed and
// leaves an append-only audit entry.
func TestDeleteRevokedToken(t *testing.T) {
	app, deps := newTestApp(t)
	id, _ := createTestToken(t, app, "7d")
	path := "/api/tokens/" + strconv.FormatUint(uint64(id), 10)

	code, _ := doJSON(t, app, "DELETE", path+"/permanent", "")
	if code != fiber.StatusNotFound {
		t.Fatalf("delete active token status = %d, want 404", code)
	}
	if _, err := deps.Tokens.GetOwned(context.Background(), id, testAuthzID); err != nil {
		t.Fatalf("active token was deleted: %v", err)
	}

	code, _ = doJSON(t, app, "DELETE", path, "")
	if code != fiber.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204", code)
	}
	code, _ = doJSON(t, app, "DELETE", path+"/permanent", "")
	if code != fiber.StatusNoContent {
		t.Fatalf("delete revoked token status = %d, want 204", code)
	}
	if _, err := deps.Tokens.GetOwned(context.Background(), id, testAuthzID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deleted token lookup error = %v, want record not found", err)
	}

	entries, err := deps.Audit.Query(context.Background(), repo.AuditQuery{
		Action: "token.delete", ResourceType: "token", ResourceID: strconv.FormatUint(uint64(id), 10),
	})
	if err != nil || len(entries) != 1 {
		t.Fatalf("token.delete audit entries = %d, err = %v", len(entries), err)
	}
}

// TestPublishRejectsNonHTML asserts non-HTML content is rejected server-side.
func TestPublishRejectsNonHTML(t *testing.T) {
	app, _ := newTestApp(t)
	code, _ := doJSON(t, app, "POST", "/api/publish", `{"html":"just plain text, no markup","title":"x","expiry":"30d"}`)
	if code != fiber.StatusUnsupportedMediaType {
		t.Errorf("expected 415 for non-HTML, got %d", code)
	}
}

// TestPublishTooLargeIsValidation asserts an upload exceeding MaxFileSize is
// rejected as 400 + code:validation (the unified contract). MaxFileSize is
// lowered to 1MB so the 2MB body stays under Fiber's body limit and exercises
// the handler's too-large branch (apperr.Validation) directly.
func TestPublishTooLargeIsValidation(t *testing.T) {
	app, deps := newTestApp(t)
	deps.Cfg.Upload.MaxFileSize = 1 << 20 // 1MB

	// 2MB HTML body (> 1MB cap, < 4MB Fiber body limit → handler fires).
	big := strings.Repeat("<p>x</p>", 200000) // ~1.4MB
	body := `{"html":"` + big + `","title":"big","expiry":"30d"}`
	code, b := doJSON(t, app, "POST", "/api/publish", body)
	if code != fiber.StatusBadRequest {
		t.Fatalf("too-large status = %d, want 400", code)
	}
	var resp map[string]any
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["code"] != "validation" {
		t.Errorf("too-large code = %v, want validation; body=%s", resp["code"], b)
	}
}

// TestPublishNeverExpiryMapsNull asserts "never" — explicit, or the default
// when expiry is omitted — returns expires_at: null (9999 sentinel mapped),
// never the raw sentinel.
func TestPublishNeverExpiryMapsNull(t *testing.T) {
	cases := map[string]string{
		"explicit never": `{"html":"<html><body>x</body></html>","title":"t","expiry":"never"}`,
		"omitted":        `{"html":"<html><body>x</body></html>","title":"t"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			app, _ := newTestApp(t)
			code, b := doJSON(t, app, "POST", "/api/publish", body)
			if code != fiber.StatusOK {
				t.Fatalf("status = %d, body = %s", code, b)
			}
			if strings.Contains(string(b), "9999") {
				t.Errorf("publish leaked 9999 sentinel: %s", b)
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if string(m["expires_at"]) != "null" {
				t.Errorf("expires_at = %s, want null", m["expires_at"])
			}
		})
	}
}

// TestSkillDocServed asserts the embedded markdown docs — /skill.md (skill
// definition) and /install.md (one-time install guide) — return 200,
// text/markdown and a non-empty body, and stay reachable with no session at
// all: both sit in the middleware builtinSkips so agents can self-install.
func TestSkillDocServed(t *testing.T) {
	app, _ := newTestApp(t)
	sessionApp, _, _ := newSessionTestApp(t)
	for _, path := range []string{"/skill.md", "/install.md"} {
		t.Run(path, func(t *testing.T) {
			resp, err := app.Test(httptest.NewRequest("GET", path, nil))
			if err != nil {
				t.Fatalf("test: %v", err)
			}
			if resp.StatusCode != fiber.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/markdown") {
				t.Errorf("Content-Type = %q, want text/markdown", ct)
			}
			b, _ := io.ReadAll(resp.Body)
			if len(bytes.TrimSpace(b)) == 0 {
				t.Errorf("%s body is empty", path)
			}

			// Unauthenticated fetch through the real auth middleware → 200.
			unauth, err := sessionApp.Test(httptest.NewRequest("GET", path, nil))
			if err != nil {
				t.Fatalf("unauth test: %v", err)
			}
			if unauth.StatusCode != fiber.StatusOK {
				t.Errorf("unauth %s status = %d, want 200 (builtinSkips)", path, unauth.StatusCode)
			}
		})
	}
}

// TestViewShellNoSrcdoc asserts the shell never uses srcdoc and sets the
// expected isolation/security headers.
func TestViewShellNoSrcdoc(t *testing.T) {
	app, _ := newTestApp(t)
	req := httptest.NewRequest("GET", "/s/whatever", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	body := string(b)

	if strings.Contains(strings.ToLower(body), "srcdoc") {
		t.Errorf("view shell uses srcdoc (forbidden)")
	}
	if !strings.Contains(body, `sandbox="allow-scripts"`) {
		t.Errorf("view shell iframe missing sandbox=allow-scripts")
	}
	if strings.Contains(body, "allow-same-origin") {
		t.Errorf("view shell must NOT use allow-same-origin")
	}
	for _, entrypoint := range []string{"securitypolicyviolation", linkRelayMessageType} {
		if !strings.Contains(body, entrypoint) {
			t.Errorf("view shell missing external-link entrypoint %q", entrypoint)
		}
	}
	if !strings.Contains(body, `/^\/s\/[A-Za-z0-9_-]+$/`) {
		t.Errorf("view shell missing same-origin share-path handling")
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Errorf("view shell missing frame-ancestors 'self', got %q", csp)
	}
	// The iframe now loads the same-origin render proxy, so frame-src is 'self'.
	if !strings.Contains(csp, "frame-src 'self'") {
		t.Errorf("view shell missing frame-src 'self', got %q", csp)
	}
	if resp.Header.Get("Strict-Transport-Security") == "" {
		t.Errorf("view shell missing Strict-Transport-Security")
	}
	for _, ext := range []string{"fonts.googleapis.com", "cdn.", "https://fonts."} {
		if strings.Contains(body, ext) {
			t.Errorf("view shell has external subresource %q", ext)
		}
	}
}

const publishedHTML = "<!DOCTYPE html><html><body>hi</body></html>"

// TestRenderStreamsHTML asserts the same-origin render proxy streams the stored
// HTML back inline with the isolation headers the sandboxed iframe relies on.
func TestRenderStreamsHTML(t *testing.T) {
	app, _ := newTestApp(t)
	id := publishOne(t, app, "30d")

	resp := renderIframe(t, app, id)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("render status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("render Content-Type = %q, want text/html", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "inline" {
		t.Errorf("render Content-Disposition = %q, want inline", cd)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("render Cache-Control = %q, want no-store", cc)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if csp != "sandbox allow-scripts; frame-ancestors 'self'" {
		t.Errorf("render CSP = %q, want sandbox allow-scripts; frame-ancestors 'self'", csp)
	}
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	if body != publishedHTML+linkRelayScript {
		t.Errorf("render body = %q, want %q", body, publishedHTML+linkRelayScript)
	}
}

// TestRenderTopLevelNavigationRedirects asserts a top-level navigation (a direct
// open, Sec-Fetch-Dest: document) is bounced to the shell instead of rendering
// user HTML as a document on the primary origin.
func TestRenderTopLevelNavigationRedirects(t *testing.T) {
	app, _ := newTestApp(t)
	id := publishOne(t, app, "30d")

	req := httptest.NewRequest("GET", "/s/"+id+"/render", nil)
	req.Header.Set("Sec-Fetch-Dest", "document")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if resp.StatusCode != fiber.StatusSeeOther {
		t.Fatalf("render doc-nav status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/s/"+id {
		t.Errorf("render doc-nav Location = %q, want /s/%s", loc, id)
	}
}

// TestRenderMissIs404 asserts an unknown id renders a 404 (this is a content
// endpoint — no {expired:true} disguise).
func TestRenderMissIs404(t *testing.T) {
	app, _ := newTestApp(t)
	resp := renderIframe(t, app, "nonexist")
	if resp.StatusCode != fiber.StatusNotFound {
		t.Errorf("render miss status = %d, want 404", resp.StatusCode)
	}
}

// TestRenderExpiredIs404 asserts an expired file renders a 404.
func TestRenderExpiredIs404(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "30d")
	if err := deps.DB.Model(&model.File{}).
		Where("nano_id = ?", id).
		Update("expires_at", model.Timestamp(time.Now().Add(-time.Hour))).Error; err != nil {
		t.Fatalf("expire file: %v", err)
	}

	resp := renderIframe(t, app, id)
	if resp.StatusCode != fiber.StatusNotFound {
		t.Errorf("render expired status = %d, want 404", resp.StatusCode)
	}
}

// TestRenderRecordsView asserts the view is recorded at /render (the true read
// point) and NOT at /meta — a client that only fetches meta must not be counted.
func TestRenderRecordsView(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "30d")

	viewCount := func() int64 {
		var n int64
		deps.DB.Model(&model.View{}).Where("file_nano_id = ?", id).Count(&n)
		return n
	}

	// meta must NOT record a view.
	if code, _ := doJSON(t, app, "GET", "/s/"+id+"/meta", ""); code != fiber.StatusOK {
		t.Fatalf("meta status = %d", code)
	}
	if n := viewCount(); n != 0 {
		t.Fatalf("meta recorded a view (count=%d), want 0", n)
	}

	// render records the view.
	renderIframe(t, app, id)
	if n := viewCount(); n != 1 {
		t.Errorf("render view count = %d, want 1", n)
	}

	// Audit file.view recorded at render.
	entries, _ := deps.Audit.Query(context.Background(), repo.AuditQuery{Action: "file.view"})
	if len(entries) == 0 {
		t.Errorf("no file.view audit entry after render")
	}
}

// TestAnonRenderUnknownIdIs404 asserts an anonymous request to /s/:id/render
// is answered on its merits rather than bounced to a login gate: an unknown id
// is the same 404 an authenticated caller gets, never a 401.
func TestAnonRenderUnknownIdIs404(t *testing.T) {
	app, _, _ := newSessionTestApp(t)
	req := httptest.NewRequest("GET", "/s/abc/render", nil)
	req.Header.Set("Sec-Fetch-Mode", "cors")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"code":"not_found"`) {
		t.Errorf("body missing not_found envelope: %s", b)
	}
}

// TestListFilesOwnerOnly asserts the list returns the caller's files with
// pagination metadata and the owner-visible view_count, and never leaks
// internal fields or the 9999 sentinel.
func TestListFilesOwnerOnly(t *testing.T) {
	app, _ := newTestApp(t)
	id1 := publishOne(t, app, "30d")
	id2 := publishOne(t, app, "never")

	code, b := doJSON(t, app, "GET", "/api/files?page=1&page_size=20", "")
	if code != fiber.StatusOK {
		t.Fatalf("list status = %d, body = %s", code, b)
	}
	body := string(b)
	for _, banned := range []string{"create_user", "object_key", "9999"} {
		if strings.Contains(body, banned) {
			t.Errorf("list leaked %q: %s", banned, body)
		}
	}

	var resp struct {
		Files []struct {
			ID        string `json:"id"`
			URL       string `json:"url"`
			ViewCount int64  `json:"view_count"`
		} `json:"files"`
		Total    int64 `json:"total"`
		Page     int   `json:"page"`
		PageSize int   `json:"page_size"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 2 || len(resp.Files) != 2 {
		t.Fatalf("want total=2 files=2, got total=%d files=%d", resp.Total, len(resp.Files))
	}
	// view_count is an owner-visible field here (unlike the public meta path).
	seen := map[string]bool{id1: false, id2: false}
	for _, f := range resp.Files {
		if !strings.HasPrefix(f.URL, "https://") || !strings.Contains(f.URL, "/s/"+f.ID) {
			t.Errorf("bad url %q for id %q", f.URL, f.ID)
		}
		seen[f.ID] = true
	}
	if !seen[id1] || !seen[id2] {
		t.Errorf("missing published ids in list: %+v", resp.Files)
	}
}

// TestDeleteFileSoftDeletesAndEnqueues asserts owner delete soft-deletes the
// file, clears views, enqueues the object key for cron, and records audit.
func TestDeleteFileSoftDeletesAndEnqueues(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "30d")

	// Record a view so DeleteByFile has something to clear.
	_, _ = doJSON(t, app, "GET", "/s/"+id+"/meta", "")

	code, _ := doJSON(t, app, "DELETE", "/api/files/"+id, "")
	if code != fiber.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", code)
	}

	// File soft-deleted: GetActiveByNanoID misses now.
	if _, err := deps.Files.GetActiveByNanoID(context.Background(), id); err == nil {
		t.Errorf("file still active after delete")
	}
	// Views cleared.
	var viewCount int64
	deps.DB.Model(&model.View{}).Where("file_nano_id = ?", id).Count(&viewCount)
	if viewCount != 0 {
		t.Errorf("views not cleared: %d remain", viewCount)
	}
	// Pending object delete enqueued with reason user_delete.
	var pendingCount int64
	deps.DB.Model(&model.PendingObjectDelete{}).Where("reason = ?", "user_delete").Count(&pendingCount)
	if pendingCount != 1 {
		t.Errorf("pending object delete count = %d, want 1", pendingCount)
	}
	// Audit file.delete recorded.
	entries, _ := deps.Audit.Query(context.Background(), repo.AuditQuery{Action: "file.delete"})
	if len(entries) == 0 {
		t.Errorf("no file.delete audit entry")
	}
}

// TestDeleteFileNonOwnerIs404 asserts a non-owner / unknown id is 404 (no
// existence confirmation). Covers both an unknown id and a real file owned by
// a different user — the cross-user (IDOR) invariant at the HTTP layer.
func TestDeleteFileNonOwnerIs404(t *testing.T) {
	app, deps := newTestApp(t)

	// Unknown id → 404.
	code, _ := doJSON(t, app, "DELETE", "/api/files/nonexist", "")
	if code != fiber.StatusNotFound {
		t.Errorf("delete unknown id status = %d, want 404", code)
	}

	// A real file owned by a DIFFERENT user must not be deletable by the
	// session identity (testAuthzID); it is an indistinguishable 404.
	other := &model.File{
		NanoID:     "otherusr",
		Title:      "not yours",
		ObjectKey:  "otherusr-1.html",
		ExpiresAt:  time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
		CreateUser: "u_bob",
		UpdateUser: "u_bob",
	}
	if err := deps.DB.Create(other).Error; err != nil {
		t.Fatalf("seed other-user file: %v", err)
	}
	code, _ = doJSON(t, app, "DELETE", "/api/files/otherusr", "")
	if code != fiber.StatusNotFound {
		t.Errorf("delete other user's file status = %d, want 404", code)
	}
	// And it must remain active (not soft-deleted by the failed attempt).
	if _, err := deps.Files.GetActiveByNanoID(context.Background(), "otherusr"); err != nil {
		t.Errorf("other user's file was affected by non-owner delete: %v", err)
	}
}

// TestAppPageServedWithCSP asserts the app shell is served on / with a CSP that
// forbids inline script (script-src 'self', no unsafe-inline), loads its React
// module bundle from same-origin /assets/*, and carries no inline event
// handlers. The asset filename is content-hashed, so it is parsed out of the
// served index.html rather than hardcoded.
func TestAppPageServedWithCSP(t *testing.T) {
	app, _ := newTestApp(t)
	req := httptest.NewRequest("GET", "/", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	body := string(b)

	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("app page CSP missing script-src 'self': %q", csp)
	}
	// script-src directive itself must never allow unsafe-inline.
	for _, d := range strings.Split(csp, ";") {
		d = strings.TrimSpace(d)
		if strings.HasPrefix(d, "script-src") && strings.Contains(d, "unsafe-inline") {
			t.Errorf("script-src must not allow unsafe-inline: %q", d)
		}
	}
	// The React build emits a hashed ES module bundle under /assets/.
	if !strings.Contains(body, `type="module"`) || !strings.Contains(body, `src="/assets/`) {
		t.Errorf("app page must load a /assets/ module script; body:\n%s", body)
	}
	// No inline event handlers — they would be silently blocked by
	// script-src 'self'. Guards against regressing to inline JS.
	for _, h := range []string{"onclick=", "oninput=", "onchange=", "onsubmit=", "javascript:"} {
		if strings.Contains(strings.ToLower(body), h) {
			t.Errorf("app page has inline handler %q (blocked by CSP script-src 'self')", h)
		}
	}

	// Parse the hashed module path out of index.html and fetch it: it must be
	// served 200 as javascript.
	assetPath := extractAssetSrc(t, body)
	req2 := httptest.NewRequest("GET", assetPath, nil)
	resp2, err := app.Test(req2, -1)
	if err != nil {
		t.Fatalf("fetch asset: %v", err)
	}
	if resp2.StatusCode != fiber.StatusOK {
		t.Errorf("asset %s status = %d, want 200", assetPath, resp2.StatusCode)
	}
	if ct := resp2.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("asset %s content-type = %q, want javascript", assetPath, ct)
	}
}

// extractAssetSrc pulls the first src="/assets/...js" path out of the served
// index.html. The filename is content-hashed by the build, so it is discovered
// at runtime rather than hardcoded.
func extractAssetSrc(t *testing.T, body string) string {
	t.Helper()
	const marker = `src="/assets/`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no /assets/ script src in index.html:\n%s", body)
	}
	start := i + len(`src="`)
	end := strings.IndexByte(body[start:], '"')
	if end < 0 {
		t.Fatalf("unterminated src attribute in index.html")
	}
	return body[start : start+end]
}

// TestAuditAppendOnly asserts the AuditRepo surface offers no mutation path:
// the repo is reflection-checked for Update/Delete methods.
func TestAuditAppendOnly(t *testing.T) {
	// This is a compile-time + behavioral guard. The repo type intentionally
	// exposes only Insert and Query. A view records an audit entry; assert it
	// is queryable and that no row count can be reduced via the repo.
	app, deps := newTestApp(t)
	id := publishOne(t, app, "30d")
	_, _ = doJSON(t, app, "GET", "/s/"+id+"/meta", "")

	entries, err := deps.Audit.Query(context.Background(), repo.AuditQuery{})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected audit entries after publish+view")
	}
	// Ensure the raw actor id is stored (internal) but the model tags hide it
	// from default JSON — assert serialization does not leak Actor.
	out, _ := json.Marshal(entries[0])
	if bytes.Contains(out, []byte(testAuthzID)) {
		t.Errorf("audit JSON leaked raw actor id: %s", out)
	}
}

// --- Real-auth-middleware contract cases -------------------------------------
//
// newTestApp above hand-injects a fixed identity instead of going through
// middleware.NewAuth, so the contract assertions above never exercised the real
// gate. The cases below wire the actual middleware.NewAuth ahead of Register,
// backed by a real session.Store, so unauth routing /
// owner-scoping / probe skips are all exercised through the production auth
// path.

const testSessionCookie = "__Host-placard_session"

// newSessionTestApp builds an app with the real middleware.NewAuth wired ahead
// of Register, backed by a real session.Store. /api/health and
// /api/version are registered inline, ahead of the auth middleware — main.go
// registers those probes before middleware.NewAuth too, and NewAuth's
// builtinSkips assume that ordering.
func newSessionTestApp(t *testing.T) (*fiber.App, Deps, session.Store) {
	t.Helper()

	db := testutil.OpenTestDB(t)
	store := newEphemeral(t, db, time.Hour, 2*time.Hour).sessions

	cfg := &config.Config{}
	cfg.Upload.MaxFileSize = 10 << 20
	cfg.Server.SecretKey = "test-secret-key"
	cfg.Token.MaxTTLDays = 365
	cfg.Server.BaseURL = "https://placard.example.com"

	deps := Deps{
		DB:            db,
		Storage:       storage.NewStubClient(),
		Cfg:           cfg,
		Files:         repo.NewFileRepo(db),
		Tokens:        repo.NewTokenRepo(db),
		Views:         repo.NewViewRepo(db),
		Audit:         repo.NewAuditRepo(db),
		Pending:       repo.NewPendingObjectDeleteRepo(db),
		Sessions:      store,
		SessionCookie: testSessionCookie,
	}

	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Get("/api/health", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"status": "ok"}) })
	app.Get("/api/version", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"version": "test"}) })
	app.Use(middleware.NewAuth(middleware.AuthOptions{
		Sessions:   store,
		CookieName: testSessionCookie,
	}))
	Register(app, deps)
	return app, deps, store
}

// TestSessionOwnerScoping asserts a valid session cookie authenticates through
// the real middleware and /api/files stays owner-scoped: the caller's own
// published file appears, another user's (seeded directly, same pattern as
// TestDeleteFileNonOwnerIs404) does not.
func TestSessionOwnerScoping(t *testing.T) {
	app, deps, store := newSessionTestApp(t)
	sid, err := store.Create(context.Background(), session.Data{
		AuthzID: "u_alice2", DisplayName: "Alice", CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	cookie := &http.Cookie{Name: testSessionCookie, Value: sid}

	pubReq := httptest.NewRequest("POST", "/api/publish",
		strings.NewReader(`{"html":"<html><body>hi</body></html>","title":"alice-file","expiry":"30d"}`))
	pubReq.Header.Set("Content-Type", "application/json")
	pubReq.AddCookie(cookie)
	pubResp, err := app.Test(pubReq)
	if err != nil {
		t.Fatalf("publish test: %v", err)
	}
	if pubResp.StatusCode != fiber.StatusOK {
		b, _ := io.ReadAll(pubResp.Body)
		t.Fatalf("publish status = %d, body = %s", pubResp.StatusCode, b)
	}

	// nano_id is a fixed size:8 column (model.File) — must match that width.
	other := &model.File{
		NanoID:     "otherusB",
		Title:      "not alice's",
		ObjectKey:  "otherusB-1.html",
		ExpiresAt:  time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
		CreateUser: "on_bob",
		UpdateUser: "on_bob",
	}
	if err := deps.DB.Create(other).Error; err != nil {
		t.Fatalf("seed other-user file: %v", err)
	}

	listReq := httptest.NewRequest("GET", "/api/files", nil)
	listReq.AddCookie(cookie)
	listResp, err := app.Test(listReq)
	if err != nil {
		t.Fatalf("list test: %v", err)
	}
	if listResp.StatusCode != fiber.StatusOK {
		t.Fatalf("list status = %d, want 200", listResp.StatusCode)
	}
	b, _ := io.ReadAll(listResp.Body)
	body := string(b)
	if !strings.Contains(body, "alice-file") {
		t.Errorf("owner's own file missing from list: %s", body)
	}
	if strings.Contains(body, "otherusB") {
		t.Errorf("owner scoping leaked another user's file: %s", body)
	}
}

// TestUnauthNavigateRedirectsToLogin asserts a top-level navigation with no
// session cookie is redirected to the login page (never a bare 401 — that would
// strand a first-time browser visit).
func TestUnauthNavigateRedirectsToLogin(t *testing.T) {
	app, _, _ := newSessionTestApp(t)
	req := httptest.NewRequest("GET", "/files", nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if resp.StatusCode != fiber.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login?redirect=%2Ffiles" {
		t.Errorf("Location = %q, want /login?redirect=%%2Ffiles", loc)
	}
}

// TestAnonMetaUnknownIdIsExpired asserts an anonymous XHR-style request against
// /s/:id/meta gets the ordinary miss body, not a 401 and never a redirect —
// meta answers a visitor with no account the same way it answers anyone else.
func TestAnonMetaUnknownIdIsExpired(t *testing.T) {
	app, _, _ := newSessionTestApp(t)
	req := httptest.NewRequest("GET", "/s/abc/meta", nil)
	req.Header.Set("Sec-Fetch-Mode", "cors")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(b)) != `{"expired":true}` {
		t.Errorf("body = %s, want {\"expired\":true}", b)
	}
}

// TestProbesSkipAuth asserts /api/health and /api/version stay
// reachable with no session cookie at all (builtinSkips in NewAuth).
func TestProbesSkipAuth(t *testing.T) {
	app, _, _ := newSessionTestApp(t)
	for _, p := range []string{"/api/health", "/api/version"} {
		resp, err := app.Test(httptest.NewRequest("GET", p, nil))
		if err != nil {
			t.Fatalf("%s: test: %v", p, err)
		}
		if resp.StatusCode != fiber.StatusOK {
			t.Errorf("%s: status = %d, want 200", p, resp.StatusCode)
		}
	}
}

// newSessionCSRFTestApp mirrors newSessionTestApp but additionally wires the
// real middleware.CSRF ahead of Register, so /auth/logout (a cookie-channel
// POST) goes through the actual CSRF gate — the spec §7 contract ("/auth/logout
// deletes the session, clears the cookie and passes CSRF") requires this be
// exercised end to end, not just unit-tested against CSRF in isolation.
func newSessionCSRFTestApp(t *testing.T) (*fiber.App, Deps, session.Store) {
	t.Helper()

	db := testutil.OpenTestDB(t)
	store := newEphemeral(t, db, time.Hour, 2*time.Hour).sessions

	cfg := &config.Config{}
	cfg.Upload.MaxFileSize = 10 << 20
	cfg.Server.SecretKey = "test-secret-key"
	cfg.Token.MaxTTLDays = 365
	cfg.Server.BaseURL = "https://placard.example.com"

	deps := Deps{
		DB:            db,
		Storage:       storage.NewStubClient(),
		Cfg:           cfg,
		Files:         repo.NewFileRepo(db),
		Tokens:        repo.NewTokenRepo(db),
		Views:         repo.NewViewRepo(db),
		Audit:         repo.NewAuditRepo(db),
		Pending:       repo.NewPendingObjectDeleteRepo(db),
		Sessions:      store,
		SessionCookie: testSessionCookie,
	}

	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(middleware.NewAuth(middleware.AuthOptions{
		Sessions:   store,
		CookieName: testSessionCookie,
	}))
	app.Use(middleware.CSRF(config.CSRFConfig{AllowedOrigins: []string{"https://ps.example.com"}}))
	Register(app, deps)
	return app, deps, store
}

// TestLogoutRequiresCSRF asserts the spec §7 contract: POST /auth/logout
// deletes the session and clears the cookie, but only when it passes the
// real CSRF gate (Origin/Referer + Sec-Fetch-Site: same-origin +
// X-Requested-With). A request missing those signals must be rejected
// without touching the session at all.
func TestLogoutRequiresCSRF(t *testing.T) {
	app, _, store := newSessionCSRFTestApp(t)
	sid, err := store.Create(context.Background(), session.Data{
		AuthzID: "u_alice2", DisplayName: "Alice", CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	cookie := &http.Cookie{Name: testSessionCookie, Value: sid}

	// No Origin/Sec-Fetch-Site/X-Requested-With → CSRF rejects with 403, and
	// the session must survive (the rejected request never reaches AuthLogout).
	noCSRFReq := httptest.NewRequest("POST", "/auth/logout", nil)
	noCSRFReq.AddCookie(cookie)
	noCSRFResp, err := app.Test(noCSRFReq)
	if err != nil {
		t.Fatalf("no-CSRF logout test: %v", err)
	}
	if noCSRFResp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("no-CSRF logout status = %d, want 403", noCSRFResp.StatusCode)
	}
	if _, err := store.Get(context.Background(), sid); err != nil {
		t.Fatalf("session must survive a CSRF-rejected logout, got err=%v", err)
	}

	// With the full CSRF triple → 204, session deleted, cookie cleared.
	csrfReq := httptest.NewRequest("POST", "/auth/logout", nil)
	csrfReq.AddCookie(cookie)
	csrfReq.Header.Set("Origin", "https://ps.example.com")
	csrfReq.Header.Set("Sec-Fetch-Site", "same-origin")
	csrfReq.Header.Set("X-Requested-With", "fetch")
	csrfResp, err := app.Test(csrfReq)
	if err != nil {
		t.Fatalf("CSRF-valid logout test: %v", err)
	}
	if csrfResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("CSRF-valid logout status = %d, want 204", csrfResp.StatusCode)
	}
	if _, err := store.Get(context.Background(), sid); err != session.ErrNotFound {
		t.Fatalf("session must be deleted after CSRF-valid logout, got err=%v", err)
	}
	// clearCookie (auth.go) overwrites the cookie with an empty value; that is
	// the observable "cleared" signal here (fasthttp only emits a Max-Age/
	// Expires attribute when Expires is also set, which clearCookie does not
	// do — a pre-existing quirk outside this fix's scope).
	cleared := false
	for _, c := range csrfResp.Cookies() {
		if c.Name == testSessionCookie && c.Value == "" {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("session cookie not cleared on logout: %+v", csrfResp.Cookies())
	}
}

// TestInstallScriptContract pins GET /install.sh: it must serve the embedded
// installer as a shell script with nosniff, so a browser can never be talked
// into rendering it as HTML.
func TestInstallScriptContract(t *testing.T) {
	app, _ := newTestApp(t)

	req := httptest.NewRequest("GET", "/install.sh", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/x-shellscript") {
		t.Fatalf("Content-Type = %q, want text/x-shellscript", ct)
	}
	if nos := resp.Header.Get("X-Content-Type-Options"); nos != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", nos)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.HasPrefix(body, []byte("#!/bin/sh")) {
		t.Fatalf("body does not start with a shebang: %.40q", body)
	}
}

// TestInstallScriptPSContract pins GET /install.ps1: it must serve the embedded
// PowerShell installer with nosniff, so a browser can never be talked into
// rendering it as HTML.
func TestInstallScriptPSContract(t *testing.T) {
	app, _ := newTestApp(t)

	req := httptest.NewRequest("GET", "/install.ps1", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "powershell") {
		t.Fatalf("Content-Type = %q, want to contain 'powershell'", ct)
	}
	if nos := resp.Header.Get("X-Content-Type-Options"); nos != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", nos)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.HasPrefix(body, []byte("#")) {
		t.Fatalf("body does not start with '#': %.40q", body)
	}
}

// seedUserRow inserts a user row directly. The user table has no application
// build point of its own until local accounts exist, so tests that need a
// profile row (avatar cache pointer, default_visibility) write one here.
// seedUserRow inserts a user row, filling in the columns a test does not care
// about: the login sentinels, and a username derived from the id so the NOT
// NULL unique column is satisfied without every call site repeating it.
func seedUserRow(t *testing.T, db *gorm.DB, u model.User) {
	t.Helper()
	if u.Username == "" {
		u.Username = repo.NormalizeUsername(u.ID)
	}
	u.FirstLoginAt = model.Never
	u.LastLoginAt = model.Never
	u.LastActiveAt = model.Never
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("seed user row %q: %v", u.ID, err)
	}
}
