package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/account"
	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/password"
	"github.com/Xm798/placard/internal/userctx"
)

// The admin surface: the account table an operator manages, and the instance
// settings they change without a restart.
//
// Nothing here reveals what anyone has published. An admin governs accounts and
// policy; the pages behind other people's share links are not theirs to read,
// and no response in this file may ever carry a share credential (see the
// AdminUserItem whitelist).

// adminDeleteFileBatch is how many of a deleted user's pages one round reclaims.
// The loop re-queries from offset 0 each time because MarkDeleted removes rows
// from the owner scope it pages over.
const adminDeleteFileBatch = 100

// requireAdmin gates every /api/admin route. It resolves the account fresh on
// each request rather than trusting a flag captured at login, so a withdrawn
// admin flag takes effect on the session that already exists.
//
// A non-admin gets the same 403 as an unknown account: the routes behind this
// are already past the login gate, so the caller learns only that this surface
// is not theirs.
func (h *Handlers) requireAdmin(c *fiber.Ctx) error {
	authzID := userctx.AuthzID(c)
	if authzID == "" {
		return apperr.Unauthorized()
	}
	// The surface behind this reaches most of the persistence layer (deleting
	// an account takes its pages, objects and credentials with it), so an
	// instance missing any of it answers 503 rather than panicking mid-delete.
	if h.deps.DB == nil || h.deps.Users == nil || h.deps.Identities == nil ||
		h.deps.Settings == nil || h.deps.Tokens == nil || h.deps.Files == nil ||
		h.deps.Versions == nil || h.deps.Pending == nil || h.deps.Views == nil {
		return apperr.Unavailable()
	}
	u, err := h.deps.Users.Get(c.UserContext(), authzID)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return apperr.PermissionDenied("admin only")
	case err != nil:
		return apperr.Unavailable("could not load account")
	}
	if !u.IsAdmin {
		return apperr.PermissionDenied("admin only")
	}
	return c.Next()
}

// AdminListUsers returns one page of accounts (GET /api/admin/users).
func (h *Handlers) AdminListUsers(c *fiber.Ctx) error {
	ctx := c.UserContext()

	page := max(1, c.QueryInt("page", 1))
	pageSize := c.QueryInt("page_size", defaultPageSize)
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	pageSize = min(pageSize, maxPageSize)

	total, err := h.deps.Users.Count(ctx)
	if err != nil {
		return apperr.Internal("could not list users")
	}
	users, err := h.deps.Users.List(ctx, (page-1)*pageSize, pageSize)
	if err != nil {
		return apperr.Internal("could not list users")
	}

	items := make([]dto.AdminUserItem, 0, len(users))
	for _, u := range users {
		items = append(items, dto.AdminUserItemFrom(u))
	}

	return c.JSON(dto.AdminUsersResponse{
		Users:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Self:     userctx.AuthzID(c),
	})
}

// AdminPatchUser flips the admin and disabled flags (PATCH /api/admin/users/:id).
// Both fields are optional; an absent one is left as it is.
//
// An admin may neither disable nor demote themselves. Both would cost this
// session its access to the very page that could undo it, and on an instance
// with one admin they would leave nobody able to reach the admin surface at
// all — recoverable only through the `admin` subcommand on the server host.
func (h *Handlers) AdminPatchUser(c *fiber.Ctx) error {
	var req struct {
		IsAdmin  *bool `json:"is_admin"`
		Disabled *bool `json:"disabled"`
	}
	if err := c.BodyParser(&req); err != nil {
		return apperr.Validation("invalid request body")
	}
	if req.IsAdmin == nil && req.Disabled == nil {
		return apperr.Validation("nothing to update")
	}

	ctx := c.UserContext()
	self := userctx.AuthzID(c)
	target := c.Params("id")
	if target == self {
		if req.Disabled != nil && *req.Disabled {
			return apperr.Validation("an admin cannot disable their own account")
		}
		if req.IsAdmin != nil && !*req.IsAdmin {
			return apperr.Validation("an admin cannot withdraw their own admin rights")
		}
	}

	rows, err := h.deps.Users.SetFlags(ctx, target, req.IsAdmin, req.Disabled)
	if err != nil {
		return apperr.Internal("could not update account")
	}
	if rows == 0 {
		return apperr.NotFound("not found")
	}
	if req.Disabled != nil {
		h.auditAdminUser(c, self, target, "admin.user.disabled", strconv.FormatBool(*req.Disabled))
	}
	if req.IsAdmin != nil {
		h.auditAdminUser(c, self, target, "admin.user.is_admin", strconv.FormatBool(*req.IsAdmin))
	}

	logStateChange(c, "admin.user.update", "", ctxlog.OutcomeSuccess)
	return c.SendStatus(fiber.StatusNoContent)
}

// AdminResetPassword sets a new local password (POST /api/admin/users/:id/password).
//
// It also gives a password to an account that had none (an OIDC-only user), which
// is what makes this the recovery path when a provider becomes unreachable.
func (h *Handlers) AdminResetPassword(c *fiber.Ctx) error {
	var req struct {
		Password string `json:"password"`
	}
	if err := c.BodyParser(&req); err != nil {
		return apperr.Validation("invalid request body")
	}
	if err := account.ValidatePassword(req.Password); err != nil {
		return apperr.Validation(err.Error())
	}

	ctx := c.UserContext()
	target := c.Params("id")
	hash, err := password.Hash(req.Password)
	if err != nil {
		return apperr.Internal("could not hash password")
	}

	rows, err := h.deps.Users.SetPasswordHash(ctx, target, hash)
	if err != nil {
		return apperr.Internal("could not update account")
	}
	if rows == 0 {
		return apperr.NotFound("not found")
	}
	// A reset changes the password and nothing else. Sessions opened with the
	// old one survive it: the session store is keyed by session id and cannot
	// be swept by account, so an operator dealing with a stolen credential
	// deletes the account rather than resetting it. Disabling refuses every
	// existing session while the flag is set, but re-enabling revives them.
	//
	// EnsureLocal is what makes the new password a login method on an account
	// provisioned through a provider, which carries no local credential row.
	if err := h.deps.Identities.EnsureLocal(ctx, target); err != nil {
		return apperr.Internal("could not update account")
	}

	h.auditAdminUser(c, userctx.AuthzID(c), target, "admin.user.password_reset", "")
	logStateChange(c, "admin.user.password_reset", "", ctxlog.OutcomeSuccess)
	return c.SendStatus(fiber.StatusNoContent)
}

// AdminDeleteUser removes an account and everything that authenticates into it
// or belongs to it (DELETE /api/admin/users/:id).
//
// Self-deletion is refused for the same reason self-disabling is, and more
// bluntly: it is the one operation on this surface that nothing — not even
// another admin — can undo.
//
// The user's pages follow the same path as an owner deleting them: object keys
// are queued under the user_delete retention window, so the pages are dead
// immediately while the bytes stay recoverable for as long as the operator
// configured.
//
// The pages go first, one transaction each, and the account row goes last. A
// failure in between leaves the account present with some of its pages already
// gone, which running the delete again finishes — the alternative, one
// transaction spanning every page and object of an account, holds write locks
// for as long as that account is large.
func (h *Handlers) AdminDeleteUser(c *fiber.Ctx) error {
	ctx := c.UserContext()
	self := userctx.AuthzID(c)
	target := c.Params("id")
	if target == self {
		return apperr.Validation("an admin cannot delete their own account")
	}

	user, err := h.deps.Users.Get(ctx, target)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return apperr.NotFound("not found")
	case err != nil:
		return apperr.Unavailable("could not load account")
	}

	if err := h.deleteOwnedFiles(ctx, target, self); err != nil {
		return apperr.Internal("could not delete account")
	}

	err = h.deps.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if user.AvatarKey != "" {
			if e := h.deps.Pending.InsertBatch(tx, []string{user.AvatarKey}, model.ReasonUserDelete, self); e != nil {
				return e
			}
		}
		if e := h.deps.Tokens.DeleteByUser(tx, target); e != nil {
			return e
		}
		if e := h.deps.Identities.DeleteByUser(tx, target); e != nil {
			return e
		}
		return h.deps.Users.DeleteTx(tx, target)
	})
	if err != nil {
		return apperr.Internal("could not delete account")
	}

	h.auditAdminUser(c, self, target, "admin.user.delete", user.Username)
	logStateChange(c, "admin.user.delete", "", ctxlog.OutcomeSuccess)
	return c.SendStatus(fiber.StatusNoContent)
}

// deleteOwnedFiles soft-deletes every page the account owns, queueing each
// version's object for the retention window, exactly as DeleteFile does for a
// single page.
//
// It pages from offset 0 every round rather than walking a cursor forward:
// MarkDeleted takes each batch out of the owner scope being listed, so offset 0
// is always the next undeleted page.
func (h *Handlers) deleteOwnedFiles(ctx context.Context, owner, actor string) error {
	for {
		files, err := h.deps.Files.ListOwned(ctx, owner, 0, adminDeleteFileBatch)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return nil
		}
		for _, f := range files {
			keys, err := h.deps.Versions.ReclaimKeys(ctx, f.NanoID, f.ObjectKey)
			if err != nil {
				return err
			}
			err = h.deps.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if e := h.deps.Pending.InsertBatch(tx, keys, model.ReasonUserDelete, actor); e != nil {
					return e
				}
				if e := h.deps.Files.MarkDeleted(tx, f.NanoID, actor); e != nil {
					return e
				}
				return h.deps.Views.DeleteByFile(tx, f.NanoID)
			})
			if err != nil {
				return err
			}
		}
	}
}

// AdminGetSettings returns the runtime instance settings (GET /api/admin/settings).
func (h *Handlers) AdminGetSettings(c *fiber.Ctx) error {
	ctx := c.UserContext()
	if h.deps.Settings == nil {
		return apperr.Unavailable()
	}

	resp := dto.AdminSettingsResponse{}
	var err error
	if resp.RegistrationOpen, err = h.deps.Settings.GetBool(ctx, model.SettingRegistrationOpen, h.seedRegistrationOpen()); err != nil {
		return apperr.Unavailable("could not read instance settings")
	}
	if resp.OIDCAutoProvision, err = h.deps.Settings.GetBool(ctx, model.SettingOIDCAutoProvision, h.seedOIDCAutoProvision()); err != nil {
		return apperr.Unavailable("could not read instance settings")
	}
	if resp.UploadMaxFileSize, err = h.deps.Settings.GetInt64(ctx, model.SettingUploadMaxFileSize, h.seedUploadMaxFileSize()); err != nil {
		return apperr.Unavailable("could not read instance settings")
	}
	return c.JSON(resp)
}

// AdminPatchSettings updates the runtime instance settings
// (PATCH /api/admin/settings). Absent fields are left as they are.
//
// Every write goes to the setting table, which is what every reader consults on
// the request that needs it — so a change is in force on the next request,
// with no restart. The one bound that survives a restart is fasthttp's
// BodyLimit, set from the config file at boot: lowering the upload cap here
// takes effect immediately, raising it past the configured value does not.
func (h *Handlers) AdminPatchSettings(c *fiber.Ctx) error {
	var req struct {
		RegistrationOpen  *bool  `json:"registration_open"`
		OIDCAutoProvision *bool  `json:"oidc_auto_provision"`
		UploadMaxFileSize *int64 `json:"upload_max_file_size"`
	}
	if err := c.BodyParser(&req); err != nil {
		return apperr.Validation("invalid request body")
	}
	if req.RegistrationOpen == nil && req.OIDCAutoProvision == nil && req.UploadMaxFileSize == nil {
		return apperr.Validation("nothing to update")
	}
	if req.UploadMaxFileSize != nil && *req.UploadMaxFileSize <= 0 {
		return apperr.Validation("upload_max_file_size must be positive")
	}
	if h.deps.Settings == nil {
		return apperr.Unavailable()
	}

	ctx := c.UserContext()
	changed := map[string]string{}
	if req.RegistrationOpen != nil {
		changed[model.SettingRegistrationOpen] = strconv.FormatBool(*req.RegistrationOpen)
	}
	if req.OIDCAutoProvision != nil {
		changed[model.SettingOIDCAutoProvision] = strconv.FormatBool(*req.OIDCAutoProvision)
	}
	if req.UploadMaxFileSize != nil {
		changed[model.SettingUploadMaxFileSize] = strconv.FormatInt(*req.UploadMaxFileSize, 10)
	}
	// One transaction for the whole patch: a request carrying two settings
	// whose second write fails would otherwise leave the first in force while
	// answering 500, and the caller would go on showing the old value for a
	// setting the instance had already adopted.
	err := h.deps.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for key, value := range changed {
			if e := h.deps.Settings.SetTx(tx, key, value); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return apperr.Internal("could not save instance settings")
	}

	h.auditAdminSettings(c, userctx.AuthzID(c), changed)
	logStateChange(c, "admin.settings.update", "", ctxlog.OutcomeSuccess)
	return h.AdminGetSettings(c)
}

func (h *Handlers) seedRegistrationOpen() bool {
	return h.deps.Cfg != nil && h.deps.Cfg.Auth.RegistrationOpen
}

func (h *Handlers) seedOIDCAutoProvision() bool {
	return h.deps.Cfg != nil && h.deps.Cfg.Auth.OIDCAutoProvision
}

func (h *Handlers) seedUploadMaxFileSize() int64 {
	if h.deps.Cfg == nil {
		return 0
	}
	return h.deps.Cfg.Upload.MaxFileSize
}

// auditAdminUser records one administrative action against an account. detail
// is the value the action settled on (the new flag, the deleted username), kept
// as a JSON object because the details column is validated JSON.
func (h *Handlers) auditAdminUser(c *fiber.Ctx, actor, target, action, detail string) {
	details := ""
	if detail != "" {
		if b, err := json.Marshal(map[string]string{"value": detail}); err == nil {
			details = string(b)
		}
	}
	h.auditBestEffort(c, &model.AuditLog{
		Action:       action,
		Actor:        actor,
		ActorName:    displayName(c),
		ResourceType: "user",
		ResourceID:   target,
		Details:      details,
	})
}

// auditAdminSettings records a settings change with the keys and values it
// wrote, so the audit trail says what the instance policy became.
func (h *Handlers) auditAdminSettings(c *fiber.Ctx, actor string, changed map[string]string) {
	details := ""
	if b, err := json.Marshal(changed); err == nil {
		details = string(b)
	}
	h.auditBestEffort(c, &model.AuditLog{
		Action:       "admin.settings.update",
		Actor:        actor,
		ActorName:    displayName(c),
		ResourceType: "setting",
		Details:      details,
	})
}
