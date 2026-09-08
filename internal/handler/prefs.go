package handler

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/userctx"
)

// GetPrefs returns the user's default_visibility preference from the DB.
// If no user row exists or the value is invalid, returns {"default_visibility":"link"} with HTTP 200.
// MUST NEVER create a user row.
func (h *Handlers) GetPrefs(c *fiber.Ctx) error {
	authzID := userctx.AuthzID(c)
	if authzID == "" {
		return apperr.Unauthorized()
	}

	u, err := h.deps.Users.Get(c.UserContext(), authzID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.JSON(dto.PrefsResponse{DefaultVisibility: model.VisibilityLink})
		}
		return apperr.Internal("could not load preferences")
	}

	vis := u.DefaultVisibility
	if !model.ValidVisibility(vis) {
		vis = model.VisibilityLink
	}

	return c.JSON(dto.PrefsResponse{DefaultVisibility: vis})
}

// PutPrefs sets the user's default_visibility preference.
// Request body: {"default_visibility":"private|link"}
// Value domain: only "private" or "link" allowed.
// If RowsAffected == 0 (no user row exists) -> 503 with apperr.Unavailable.
// Success (RowsAffected > 0) -> HTTP 204, no body.
func (h *Handlers) PutPrefs(c *fiber.Ctx) error {
	authzID := userctx.AuthzID(c)
	if authzID == "" {
		return apperr.Unauthorized()
	}

	var req struct {
		DefaultVisibility string `json:"default_visibility"`
	}
	if err := c.BodyParser(&req); err != nil {
		return apperr.Validation("invalid request body")
	}

	v := req.DefaultVisibility

	if !model.ValidVisibility(v) {
		return apperr.Validation("invalid visibility value")
	}

	rowsAffected, err := h.deps.Users.SetDefaultVisibility(c.UserContext(), authzID, v)
	if err != nil {
		return apperr.Internal("could not save preferences")
	}

	if rowsAffected == 0 {
		return apperr.Unavailable("profile not ready")
	}

	return c.SendStatus(fiber.StatusNoContent)
}
