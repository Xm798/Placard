package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

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

// newAvatarTestApp is newTestApp's dev_mock shape, but lets the caller inject
// Deps.AvatarHTTPClient and/or Deps.Storage (either nil for the production
// default: a bounded-timeout *http.Client, and a fresh in-memory stub,
// respectively) BEFORE Register mounts the routes. newTestApp itself can't
// be reused for this: Register captures its Deps by value, so mutating the
// Deps struct newTestApp returns never reaches the already-mounted handler.
func newAvatarTestApp(t *testing.T, client *http.Client, objectStore storage.Client) (*fiber.App, Deps) {
	t.Helper()

	db := testutil.OpenTestDB(t)

	cfg := &config.Config{}
	cfg.Upload.MaxFileSize = 10 << 20
	cfg.Server.SecretKey = "test-secret-key"
	cfg.Token.MaxTTLDays = 365
	cfg.Server.BaseURL = "https://placard.example.com"

	if objectStore == nil {
		objectStore = storage.NewStubClient()
	}

	deps := Deps{
		DB:               db,
		Storage:          objectStore,
		Cfg:              cfg,
		Files:            repo.NewFileRepo(db),
		Tokens:           repo.NewTokenRepo(db),
		Views:            repo.NewViewRepo(db),
		Audit:            repo.NewAuditRepo(db),
		Pending:          repo.NewPendingObjectDeleteRepo(db),
		Versions:         repo.NewFileVersionRepo(db),
		Users:            repo.NewUserRepo(db),
		AvatarHTTPClient: client,
	}

	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(func(c *fiber.Ctx) error {
		userctx.Set(c, userctx.Identity{AuthzID: testAuthzID, DisplayName: "Alice", AuthChannel: userctx.ChannelDevMock})
		return c.Next()
	})
	Register(app, deps)
	return app, deps
}

// closeTrackingStorage wraps an storage.Client, recording (via a shared *bool)
// whether Close() was invoked on the io.ReadCloser a GetObject call
// returned. Used to assert serveStorageAvatar releases the storage reader on every
// exit path, including its own reject branches (oversized/corrupt), not just
// the success path.
type closeTrackingStorage struct {
	storage.Client
	closed *bool
}

func (f *closeTrackingStorage) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := f.Client.GetObject(ctx, key)
	if err != nil {
		return nil, err
	}
	return &trackingReadCloser{Reader: rc, closer: rc, closed: f.closed}, nil
}

type trackingReadCloser struct {
	io.Reader
	closer io.Closer
	closed *bool
}

func (t *trackingReadCloser) Close() error {
	*t.closed = true
	return t.closer.Close()
}

func getAvatarRaw(t *testing.T, app *fiber.App, targetID, ifNoneMatch string) *http.Response {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/users/"+targetID+"/avatar", nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("avatar test: %v", err)
	}
	return resp
}

// sniffableJPEGBody returns n bytes starting with the real JPEG SOI magic
// sequence (0xFF 0xD8 0xFF) so http.DetectContentType — which UserAvatar uses,
// the stored object carrying no content-type of its own — actually reports
// image/jpeg. avatar_fetch_test.go's jpegBody (all 0xFF) does NOT sniff as an
// image and is only valid where a header-declared content-type is used.
func sniffableJPEGBody(n int) []byte {
	b := make([]byte, n)
	copy(b, []byte{0xFF, 0xD8, 0xFF})
	return b
}

// seedStorageAvatar seeds a user row whose avatar_key points at a stored object
// with the given bytes/content-type.
func seedStorageAvatar(t *testing.T, deps Deps, authzID string, body []byte, contentType string) {
	t.Helper()
	row := model.User{
		ID: authzID, Username: repo.NormalizeUsername(authzID), DisplayName: "Target",
		FirstLoginAt: model.Never, LastLoginAt: model.Never, LastActiveAt: model.Never,
	}
	if err := deps.DB.Create(&row).Error; err != nil {
		t.Fatalf("seed user row: %v", err)
	}
	key := avatarKey(authzID)
	if err := deps.Storage.PutObject(context.Background(), key, bytes.NewReader(body), contentType); err != nil {
		t.Fatalf("seed storage object: %v", err)
	}
	if err := deps.Users.SetAvatar(context.Background(), authzID, "https://cdn.example/"+authzID+".jpg", key); err != nil {
		t.Fatalf("seed avatar key: %v", err)
	}
}

// TestUserAvatarStorageHitStreamsWithCacheHeaders asserts a user row with a
// non-empty avatar_key streams the cached bytes back with the cache headers, a
// sniffed Content-Type (the object store returns none), and
// X-Content-Type-Options: nosniff.
func TestUserAvatarStorageHitStreamsWithCacheHeaders(t *testing.T) {
	app, deps := newAvatarTestApp(t, nil, nil)
	body := sniffableJPEGBody(1000)
	seedStorageAvatar(t, deps, "u_storage_hit", body, "image/jpeg")

	resp := getAvatarRaw(t, app, "u_storage_hit", "")
	if resp.StatusCode != fiber.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body mismatch: got %d bytes, want %d bytes", len(got), len(body))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content-type = %q, want image/jpeg (sniffed from magic bytes)", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "private, max-age=86400" {
		t.Errorf("cache-control = %q, want private, max-age=86400", cc)
	}
	if resp.Header.Get("ETag") == "" {
		t.Error("missing ETag on storage-hit response")
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// TestUserAvatarIfNoneMatchIs304 asserts a conditional request carrying the
// prior response's ETag gets back an empty-body 304 (never touching storage a
// second time is implicit — the etag is derived purely from the user row).
func TestUserAvatarIfNoneMatchIs304(t *testing.T) {
	app, deps := newAvatarTestApp(t, nil, nil)
	seedStorageAvatar(t, deps, "ou_304", sniffableJPEGBody(50), "image/jpeg")

	first := getAvatarRaw(t, app, "ou_304", "")
	if first.StatusCode != fiber.StatusOK {
		t.Fatalf("first request status = %d", first.StatusCode)
	}
	etag := first.Header.Get("ETag")
	if etag == "" {
		t.Fatal("first response missing ETag")
	}
	_, _ = io.ReadAll(first.Body)

	second := getAvatarRaw(t, app, "ou_304", etag)
	if second.StatusCode != fiber.StatusNotModified {
		t.Fatalf("conditional request status = %d, want 304", second.StatusCode)
	}
	b, _ := io.ReadAll(second.Body)
	if len(b) != 0 {
		t.Errorf("304 body = %d bytes, want empty", len(b))
	}
	if cc := second.Header.Get("Cache-Control"); cc != "private, max-age=86400" {
		t.Errorf("304 cache-control = %q", cc)
	}
}

// TestUserAvatarStorageCorruptObjectRejectedIndependentOf304 asserts an object
// whose bytes no longer sniff to an allow-listed image type (simulating
// corruption) is served as 404, never as image bytes.
func TestUserAvatarStorageCorruptObjectIs404(t *testing.T) {
	app, deps := newAvatarTestApp(t, nil, nil)
	// PutObject's declared Content-Type is irrelevant to level-1 serving —
	// only the sniffed bytes matter — so store plain text under the hood.
	seedStorageAvatar(t, deps, "u_corrupt", []byte("not an image, plain text body"), "text/plain")

	resp := getAvatarRaw(t, app, "u_corrupt", "")
	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a non-image cached object", resp.StatusCode)
	}
}

// TestUserAvatarStorageOversizedObjectIs404AndCloses asserts serveStorageAvatar has
// its own read-side 256KB cap (added in review round 1): a cached object
// over the limit is rejected as 404 — never streamed/served truncated or
// oversized — regardless of whether cacheAvatarSync's write-side gate should
// have already prevented it from existing. The storage reader must still be
// closed on this reject path, verified via closeTrackingStorage.
func TestUserAvatarStorageOversizedObjectIs404AndCloses(t *testing.T) {
	closed := false
	spy := &closeTrackingStorage{Client: storage.NewStubClient(), closed: &closed}
	app, deps := newAvatarTestApp(t, nil, spy)
	seedStorageAvatar(t, deps, "ou_oversize", sniffableJPEGBody(avatarMaxBytes+1), "image/jpeg")

	resp := getAvatarRaw(t, app, "ou_oversize", "")
	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("status = %d, want 404 rejecting an over-256KB cached object", resp.StatusCode)
	}
	if !closed {
		t.Error("storage object reader was never closed on the oversized-reject path")
	}
}

// TestUserAvatarMissIs404 asserts a target with no user row (and so no
// avatar_key) is a plain 404 — never an error leaking whether the row exists.
func TestUserAvatarMissIs404(t *testing.T) {
	app, _ := newAvatarTestApp(t, nil, nil)
	resp := getAvatarRaw(t, app, "u_nobody", "")
	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("status = %d, want 404 on a miss", resp.StatusCode)
	}
}

// --- PAT rejection: needs the real session/PAT channel, not dev_mock -------

const avatarSessionTestAuthzID = "u_alice_avatar"

// newAvatarSessionTestApp wires the real middleware.NewAuth ahead of Register —
// needed because dev_mock (newAvatarTestApp above) never exercises the
// Bearer/PAT channel this test asserts against.
func newAvatarSessionTestApp(t *testing.T) (*fiber.App, session.Store) {
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
		Versions:      repo.NewFileVersionRepo(db),
		Users:         repo.NewUserRepo(db),
		Sessions:      store,
		SessionCookie: testSessionCookie,
	}

	h := New(deps)
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(middleware.NewAuth(middleware.AuthOptions{
		Sessions:       store,
		CookieName:     testSessionCookie,
		TokenValidator: h.TokenValidator(),
	}))
	h.Mount(app)
	return app, store
}

// TestUserAvatarPATForbidden asserts a PAT (Bearer) caller is rejected 403 by
// SessionOnly, never reaching UserAvatar.
func TestUserAvatarPATForbidden(t *testing.T) {
	app, store := newAvatarSessionTestApp(t)
	sid := createTestSession(t, store, avatarSessionTestAuthzID)
	cookie := &http.Cookie{Name: testSessionCookie, Value: sid}

	// Mint a PAT via the session-authenticated /api/tokens endpoint.
	req := httptest.NewRequest("POST", "/api/tokens", strings.NewReader(`{"name":"cli","expiry":"30d"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create token status = %d, body = %s", resp.StatusCode, b)
	}
	var cr struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatalf("decode token: %v", err)
	}

	req2 := httptest.NewRequest("GET", "/api/users/"+avatarSessionTestAuthzID+"/avatar", nil)
	req2.Header.Set("Authorization", "Bearer "+cr.Token)
	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatalf("avatar test: %v", err)
	}
	if resp2.StatusCode != fiber.StatusForbidden {
		b, _ := io.ReadAll(resp2.Body)
		t.Fatalf("PAT avatar status = %d, body = %s, want 403", resp2.StatusCode, b)
	}
}

// --- CSP tightening ----------------------------------------------------------

// TestAppPageCSPImgSrcTightened asserts the SPA shell's CSP no longer admits
// arbitrary https: image sources — only 'self' and data: — now that avatars
// are served same-origin via UserAvatar instead of raw upstream CDN URLs.
func TestAppPageCSPImgSrcTightened(t *testing.T) {
	app, _ := newTestApp(t)
	req := httptest.NewRequest("GET", "/", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	csp := resp.Header.Get("Content-Security-Policy")

	found := false
	for _, d := range strings.Split(csp, ";") {
		d = strings.TrimSpace(d)
		if !strings.HasPrefix(d, "img-src") {
			continue
		}
		found = true
		if d != "img-src 'self' data:" {
			t.Errorf("img-src = %q, want exactly \"img-src 'self' data:\" (no https:)", d)
		}
	}
	if !found {
		t.Fatalf("CSP has no img-src directive: %q", csp)
	}
}

// createTestSession mints a session row for authzID and returns its id.
func createTestSession(t *testing.T, store session.Store, authzID string) string {
	t.Helper()
	sid, err := store.Create(context.Background(), session.Data{
		AuthzID: authzID, DisplayName: "Alice", CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sid
}
