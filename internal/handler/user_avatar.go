package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/storage"
)

// UserAvatar handles GET /api/users/:authz_id/avatar, a same-origin proxy for
// another user's avatar. It exists so the frontend never embeds a raw upstream
// CDN URL directly in an <img src> — the tightened SPA CSP (img-src 'self'
// data:, see AppPage) would block that anyway. Session-only: routes.go wires
// middleware.SessionOnly() ahead of this handler, so a PAT caller never
// reaches it. No per-user rate limit is attached deliberately — avatars are
// fetched in bursts (a file list) and share the global IP-level surface
// instead.
//
// The target's user row must carry a non-empty avatar_key, in which case the
// cached object is served with a long private cache and an ETag good for
// conditional (304) revalidation. No row, no avatar_key, or a missing object
// is a 404.
func (h *Handlers) UserAvatar(c *fiber.Ctx) error {
	targetID := c.Params("authz_id")
	if targetID == "" {
		return apperr.NotFound()
	}
	if h.deps.Users == nil || h.deps.Storage == nil {
		return apperr.NotFound()
	}

	user, err := h.deps.Users.Get(c.UserContext(), targetID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound()
		}
		return apperr.Internal("could not load user")
	}
	if user.AvatarKey == "" {
		return apperr.NotFound()
	}
	return h.serveStorageAvatar(c, user)
}

// serveStorageAvatar writes the cached avatar object, a 304, or a 404.
//
// The object is read fully into memory, capped at avatarMaxBytes+1 (one byte
// past the cap, so an over-limit object is distinguished from one landing
// exactly on it). The write side (cacheAvatarSync) already rejects anything
// larger, but a read-side cap keeps that from being an implicit cross-file
// invariant with nothing here to catch a violation of it. A streamed response
// can't be turned into a 404 once fasthttp has started writing it, so
// guaranteeing the reject response requires deciding pass/fail before anything
// is written — which, given the cap is only 256KB, makes buffering-then-Send
// the only way to get there.
func (h *Handlers) serveStorageAvatar(c *fiber.Ctx, user *model.User) error {
	etag := avatarETag(user.UpdateTime)
	// Computed from the user row alone, so a conditional request that matches
	// is answered without ever touching storage.
	if c.Get(fiber.HeaderIfNoneMatch) == etag {
		setAvatarHeaders(c, etag, "private, max-age=86400")
		return c.SendStatus(fiber.StatusNotModified)
	}

	rc, err := h.deps.Storage.GetObject(c.UserContext(), user.AvatarKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return apperr.NotFound()
		}
		return apperr.Internal("could not load avatar")
	}
	defer func() { _ = rc.Close() }()

	body, rerr := io.ReadAll(io.LimitReader(rc, avatarMaxBytes+1))
	if rerr != nil {
		return apperr.Internal("could not read avatar")
	}
	if len(body) > avatarMaxBytes {
		// Never seen in practice (cacheAvatarSync's write-side gate rejects
		// this at cache time), but the read side must not simply trust that
		// — see the doc comment above.
		return apperr.NotFound()
	}

	// http.DetectContentType only ever inspects its own first 512 bytes
	// (the mimesniff spec's cap) regardless of how much is passed in, so
	// handing it the full (already capped) body is equivalent to sniffing a
	// separate window and simpler now that nothing downstream needs the
	// unread remainder of a stream.
	ct := http.DetectContentType(body)
	if !avatarContentTypes[ct] {
		// A cached object whose bytes no longer look like an allow-listed
		// image (corrupted, or written by something else entirely) is
		// treated the same as "no avatar" — never served.
		return apperr.NotFound()
	}

	setAvatarHeaders(c, etag, "private, max-age=86400")
	c.Set(fiber.HeaderContentType, ct)
	return c.Send(body)
}

// avatarETag derives a weak validator from the user row's update_time: any
// change to the row (including a fresh SetAvatarKey write) advances it, which
// is exactly the invalidation signal a cached avatar needs.
func avatarETag(t time.Time) string {
	return fmt.Sprintf(`W/"%d"`, t.Unix())
}

// setAvatarHeaders sets the headers shared by the served and 304 responses,
// including X-Content-Type-Options: nosniff via setCommonSecurityHeaders.
func setAvatarHeaders(c *fiber.Ctx, etag, cacheControl string) {
	setCommonSecurityHeaders(c)
	c.Set("Cache-Control", cacheControl)
	c.Set("ETag", etag)
}
