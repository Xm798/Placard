package middleware

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/logger"
	"github.com/Xm798/placard/internal/session"
	"github.com/Xm798/placard/internal/userctx"
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// TokenValidator validates a PAT bearer token and returns the resolved
// identity. The handler package owns token storage/HMAC verification and
// supplies an implementation; the auth middleware calls it for requests that
// arrive with an "Authorization: Bearer" header (CLI / API channel).
//
// Contract:
//   - token is the raw value after the "Bearer " prefix (already trimmed).
//   - returns (identity, true) when the token is valid, unrevoked and unexpired
//     (validator enforces fail-closed expiry per plan).
//   - returns (_, false) for any invalid/revoked/expired token; the middleware
//     responds 401 and never falls through to the session cookie.
type TokenValidator func(token string) (userctx.Identity, bool)

// SessionGetter is the read side of the login session store, scoped down from
// session.Store so the middleware can be tested against a stub.
type SessionGetter interface {
	Get(ctx context.Context, id string) (session.Data, error)
}

// AuthOptions configures the authentication middleware.
//
// TokenValidator is optional; if set, requests carrying "Authorization: Bearer"
// are authenticated via the PAT channel instead of the session cookie.
type AuthOptions struct {
	DevMock        config.DevMockConfig
	TokenValidator TokenValidator
	Sessions       SessionGetter
	CookieName     string     // full cookie name incl. __Host- prefix
	FailLimiter    *IPLimiter // optional; throttles per-IP failed authn attempts

	// TouchActive, when set, is invoked with the resolved AuthzID after every
	// successful identity resolution (dev_mock, PAT, session) — nil-safe at the
	// call site, so it may be left unset in tests that don't care. main.go
	// wires an *ActiveToucher (throttled, async, update-only) here. Never
	// allowed to block or fail the auth decision.
	TouchActive func(authzID string)

	// AccountStatus reports whether a resolved user id still names an account
	// that may authenticate. It is what makes disabling a user take effect
	// immediately: an admin's decision has to reach the sessions and PATs
	// issued before it, and nothing else re-checks either.
	//
	// A false verdict (disabled, or the row is gone) is 401; a non-nil error is
	// 503, so a database blip never mass-logs-out live users. Left nil the
	// check is skipped — for tests that run no database.
	//
	// The dev_mock channel deliberately does not consult it: it is a local-only
	// bypass of credential checking altogether (config.Validate rejects it
	// outside APP_ENV=local), and main.go materializes its account row at boot.
	AccountStatus func(ctx context.Context, userID string) (bool, error)
}

// errAccountInactive marks the "credential is valid, account is not" verdict so
// each channel can render it in its own shape — a redirect to login for a
// browser navigation, a 401 envelope for everything else.
var errAccountInactive = errors.New("middleware: account disabled or missing")

// accountActive consults AccountStatus, if one is wired. It returns the error
// the caller must return, or nil to let the request proceed.
func (o AuthOptions) accountActive(c *fiber.Ctx, userID string, log *zap.Logger) error {
	if o.AccountStatus == nil {
		return nil
	}
	active, err := o.AccountStatus(c.UserContext(), userID)
	if err != nil {
		log.Error("account status lookup failed", zap.Error(err))
		return apperr.Unavailable()
	}
	if !active {
		return errAccountInactive
	}
	return nil
}

// touchActive invokes TouchActive when configured. Nil-safe.
func (o AuthOptions) touchActive(authzID string) {
	if o.TouchActive != nil {
		o.TouchActive(authzID)
	}
}

// hitFailure records a failed-authn attempt (bad PAT token) into the per-IP
// budget when a limiter is configured. Nil-safe. Note: idle-expired/unknown
// sessions deliberately do NOT hit this budget — see authenticateSession.
func (o AuthOptions) hitFailure(c *fiber.Ctx) {
	if o.FailLimiter != nil {
		o.FailLimiter.Hit(c.UserContext(), c.IP())
	}
}

// builtinSkips are the only unauthenticated paths.
//
// FAIL-CLOSED: matching is by exact path — there is deliberately no "/auth/*"
// wildcard. Any route added under /auth/ from now on REQUIRES authentication by
// default and must be listed here explicitly to be exempt. Reason: a prefix
// match plus a hand-written exclusion is bypassable with a trailing slash (GET
// /auth/device/ misses the exact exclusion but hits the prefix match), and
// StrictRouting is off.
var builtinSkips = map[string]struct{}{
	"/api/health": {}, "/api/ready": {}, "/api/version": {},
	"/auth/logout": {}, "/auth/logged-out": {},
	"/auth/device/code": {}, "/auth/device/token": {},
	"/api/auth/status": {}, "/api/auth/register": {}, "/api/auth/login": {},
	"/auth/oidc/start": {}, "/auth/oidc/callback": {},
	"/login": {}, "/register": {},
	"/skill.md": {}, "/install.md": {}, "/install.sh": {}, "/install.ps1": {},
}

// assetPrefix is the one prefix skip, and a prefix because the SPA bundle is
// served under content-hashed filenames nobody can enumerate in advance. These
// are the login and registration pages' own script and stylesheet: gating them
// on a session would leave an unauthenticated visitor a blank page at /login.
// They are static build output and carry nothing user-specific.
const assetPrefix = "/assets/"

// sharePrefix covers the three share-link routes (/s/:id, /s/:id/meta,
// /s/:id/render), the one surface with OPTIONAL authentication: a visitor with
// no account sees a `link` page, and a session is resolved when one is present
// so the owner also reaches their own `private` page. Missing or unusable
// credentials continue anonymously instead of 401/redirecting.
//
// A prefix is safe here where it would not be under /auth/: every path under
// /s/ is a share-link route, and each of the three handlers runs
// authz.View itself — anonymous does not mean unauthorized, it means
// authorized as nobody.
const sharePrefix = "/s/"

// hasPathPrefix is the prefix test to use against a request path, matching
// case-insensitively because Fiber routes case-insensitively by default. A
// case-sensitive check disagrees with the router: GET /S/abc12345 reaches
// ViewShell but would miss sharePrefix, and the anonymous visitor a share link
// exists for would be redirected to /login instead of shown the page.
func hasPathPrefix(path, prefix string) bool {
	return len(path) >= len(prefix) && strings.EqualFold(path[:len(prefix)], prefix)
}

// NewAuth builds the authentication middleware (fiber.Handler).
//
// Order of resolution:
//  1. Built-in skips → skip auth entirely.
//  2. DevMock.Enabled → inject a fixed identity (config.Validate rejects this
//     outside local).
//  3. FailLimiter (if set) → 429 when the caller's IP is over its failed-authn
//     budget, before any credential is checked.
//  4. Authorization: Bearer present → PAT channel via TokenValidator; an
//     invalid token records a FailLimiter hit. Bearer credentials are accepted
//     only under /api/ — the browser surfaces are cookie-only.
//  5. Session cookie lookup. Neither a missing cookie nor an idle-expired/
//     unknown session records a FailLimiter hit — both are dominated by benign
//     returning-user traffic (see authenticateSession).
//  6. AccountStatus (if set) on whichever id step 4 or 5 resolved: a valid
//     credential for a disabled or deleted account is refused. This costs one
//     primary-key lookup per authenticated request, which is the price of an
//     admin's decision taking effect on credentials that already exist rather
//     than whenever they happen to expire.
//
// Under sharePrefix every "no identity" verdict in steps 3-6 continues
// anonymously rather than refusing the request — see shareSession.
func NewAuth(opts AuthOptions) fiber.Handler {
	log := logger.Module("authn")

	return func(c *fiber.Ctx) error {
		path := c.Path()
		if _, skip := builtinSkips[path]; skip {
			return c.Next()
		}
		if hasPathPrefix(path, assetPrefix) {
			return c.Next()
		}

		if opts.DevMock.Enabled {
			identity := Identity(opts.DevMock)
			identity.AuthChannel = userctx.ChannelDevMock
			userctx.Set(c, identity)
			opts.touchActive(identity.AuthzID)
			return c.Next()
		}

		// Share links resolve a session when there is one and serve anonymously
		// when there is not, so neither the failed-authn budget nor the PAT
		// channel below applies: an anonymous view is not a failed attempt, and
		// a bearer credential must never stand in for its owner here.
		if hasPathPrefix(path, sharePrefix) {
			return shareSession(c, opts, log)
		}

		if opts.FailLimiter != nil && opts.FailLimiter.Exceeded(c.UserContext(), c.IP()) {
			return tooManyRequests()
		}

		// PAT channel: Bearer tokens never fall through to cookie auth.
		if token, ok := bearerToken(c); ok && strings.HasPrefix(path, "/api/") {
			if opts.TokenValidator == nil {
				return unauthorized()
			}
			id, valid := opts.TokenValidator(token)
			if !valid {
				opts.hitFailure(c)
				return unauthorized()
			}
			if err := opts.accountActive(c, id.AuthzID, log); err != nil {
				if errors.Is(err, errAccountInactive) {
					return unauthorized()
				}
				return err
			}
			userctx.Set(c, id)
			opts.touchActive(id.AuthzID)
			return c.Next()
		}

		return authenticateSession(c, opts, log)
	}
}

// bearerToken extracts the raw token from an "Authorization: Bearer <token>"
// header. It returns ("", false) when the header is absent or not a Bearer
// credential.
func bearerToken(c *fiber.Ctx) (string, bool) {
	h := c.Get(fiber.HeaderAuthorization)
	if h == "" {
		return "", false
	}
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// shareSession resolves the session cookie for a share-link route without ever
// refusing the request: a missing cookie, an idle-expired session, a disabled
// account, or a session store that is down all leave the request anonymous, and
// the handler's own authz.View decides what an anonymous caller may see.
//
// Degrading a store outage to anonymous rather than 503 is deliberate: share
// links are the one surface that must keep serving `link` pages when the
// session store is unavailable. The cost is that an owner cannot reach their
// own `private` page during such an outage, which is the fail-closed direction.
func shareSession(c *fiber.Ctx, opts AuthOptions, log *zap.Logger) error {
	sid := c.Cookies(opts.CookieName)
	if sid == "" || opts.Sessions == nil {
		return c.Next()
	}
	d, err := opts.Sessions.Get(c.UserContext(), sid)
	if err != nil {
		if !errors.Is(err, session.ErrNotFound) {
			log.Warn("session store unavailable, serving share link anonymously", zap.Error(err))
		}
		return c.Next()
	}
	if d.AuthzID == "" {
		return c.Next()
	}
	if err := opts.accountActive(c, d.AuthzID, log); err != nil {
		return c.Next()
	}
	userctx.Set(c, userctx.Identity{
		AuthzID:     d.AuthzID,
		DisplayName: d.DisplayName,
		AvatarURL:   d.AvatarURL,
		AuthChannel: userctx.ChannelSession,
	})
	opts.touchActive(d.AuthzID)
	return c.Next()
}

// authenticateSession resolves the login session cookie. "No session" (missing
// cookie / unknown id) routes to login or 401; an infrastructure error is 503 so
// a store blip never mass-logs-out live users.
func authenticateSession(c *fiber.Ctx, opts AuthOptions, log *zap.Logger) error {
	sid := c.Cookies(opts.CookieName)
	if sid == "" {
		return unauthenticated(c)
	}
	// Misconfiguration guard: a nil store is an infrastructure problem, not a
	// bad credential → 503, never a panic swallowed by recover as an
	// envelope-less 500.
	if opts.Sessions == nil {
		log.Error("session store not configured")
		return apperr.Unavailable()
	}
	d, err := opts.Sessions.Get(c.UserContext(), sid)
	if errors.Is(err, session.ErrNotFound) {
		// Deliberately NO hitFailure here. The cookie MaxAge is the absolute TTL
		// (30d) while the stored session expires on the shorter idle TTL (7d),
		// so a returning user with an idle-expired session presents a valid-looking
		// cookie whose session is simply gone. This benign, common event is
		// indistinguishable from a probe, and behind a shared office NAT enough of
		// them per minute would trip the per-IP authfail budget and 429 everyone's
		// logins. Consistent with the no-Hit on a missing cookie above; the failure
		// budget still guards the PAT bad-token path. Leaving this path unthrottled
		// is safe: session IDs carry 256 bits of crypto/rand entropy, so guessing
		// one is not a credential-attack vector, and a junk-cookie probe costs one
		// store lookup — comparable to any anonymous request the app already
		// serves without a limiter.
		return unauthenticated(c)
	}
	if err != nil {
		log.Error("session store unavailable", zap.Error(err))
		return apperr.Unavailable()
	}
	// Fail-closed: never inject an identity with an empty authz id.
	//
	// This is an ordinary state, not an anomaly: an OIDC sign-in that has not
	// come back yet holds its flow on a session with no identity (see
	// handler.OIDCStart), and an abandoned one leaves that cookie in the
	// browser until it expires. Hence Debug — a Warn here would fire on every
	// abandoned sign-in and teach operators to ignore the line.
	if d.AuthzID == "" {
		log.Debug("session carries no authz id, treating as unauthenticated")
		return unauthenticated(c)
	}
	if err := opts.accountActive(c, d.AuthzID, log); err != nil {
		if errors.Is(err, errAccountInactive) {
			return unauthenticated(c)
		}
		return err
	}
	userctx.Set(c, userctx.Identity{
		AuthzID:     d.AuthzID,
		DisplayName: d.DisplayName,
		AvatarURL:   d.AvatarURL,
		AuthChannel: userctx.ChannelSession,
	})
	opts.touchActive(d.AuthzID)
	return c.Next()
}

// unauthenticated routes a request with no session: navigations land on the
// login page, and XHR/subresource requests (incl. /s/:id/meta and /assets/*,
// which are NOT under /api/) get a 401 JSON envelope.
//
// The redirect carries OriginalURL(), not Path(): dropping the query strands
// deep links — /auth/device?user_code=X came back as a bare confirmation page
// with nothing to confirm. This is a PUBLIC middleware, so the behaviour applies
// to every deep link, not just the device flow. Anything reading the parameter
// back must validate it as a same-site path before redirecting to it.
func unauthenticated(c *fiber.Ctx) error {
	if isNavigate(c) {
		if c.Path() == "/" {
			return c.Redirect("/login", fiber.StatusFound)
		}
		return c.Redirect("/login?redirect="+url.QueryEscape(c.OriginalURL()), fiber.StatusFound)
	}
	return unauthorized()
}

// isNavigate: trust Sec-Fetch-Mode when present; otherwise fall back to the
// Accept header (older webviews may omit fetch metadata).
func isNavigate(c *fiber.Ctx) bool {
	if m := c.Get("Sec-Fetch-Mode"); m != "" {
		return strings.EqualFold(m, "navigate")
	}
	return strings.Contains(c.Get(fiber.HeaderAccept), "text/html")
}

// Identity maps a DevMock config to an identity for local development.
func Identity(m config.DevMockConfig) userctx.Identity {
	display := m.DisplayName
	if display == "" {
		display = m.User
	}
	return userctx.Identity{
		AuthzID:     m.UID,
		DisplayName: display,
	}
}

// unauthorized returns the typed 401 error; the caller returns it so Fiber's
// ErrorHandler renders the unified envelope (no detail leaked).
func unauthorized() *apperr.Error {
	return apperr.Unauthorized()
}
