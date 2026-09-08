package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/model"
)

func addr(s string) *string { return &s }

// avatarHandlers builds a Handlers with only what avatarSourceURL reads: the
// Gravatar switch.
func avatarHandlers(gravatar bool) *Handlers {
	cfg := &config.Config{}
	cfg.Avatar.GravatarFallback = gravatar
	return &Handlers{deps: Deps{Cfg: cfg}}
}

// The provider's picture wins over everything else, whatever the Gravatar
// switch says.
func TestAvatarSourcePrefersProviderPicture(t *testing.T) {
	h := avatarHandlers(true)
	user := &model.User{ID: "u_a", Email: addr("alice@example.com")}
	if got := h.avatarSourceURL(user, " https://cdn.example.com/a.png "); got != "https://cdn.example.com/a.png" {
		t.Errorf("source = %q, want the provider picture", got)
	}
}

// With no picture, an account with an address falls back to its Gravatar,
// asked for with d=404 so a missing one is a miss rather than a placeholder.
func TestAvatarSourceFallsBackToGravatar(t *testing.T) {
	h := avatarHandlers(true)
	user := &model.User{ID: "u_a", Email: addr("Alice@Example.com ")}

	got := h.avatarSourceURL(user, "")
	sum := sha256.Sum256([]byte("alice@example.com"))
	want := gravatarBase + hex.EncodeToString(sum[:]) + "?d=404&s=" + gravatarSize
	if got != want {
		t.Errorf("source = %q, want %q", got, want)
	}
	if !strings.Contains(got, "d=404") {
		t.Errorf("gravatar url %q must ask for d=404", got)
	}
}

// An account with no address has no third source: the frontend renders its
// initial letter.
func TestAvatarSourceEmptyWithoutEmail(t *testing.T) {
	h := avatarHandlers(true)
	if got := h.avatarSourceURL(&model.User{ID: "u_a"}, ""); got != "" {
		t.Errorf("source = %q, want empty", got)
	}
}

// With the switch off, no Gravatar URL is produced at all — which is what
// stops the server from disclosing a hash of the address to a third party.
func TestAvatarSourceGravatarDisabledEmitsNoRequest(t *testing.T) {
	h := avatarHandlers(false)
	user := &model.User{ID: "u_a", Email: addr("alice@example.com")}
	if got := h.avatarSourceURL(user, ""); got != "" {
		t.Errorf("source = %q with gravatar_fallback off, want empty", got)
	}

	// And the pipeline the resolved URL feeds refuses to make a request for
	// an empty source, so nothing downstream can reintroduce one.
	requests := 0
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegBody(10))
	}))
	defer cdn.Close()

	_, deps := newTestApp(t)
	deps.Cfg.Avatar.GravatarFallback = false
	deps.AvatarHTTPClient = cdn.Client()
	seedUserRow(t, deps.DB, model.User{ID: "u_a", DisplayName: "A", Email: addr("alice@example.com")})

	if err := New(deps).cacheAvatarSync(context.Background(), "u_a", ""); err != nil {
		t.Fatalf("cacheAvatarSync with no source: %v", err)
	}
	if requests != 0 {
		t.Errorf("upstream received %d requests, want 0", requests)
	}
}

// A Gravatar miss (the 404 d=404 asks for) leaves the account with no cached
// avatar rather than caching the error page.
func TestGravatarMissLeavesNoCachedAvatar(t *testing.T) {
	gravatar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer gravatar.Close()

	_, deps := newTestApp(t)
	deps.AvatarHTTPClient = gravatar.Client()
	seedUserRow(t, deps.DB, model.User{ID: "u_a", DisplayName: "A"})

	if err := New(deps).cacheAvatarSync(context.Background(), "u_a", gravatar.URL+"/avatar/hash?d=404"); err == nil {
		t.Fatal("cacheAvatarSync accepted a 404 response")
	}
	if _, ok := getStorageObject(t, deps, avatarKey("u_a")); ok {
		t.Error("a Gravatar miss cached an object")
	}
	u, err := deps.Users.Get(context.Background(), "u_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if u.AvatarKey != "" {
		t.Errorf("avatar_key = %q after a Gravatar miss, want empty", u.AvatarKey)
	}
}

// The provider's picture is fetched through the same pipeline every other
// avatar goes through, and lands on the account's fixed object key.
func TestOIDCPictureIsCachedThroughTheAvatarPipeline(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(jpegBody(64))
	}))
	defer cdn.Close()

	_, deps := newTestApp(t)
	deps.AvatarHTTPClient = cdn.Client()
	deps.Cfg.Avatar.GravatarFallback = true
	seedUserRow(t, deps.DB, model.User{ID: "u_a", DisplayName: "A", Email: addr("alice@example.com")})
	h := New(deps)

	user, err := deps.Users.Get(context.Background(), "u_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	picture := cdn.URL + "/picture.png"
	if err := h.cacheAvatarSync(context.Background(), user.ID, h.avatarSourceURL(user, picture)); err != nil {
		t.Fatalf("cacheAvatarSync: %v", err)
	}

	body, ok := getStorageObject(t, deps, avatarKey("u_a"))
	if !ok || len(body) != 64 {
		t.Fatalf("storage object = (%v, %d bytes), want 64 bytes", ok, len(body))
	}
	cached, err := deps.Users.Get(context.Background(), "u_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cached.AvatarSourceURL != picture {
		t.Errorf("avatar_source_url = %q, want the provider picture %q", cached.AvatarSourceURL, picture)
	}
}

// The OIDC "picture" claim is a profile field the user controls on most
// identity providers, so the fetch pipeline must not follow it into the
// server's own network.
func TestAvatarFetchRefusesNonPublicAddresses(t *testing.T) {
	// The production client is what a real fetch uses; the loopback address
	// the fake CDN is on is exactly the class this refuses.
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(jpegBody(16))
	}))
	defer internal.Close()

	_, deps := newTestApp(t)
	seedUserRow(t, deps.DB, model.User{ID: "u_a", DisplayName: "A"})

	err := New(deps).cacheAvatarSync(context.Background(), "u_a", internal.URL+"/picture.png")
	if err == nil {
		t.Fatal("the avatar pipeline reached a loopback address")
	}
	if !strings.Contains(err.Error(), "non-public address") {
		t.Fatalf("error = %v, want it to name the refused address class", err)
	}
	if _, ok := getStorageObject(t, deps, avatarKey("u_a")); ok {
		t.Error("a refused fetch cached an object")
	}
}

// A scheme that never dials would slip past the dialer's address check, so it
// is refused before the request is built.
func TestAvatarFetchRejectsNonHTTPSchemes(t *testing.T) {
	_, deps := newTestApp(t)
	seedUserRow(t, deps.DB, model.User{ID: "u_a", DisplayName: "A"})
	h := New(deps)

	for _, src := range []string{"file:///etc/passwd", "data:image/png;base64,AAAA", "gopher://x/1"} {
		err := h.cacheAvatarSync(context.Background(), "u_a", src)
		if err == nil || !strings.Contains(err.Error(), "scheme") {
			t.Errorf("cacheAvatarSync(%q) = %v, want a scheme rejection", src, err)
		}
	}
}

func TestPublicUnicast(t *testing.T) {
	refused := []string{
		"127.0.0.1", "::1", // loopback
		"10.0.0.1", "192.168.1.1", "172.16.0.1", "fd00::1", // private
		"169.254.169.254", "fe80::1", // link-local: cloud metadata lives here
		"100.64.0.1", "100.127.255.255", // carrier-grade NAT
		"0.0.0.0", "224.0.0.1", // unspecified, multicast
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", // IPv4-mapped IPv6
	}
	for _, s := range refused {
		if publicUnicast(net.ParseIP(s)) {
			t.Errorf("publicUnicast(%s) = true, want false", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "93.184.216.34", "2606:2800:220:1::1", "100.63.255.255", "100.128.0.1"} {
		if !publicUnicast(net.ParseIP(s)) {
			t.Errorf("publicUnicast(%s) = false, want true", s)
		}
	}
}
