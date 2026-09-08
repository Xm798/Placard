package handler

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/logger"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/sharecode"
	"github.com/Xm798/placard/internal/userctx"
)

// shareTicketTTL is how long one unlock lasts. It is deliberately long — the
// point of the code is that a visitor types it once and then reads the page
// like any other link — and is capped by the page's own expiry, so a ticket
// never outlives the content it unlocks.
const shareTicketTTL = 7 * 24 * time.Hour

// shareCookieName is the cookie an unlock ticket for nanoID lives in.
func (h *Handlers) shareCookieName(nanoID string) string {
	return sharecode.CookieName(h.secureCookies(), nanoID)
}

// activeShareCode returns the page's plaintext share code and whether it has
// one at all.
//
// A stored code that will not decrypt reads as "no code": server.secret_key
// was rotated or lost, and the alternative — refusing to serve the page — would
// lock the owner out of their own content over a key change they can no longer
// undo. It is logged because it is a real misconfiguration, and the page is
// open to anyone holding the link until the owner generates a new code.
func (h *Handlers) activeShareCode(file *model.File) (string, bool) {
	if file.ShareCodeEnc == "" {
		return "", false
	}
	code, err := sharecode.Open(h.deps.Cfg.Server.SecretKey, file.ShareCodeEnc)
	if err != nil {
		logger.Module("sharecode").Warn("stored share code does not decrypt; serving the page as uncoded",
			zap.String("nano_id", file.NanoID), zap.Error(err))
		return "", false
	}
	return code, true
}

// shareCodeLocked reports whether file's share code still stands between this
// caller and the content. The owner is never locked out of their own page, and
// a valid unlock ticket clears it for everyone else.
//
// Callers MUST run authz.View (authorizeViewOrAudit) first: this is the second
// gate, not a replacement for the first — a `private` page stays a 404 for a
// stranger whether or not it also carries a code.
func (h *Handlers) shareCodeLocked(c *fiber.Ctx, file *model.File, authzid string) bool {
	if file.ShareCodeEnc == "" {
		return false
	}
	if authzid != "" && authzid == file.CreateUser {
		return false
	}
	if _, has := h.activeShareCode(file); !has {
		return false
	}
	return !sharecode.ValidTicket(h.deps.Cfg.Server.SecretKey, file.NanoID,
		file.ShareCodeVersion, time.Now(), c.Cookies(h.shareCookieName(file.NanoID)))
}

// errShareCodeRequired is the 403 meta and render answer a locked page with.
// It is a distinct code from a plain permission denial so the viewer shell can
// send the visitor to the unlock page instead of the generic failure state,
// and it confirms nothing a locked /s/:id does not already say.
func errShareCodeRequired() *apperr.Error {
	return apperr.New("share_code_required", fiber.StatusForbidden, "share code required")
}

// GenerateShareCode handles POST /api/files/:id/share-code. Owner-only: a
// non-owner or unknown id is an indistinguishable 404. It mints a fresh code
// even when the page already has one — regenerating is how an owner revokes
// access, and the version bump inside SetShareCode expires every ticket issued
// under the old one.
func (h *Handlers) GenerateShareCode(c *fiber.Ctx) error {
	nanoID := c.Params("id")
	_, authzid, err := h.ownedFile(c, nanoID)
	if err != nil {
		return err
	}

	code, err := sharecode.Generate()
	if err != nil {
		return apperr.Internal("could not generate a share code")
	}
	enc, err := sharecode.Seal(h.deps.Cfg.Server.SecretKey, code)
	if err != nil {
		return apperr.Internal("could not generate a share code")
	}
	if err := h.deps.Files.SetShareCode(h.deps.DB.WithContext(c.UserContext()), nanoID, enc, authzid); err != nil {
		return apperr.Internal("could not save the share code")
	}

	h.auditFile(c, authzid, nanoID, "file.share_code_set")
	logStateChange(c, "file.share_code_set", nanoID, ctxlog.OutcomeSuccess)
	return c.JSON(dto.ShareCodeResponse{ShareCode: code})
}

// ClearShareCode handles DELETE /api/files/:id/share-code, returning the page
// to a plain link anyone can open. Idempotent: clearing a page that has no code
// still bumps the version, which costs nothing and keeps the write unconditional.
func (h *Handlers) ClearShareCode(c *fiber.Ctx) error {
	nanoID := c.Params("id")
	_, authzid, err := h.ownedFile(c, nanoID)
	if err != nil {
		return err
	}
	if err := h.deps.Files.SetShareCode(h.deps.DB.WithContext(c.UserContext()), nanoID, "", authzid); err != nil {
		return apperr.Internal("could not clear the share code")
	}
	h.auditFile(c, authzid, nanoID, "file.share_code_clear")
	logStateChange(c, "file.share_code_clear", nanoID, ctxlog.OutcomeSuccess)
	return c.SendStatus(fiber.StatusNoContent)
}

// unlockRequest is the POST /s/:id/unlock body.
type unlockRequest struct {
	Code string `json:"code"`
}

// Unlock handles POST /s/:id/unlock: the visitor submits the code and, when it
// matches, receives the per-file ticket cookie that clears meta and render.
//
// A page that cannot be viewed at all, one that carries no code, and one that
// does not exist all answer the same 404 — the endpoint must not become a way
// to ask which pages are code-protected.
//
// The failure budget is per file AND per IP: one address hammering one page is
// what it exists to stop, and keying it on the file too means an attacker
// cannot spend another page's budget to cover their tracks, nor lock a
// bystander out of a different page. It is consulted before the code is
// checked, so it also bounds how many file.unlock_denied rows one caller can
// append per window.
//
// Only the denial is audited, mirroring what /render does with anonymous
// views: a success here names nobody the access log has not already recorded,
// while audit_log is append-only with no retention sweep.
func (h *Handlers) Unlock(c *fiber.Ctx) error {
	nanoID := c.Params("id")

	c.Set("Cache-Control", "no-store")

	file, err := h.deps.Files.GetActiveByNanoID(c.UserContext(), nanoID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.Unavailable()
		}
		return apperr.NotFound("not found")
	}
	authzid := userctx.AuthzID(c)
	if !h.authorizeViewOrAudit(c, file, authzid) {
		return apperr.NotFound("not found")
	}
	stored, has := h.activeShareCode(file)
	if !has {
		return apperr.NotFound("not found")
	}

	if h.shareCodeExceeded(c, file.NanoID) {
		return apperr.RateLimited("too many attempts; try again in a minute")
	}

	var req unlockRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return apperr.Validation("invalid json body")
	}
	// A malformed submission is a wrong code like any other: same budget, same
	// answer. Checking the shape first only avoids comparing against a value
	// that could not have matched.
	if !sharecode.Valid(req.Code) || !sharecode.Equal(req.Code, stored) {
		h.shareCodeHit(c, file.NanoID)
		h.auditBestEffort(c, &model.AuditLog{
			Action:       "file.unlock_denied",
			Outcome:      "denied",
			Actor:        authzid,
			FileNanoID:   file.NanoID,
			ResourceType: "file",
			ResourceID:   file.NanoID,
		})
		return apperr.PermissionDenied("incorrect share code")
	}

	exp := h.ticketExpiry(file)
	// The stored nano id, never the raw path parameter: what goes into a
	// cookie NAME must come from the database, not from the request.
	c.Cookie(&fiber.Cookie{
		Name:     h.shareCookieName(file.NanoID),
		Value:    sharecode.Ticket(h.deps.Cfg.Server.SecretKey, file.NanoID, file.ShareCodeVersion, exp),
		Path:     "/",
		MaxAge:   int(time.Until(exp).Seconds()),
		Secure:   h.secureCookies(),
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteLaxMode,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

// ticketExpiry is shareTicketTTL from now, never past the page's own expiry —
// an unlock must not outlive what it unlocks. The 9999 sentinel is always
// further out, so a page that never expires simply gets the full TTL.
func (h *Handlers) ticketExpiry(file *model.File) time.Time {
	exp := time.Now().Add(shareTicketTTL)
	if file.ExpiresAt.Before(exp) {
		return file.ExpiresAt
	}
	return exp
}

// shareCodeSubject is the failure budget's key: this file as seen from this
// address. IPLimiter keys on whatever subject it is handed, so composing the
// two here is what makes the budget per-file+IP.
func shareCodeSubject(c *fiber.Ctx, nanoID string) string {
	return nanoID + "|" + c.IP()
}

func (h *Handlers) shareCodeExceeded(c *fiber.Ctx, nanoID string) bool {
	if h.deps.ShareCodeLimiter == nil {
		return false
	}
	return h.deps.ShareCodeLimiter.Exceeded(c.UserContext(), shareCodeSubject(c, nanoID))
}

func (h *Handlers) shareCodeHit(c *fiber.Ctx, nanoID string) {
	if h.deps.ShareCodeLimiter == nil {
		return
	}
	h.deps.ShareCodeLimiter.Hit(c.UserContext(), shareCodeSubject(c, nanoID))
}

// mintShareCode seals a fresh code onto a file row that is about to be
// inserted, so `publish --password auto` commits the page and its code in the
// same transaction rather than leaving a window where the link works without
// one. Returns the plaintext for the one response that carries it.
func (h *Handlers) mintShareCode(file *model.File) (string, error) {
	code, err := sharecode.Generate()
	if err != nil {
		return "", err
	}
	enc, err := sharecode.Seal(h.deps.Cfg.Server.SecretKey, code)
	if err != nil {
		return "", err
	}
	file.ShareCodeEnc = enc
	file.ShareCodeVersion = 1
	return code, nil
}
