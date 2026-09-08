package handler

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/storage"
	"github.com/Xm798/placard/internal/userctx"
)

// Render handles GET /s/:id/render. It is the same-origin proxy that streams
// the stored HTML object back with Content-Disposition: inline, so the viewer
// shell's sandboxed iframe renders it (a backend need not persist the
// Content-Disposition supplied to PutObject, so only a proxy that owns the
// response headers can guarantee it). It is the authoritative, un-bypassable
// point at which the content is actually read, and therefore where a view is
// recorded — for an anonymous visitor too, whose view row carries an empty
// viewer.
//
// A caller authz.View denies gets the same 404 a missing id gets: a `private`
// page must not be distinguishable from one that was never published. A page
// carrying a share code answers 403 share_code_required until the caller
// presents the unlock ticket cookie — see Unlock.
func (h *Handlers) Render(c *fiber.Ctx) error {
	id := c.Params("id")

	// Direct-open guard: a top-level navigation must never render user HTML as a
	// document on the primary origin. Bounce it back to the shell, which loads
	// the content in a sandboxed iframe instead. Older browsers that omit
	// Sec-Fetch-Dest fall through to the CSP sandbox header set below.
	if c.Get("Sec-Fetch-Dest") == "document" {
		target := "/s/" + id
		if q := string(c.Request().URI().QueryString()); q != "" {
			target += "?" + q
		}
		return c.Redirect(target, fiber.StatusSeeOther)
	}

	// Same public, non-owner-scoped load as meta (is_deleted=0, not expired).
	file, err := h.deps.Files.GetActiveByNanoID(c.UserContext(), id)
	if err != nil {
		return apperr.NotFound()
	}

	// authorizeViewOrAudit runs before the ?v branch and before every side effect
	// below (Views.Upsert, the file.view audit, Storage.GetObject) — a denied
	// caller must never trigger any of them.
	authzid := userctx.AuthzID(c)
	if !h.authorizeViewOrAudit(c, file, authzid) {
		return apperr.NotFound()
	}
	// Second gate, and the load-bearing one for a code-protected page: /render
	// is the un-bypassable read point, so this is where a missing or stale
	// unlock ticket has to stop the content — before the view row, the audit
	// and the object read below.
	if h.shareCodeLocked(c, file, authzid) {
		return errShareCodeRequired()
	}

	// ?v=N owner-only preview: only the owner with an existing version row gets
	// the old object; everyone else falls back to the serving cache (param
	// ignored — no 404, no version probing).
	key := file.ObjectKey
	ownerPreview := false
	if v := c.QueryInt("v", 0); v > 0 && authzid != "" && authzid == file.CreateUser {
		if ver, verr := h.deps.Versions.GetByVersion(c.UserContext(), id, v); verr == nil {
			key = ver.ObjectKey
			ownerPreview = true
		}
	}

	// c.UserContext(), never c.Context() (pooled, reset once the response is
	// written), and never a WithTimeout ctx paired with `defer cancel()`: rc is
	// handed to SendStream and drained by fasthttp *after* this handler returns,
	// so cancelling on return truncates the page silently — no error, no log
	// line. See storage.Client's doc comment for the full rule.
	rc, err := h.deps.Storage.GetObject(c.UserContext(), key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return apperr.NotFound()
		}
		return apperr.Internal("could not load content")
	}

	// Record the view now the content is confirmed loadable: /render is the
	// un-bypassable read point (a client may fetch it without /meta), and
	// recording only after GetObject succeeds keeps "a view" meaning a real read.
	// Upsert keeps one row per (file, viewer) and increments its counter, so
	// view_count counts reads rather than readers; every anonymous visitor
	// shares the single empty-viewer row. Owner ?v= previews are
	// skipped entirely — an owner flipping through history must not pollute
	// their own view stats or the audit trail.
	//
	// Only a named reader is audited. audit_log is append-only with no
	// retention sweep, and a file.view row for an anonymous reader would add a
	// permanent row per page view of a page built to be viewed by anyone, while
	// naming nobody: the view row already counts the read, and the access log
	// already carries the per-request IP and user agent.
	if !ownerPreview {
		_ = h.deps.Views.Upsert(c.UserContext(), file.NanoID, authzid, displayName(c))
		if authzid != "" {
			h.auditFile(c, authzid, file.NanoID, "file.view")
		}
	}

	// Common HTML security headers (nosniff / HSTS / Referrer-Policy) via the
	// shared seam; only the content-specific headers below are set by hand.
	setCommonSecurityHeaders(c)
	c.Set(fiber.HeaderContentType, htmlContentType)
	c.Set("Content-Disposition", "inline")
	c.Set("Cache-Control", "no-store")
	// CSP sandbox is the load-bearing isolation for this endpoint: it forces the
	// document into an opaque origin under EVERY load path (including a direct
	// top-level navigation), so SameSite session cookies are never sent on its
	// subrequests and the user HTML can never execute on the primary origin.
	// NEVER add allow-same-origin — that would defeat the entire isolation.
	c.Set("Content-Security-Policy", "sandbox allow-scripts; frame-ancestors 'self'")

	// fasthttp's Response.closeBodyStream() closes a bodyStream implementing
	// io.Closer after the response is written (valyala/fasthttp http.go), so
	// SendStream owns the Close — a defer here would double-close.
	return c.SendStream(withLinkRelay(rc))
}
