package handler

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/account"
	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/logger"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/password"
	"github.com/Xm798/placard/internal/userctx"
)

// AccountProfile returns the signed-in account's username, email and whether it
// has a password (GET /api/account).
func (h *Handlers) AccountProfile(c *fiber.Ctx) error {
	user, err := h.currentUser(c)
	if err != nil {
		return err
	}
	resp := dto.ProfileResponse{
		Username:    user.Username,
		DisplayName: user.DisplayName,
		HasPassword: user.PasswordHash != "",
	}
	if user.Email != nil {
		resp.Email = *user.Email
	}
	return c.JSON(resp)
}

// AccountChangePassword replaces the signed-in account's password
// (PUT /api/account/password).
//
// An account that already has a password must present it: a session cookie
// alone proves only that this browser is signed in, and the password is what
// signs in from every other one. An account provisioned through a provider has
// none to present, so the same call sets its first password and makes it a
// login method.
//
// A wrong current password is answered with 403 rather than 401 — 401 means
// "the session is gone" to the app, which would send the user to the login page
// — and is charged to the same per-IP failure budget as a failed login, since
// it is the same guess against the same hash.
//
// The change signs the account out of every other browser (see
// UserRepo.SetPasswordHash). The caller's own session is on the old epoch too,
// so it is replaced with one on the new epoch and this browser stays signed in.
func (h *Handlers) AccountChangePassword(c *fiber.Ctx) error {
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := c.BodyParser(&req); err != nil {
		return apperr.Validation("invalid request body")
	}
	if err := account.ValidatePassword(req.NewPassword); err != nil {
		return apperr.Validation(err.Error())
	}

	ctx := c.UserContext()
	if h.deps.Identities == nil || h.deps.Sessions == nil {
		return apperr.Unavailable()
	}
	if h.deps.FailLimiter != nil && h.deps.FailLimiter.Exceeded(ctx, c.IP()) {
		return apperr.RateLimited()
	}
	user, err := h.currentUser(c)
	if err != nil {
		return err
	}
	if user.PasswordHash != "" && !password.Verify(user.PasswordHash, req.CurrentPassword) {
		if h.deps.FailLimiter != nil {
			h.deps.FailLimiter.Hit(ctx, c.IP())
		}
		h.auditAuth(c, "auth.password_change", user.ID, user.Username, "denied", "bad_password")
		return apperr.PermissionDenied("current password is incorrect")
	}

	hash, err := password.Hash(req.NewPassword)
	if err != nil {
		return apperr.Internal("could not hash password")
	}
	epoch, err := h.deps.Users.SetPasswordRevokingSessions(ctx, user.ID, hash)
	if err != nil {
		return apperr.Internal("could not update account")
	}
	// Only a first password needs the local credential row; an account that
	// had a password already has one.
	if user.PasswordHash == "" {
		if err := h.deps.Identities.EnsureLocal(ctx, user.ID); err != nil {
			return apperr.Internal("could not update account")
		}
	}

	if sid := c.Cookies(h.deps.SessionCookie); sid != "" {
		if err := h.deps.Sessions.Delete(ctx, sid); err != nil {
			logger.Module("auth").Warn("drop pre-change session failed", zap.Error(err))
		}
	}
	user.SessionEpoch = epoch
	if err := h.openSession(c, user); err != nil {
		return err
	}

	h.auditAuth(c, "auth.password_change", user.ID, user.Username, "success", "")
	logStateChange(c, "auth.password_change", "", ctxlog.OutcomeSuccess)
	return c.SendStatus(fiber.StatusNoContent)
}

// currentUser loads the user row behind the request's identity. A session whose
// account row is gone answers 404: the account was deleted under it.
func (h *Handlers) currentUser(c *fiber.Ctx) (*model.User, error) {
	if h.deps.Users == nil {
		return nil, apperr.Unavailable()
	}
	user, err := h.deps.Users.Get(c.UserContext(), userctx.AuthzID(c))
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, apperr.NotFound("not found")
	case err != nil:
		return nil, apperr.Unavailable("could not load account")
	}
	return user, nil
}
