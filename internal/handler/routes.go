package handler

import (
	"context"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/devicecode"
	"github.com/Xm798/placard/internal/middleware"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/oidc"
	"github.com/Xm798/placard/internal/repo"
	"github.com/Xm798/placard/internal/session"
	"github.com/Xm798/placard/internal/storage"
)

// Deps is the integration seam consumed by main.go. All collaborators are
// injected so the package stays testable with the storage stub and an in-memory
// DB. Construct the repos from a *gorm.DB, pick the production or stub
// storage.Client from config, then call Register.
type auditStore interface {
	Insert(ctx context.Context, entry *model.AuditLog) error
	Query(ctx context.Context, q repo.AuditQuery) ([]model.AuditLog, error)
}

type Deps struct {
	DB       *gorm.DB
	Storage  storage.Client
	Cfg      *config.Config
	Files    *repo.FileRepo
	Tokens   *repo.TokenRepo
	Views    *repo.ViewRepo
	Audit    auditStore
	Pending  *repo.PendingObjectDeleteRepo
	Versions *repo.FileVersionRepo

	// Identities and Settings back local-account registration: the account and
	// its "local" credential row are written together, and the registration
	// switch is read from the setting table (config seeds it, the table is
	// authoritative afterwards).
	Identities *repo.UserIdentityRepo
	Settings   *repo.SettingRepo

	// OIDC holds the configured identity providers. Nil (or empty) leaves the
	// instance password-only: the login page renders no provider buttons and
	// the /auth/oidc/* routes answer 404 rather than being mounted at all.
	OIDC *oidc.Registry

	// AuthLimiter, when non-nil, wraps the three unauthenticated /api/auth/*
	// endpoints — a per-IP cap (keyClass "authroute") on the two that run an
	// argon2 hash before they can answer, and on the status probe that anyone
	// can call in a loop.
	AuthLimiter fiber.Handler

	// FailLimiter is the per-IP failed-authn budget. POST /api/auth/login is in
	// the auth middleware's builtin skips, so the middleware's own check never
	// runs for it and the handler consults and feeds this limiter itself. Nil
	// leaves password attempts unmetered — acceptable only in tests.
	FailLimiter *middleware.IPLimiter

	// Users backs the publish/PATCH visibility default-resolution fallback
	// (Users.Get(authzID).DefaultVisibility) — see resolveVisibility in
	// publish.go. Nil-guarded there, so it may stay nil in tests that never
	// exercise the no-param fallback path. It also enriches /api/me with the
	// latest display profile for PAT-authenticated requests.
	Users *repo.UserRepo

	// AvatarHTTPClient, when set, is used by cacheAvatarAsync to fetch source
	// avatar bytes — tests inject one pointed at an httptest fake CDN (or with
	// a short Timeout to exercise the timeout gate). Nil in production falls
	// back to a client with avatarFetchTimeout.
	AvatarHTTPClient *http.Client

	// PublishLimiter, when non-nil, is applied ONLY to POST /api/publish (the
	// per-user hourly upload limit). It deliberately does NOT wrap the read
	// paths (/s/:id, /s/:id/meta) so viewing is never rate-limited by uploads.
	// main.go constructs the limiter and injects it here.
	PublishLimiter fiber.Handler

	Sessions      session.Store
	SessionCookie string // full cookie name, __Host- prefixed over HTTPS (SessionCookieName)

	// DeviceCodes backs the CLI device-code login flow (/auth/device*). Nil
	// disables those endpoints with a 503 rather than a nil-pointer panic, so
	// a deployment without a device-code store degrades cleanly.
	DeviceCodes devicecode.Store

	// DeviceLimiter, when non-nil, wraps ONLY POST /auth/device/code — a
	// per-IP cap on an unauthenticated mint endpoint (keyClass "devicecode").
	DeviceLimiter fiber.Handler

	// ShareCodeLimiter, when non-nil, is the per-file+IP budget for WRONG
	// share-code submissions at POST /s/:id/unlock. It counts failures only
	// (Hit/Exceeded), never correct submissions — a visitor who types the code
	// right the first time must not be metered — and it is what actually
	// protects a 6-digit secret. Nil leaves guesses unmetered: acceptable only
	// in tests.
	ShareCodeLimiter *middleware.IPLimiter

	// CLIRelease resolves the newest published CLI version, which the served
	// install scripts are pinned to. Nil — the default, and what every test
	// uses — serves them unpinned: they then resolve the release themselves,
	// which is also what an instance with no outbound network falls back to.
	CLIRelease func(context.Context) (string, error)

	// AnonRenderLimiter, when non-nil, wraps every /s/:id route — the per-IP
	// budget for anonymous page views. main.go wraps it in middleware.AnonOnly
	// so a signed-in visitor passes through unmetered.
	//
	// All of them and not just /render: every one costs a database read before
	// it can answer, and /meta, /render and /unlock each WRITE a row on the way
	// (a denial audit, or the view record), so leaving any unmetered hands an
	// anonymous caller an unbounded write loop against a page they cannot even
	// read. One page view spends three of the budget.
	AnonRenderLimiter fiber.Handler
}

// New builds a Handlers from Deps. main.go uses this to obtain the
// TokenValidator before wiring the auth middleware, then calls h.Mount(app).
//
// The method value binds even a nil deps.Tokens safely — it is only invoked
// through TokenValidator, which auth-only tests never exercise.
func New(deps Deps) *Handlers {
	return &Handlers{
		deps:       deps,
		lastUsed:   newLastUsedToucher(deps.Tokens.TouchLastUsed),
		cliVersion: newCLIVersionResolver(deps.CLIRelease),
	}
}

// Register wires all Placard routes onto app. Single integration point with
// main.go when the TokenValidator is not needed separately.
func Register(app *fiber.App, deps Deps) {
	New(deps).Mount(app)
}

// Mount registers this Handlers' routes onto app.
//
// Note: the /s/:id routes are the instance's only optionally-authenticated
// ones (middleware sharePrefix) — anyone holding a link reaches a `link` page
// without an account. What gates them is authz.View inside each handler, which
// admits an anonymous caller to a `link` page only and answers everything else
// exactly as it answers a missing id. A page carrying a share code adds a
// second gate behind that one (h.shareCodeLocked); neither replaces the other.
func (h *Handlers) Mount(app *fiber.App) {
	app.Post("/auth/logout", h.AuthLogout)
	// Static post-logout landing page. The client redirects here instead of the
	// login page so logging out does not immediately look like a login prompt.
	app.Get("/auth/logged-out", h.AuthLoggedOut)

	api := app.Group("/api")
	// Upload rate limit applies only to publish, never to the read paths.
	if h.deps.PublishLimiter != nil {
		api.Post("/publish", h.deps.PublishLimiter, h.Publish)
	} else {
		api.Post("/publish", h.Publish)
	}
	api.Post("/tokens", h.CreateToken)
	api.Get("/tokens", h.ListTokens)
	api.Delete("/tokens/:id", h.RevokeToken)
	api.Delete("/tokens/:id/permanent", h.DeleteRevokedToken)

	api.Get("/files", h.ListFiles)
	api.Delete("/files/:id", h.DeleteFile)
	api.Get("/files/:id/versions", h.ListVersions)
	api.Patch("/files/:id", h.PatchFile)
	// Restore creates a new head version (same write amplification as
	// publish), so it shares the per-user hourly upload limiter.
	if h.deps.PublishLimiter != nil {
		api.Post("/files/:id/versions/:v/restore", h.deps.PublishLimiter, h.RestoreVersion)
	} else {
		api.Post("/files/:id/versions/:v/restore", h.RestoreVersion)
	}
	// Share code. Session-or-PAT like the rest of /api/files/*: `publish
	// --password auto` mints one from the CLI, so the owner must be able to
	// manage it from there too.
	api.Post("/files/:id/share-code", h.GenerateShareCode)
	api.Delete("/files/:id/share-code", h.ClearShareCode)
	api.Get("/prefs", h.GetPrefs)
	api.Put("/prefs", h.PutPrefs)
	api.Get("/me", h.Me)

	// Local accounts. All three are in middleware.builtinSkips: status and
	// register/login are what an unauthenticated visitor needs to get in.
	// They stay behind the CSRF middleware like every other cookie-channel
	// write — the browser sends them as fetch() calls that carry
	// X-Requested-With.
	if h.deps.AuthLimiter != nil {
		api.Get("/auth/status", h.deps.AuthLimiter, h.AuthStatus)
		api.Post("/auth/register", h.deps.AuthLimiter, h.AuthRegister)
		api.Post("/auth/login", h.deps.AuthLimiter, h.AuthLogin)
	} else {
		api.Get("/auth/status", h.AuthStatus)
		api.Post("/auth/register", h.AuthRegister)
		api.Post("/auth/login", h.AuthLogin)
	}
	// Linked identities, for the settings page. Session-only: how an account
	// signs in is browser-surface business, and a personal access token exists
	// to publish pages, not to remove the credential that would be used to
	// revoke it.
	api.Get("/auth/identities", middleware.SessionOnly(), h.OIDCIdentities)
	api.Delete("/auth/identities/:provider", middleware.SessionOnly(), h.OIDCUnlink)

	// OIDC login. Both are browser navigations, unauthenticated (they are in
	// middleware.builtinSkips) and GET, so the CSRF middleware passes them as
	// safe methods; the callback's protection is the server-side flow state it
	// compares the state parameter against.
	//
	// The provider is a query parameter on start and is not on the callback at
	// all: builtinSkips matches exact paths, and the callback reads the
	// provider back out of the flow it stored.
	//
	// oidcBrowserPage keeps each failure's status and renders it as a page
	// rather than the JSON envelope — a navigation must not dead-end on one.
	if h.deps.OIDC != nil && h.deps.OIDC.Len() > 0 {
		start := oidcBrowserPage(h.OIDCStart)
		if h.deps.AuthLimiter != nil {
			app.Get("/auth/oidc/start", h.deps.AuthLimiter, start)
		} else {
			app.Get("/auth/oidc/start", start)
		}
		app.Get(OIDCCallbackPath, oidcBrowserPage(h.OIDCCallback))
	}

	// Instance administration. Session-only for the same reason the identity
	// routes are: a personal access token exists to publish pages, and nothing
	// that ships with Placard drives this surface from a CLI. requireAdmin
	// re-reads the flag from the user row on every request, so it is the gate
	// rather than anything captured at login.
	admin := api.Group("/admin", middleware.SessionOnly(), h.requireAdmin)
	admin.Get("/users", h.AdminListUsers)
	admin.Patch("/users/:id", h.AdminPatchUser)
	admin.Post("/users/:id/password", h.AdminResetPassword)
	admin.Delete("/users/:id", h.AdminDeleteUser)
	admin.Get("/settings", h.AdminGetSettings)
	admin.Patch("/settings", h.AdminPatchSettings)

	// Avatar proxy: no per-user limiter attached — avatars are fetched in
	// bursts (file lists) and share the global IP-level rate surface instead.
	api.Get("/users/:authz_id/avatar", middleware.SessionOnly(), h.UserAvatar)

	// Share links, registered one route at a time rather than through
	// app.Group("/s", …): Fiber matches group and Use middleware by raw string
	// prefix, not by path segment, so a "/s" group also runs on /settings and
	// /skill.md — which would spend the anonymous visitor budget on the
	// settings page and the skill doc. middleware.sharePrefix gets this right
	// with its trailing slash, and the two have to agree on what a share route
	// is.
	//
	// The unlock POST carries the same middleware as the three reads: typing a
	// code is part of viewing the page, and a PAT must no more stand in for a
	// session here than it may on /render. Its own per-file+IP failure budget
	// is enforced inside the handler, where the file id is known.
	shareChain := func(handler fiber.Handler) []fiber.Handler {
		chain := []fiber.Handler{middleware.BrowserOnly()}
		if h.deps.AnonRenderLimiter != nil {
			chain = append(chain, h.deps.AnonRenderLimiter)
		}
		return append(chain, handler)
	}
	app.Get("/s/:id", shareChain(h.ViewShell)...)
	app.Get("/s/:id/meta", shareChain(h.Meta)...)
	app.Get("/s/:id/render", shareChain(h.Render)...)
	app.Post("/s/:id/unlock", shareChain(h.Unlock)...)

	// Login and registration are SPA routes like any other page; they are
	// exempt from the login gate (middleware.builtinSkips) so an
	// unauthenticated visitor can reach them, and so is /assets/*, without
	// which the shell they serve could not load its own bundle.
	app.Get("/login", h.AppPage)
	app.Get("/register", h.AppPage)
	app.Get("/", h.AppPage)
	app.Get("/files", h.AppPage)
	app.Get("/settings", h.AppPage)
	app.Get("/docs", h.AppPage)
	// The admin page is a shell like any other route; whether the visitor is an
	// admin is decided by /api/admin/* answering them, and the page renders the
	// same not-found body as an unknown route when it does not.
	app.Get("/admin", h.AppPage)
	app.Get("/assets/*", h.Assets)
	// Unauthenticated (middleware builtinSkips) — agents self-install from them.
	app.Get("/skill.md", h.SkillDoc)
	app.Get("/install.md", h.InstallDoc)
	app.Get("/install.sh", h.InstallScript)
	app.Get("/install.ps1", h.InstallScriptPS)

	// Device-code login endpoints.
	//
	// /auth/device/code and /auth/device/token are in middleware.builtinSkips
	// (unauthenticated) and in csrfExactExempt. /auth/device and
	// /auth/device/approve are deliberately in NEITHER list, so the login gate
	// applies to them with no extra code — that is the whole point of the
	// whitelist flip.
	if h.deps.DeviceCodes != nil {
		if h.deps.DeviceLimiter != nil {
			app.Post("/auth/device/code", h.deps.DeviceLimiter, h.DeviceCode)
		} else {
			app.Post("/auth/device/code", h.DeviceCode)
		}
		// Neither of these is in builtinSkips, so both sit behind the login
		// gate with no extra code. approve IS in csrfExactExempt — its CSRF
		// defence is implemented inside DeviceApprove.
		app.Get("/auth/device", h.DevicePage)
		app.Post("/auth/device/approve", h.DeviceApprove)
		// /auth/device/token is unauthenticated (builtinSkips) and
		// CSRF-exempt: identity comes from the device_code in the body, so no
		// cookie is consumed.
		app.Post("/auth/device/token", h.DeviceToken)
	}
}
