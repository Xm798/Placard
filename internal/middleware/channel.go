package middleware

import (
	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/userctx"
	"github.com/gofiber/fiber/v2"
)

// CookieChannel reports whether the request was authenticated by a browser
// cookie rather than a bearer credential.
//
// Exported for a handler that keeps its route open to both channels but
// withholds a browser-only field from the PAT one — see ListFiles, which
// serves `placard ls` over a token yet returns share codes only to a session.
func CookieChannel(c *fiber.Ctx) bool {
	switch userctx.AuthChannel(c) {
	case userctx.ChannelSession, userctx.ChannelDevMock:
		return true
	default:
		return false
	}
}

// BrowserOnly allows a request the browser made with its own cookie, and an
// anonymous one: the share-link routes it guards are open to visitors with no
// account, and decide for themselves what such a visitor may see.
//
// What it still rejects with 403 is a PAT-authenticated request. A CLI
// credential must never stand in for its owner's session on these routes — that
// would turn a leaked token into read access to every `private` page its owner
// has, while an anonymous caller reaches only `link` pages.
func BrowserOnly() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if userctx.AuthChannel(c) == userctx.ChannelPAT {
			return apperr.PermissionDenied("session required")
		}
		return c.Next()
	}
}

// SessionOnly allows only session-authenticated requests. It rejects PAT tokens
// and unauthenticated requests with 403 — endpoints behind it read data the
// logged-in user is entitled to but an API credential is not.
func SessionOnly() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if CookieChannel(c) {
			return c.Next()
		}
		return apperr.PermissionDenied("session required")
	}
}
