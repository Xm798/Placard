package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/logger"
)

const (
	// avatarCacheTimeout bounds the whole detached cacheAvatarAsync goroutine
	// (row read + HTTP fetch + storage put + row write), mirroring
	// lastUsedTouchTimeout's role for the token toucher.
	avatarCacheTimeout = 5 * time.Second
	// avatarFetchTimeout bounds the HTTP GET to the upstream avatar URL.
	avatarFetchTimeout = 3 * time.Second
	// avatarMaxBytes is the spec-fixed transfer cap; anything larger is
	// dropped rather than cached.
	avatarMaxBytes = 256 << 10
)

// avatarContentTypes is the strict content-type allow-list — no SVG (a
// script-capable format), matching the spec constant exactly.
var avatarContentTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

// cacheAvatarAsync mirrors lastUsedToucher's detached-goroutine pattern: the
// actual fetch+cache work runs off the request path with its own timeout, so a
// slow or broken upstream CDN can never slow down the request that triggered
// it. Every failure is logged only — best-effort, no retry.
func (h *Handlers) cacheAvatarAsync(authzID, srcURL string) {
	if strings.TrimSpace(srcURL) == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), avatarCacheTimeout)
		defer cancel()
		if err := h.cacheAvatarSync(ctx, authzID, srcURL); err != nil {
			logger.Module("avatar").Warn("cache avatar failed",
				zap.String("authz_id", authzID), zap.Error(err))
		}
	}()
}

// avatarHTTPClient returns the injectable client (Deps.AvatarHTTPClient, for
// tests pointing at an httptest fake CDN or exercising the timeout gate) or the
// production client: bounded by avatarFetchTimeout and refused any address that
// is not publicly routable.
//
// The injected client deliberately carries no such restriction — the fake CDNs
// the tests point it at are all on loopback.
func (h *Handlers) avatarHTTPClient() *http.Client {
	if h.deps.AvatarHTTPClient != nil {
		return h.deps.AvatarHTTPClient
	}
	return publicOnlyClient
}

// publicOnlyClient is the outbound client every production avatar fetch uses.
//
// The URL it is handed is attacker-influenced: it is the OIDC "picture" claim,
// which on most identity providers is a profile field the user sets. Without a
// destination check the server would be a request proxy into its own network —
// a cloud metadata endpoint, an unauthenticated admin port — reachable by
// putting the address in a profile. Filtering happens at dial time rather than
// on the URL string because that is the only point that sees the address the
// connection actually goes to, after DNS and after every redirect.
var publicOnlyClient = &http.Client{
	Timeout: avatarFetchTimeout,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: avatarFetchTimeout,
			Control: refusePrivateAddress,
		}).DialContext,
	},
}

// refusePrivateAddress is the net.Dialer control hook that rejects a connection
// to anything but a public unicast address.
func refusePrivateAddress(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("avatar fetch: unparsable address %q", address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("avatar fetch: unresolved address %q", host)
	}
	if !publicUnicast(ip) {
		return fmt.Errorf("avatar fetch: refused non-public address %s", ip)
	}
	return nil
}

// publicUnicast reports whether ip is an address the open internet routes to.
// Loopback, link-local (which is where cloud metadata services live),
// multicast and the RFC 1918 / RFC 4193 private ranges are all excluded.
func publicUnicast(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// Neither IsPrivate nor IsLinkLocal covers these: 100.64.0.0/10 is
	// carrier-grade NAT (a provider's internal space) and ::ffff:0:0/96 wraps
	// an IPv4 address that the checks above would otherwise not see.
	if v4 := ip.To4(); v4 != nil {
		return !(v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127)
	}
	return true
}

// cacheAvatarSync does the actual work synchronously — deliberately a
// separate, directly-testable function from cacheAvatarAsync's goroutine
// wrapper. Nil-guarded on Users/Storage so it is a safe no-op in tests/configs
// that don't wire them.
//
// Order: skip if unchanged (same source URL, already cached) → download under
// three gates (content-type allow-list, 256KB cap, client timeout) → PutObject
// to the fixed avatars/{authz_id} key (overwrite) → point the user row at it.
// Every step is best-effort: any failure returns an error for the caller to
// log, never a retry.
func (h *Handlers) cacheAvatarSync(ctx context.Context, authzID, srcURL string) error {
	if h.deps.Users == nil || h.deps.Storage == nil {
		return nil
	}
	// No source is a legitimate state — an account with neither a provider
	// picture nor a Gravatar address renders as its initial letter — and both
	// entry points reach here, so the decision lives here rather than only in
	// the async wrapper's early return.
	if strings.TrimSpace(srcURL) == "" {
		return nil
	}

	cur, err := h.deps.Users.Get(ctx, authzID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("load user row: %w", err)
	}
	if cur != nil && cur.AvatarSourceURL == srcURL && cur.AvatarKey != "" {
		return nil // unchanged since the last successful cache
	}

	body, ct, err := fetchValidatedImage(ctx, h.avatarHTTPClient(), srcURL)
	if err != nil {
		return err
	}

	key := "avatars/" + authzID
	if err := h.deps.Storage.PutObject(ctx, key, bytes.NewReader(body), ct); err != nil {
		return fmt.Errorf("put avatar object: %w", err)
	}
	if err := h.deps.Users.SetAvatar(ctx, authzID, srcURL, key); err != nil {
		return fmt.Errorf("set avatar key: %w", err)
	}
	return nil
}

// fetchValidatedImage GETs srcURL and applies the three gates shared by every
// avatar fetch path: 200 status, an allow-listed content-type, and the
// avatarMaxBytes read cap — one byte past the cap, so an over-limit body is
// distinguished from one landing exactly on it, rather than silently
// truncated.
func fetchValidatedImage(ctx context.Context, client *http.Client, srcURL string) (body []byte, contentType string, err error) {
	// A scheme allowlist before anything else: the dialer's address check only
	// applies to schemes that dial, and "file:" / "data:" would not.
	parsed, err := url.Parse(srcURL)
	if err != nil {
		return nil, "", fmt.Errorf("parse avatar url: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, "", fmt.Errorf("rejected avatar url scheme %q", parsed.Scheme)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build avatar request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch avatar: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("fetch avatar: unexpected status %d", resp.StatusCode)
	}

	ct := avatarBaseContentType(resp.Header.Get("Content-Type"))
	if !avatarContentTypes[ct] {
		return nil, "", fmt.Errorf("rejected content-type %q", ct)
	}

	// Read one byte past the cap so an over-limit body is detected (vs. a
	// body that happens to end exactly at the cap) and rejected rather than
	// silently truncated.
	b, err := io.ReadAll(io.LimitReader(resp.Body, avatarMaxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read avatar body: %w", err)
	}
	if len(b) > avatarMaxBytes {
		return nil, "", fmt.Errorf("avatar body exceeds %d bytes", avatarMaxBytes)
	}
	return b, ct, nil
}

// avatarBaseContentType strips any ";charset=..." parameter so the allow-list
// compares only the media type. Falls back to the trimmed raw header on a
// parse failure (e.g. empty header) — the allow-list check will reject it.
func avatarBaseContentType(raw string) string {
	base, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return base
}
