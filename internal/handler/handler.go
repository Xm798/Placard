// Package handler implements the Placard HTTP endpoints (Fiber).
//
// Security invariants enforced here:
//   - Ownership/authz/audit identity is always userctx.AuthzID().
//   - Rendering goes through the same-origin proxy /s/:id/render, which validates
//     expiry/deletion before streaming the object with Content-Disposition:
//     inline under a CSP sandbox. No srcdoc, no /raw.
//   - Expired/deleted/missing/denied meta returns ONLY {expired:true} — no
//     render_url, no title. render_url is a same-origin path (/s/:id/render).
//   - Outbound responses go through dto, never raw ORM structs.
package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/Xm798/placard/internal/logger"
	"github.com/Xm798/placard/internal/middleware"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/userctx"
)

const (
	// nanoIDLen is the share id length (62^8 keyspace).
	nanoIDLen = 8
	// patNanoIDLen is the random body length of a PAT (≈192-bit).
	patNanoIDLen = 32
	// patPrefix marks PATs for recognition and secret scanning.
	patPrefix = "pl_"
	// htmlContentType is the Content-Type stored on objects and re-sent by
	// the /s/:id/render proxy so the viewer iframe renders HTML inline.
	htmlContentType = "text/html; charset=utf-8"
	// nanoIDMaxRetries bounds uk_nano_id collision and version CAS retries.
	nanoIDMaxRetries = 3
)

// neverSentinel is the 9999 "never expires" marker.
var neverSentinel = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

// fileExpired reports whether f's lifetime has elapsed. GetOwned/ownedFile
// only enforce ownership + is_deleted=0, not liveness — mutation endpoints
// that would otherwise resurrect an expired-but-not-yet-cleaned-up page
// (republish, restore) call this explicitly and map a hit to the same
// indistinguishable 404 as an owner-scope miss. PATCH (pin/unpin) and GET
// versions deliberately keep reading an expired page (its history is still
// inspectable/manageable) — only endpoints that write a NEW version behind
// the share link enforce this.
func fileExpired(f *model.File) bool {
	return f.ExpiresAt.Before(time.Now())
}

// Handlers binds Deps to the route handler methods.
type Handlers struct {
	deps Deps
	// lastUsed stamps token.last_used_at after successful Bearer auth
	// (throttled, async). Wired in New; tests swap in a fake touch func.
	lastUsed *lastUsedToucher
	// cliVersion pins the CLI release the served install scripts install.
	cliVersion *cliVersionResolver
}

func (h *Handlers) insertAudit(c *fiber.Ctx, entry *model.AuditLog) error {
	if h.deps.Audit == nil {
		return nil
	}
	if entry.Outcome == "" {
		entry.Outcome = "success"
	}
	if entry.AuthChannel == "" {
		entry.AuthChannel = authChannel(c)
	}
	if entry.CreateUser == "" {
		entry.CreateUser = entry.Actor
	}
	entry.IP = middleware.ClientIP(c)
	entry.UserAgent = truncateUA(c.Get(fiber.HeaderUserAgent))
	entry.RequestID = truncateString(middleware.RequestIDFromCtx(c), 64)
	return h.deps.Audit.Insert(c.UserContext(), entry)
}

func truncateString(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}

func (h *Handlers) auditBestEffort(c *fiber.Ctx, entry *model.AuditLog) {
	if err := h.insertAudit(c, entry); err != nil {
		logger.Module("audit").Error("insert failed",
			zap.String("action", entry.Action),
			zap.String("request_id", middleware.RequestIDFromCtx(c)),
			zap.Error(err))
	}
}

func authChannel(c *fiber.Ctx) string {
	if identity, ok := userctx.Get(c); ok {
		return identity.AuthChannel
	}
	return ""
}

// hashToken computes HMAC-SHA256(secretKey, token) as lowercase hex. The key
// lives outside the DB (server.secret_key / <data_dir>/secret.key) so a
// token-table dump alone is useless.
func hashToken(secretKey, token string) string {
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

// objectKey builds the versioned object key {year}/{month}/{nanoid}-{version}.html.
func objectKey(nanoID string, version int) string {
	now := time.Now().UTC()
	return fmt.Sprintf("%04d/%02d/%s-%d.html", now.Year(), int(now.Month()), nanoID, version)
}

// objectVersionKey builds a unique object key for one version publish attempt.
func objectVersionKey(nanoID string, version int) (string, error) {
	nonce := make([]byte, 4)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate version key nonce: %w", err)
	}
	now := time.Now().UTC()
	return fmt.Sprintf("%04d/%02d/%s-%d-%s.html", now.Year(), int(now.Month()), nanoID, version, hex.EncodeToString(nonce)), nil
}

// parseFileExpiry converts an expiry token to an absolute expires_at for files.
// Permanent is legitimate for files (F16): "never"/"permanent" — and an omitted/
// empty expiry, which defaults to permanent — map to the 9999 sentinel.
// Bounded forms: "<n>d/w/y".
func parseFileExpiry(expiry string) (time.Time, error) {
	expiry = strings.TrimSpace(strings.ToLower(expiry))
	if expiry == "" || expiry == "never" || expiry == "permanent" {
		return neverSentinel, nil
	}
	d, err := parseDuration(expiry)
	if err != nil {
		return time.Time{}, err
	}
	return model.Timestamp(time.Now().Add(d)), nil
}

// parseTokenExpiry converts an expiry token to an absolute expires_at for PATs.
// The server enforces a max lifetime: "never" or anything beyond maxDays is
// truncated to maxDays — there is no truly-permanent option for tokens.
func parseTokenExpiry(expiry string, maxDays int) (time.Time, error) {
	if maxDays <= 0 {
		maxDays = 365
	}
	cap := model.Timestamp(time.Now().Add(time.Duration(maxDays) * 24 * time.Hour))

	expiry = strings.TrimSpace(strings.ToLower(expiry))
	if expiry == "" || expiry == "never" || expiry == "permanent" {
		return cap, nil
	}
	d, err := parseDuration(expiry)
	if err != nil {
		return time.Time{}, err
	}
	exp := model.Timestamp(time.Now().Add(d))
	if exp.After(cap) {
		return cap, nil
	}
	return exp, nil
}

// parseDuration parses "<n><unit>" where unit is d(ays)/w(eeks)/y(ears).
func parseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid expiry %q", s)
	}
	unit := s[len(s)-1]
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid expiry %q", s)
	}
	day := 24 * time.Hour
	switch unit {
	case 'd':
		return time.Duration(n) * day, nil
	case 'w':
		return time.Duration(n) * 7 * day, nil
	case 'y':
		return time.Duration(n) * 365 * day, nil
	default:
		return 0, fmt.Errorf("invalid expiry unit in %q", s)
	}
}

// looksLikeHTML decides whether body (with optional declared content type) is
// HTML. The server never trusts the frontend's accept hint: it inspects the
// declared type AND sniffs the leading bytes for <!doctype / <html.
func looksLikeHTML(contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "text/html") {
		return true
	}
	// sniff first non-space bytes
	head := strings.ToLower(strings.TrimSpace(string(body)))
	if len(head) > 1024 {
		head = head[:1024]
	}
	return strings.Contains(head, "<!doctype") || strings.Contains(head, "<html")
}
