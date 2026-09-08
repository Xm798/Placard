package handler

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/logger"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/session"
	"github.com/Xm798/placard/internal/userctx"
)

// AuthLogout deletes the server-side session and clears the cookie. Runs
// through the CSRF middleware like every cookie-channel POST.
func (h *Handlers) AuthLogout(c *fiber.Ctx) error {
	sid := c.Cookies(h.deps.SessionCookie)
	if sid == "" {
		h.clearSessionCookie(c)
		return c.SendStatus(fiber.StatusNoContent)
	}
	if h.deps.Sessions == nil {
		return apperr.Unavailable()
	}
	d, err := h.deps.Sessions.Get(c.UserContext(), sid)
	if err == nil {
		if err := h.deps.Sessions.Delete(c.UserContext(), sid); err != nil {
			return apperr.Unavailable()
		}
		h.auditBestEffort(c, &model.AuditLog{
			Action: "auth.logout", Outcome: "success",
			AuthChannel:  userctx.ChannelSession,
			Actor:        d.AuthzID,
			ActorName:    d.DisplayName,
			ResourceType: "session",
		})
		logStateChange(c, "auth.logout", "", ctxlog.OutcomeSuccess)
	} else if err != session.ErrNotFound {
		return apperr.Unavailable()
	}
	h.clearSessionCookie(c)
	return c.SendStatus(fiber.StatusNoContent)
}

// AuthLoggedOut serves the "logged out" landing page (GET /auth/logged-out,
// auth-exempt via the middleware's built-in skips). The client redirects here
// rather than to the login page so a logout is visibly final instead of looking
// like a fresh login prompt. The page carries no JavaScript and only links to
// login.
func (h *Handlers) AuthLoggedOut(c *fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	setCommonSecurityHeaders(c)
	varyByLanguage(c)
	c.Set("Content-Security-Policy", "default-src 'self'; "+
		"style-src 'self' 'unsafe-inline'; script-src 'none'; "+
		"img-src 'self' data:; frame-ancestors 'self'; base-uri 'none'")
	return c.Send(loggedOutPages[pageLang(c)])
}

// Me returns the authenticated user's identity.
//
// The user row is the source of truth for everything but the fallback: a
// session carries a display-name snapshot taken at login, and username and
// is_admin are not in it at all — the frontend needs both to decide what to
// render, and a stale admin flag would show an admin entry point to someone who
// has just been demoted. An identity whose row is missing still answers from
// the snapshot rather than failing.
//
// avatar_url is deliberately NOT refreshed from the row. user.avatar_source_url
// is an upstream address the server fetches from, not something to hand out:
// with the Gravatar fallback it is a hash of the account's email address. The
// frontend builds the same-origin proxy path from authz_id and never reads this
// field; it carries only the session snapshot, for callers that still do.
func (h *Handlers) Me(c *fiber.Ctx) error {
	id, ok := userctx.Get(c)
	if !ok {
		return apperr.Unauthorized()
	}
	resp := dto.MeResponse{
		AuthzID:     id.AuthzID,
		DisplayName: id.DisplayName,
		AvatarURL:   id.AvatarURL,
	}
	if h.deps.Users != nil {
		u, err := h.deps.Users.Get(c.UserContext(), id.AuthzID)
		switch {
		case err == nil:
			// The row wins on everything it actually holds. An empty
			// display_name is not an answer, though — the session's snapshot is
			// what the header has been showing, and replacing it with "" would
			// blank the name rather than correct it.
			if u.DisplayName != "" {
				resp.DisplayName = u.DisplayName
			}
			resp.Username = u.Username
			resp.IsAdmin = u.IsAdmin
		case !errors.Is(err, gorm.ErrRecordNotFound):
			logger.Module("auth").Warn("load display profile failed",
				zap.String("authz_id", id.AuthzID), zap.Error(err))
		}
	}
	return c.JSON(resp)
}
