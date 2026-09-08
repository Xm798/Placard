package handler

import (
	"strings"

	"github.com/gofiber/fiber/v2"
)

// isPlainHTTP is case-insensitive because a scheme is: a base_url written
// "HTTP://box.internal" that read as HTTPS would ship __Host- cookies the
// browser silently drops, and every flow that depends on one would loop with
// no error.
func isPlainHTTP(baseURL string) bool {
	return strings.HasPrefix(strings.ToLower(baseURL), "http://")
}

// secureCookies reports whether cookies this instance issues can carry Secure
// and the __Host- prefix, which requires it and is therefore only honoured
// over HTTPS. Derived from server.base_url rather than a switch of its own: an
// operator who serves the instance over TLS already says so there, and a
// separate flag could be left in its development position on a production
// deployment.
func (h *Handlers) secureCookies() bool {
	return !isPlainHTTP(h.deps.Cfg.Server.BaseURL)
}

// SessionCookieName returns the login cookie's name for an instance served
// from baseURL. __Host- pins the cookie to this exact host at path / and is
// honoured only alongside Secure, so a plain-HTTP instance gets the bare name
// and can still hold a session — a LAN deployment on http://192.168.x.x is a
// legitimate way to self-host.
//
// The prefix and setSessionCookie's Secure attribute MUST follow the same
// predicate. A __Host- cookie that arrives without Secure is discarded with no
// error and no console message, which reads to the operator as "sign-in
// returns 200, then the UI bounces back to the login page" — they live in one
// file so the two cannot drift apart again.
func SessionCookieName(baseURL, name string) string {
	if isPlainHTTP(baseURL) {
		return name
	}
	return "__Host-" + name
}

// setSessionCookie writes the login cookie. Its attributes must stay identical
// to clearSessionCookie's — a Set-Cookie differing in Path or Secure creates a
// second cookie instead of replacing the first.
func (h *Handlers) setSessionCookie(c *fiber.Ctx, value string, maxAge int) {
	c.Cookie(&fiber.Cookie{
		Name:     h.deps.SessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   h.secureCookies(),
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteLaxMode,
	})
}

// clearSessionCookie expires the login cookie, repeating the attributes it was
// set with.
func (h *Handlers) clearSessionCookie(c *fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name:     h.deps.SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   h.secureCookies(),
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteLaxMode,
	})
}
