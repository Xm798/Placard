package handler

import (
	"context"
	"errors"
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
	"github.com/Xm798/placard/internal/password"
	"github.com/Xm798/placard/internal/session"
	"github.com/Xm798/placard/internal/userctx"
)

// AuthStatus reports what the login page has to render before anyone is
// authenticated: whether registration is open, and whether this instance still
// has no accounts at all (in which case the first visitor registers the admin
// regardless of the switch). Unauthenticated by design — it exposes only
// instance-level policy, never anything about a user.
func (h *Handlers) AuthStatus(c *fiber.Ctx) error {
	ctx := c.UserContext()
	empty, err := h.instanceIsEmpty(ctx)
	if err != nil {
		return apperr.Unavailable("could not read instance state")
	}
	open, err := h.registrationOpen(ctx)
	if err != nil {
		return apperr.Unavailable("could not read instance settings")
	}
	return c.JSON(dto.AuthStatusResponse{
		RegistrationOpen: open || empty,
		SetupRequired:    empty,
		OIDCProviders:    h.oidcDescriptors(),
	})
}

// AuthRegister creates a local account and logs it in (POST /api/auth/register).
//
// Registration is refused with 403 while auth.registration_open is off, with
// one exception: an instance with no accounts always accepts the first one and
// makes it an admin, because otherwise a fresh install has no way to reach the
// switch that opens registration.
func (h *Handlers) AuthRegister(c *fiber.Ctx) error {
	var req struct {
		Username    string `json:"username"`
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := c.BodyParser(&req); err != nil {
		return apperr.Validation("invalid request body")
	}

	username, err := account.ValidateUsername(req.Username)
	if err != nil {
		return apperr.Validation(err.Error())
	}
	email, err := account.ValidateEmail(req.Email)
	if err != nil {
		return apperr.Validation(err.Error())
	}
	if err := account.ValidatePassword(req.Password); err != nil {
		return apperr.Validation(err.Error())
	}
	displayName, err := account.ValidateDisplayName(req.DisplayName, username)
	if err != nil {
		return apperr.Validation(err.Error())
	}

	ctx := c.UserContext()
	if h.deps.Users == nil || h.deps.Identities == nil || h.deps.Settings == nil || h.deps.DB == nil {
		return apperr.Unavailable()
	}

	empty, err := h.instanceIsEmpty(ctx)
	if err != nil {
		return apperr.Unavailable("could not read instance state")
	}
	if !empty {
		open, err := h.registrationOpen(ctx)
		if err != nil {
			return apperr.Unavailable("could not read instance settings")
		}
		if !open {
			h.auditAuth(c, "auth.register", "", username, "denied", "registration_closed")
			return apperr.PermissionDenied("registration is closed")
		}
	}

	hash, err := password.Hash(req.Password)
	if err != nil {
		return apperr.Internal("could not hash password")
	}

	user := &model.User{
		ID:                idgen.Generate(model.UserIDLen),
		Username:          username,
		Email:             email,
		DisplayName:       displayName,
		PasswordHash:      hash,
		IsAdmin:           empty,
		DefaultVisibility: model.VisibilityLink,
		FirstLoginAt:      model.Never,
		LastLoginAt:       model.Never,
		LastActiveAt:      model.Never,
	}

	localIdentity := model.UserIdentity{
		Provider: model.ProviderLocal,
		Subject:  user.ID,
		UserID:   user.ID,
	}
	if err := h.createAccount(ctx, user, localIdentity, empty); err != nil {
		switch {
		case errors.Is(err, errAccountTaken):
			return apperr.Conflict("username or email already in use")
		case errors.Is(err, errBootstrapLost):
			// Another registration won the empty-instance race and became the
			// admin, so this one is an ordinary registration against an
			// instance whose switch is still off.
			h.auditAuth(c, "auth.register", "", username, "denied", "registration_closed")
			return apperr.PermissionDenied("registration is closed")
		default:
			return apperr.Internal("could not create account")
		}
	}

	h.auditAuth(c, "auth.register", user.ID, user.Username, "success", "")
	logStateChange(c, "auth.register", "", ctxlog.OutcomeSuccess)
	return h.startSession(c, user, fiber.StatusCreated)
}

// AuthLogin authenticates a username-or-email plus password and opens a session
// (POST /api/auth/login).
//
// Every rejection returns the same 401 with the same message. Which of "no such
// account", "wrong password" and "account disabled" actually happened is never
// distinguishable from the response — telling them apart is how a login form
// becomes an account enumerator.
func (h *Handlers) AuthLogin(c *fiber.Ctx) error {
	var req struct {
		Identifier string `json:"identifier"`
		Password   string `json:"password"`
	}
	if err := c.BodyParser(&req); err != nil {
		return apperr.Validation("invalid request body")
	}
	identifier := strings.TrimSpace(req.Identifier)
	if identifier == "" || req.Password == "" {
		return apperr.Validation("identifier and password are required")
	}

	ctx := c.UserContext()
	// This route is in the auth middleware's builtin skips, so the failed-authn
	// budget it normally applies never ran — the password path has to consult
	// and feed it here or brute force against it is unmetered.
	if h.deps.FailLimiter != nil && h.deps.FailLimiter.Exceeded(ctx, c.IP()) {
		return apperr.RateLimited()
	}
	if h.deps.Users == nil {
		return apperr.Unavailable()
	}

	user, err := h.deps.Users.GetByIdentifier(ctx, identifier)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// The identifier is deliberately NOT recorded: people type passwords
		// into the username field, and this is the one path where whatever was
		// typed belongs to no account and would land in the audit trail as-is.
		// The reason code and the IP are what an operator needs here.
		password.VerifyDecoy(req.Password)
		return h.rejectLogin(c, "", "", "unknown_identifier")
	}
	if err != nil {
		return apperr.Unavailable("could not load account")
	}
	if !password.Verify(user.PasswordHash, req.Password) {
		return h.rejectLogin(c, user.ID, user.Username, "bad_password")
	}
	// After the password check, not before: answering "disabled" to anyone who
	// merely names the account would confirm that it exists.
	if user.Disabled {
		return h.rejectLogin(c, user.ID, user.Username, "account_disabled")
	}

	if err := h.deps.Users.StampLogin(ctx, user.ID, time.Now()); err != nil {
		// Login-activity telemetry, not the decision — a failed stamp must not
		// cost a valid user their session.
		logger.Module("auth").Warn("stamp login times failed",
			zap.String("user_id", user.ID), zap.Error(err))
	}

	h.auditAuth(c, "auth.login", user.ID, user.Username, "success", "")
	logStateChange(c, "auth.login", "", ctxlog.OutcomeSuccess)
	return h.startSession(c, user, fiber.StatusOK)
}

// rejectLogin records one failed attempt against the per-IP budget and returns
// the single indistinguishable 401 every failure path shares.
func (h *Handlers) rejectLogin(c *fiber.Ctx, userID, actorName, reason string) error {
	if h.deps.FailLimiter != nil {
		h.deps.FailLimiter.Hit(c.UserContext(), c.IP())
	}
	h.auditAuth(c, "auth.login", userID, actorName, "denied", reason)
	return apperr.Unauthorized("invalid credentials")
}

// errAccountTaken and errBootstrapLost are createAccount's two expected
// failures, separated from an infrastructure error so the handler can answer
// 409 and 403 rather than 500.
var (
	errAccountTaken  = errors.New("handler: username or email already in use")
	errBootstrapLost = errors.New("handler: lost the first-account race")
	// errIdentityTaken means the (provider, subject) pair is already bound —
	// to this account or to another one. Only the OIDC paths can hit it: a
	// local registration's subject is the user id it just generated.
	errIdentityTaken = errors.New("handler: identity already linked")
)

// createAccount writes the user row and the credential it was created from in
// one transaction, so an account can never exist with no way to log into it.
// identity is the "local" row for a password registration and the provider's
// (provider, subject) pair for an OIDC provisioning.
//
// bootstrap means "this is meant to be the instance's first account, so make it
// an admin". Reading an empty user table before the insert is not enough to
// establish that: under a snapshot each of two concurrent registrations sees
// only its own row and both would conclude they were first. claimBootstrap
// settles it with a primary key instead.
func (h *Handlers) createAccount(ctx context.Context, user *model.User, identity model.UserIdentity, bootstrap bool) error {
	identity.UserID = user.ID
	return h.deps.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(user).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return errAccountTaken
			}
			return err
		}
		if bootstrap {
			if err := h.claimBootstrap(tx); err != nil {
				return err
			}
		}
		if err := tx.Create(&identity).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return errIdentityTaken
			}
			return err
		}
		return nil
	})
}

// claimBootstrap takes the one-time "first account" slot inside the caller's
// transaction, returning errBootstrapLost when another registration already
// holds it.
//
// The exclusion is the setting table's primary key: a second transaction
// inserting the same key waits for this one and then sees it taken, which is a
// guarantee a COUNT cannot give across engines.
//
// A key that is already there is not automatically a loss. The marker outlives
// the account that wrote it, so an instance whose accounts were all removed (by
// the admin CLI, say) would otherwise never be able to bootstrap a new admin
// again. Because the conflicting insert has waited for the other transaction to
// finish, the user count is decisive at that point: ours being the only row
// means the marker is stale rather than contested, and this registration takes
// it over.
func (h *Handlers) claimBootstrap(tx *gorm.DB) error {
	now := model.Now().Format(time.RFC3339)
	claimed, err := h.deps.Settings.ClaimTx(tx, model.SettingBootstrapAt, now)
	if err != nil {
		return err
	}
	if claimed {
		return nil
	}

	var accounts int64
	if err := tx.Model(&model.User{}).Count(&accounts).Error; err != nil {
		return err
	}
	if accounts != 1 {
		return errBootstrapLost
	}
	return h.deps.Settings.SetTx(tx, model.SettingBootstrapAt, now)
}

// startSession mints the login session, sets the cookie and returns the account
// body both register and login answer with.
func (h *Handlers) startSession(c *fiber.Ctx, user *model.User, status int) error {
	if h.deps.Sessions == nil {
		return apperr.Unavailable()
	}
	// A password account has no provider picture, so this resolves to the
	// Gravatar for its address or to nothing at all. Detached and best-effort:
	// see cacheAvatarAsync.
	h.refreshAvatar(user, "")
	sid, err := h.deps.Sessions.Create(c.UserContext(), session.Data{
		AuthzID:     user.ID,
		DisplayName: user.DisplayName,
		CreatedAt:   model.Now(),
	})
	if err != nil {
		return apperr.Unavailable("could not open a session")
	}
	h.setSessionCookie(c, sid, h.sessionCookieMaxAge())
	return c.Status(status).JSON(accountDTO(user))
}

// sessionCookieMaxAge is the cookie's lifetime: the session's absolute cap, so
// the browser stops presenting a cookie the store would refuse anyway. The
// store's shorter idle TTL still expires the session first when the user goes
// away — the cookie outliving it is what authenticateSession's "valid-looking
// cookie, session gone" path is about.
func (h *Handlers) sessionCookieMaxAge() int {
	ttl := h.deps.Cfg.Auth.Session.AbsoluteTTL
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	return int(ttl.Seconds())
}

func accountDTO(u *model.User) dto.AccountResponse {
	resp := dto.AccountResponse{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		IsAdmin:     u.IsAdmin,
	}
	if u.Email != nil {
		resp.Email = *u.Email
	}
	return resp
}

// instanceIsEmpty reports whether no account exists yet — the state that lets
// the first registration through with the switch off.
func (h *Handlers) instanceIsEmpty(ctx context.Context) (bool, error) {
	if h.deps.Users == nil {
		return false, nil
	}
	any, err := h.deps.Users.Any(ctx)
	if err != nil {
		return false, err
	}
	return !any, nil
}

// registrationOpen reads the switch from the setting table, falling back to the
// config seed when Settings is unwired.
func (h *Handlers) registrationOpen(ctx context.Context) (bool, error) {
	def := h.deps.Cfg != nil && h.deps.Cfg.Auth.RegistrationOpen
	if h.deps.Settings == nil {
		return def, nil
	}
	return h.deps.Settings.GetBool(ctx, model.SettingRegistrationOpen, def)
}

// auditAuth writes one authentication audit row. Authentication events can be
// anonymous (an identifier that resolves to nothing), so actor stays empty and
// only the display-name snapshot carries what the caller typed.
func (h *Handlers) auditAuth(c *fiber.Ctx, action, userID, actorName, outcome, reason string) {
	h.auditBestEffort(c, &model.AuditLog{
		Action:       action,
		Outcome:      outcome,
		ReasonCode:   reason,
		AuthChannel:  userctx.ChannelSession,
		Actor:        userID,
		ActorName:    actorName,
		ResourceType: "user",
		ResourceID:   userID,
	})
}
