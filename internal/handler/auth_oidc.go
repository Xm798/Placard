package handler

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/account"
	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/idgen"
	"github.com/Xm798/placard/internal/logger"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/oidc"
	"github.com/Xm798/placard/internal/repo"
	"github.com/Xm798/placard/internal/session"
	"github.com/Xm798/placard/internal/userctx"
)

// usernameSuffixTries bounds how many "alice2, alice3, …" candidates a
// provisioning tries before giving the account a random handle instead. The
// loop exists for the ordinary case of two providers using the same preferred
// username; it is not a way to enumerate a busy namespace one insert at a time.
const usernameSuffixTries = 20

// OIDCCallbackPath is the path every provider is configured to redirect back
// to. Exported because the absolute redirect_uri is assembled in main.go from
// server.base_url, and the two halves must not drift: a mismatch is only
// visible as the provider refusing the authorization request.
const OIDCCallbackPath = "/auth/oidc/callback"

// oidcDescriptors lists the configured providers for the login page. Returns an
// empty slice — never nil — so a caller ranging over it needs no nil check.
func (h *Handlers) oidcDescriptors() []dto.OIDCProvider {
	if h.deps.OIDC == nil {
		return []dto.OIDCProvider{}
	}
	src := h.deps.OIDC.List()
	out := make([]dto.OIDCProvider, 0, len(src))
	for _, d := range src {
		out = append(out, dto.OIDCProvider{Name: d.Name, DisplayName: d.Label})
	}
	return out
}

// OIDCStart begins an authorization request (GET /auth/oidc/start).
//
// It is reached by two kinds of visitor and treats them differently in exactly
// one respect: someone who is already signed in keeps their session and gets
// the flow written onto it (linking a provider to the account they hold),
// while anyone else gets a PENDING session — a session row carrying the flow
// and no authz id, which the auth middleware refuses to authenticate with.
// Either way the flow lives server-side, so nothing the browser could edit
// takes part in the callback's checks.
func (h *Handlers) OIDCStart(c *fiber.Ctx) error {
	provider, aerr := h.oidcProvider(c.Query("provider"))
	if aerr != nil {
		return aerr
	}
	if h.deps.Sessions == nil {
		return apperr.Unavailable()
	}

	state, nonce, err := newFlowSecrets()
	if err != nil {
		return apperr.Internal("could not start sign-in")
	}
	authURL, verifier, err := provider.AuthCodeURL(c.UserContext(), state, nonce)
	if err != nil {
		logger.Module("auth").Warn("oidc discovery failed",
			zap.String("provider", provider.Name()), zap.Error(err))
		return apperr.Unavailable("identity provider is unavailable")
	}

	flow := &session.OIDCFlow{
		Provider: provider.Name(),
		State:    state,
		Nonce:    nonce,
		Verifier: verifier,
		Redirect: safeRedirectPath(c.Query("redirect")),
	}
	if err := h.storeFlow(c, flow); err != nil {
		return err
	}
	return c.Redirect(authURL, fiber.StatusFound)
}

// storeFlow attaches the pending flow to the caller's session, minting a
// pending one when they have none. The cookie is (re)written in both branches:
// in the linking branch it is already the right value, and in the pending
// branch it is what lets the callback find the flow again.
func (h *Handlers) storeFlow(c *fiber.Ctx, flow *session.OIDCFlow) error {
	ctx := c.UserContext()
	sid := c.Cookies(h.deps.SessionCookie)
	if sid != "" {
		d, err := h.deps.Sessions.Get(ctx, sid)
		switch {
		case err == nil:
			// Either kind of existing session is written in place: a live one
			// so linking keeps the caller signed in, and a pending one so
			// clicking a provider button repeatedly reuses its row instead of
			// leaving a new one behind each time.
			d.OIDC = flow
			if err := h.deps.Sessions.Update(ctx, sid, d); err != nil {
				return apperr.Unavailable("could not start sign-in")
			}
			return nil
		case !errors.Is(err, session.ErrNotFound):
			return apperr.Unavailable("could not start sign-in")
		}
	}

	// No session at all: a pending one, carrying the flow and no identity.
	pending, err := h.deps.Sessions.Create(ctx, session.Data{CreatedAt: model.Now(), OIDC: flow})
	if err != nil {
		return apperr.Unavailable("could not start sign-in")
	}
	h.setSessionCookie(c, pending, h.sessionCookieMaxAge())
	return nil
}

// OIDCCallback completes an authorization request (GET /auth/oidc/callback).
//
// The provider is taken from the stored flow rather than from the query, which
// is why the callback URL registered upstream carries no parameters of its own:
// the server already knows which flow this browser started, and a provider
// name supplied by the caller would let one provider's code be redeemed as
// another's.
//
// Every rejection here — a missing or replayed flow, a state or nonce
// mismatch, an unverifiable id token — answers with a status and creates
// nothing.
func (h *Handlers) OIDCCallback(c *fiber.Ctx) error {
	if h.deps.OIDC == nil || h.deps.OIDC.Len() == 0 || h.deps.Sessions == nil {
		return apperr.NotFound()
	}
	ctx := c.UserContext()

	sid := c.Cookies(h.deps.SessionCookie)
	if sid == "" {
		return apperr.Validation("no sign-in is in progress")
	}
	d, err := h.deps.Sessions.Get(ctx, sid)
	if errors.Is(err, session.ErrNotFound) {
		return apperr.Validation("the sign-in attempt expired, please try again")
	}
	if err != nil {
		return apperr.Unavailable("could not read the sign-in attempt")
	}
	flow := d.OIDC
	if flow == nil {
		return apperr.Validation("no sign-in is in progress")
	}
	// Consumed before it is checked, so a replayed callback finds nothing to
	// compare against however the first one turned out.
	if err := h.clearFlow(ctx, sid, d); err != nil {
		return err
	}

	if reason := c.Query("error"); reason != "" {
		h.auditOIDC(c, "auth.oidc", "", "", flow.Provider, "denied", "provider_error")
		return apperr.PermissionDenied("the identity provider refused the sign-in")
	}
	if subtle.ConstantTimeCompare([]byte(c.Query("state")), []byte(flow.State)) != 1 {
		h.auditOIDC(c, "auth.oidc", "", "", flow.Provider, "denied", "state_mismatch")
		return apperr.Validation("sign-in state does not match")
	}
	code := c.Query("code")
	if code == "" {
		return apperr.Validation("the identity provider returned no authorization code")
	}
	// A flow record written before PKCE carries no verifier and can no longer
	// be redeemed; it is dropped here rather than at the token endpoint.
	if flow.Verifier == "" {
		return apperr.Validation("the sign-in attempt expired, please try again")
	}

	provider, err := h.deps.OIDC.Get(flow.Provider)
	if err != nil {
		return apperr.Validation("that sign-in provider is no longer configured")
	}
	claims, err := provider.Exchange(ctx, code, flow.Nonce, flow.Verifier)
	if err != nil {
		// One reason code for every verification failure: the distinction
		// between a bad signature, a wrong audience and a replayed nonce is an
		// operator's to make from the provider's logs, and answering it here
		// would describe the check to whoever tripped it.
		logger.Module("auth").Warn("oidc callback verification failed",
			zap.String("provider", flow.Provider), zap.Error(err))
		h.auditOIDC(c, "auth.oidc", "", "", flow.Provider, "denied", "invalid_id_token")
		return apperr.Validation("could not verify the sign-in")
	}

	if d.AuthzID != "" {
		return h.completeOIDCLink(c, d.AuthzID, provider.Name(), claims, flow.Redirect)
	}
	return h.completeOIDCLogin(c, sid, provider.Name(), claims, flow.Redirect)
}

// completeOIDCLink binds a verified upstream identity to the account that
// started the flow while signed in. A subject already bound elsewhere is a
// conflict rather than a takeover: (provider, subject) is unique, and the row
// that holds it stays where it is.
func (h *Handlers) completeOIDCLink(c *fiber.Ctx, userID, provider string, claims *oidc.Claims, redirect string) error {
	ctx := c.UserContext()
	if h.deps.Identities == nil || h.deps.Users == nil {
		return apperr.Unavailable()
	}

	// The callback is unauthenticated (it has to be — the login flow arrives
	// here with no identity), so the auth middleware's account-status check
	// never ran for it. A session that outlived its account being disabled
	// must not be able to add a credential to it.
	user, err := h.deps.Users.Get(ctx, userID)
	if err != nil {
		return apperr.Unavailable("could not load account")
	}
	if user.Disabled {
		return apperr.Unauthorized()
	}

	existing, err := h.deps.Identities.GetByProviderSubject(ctx, provider, claims.Subject)
	switch {
	case err == nil && existing.UserID == userID:
		// Already linked; re-running the flow is how a user refreshes the
		// avatar, so this is a success, not a conflict.
	case err == nil:
		h.auditOIDC(c, "auth.oidc.link", userID, user.Username, provider, "denied", "subject_taken")
		return apperr.Conflict("that identity is already linked to another account")
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return apperr.Unavailable("could not read linked identities")
	default:
		if err := h.deps.Identities.Create(ctx, &model.UserIdentity{
			Provider: provider, Subject: claims.Subject, UserID: userID,
		}); err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return apperr.Conflict("that identity is already linked to another account")
			}
			return apperr.Internal("could not link the identity")
		}
	}

	h.refreshAvatar(user, claims.Picture)
	h.auditOIDC(c, "auth.oidc.link", userID, user.Username, provider, "success", "")
	logStateChange(c, "auth.oidc.link", "", ctxlog.OutcomeSuccess)
	return c.Redirect(redirectOr(redirect, "/settings"), fiber.StatusFound)
}

// completeOIDCLogin resolves a verified identity to an account and signs it in.
//
// Resolution order, and the only order in which an account may be reached:
//  1. the (provider, subject) pair already bound to an account — the identity
//     itself,
//  2. an account holding the same address, when the provider says it verified
//     it: an unverified address is a claim, not a proof, and linking on one
//     would let anyone who can set their profile email take over an account,
//  3. a new account, if auto-provisioning is on.
//
// ⚠ Step 2 verifies the PROVIDER's side of the address only. Placard does not
// verify the addresses typed into local registration, so on an instance that
// leaves auth.registration_open ON, someone can register locally claiming an
// address they do not own and then receive whoever signs in with it through
// SSO. The switch defaults to OFF, which is what keeps the two sides honest;
// an instance that opens registration AND configures SSO is trusting everyone
// who can register. Verifying local addresses (or restricting this step to
// password-less accounts) is what would remove the dependency.
func (h *Handlers) completeOIDCLogin(c *fiber.Ctx, pendingSID, provider string, claims *oidc.Claims, redirect string) error {
	ctx := c.UserContext()
	if h.deps.Users == nil || h.deps.Identities == nil || h.deps.DB == nil {
		return apperr.Unavailable()
	}

	user, aerr := h.resolveOIDCUser(ctx, c, provider, claims)
	if aerr != nil {
		return aerr
	}
	if user.Disabled {
		h.auditOIDC(c, "auth.oidc", user.ID, user.Username, provider, "denied", "account_disabled")
		return apperr.Unauthorized("invalid credentials")
	}

	if err := h.deps.Users.StampLogin(ctx, user.ID, time.Now()); err != nil {
		logger.Module("auth").Warn("stamp login times failed",
			zap.String("user_id", user.ID), zap.Error(err))
	}
	h.refreshAvatar(user, claims.Picture)

	// The pending session is replaced rather than promoted: a session id that
	// existed before authentication must not survive it.
	if err := h.deps.Sessions.Delete(ctx, pendingSID); err != nil {
		logger.Module("auth").Warn("drop pending session failed", zap.Error(err))
	}
	sid, err := h.deps.Sessions.Create(ctx, session.Data{
		AuthzID:     user.ID,
		DisplayName: user.DisplayName,
		CreatedAt:   model.Now(),
	})
	if err != nil {
		return apperr.Unavailable("could not open a session")
	}
	h.setSessionCookie(c, sid, h.sessionCookieMaxAge())

	h.auditOIDC(c, "auth.oidc", user.ID, user.Username, provider, "success", "")
	logStateChange(c, "auth.oidc", "", ctxlog.OutcomeSuccess)
	return c.Redirect(redirectOr(redirect, "/"), fiber.StatusFound)
}

// resolveOIDCUser runs the three-step resolution completeOIDCLogin documents
// and returns the account the caller signs in as.
func (h *Handlers) resolveOIDCUser(ctx context.Context, c *fiber.Ctx, provider string, claims *oidc.Claims) (*model.User, *apperr.Error) {
	identity, err := h.deps.Identities.GetByProviderSubject(ctx, provider, claims.Subject)
	switch {
	case err == nil:
		user, uerr := h.deps.Users.Get(ctx, identity.UserID)
		if uerr != nil {
			// An identity pointing at no account is a broken row, not a login.
			return nil, apperr.Unavailable("could not load the linked account")
		}
		return user, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, apperr.Unavailable("could not read linked identities")
	}

	if claims.EmailVerified && claims.Email != "" {
		user, err := h.deps.Users.GetByEmail(ctx, claims.Email)
		switch {
		case err == nil:
			// Disabled first: the caller is refused either way, but a
			// suspended account must not keep collecting new credentials (and
			// success-labelled audit rows) from repeated sign-in attempts.
			if user.Disabled {
				return user, nil
			}
			if cerr := h.deps.Identities.Create(ctx, &model.UserIdentity{
				Provider: provider, Subject: claims.Subject, UserID: user.ID,
			}); cerr != nil {
				return nil, apperr.Internal("could not link the identity")
			}
			h.auditOIDC(c, "auth.oidc.link", user.ID, user.Username, provider, "success", "email_match")
			return user, nil
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return nil, apperr.Unavailable("could not load account")
		}
	}

	provision, perr := h.oidcAutoProvision(ctx)
	if perr != nil {
		return nil, apperr.Unavailable("could not read instance settings")
	}
	if !provision {
		h.auditOIDC(c, "auth.oidc", "", "", provider, "denied", "auto_provision_disabled")
		return nil, apperr.PermissionDenied("this instance does not create accounts from single sign-on")
	}
	return h.provisionOIDCUser(ctx, provider, claims)
}

// provisionOIDCUser creates an account for a verified identity that belongs to
// none yet.
//
// The account has no password hash: it authenticates through its identity row
// alone. The username comes from preferred_username, and a taken one is
// retried with a numeric suffix — the conflict is settled by the unique index
// rather than by a pre-check, so two concurrent provisionings cannot both pass
// a "is it free?" test and then race for the insert.
func (h *Handlers) provisionOIDCUser(ctx context.Context, provider string, claims *oidc.Claims) (*model.User, *apperr.Error) {
	email := h.provisionableEmail(ctx, claims)
	base := usernameCandidate(claims, provider)

	empty, err := h.instanceIsEmpty(ctx)
	if err != nil {
		return nil, apperr.Unavailable("could not read instance state")
	}

	// attempt indexes the username candidate and advances only on a name
	// collision, so losing the first-account race retries the SAME name as an
	// ordinary account rather than skipping to a suffixed one. The loop bound
	// counts iterations instead, which keeps it finite whatever the inserts do.
	attempt := 0
	for i := 0; i <= usernameSuffixTries+1; i++ {
		username := suffixedUsername(base, attempt)
		user := &model.User{
			ID:                idgen.Generate(model.UserIDLen),
			Username:          username,
			Email:             email,
			DisplayName:       oidcDisplayName(claims, username),
			IsAdmin:           empty,
			DefaultVisibility: model.VisibilityLink,
			FirstLoginAt:      model.Never,
			LastLoginAt:       model.Never,
			LastActiveAt:      model.Never,
		}
		cerr := h.createAccount(ctx, user, model.UserIdentity{
			Provider: provider, Subject: claims.Subject,
		}, empty)
		switch {
		case cerr == nil:
			return user, nil
		case errors.Is(cerr, errAccountTaken):
			// Username (or, in a race, the address) is taken — try the next
			// candidate. The address is dropped after the first collision so a
			// contested email cannot make every candidate fail.
			email = nil
			attempt++
		case errors.Is(cerr, errIdentityTaken):
			// Another request provisioned this same subject while this one was
			// deciding on a username. Its account is the right answer.
			if identity, gerr := h.deps.Identities.GetByProviderSubject(ctx, provider, claims.Subject); gerr == nil {
				if u, uerr := h.deps.Users.Get(ctx, identity.UserID); uerr == nil {
					return u, nil
				}
			}
			return nil, apperr.Internal("could not create account")
		case errors.Is(cerr, errBootstrapLost):
			// Lost the first-account race: retry as an ordinary account.
			empty = false
		default:
			return nil, apperr.Internal("could not create account")
		}
	}
	return nil, apperr.Internal("could not find a free username")
}

// provisionableEmail returns the address to store on a provisioned account, or
// nil for none. It is kept only when the provider verified it AND no account
// holds it — an unverified address is not this user's to claim, and a taken one
// would only make the insert fail.
func (h *Handlers) provisionableEmail(ctx context.Context, claims *oidc.Claims) *string {
	if !claims.EmailVerified || claims.Email == "" {
		return nil
	}
	if _, err := h.deps.Users.GetByEmail(ctx, claims.Email); !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	addr := strings.ToLower(strings.TrimSpace(claims.Email))
	if len(addr) > account.EmailMaxLen {
		return nil
	}
	return &addr
}

// oidcAutoProvision reads the switch from the setting table, falling back to
// the config seed when Settings is unwired.
func (h *Handlers) oidcAutoProvision(ctx context.Context) (bool, error) {
	def := h.deps.Cfg != nil && h.deps.Cfg.Auth.OIDCAutoProvision
	if h.deps.Settings == nil {
		return def, nil
	}
	return h.deps.Settings.GetBool(ctx, model.SettingOIDCAutoProvision, def)
}

// OIDCIdentities lists what the authenticated account can sign in with
// (GET /api/auth/identities).
func (h *Handlers) OIDCIdentities(c *fiber.Ctx) error {
	id, ok := userctx.Get(c)
	if !ok {
		return apperr.Unauthorized()
	}
	if h.deps.Identities == nil || h.deps.Users == nil {
		return apperr.Unavailable()
	}
	ctx := c.UserContext()

	user, err := h.deps.Users.Get(ctx, id.AuthzID)
	if err != nil {
		return apperr.Unavailable("could not load account")
	}
	rows, err := h.deps.Identities.ListByUser(ctx, id.AuthzID)
	if err != nil {
		return apperr.Unavailable("could not read linked identities")
	}

	resp := dto.IdentitiesResponse{
		Identities:  make([]dto.IdentityItem, 0, len(rows)),
		HasPassword: user.PasswordHash != "",
	}
	linked := make(map[string]struct{}, len(rows))
	methods := loginMethodCount(user, rows)
	for _, row := range rows {
		if row.Provider == model.ProviderLocal {
			// The local row is the password itself, which the identity list
			// reports through HasPassword rather than as an unlinkable entry.
			continue
		}
		linked[row.Provider] = struct{}{}
		resp.Identities = append(resp.Identities, dto.IdentityItem{
			Provider:    row.Provider,
			DisplayName: h.providerLabel(row.Provider),
			LinkedAt:    row.CreateTime,
			CanUnlink:   methods > 1,
		})
	}
	for _, p := range h.oidcDescriptors() {
		if _, bound := linked[p.Name]; !bound {
			resp.Available = append(resp.Available, p)
		}
	}
	return c.JSON(resp)
}

// OIDCUnlink removes one linked provider from the authenticated account
// (DELETE /api/auth/identities/:provider).
//
// The last remaining way to sign in is refused: an account with no password
// and no identity is unreachable, and nothing else in the product would give
// it back.
func (h *Handlers) OIDCUnlink(c *fiber.Ctx) error {
	id, ok := userctx.Get(c)
	if !ok {
		return apperr.Unauthorized()
	}
	provider := strings.ToLower(strings.TrimSpace(c.Params("provider")))
	if provider == "" || provider == model.ProviderLocal {
		return apperr.Validation("that credential cannot be unlinked")
	}
	if h.deps.Identities == nil || h.deps.Users == nil {
		return apperr.Unavailable()
	}
	ctx := c.UserContext()

	user, err := h.deps.Users.Get(ctx, id.AuthzID)
	if err != nil {
		return apperr.Unavailable("could not load account")
	}

	// The "keep one login method" rule is enforced inside the delete's own
	// transaction, not by a check here: two concurrent unlinks of two
	// different providers would each pass a check made out here and each
	// delete, leaving an account nobody can sign in to.
	removed, err := h.deps.Identities.DeleteUnlessLast(ctx, id.AuthzID, provider, user.PasswordHash != "")
	switch {
	case errors.Is(err, repo.ErrLastLoginMethod):
		h.auditOIDC(c, "auth.oidc.unlink", user.ID, user.Username, provider, "denied", "last_login_method")
		return apperr.Validation("this is the only way left to sign in to this account")
	case err != nil:
		return apperr.Internal("could not unlink the identity")
	case !removed:
		return apperr.NotFound("that identity is not linked")
	}
	h.auditOIDC(c, "auth.oidc.unlink", user.ID, user.Username, provider, "success", "")
	logStateChange(c, "auth.oidc.unlink", "", ctxlog.OutcomeSuccess)
	return c.SendStatus(fiber.StatusNoContent)
}

// auditOIDC writes one audit row for a single sign-on event.
//
// It is auth_local's auditAuth with the provider recorded as the resource
// rather than folded into the actor name: an event can be anonymous (a
// callback that verified nothing) or name a user, but it always names a
// provider, and ActorName is a display-name snapshot that an admin UI renders
// as a person.
func (h *Handlers) auditOIDC(c *fiber.Ctx, action, userID, actorName, provider, outcome, reason string) {
	h.auditBestEffort(c, &model.AuditLog{
		Action:       action,
		Outcome:      outcome,
		ReasonCode:   reason,
		AuthChannel:  userctx.ChannelSession,
		Actor:        userID,
		ActorName:    actorName,
		ResourceType: "oidc_identity",
		ResourceID:   provider,
	})
}

// loginMethodCount is how many ways the account can still be signed into: the
// password, if it has one, plus each linked provider. The "local" identity row
// is not counted separately — it exists alongside the password hash and would
// double-count the same credential.
func loginMethodCount(user *model.User, rows []model.UserIdentity) int {
	n := 0
	if user.PasswordHash != "" {
		n++
	}
	for _, row := range rows {
		if row.Provider != model.ProviderLocal {
			n++
		}
	}
	return n
}

// providerLabel is the configured display name for a provider, falling back to
// its key — a provider that was removed from the config still has identities
// pointing at it, and the settings page has to name them somehow.
func (h *Handlers) providerLabel(name string) string {
	if h.deps.OIDC != nil {
		if p, err := h.deps.OIDC.Get(name); err == nil {
			return p.Label()
		}
	}
	return name
}

// oidcBrowserPage adapts an OIDC handler for a browser navigation.
//
// Both SSO legs are top-level navigations, not fetch() calls, so a failure has
// to land on something a person can read and leave. The handlers below express
// their failures as apperr values like every other endpoint; this converts one
// into the HTML failure page while keeping the status the check produced — the
// status is the contract (a rejected state or nonce is a 400 that created
// nothing), and only the body changes.
//
// A non-apperr error is left alone for the ErrorHandler: those are bugs, and
// dressing one up as an expected sign-in failure would hide it.
func oidcBrowserPage(handler fiber.Handler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		err := handler(c)
		var appErr *apperr.Error
		if !errors.As(err, &appErr) {
			return err
		}
		// The specific reason stays in the audit trail and the server log,
		// where an operator can read it and whoever tripped it cannot.
		logger.Module("auth").Info("oidc leg failed",
			zap.String("path", c.Path()),
			zap.String("code", appErr.Code),
			zap.Error(appErr))
		c.Status(appErr.HTTPStatus)
		c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
		setCommonSecurityHeaders(c)
		varyByLanguage(c)
		c.Set("Content-Security-Policy", "default-src 'self'; "+
			"style-src 'self' 'unsafe-inline'; script-src 'none'; "+
			"img-src 'self' data:; frame-ancestors 'self'; base-uri 'none'")
		return c.Send(oidcErrorPages[pageLang(c)])
	}
}

// oidcProvider resolves the requested provider, mapping an instance with no
// OIDC configured and an unknown name to the same 404 — neither describes
// what the instance is configured with.
func (h *Handlers) oidcProvider(name string) (*oidc.Provider, *apperr.Error) {
	if h.deps.OIDC == nil || h.deps.OIDC.Len() == 0 {
		return nil, apperr.NotFound()
	}
	p, err := h.deps.OIDC.Get(name)
	if err != nil {
		return nil, apperr.NotFound()
	}
	return p, nil
}

// clearFlow removes the pending flow from a session so it is consumed exactly
// once. A session that vanished between the read and this write is treated as
// a consumed flow: there is nothing left to sign in with either way.
func (h *Handlers) clearFlow(ctx context.Context, sid string, d session.Data) *apperr.Error {
	d.OIDC = nil
	err := h.deps.Sessions.Update(ctx, sid, d)
	if err != nil && !errors.Is(err, session.ErrNotFound) {
		return apperr.Unavailable("could not read the sign-in attempt")
	}
	return nil
}

// newFlowSecrets mints the state and nonce, each 256 bits of crypto/rand.
func newFlowSecrets() (state, nonce string, err error) {
	var buf [64]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", "", err
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(buf[:32]), enc.EncodeToString(buf[32:]), nil
}

// redirectMaxLen bounds a stored destination. The flow is persisted as JSON in
// a 1024-byte column alongside a provider name and three 43-character secrets,
// so an unbounded redirect would not fail validation — it would fail the
// INSERT, on Postgres only, as a 503 nobody could explain.
const redirectMaxLen = 512

// safeRedirectPath confines a caller-supplied post-login destination to this
// site, returning "" for anything else so the caller falls back to its own
// default.
//
// The value is resolved the way a browser will resolve it rather than
// pattern-matched, because the two do not agree on what the string even is:
// browsers strip TAB, LF and CR out of a URL BEFORE parsing it, so
// "/\t/evil.example" is a same-looking path here and "//evil.example" — an
// off-site, scheme-relative URL — by the time it is navigated to. Stripping the
// same characters first and then resolving against a fixed base is what makes
// this check see what the browser will see.
func safeRedirectPath(raw string) string {
	if raw == "" || len(raw) > redirectMaxLen {
		return ""
	}
	stripped := strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, raw)
	if stripped == "" || stripped[0] != '/' {
		return ""
	}
	// More than one leading slash is an authority, not a path. Browsers
	// implement "special authority ignore slashes": every leading '/' and '\'
	// is skipped and what follows becomes the HOST, so "///evil.example" and
	// "/\evil.example" both navigate off-site. net/url does not read them that
	// way, which is why this is a separate check rather than something the
	// resolution below would catch.
	if len(stripped) > 1 && (stripped[1] == '/' || stripped[1] == '\\') {
		return ""
	}
	// A relative reference resolved against an absolute base: anything that
	// still manages to change the host or scheme is off-site.
	base := url.URL{Scheme: "https", Host: "placard.invalid"}
	ref, err := url.Parse(strings.ReplaceAll(stripped, "\\", "/"))
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(ref)
	if resolved.Scheme != base.Scheme || resolved.Host != base.Host {
		return ""
	}
	return resolved.RequestURI() + fragmentOf(resolved)
}

// fragmentOf re-attaches the fragment RequestURI() drops, so a deep link into a
// page survives the round trip through the identity provider.
func fragmentOf(u *url.URL) string {
	if u.Fragment == "" {
		return ""
	}
	return "#" + u.EscapedFragment()
}

// redirectOr falls back to def for a destination that did not survive
// validation.
func redirectOr(path, def string) string {
	if path == "" {
		return def
	}
	return path
}

// usernameCandidate derives a stored username from the provider's
// preferred_username, then the email local part, then the provider name — the
// last of which always yields something, so provisioning never fails for want
// of a name.
func usernameCandidate(claims *oidc.Claims, provider string) string {
	for _, raw := range []string{claims.PreferredUsername, emailLocalPart(claims.Email), provider} {
		if name := sanitizeUsername(raw); name != "" {
			return name
		}
	}
	return "user"
}

// emailLocalPart is everything before the last "@", or "" when there is none.
func emailLocalPart(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return ""
	}
	return email[:at]
}

// sanitizeUsername coerces an upstream handle into the character set
// account.ValidateUsername enforces on a registration, dropping anything else rather
// than rejecting the whole name — an identity provider's idea of a username is
// not Placard's, and a login must not fail because of a space or an umlaut.
//
// Returns "" when nothing usable survives, or when the result is too short to
// be a username; the caller moves on to its next source.
func sanitizeUsername(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		switch {
		case account.IsUsernameRune(r):
			b.WriteRune(r)
		}
	}
	name := b.String()
	// The leading character must be alphanumeric and the suffix loop appends
	// digits, so trim to a length that leaves room for one.
	for name != "" && !account.IsAlphanumeric(rune(name[0])) {
		name = name[1:]
	}
	if len(name) < account.UsernameMinLen {
		return ""
	}
	if len(name) > account.UsernameMaxLen-3 {
		name = name[:account.UsernameMaxLen-3]
	}
	return name
}

// suffixedUsername is base for the first attempt and base2, base3, … after
// that. The final attempt gets a random suffix instead: a namespace where
// twenty consecutive numbers are taken is not one more number away from free.
func suffixedUsername(base string, attempt int) string {
	switch {
	case attempt == 0:
		return base
	case attempt < usernameSuffixTries:
		return base + strconv.Itoa(attempt+1)
	default:
		return base + strings.ToLower(idgen.Generate(3))
	}
}

// oidcDisplayName is the provider's name claim, bounded to the column's width
// in runes (the column counts characters on Postgres, so cutting bytes could
// split one), falling back to the username.
func oidcDisplayName(claims *oidc.Claims, username string) string {
	name := strings.TrimSpace(claims.Name)
	if name == "" {
		return username
	}
	if r := []rune(name); len(r) > account.DisplayNameMaxLen {
		return string(r[:account.DisplayNameMaxLen])
	}
	return name
}
