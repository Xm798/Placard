package middleware

import (
	"net/url"
	"strings"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/config"
	"github.com/gofiber/fiber/v2"
)

// csrfExactExempt lists the ONLY requests that bypass the CSRF middleware,
// keyed by exact "METHOD path". Exact paths on purpose: a "/auth/*" prefix
// exemption would silently disarm POST /auth/logout (see auth.go's comment —
// it relies on this middleware like every other cookie-channel POST), and
// StrictRouting is off, so a prefix rule would also cover trailing-slash and
// future sibling routes nobody reviewed.
//
// TWO DIFFERENT REASONS live in this table. Do not collapse them into one:
//
//   - /auth/device/code, /auth/device/token consume NO cookie. Their identity
//     comes entirely from the device_code in the request body, so the cookie a
//     browser auto-attaches is meaningless to them and CSRF protection is
//     meaningless too. Exempting them costs nothing.
//
//   - /auth/device/approve DOES consume the session cookie — it is exactly the
//     kind of write CSRF exists to protect. It is here only because the
//     middleware is UNUSABLE for it: the check below hard-requires
//     X-Requested-With, and the confirmation page is a script-src 'none' plain
//     <form> that cannot set any custom header. Its protection is
//     re-implemented INSIDE the handler — synchronizer token bound to the
//     session and consumed once, Sec-Fetch-Site: same-origin fail-closed, and
//     Origin ∈ AllowedOrigins. DELETING THAT HANDLER-SIDE CHECK LEAVES APPROVE
//     COMPLETELY UNPROTECTED. Exempting the middleware is not exempting the
//     protection.
var csrfExactExempt = map[string]struct{}{
	"POST /auth/device/code":    {},
	"POST /auth/device/token":   {},
	"POST /auth/device/approve": {},
}

// CSRF protects cookie-channel writes. The session cookie is sent
// automatically by the browser, so multipart/form-data POSTs are CSRF-able.
//
// The PAT channel (Authorization: Bearer) is naturally immune (browsers never
// auto-attach Bearer) and is exempt. For the cookie channel, a write must
// satisfy ALL of:
//   - Origin or Referer present and its origin ∈ AllowedOrigins (main origin only)
//   - Sec-Fetch-Site: same-origin
//   - a custom header (X-Requested-With) a simple request cannot set
//
// Any failure → 403. Safe methods (GET/HEAD/OPTIONS) pass through.
func CSRF(cfg config.CSRFConfig) fiber.Handler {
	allowed := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		allowed[strings.TrimRight(o, "/")] = struct{}{}
	}

	return func(c *fiber.Ctx) error {
		if isSafeMethod(c.Method()) {
			return c.Next()
		}

		if _, ok := csrfExactExempt[c.Method()+" "+c.Path()]; ok {
			return c.Next()
		}

		// PAT channel is exempt — Bearer credentials are not auto-attached.
		if _, ok := bearerToken(c); ok {
			return c.Next()
		}

		origin := originOf(c)
		if origin == "" {
			return forbidden()
		}
		if _, ok := allowed[origin]; !ok {
			return forbidden()
		}

		if !strings.EqualFold(c.Get("Sec-Fetch-Site"), "same-origin") {
			return forbidden()
		}

		if c.Get("X-Requested-With") == "" {
			return forbidden()
		}

		return c.Next()
	}
}

// originOf returns the request origin derived from the Origin header, falling
// back to the scheme+host of the Referer. Returns "" when neither is usable.
func originOf(c *fiber.Ctx) string {
	if o := strings.TrimSpace(c.Get(fiber.HeaderOrigin)); o != "" {
		return strings.TrimRight(o, "/")
	}
	if ref := strings.TrimSpace(c.Get(fiber.HeaderReferer)); ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Scheme != "" && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	return ""
}

// RequestOrigin is originOf, exported for handlers that must re-implement the
// CSRF check themselves — POST /auth/device/approve is exempt from this
// middleware (see csrfExactExempt) yet still has to validate Origin, and it
// must use the exact same normalization rather than a second copy of it.
func RequestOrigin(c *fiber.Ctx) string { return originOf(c) }

// OriginAllowed reports whether origin is in cfg.AllowedOrigins, applying the
// same trailing-slash normalization CSRF() applies when it builds its set.
func OriginAllowed(cfg config.CSRFConfig, origin string) bool {
	if origin == "" {
		return false
	}
	for _, o := range cfg.AllowedOrigins {
		if strings.TrimRight(o, "/") == origin {
			return true
		}
	}
	return false
}

func isSafeMethod(m string) bool {
	switch m {
	case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
		return true
	}
	return false
}

// forbidden returns the typed 403 error; CSRF failure is a request-level
// rejection (not an owner-scope resource miss), so 403 is correct here — it does
// not leak resource existence. The caller returns it so Fiber's ErrorHandler
// renders the unified envelope.
func forbidden() *apperr.Error {
	return apperr.PermissionDenied()
}
