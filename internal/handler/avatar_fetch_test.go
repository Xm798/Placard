package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/storage"
)

// avatarKey mirrors cacheAvatarSync's fixed object key naming.
func avatarKey(authzID string) string { return "avatars/" + authzID }

// getStorageObject returns the stored bytes for key, or (nil, false) on a miss.
func getStorageObject(t *testing.T, deps Deps, key string) ([]byte, bool) {
	t.Helper()
	rc, err := deps.Storage.GetObject(context.Background(), key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, false
		}
		t.Fatalf("GetObject(%q): %v", key, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read object %q: %v", key, err)
	}
	return b, true
}

// jpegBody returns a body of n bytes with a minimal JPEG-ish leading marker
// (content doesn't need to be a real image — only Content-Type is checked).
func jpegBody(n int) []byte {
	b := bytes.Repeat([]byte{0xFF}, n)
	return b
}

func TestCacheAvatarSyncDownloadsAndCaches(t *testing.T) {
	_, deps := newTestApp(t)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegBody(100))
	}))
	defer cdn.Close()
	// The production client refuses non-public addresses; the fake CDN is on
	// loopback, so the test drives the pipeline through the CDN's own client.
	deps.AvatarHTTPClient = cdn.Client()
	h := New(deps)

	// cacheAvatarSync's SetAvatarKey write is update-only (avatar caching is
	// never a user-table build point), so the row must already exist.
	seedUserRow(t, deps.DB, model.User{ID: "u_a", DisplayName: "A"})

	err := h.cacheAvatarSync(context.Background(), "u_a", cdn.URL+"/avatar.jpg")
	if err != nil {
		t.Fatalf("cacheAvatarSync: %v", err)
	}

	body, ok := getStorageObject(t, deps, avatarKey("u_a"))
	if !ok || len(body) != 100 {
		t.Fatalf("storage object = (%v, %v), want 100 bytes", ok, len(body))
	}
	u, err := deps.Users.Get(context.Background(), "u_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if u.AvatarKey != avatarKey("u_a") || u.AvatarSourceURL != cdn.URL+"/avatar.jpg" {
		t.Fatalf("user row = %+v", u)
	}
}

func TestCacheAvatarSyncSkipsWhenUnchanged(t *testing.T) {
	_, deps := newTestApp(t)
	hit := false
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegBody(10))
	}))
	defer cdn.Close()
	srcURL := cdn.URL + "/avatar.jpg"

	seedUserRow(t, deps.DB, model.User{ID: "u_b", DisplayName: "B", AvatarSourceURL: srcURL})
	if err := deps.Users.SetAvatar(context.Background(), "u_b", srcURL, avatarKey("u_b")); err != nil {
		t.Fatalf("seed avatar key: %v", err)
	}

	h := New(deps)
	if err := h.cacheAvatarSync(context.Background(), "u_b", srcURL); err != nil {
		t.Fatalf("cacheAvatarSync: %v", err)
	}
	if hit {
		t.Fatal("CDN was hit despite unchanged avatar_url + existing avatar_key")
	}
}

func TestCacheAvatarSyncRedownloadsWhenURLChanges(t *testing.T) {
	_, deps := newTestApp(t)
	hits := 0
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("new-avatar-bytes"))
	}))
	defer cdn.Close()

	seedUserRow(t, deps.DB, model.User{ID: "u_c", DisplayName: "C", AvatarSourceURL: "https://old.example/a.png"})
	if err := deps.Users.SetAvatar(context.Background(), "u_c", "https://old.example/a.png", avatarKey("u_c")); err != nil {
		t.Fatalf("seed avatar key: %v", err)
	}

	// The production client refuses non-public addresses; the fake CDN is on
	// loopback, so the test drives the pipeline through the CDN's own client.
	deps.AvatarHTTPClient = cdn.Client()
	h := New(deps)
	newURL := cdn.URL + "/new.png"
	if err := h.cacheAvatarSync(context.Background(), "u_c", newURL); err != nil {
		t.Fatalf("cacheAvatarSync: %v", err)
	}
	if hits != 1 {
		t.Fatalf("CDN hits = %d, want 1 (must re-download on URL change)", hits)
	}
	u, err := deps.Users.Get(context.Background(), "u_c")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if u.AvatarSourceURL != newURL {
		t.Fatalf("avatar_source_url = %q, want %q", u.AvatarSourceURL, newURL)
	}
	body, ok := getStorageObject(t, deps, avatarKey("u_c"))
	if !ok || string(body) != "new-avatar-bytes" {
		t.Fatalf("storage object = (%v, %q), want new-avatar-bytes", ok, body)
	}
}

func TestCacheAvatarSyncRejectsSVG(t *testing.T) {
	_, deps := newTestApp(t)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte("<svg onload=alert(1)></svg>"))
	}))
	defer cdn.Close()
	// The production client refuses non-public addresses; the fake CDN is on
	// loopback, so the test drives the pipeline through the CDN's own client.
	deps.AvatarHTTPClient = cdn.Client()
	h := New(deps)

	err := h.cacheAvatarSync(context.Background(), "u_svg", cdn.URL+"/x.svg")
	if err == nil {
		t.Fatal("want error rejecting image/svg+xml, got nil")
	}
	if _, ok := getStorageObject(t, deps, avatarKey("u_svg")); ok {
		t.Fatal("SVG must never be cached to storage")
	}
}

func TestCacheAvatarSyncRejectsOversized(t *testing.T) {
	_, deps := newTestApp(t)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegBody(avatarMaxBytes + 1))
	}))
	defer cdn.Close()
	// The production client refuses non-public addresses; the fake CDN is on
	// loopback, so the test drives the pipeline through the CDN's own client.
	deps.AvatarHTTPClient = cdn.Client()
	h := New(deps)

	err := h.cacheAvatarSync(context.Background(), "u_big", cdn.URL+"/big.jpg")
	if err == nil {
		t.Fatal("want error rejecting an over-256KB body, got nil")
	}
	if _, ok := getStorageObject(t, deps, avatarKey("u_big")); ok {
		t.Fatal("oversized body must never be cached to storage")
	}
}

func TestCacheAvatarSyncRejectsUpstreamError(t *testing.T) {
	_, deps := newTestApp(t)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer cdn.Close()
	// The production client refuses non-public addresses; the fake CDN is on
	// loopback, so the test drives the pipeline through the CDN's own client.
	deps.AvatarHTTPClient = cdn.Client()
	h := New(deps)

	if err := h.cacheAvatarSync(context.Background(), "u_404", cdn.URL+"/gone.jpg"); err == nil {
		t.Fatal("want error on non-200 upstream response, got nil")
	}
}

// The HTTP client is injectable (Deps.AvatarHTTPClient) specifically so tests
// can exercise the timeout gate without a real 3s wait.
func TestCacheAvatarSyncRespectsInjectedClientTimeout(t *testing.T) {
	_, deps := newTestApp(t)
	block := make(chan struct{})
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // never responds within the test's lifetime
	}))
	// LIFO order matters: httptest.Server.Close() blocks until outstanding
	// requests complete, so the block channel must be closed (unblocking the
	// handler) BEFORE cdn.Close() runs — i.e. deferred AFTER it.
	defer cdn.Close()
	defer close(block)
	deps.AvatarHTTPClient = &http.Client{Timeout: 50 * time.Millisecond}
	h := New(deps)

	err := h.cacheAvatarSync(context.Background(), "u_slow", cdn.URL+"/slow.jpg")
	if err == nil {
		t.Fatal("want timeout error, got nil")
	}
}

// Nil Users/Storage (e.g. auth tests that don't wire a DB) must be a safe no-op,
// never a nil-pointer panic — cacheAvatarAsync runs in a detached goroutine
// where a panic would crash the whole process.
func TestCacheAvatarSyncNilDepsNoop(t *testing.T) {
	h := New(Deps{})
	if err := h.cacheAvatarSync(context.Background(), "u_x", "https://cdn.example/a.jpg"); err != nil {
		t.Fatalf("cacheAvatarSync with nil deps: %v", err)
	}
}

// cacheAvatarAsync must not spawn a fetch at all for an empty source URL.
func TestCacheAvatarAsyncSkipsEmptyURL(t *testing.T) {
	_, deps := newTestApp(t)
	hit := false
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer cdn.Close()
	// The production client refuses non-public addresses; the fake CDN is on
	// loopback, so the test drives the pipeline through the CDN's own client.
	deps.AvatarHTTPClient = cdn.Client()
	h := New(deps)

	h.cacheAvatarAsync("u_empty", "")
	time.Sleep(100 * time.Millisecond)
	if hit {
		t.Fatal("cacheAvatarAsync must skip an empty srcURL entirely")
	}
}

// End-to-end: cacheAvatarAsync's detached goroutine actually performs the
// same work as cacheAvatarSync, observable by polling storage/the user row.
func TestCacheAvatarAsyncEndToEnd(t *testing.T) {
	_, deps := newTestApp(t)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write([]byte("webp-bytes"))
	}))
	defer cdn.Close()
	// The production client refuses non-public addresses; the fake CDN is on
	// loopback, so the test drives the pipeline through the CDN's own client.
	deps.AvatarHTTPClient = cdn.Client()
	h := New(deps)

	h.cacheAvatarAsync("u_async", cdn.URL+"/a.webp")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := getStorageObject(t, deps, avatarKey("u_async")); ok {
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("avatar was never cached asynchronously within the deadline")
}
