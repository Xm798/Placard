package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/userctx"
)

// ownedFile resolves the caller's authzid and loads their file by nanoID,
// collapsing "no session" and "not found/not owner" into the standard
// unauthorized/404 responses shared by every owner-only version endpoint
// below. Non-owner and unknown id stay indistinguishable (404, no existence
// leak).
func (h *Handlers) ownedFile(c *fiber.Ctx, nanoID string) (*model.File, string, error) {
	authzid := userctx.AuthzID(c)
	if authzid == "" {
		return nil, "", apperr.Unauthorized()
	}
	file, err := h.deps.Files.GetOwned(c.UserContext(), nanoID, authzid)
	if err != nil {
		return nil, "", apperr.NotFound("not found")
	}
	return file, authzid, nil
}

// ListVersions handles GET /api/files/:id/versions. Owner-only: non-owner or
// unknown id is an indistinguishable 404. This is the ONLY endpoint exposing
// version history — the public /s/:id/meta never leaks it.
func (h *Handlers) ListVersions(c *fiber.Ctx) error {
	nanoID := c.Params("id")
	file, _, err := h.ownedFile(c, nanoID)
	if err != nil {
		return err
	}
	versions, err := h.deps.Versions.ListByNanoID(c.UserContext(), nanoID)
	if err != nil {
		return apperr.Internal("could not list versions")
	}
	items := make([]dto.FileVersionItem, 0, len(versions))
	for _, v := range versions {
		items = append(items, dto.FileVersionItem{
			Version: v.Version, Title: v.Title, SizeBytes: v.SizeBytes, CreateTime: v.CreateTime,
		})
	}
	return c.JSON(dto.FileVersionsResponse{
		LatestVersion: file.LatestVersion, SharedVersion: file.SharedVersion, Versions: items,
	})
}

// patchFileRequest holds PATCH /api/files/:id's two independently-optional
// fields — at least one must be present. shared_version pins/unpins the
// serving version (unchanged semantics); visibility changes the file's
// visibility. Either or both may be set in one request.
type patchFileRequest struct {
	SharedVersion *int    `json:"shared_version"`
	Visibility    *string `json:"visibility"`
}

// PatchFile handles PATCH /api/files/:id: pinning/unpinning the served version
// and/or changing visibility, both inside one tx so a concurrent republish
// can't race either mutation. At least one field must be present. Each
// present field is validated up front (before opening the tx) so a doomed
// request never takes the row lock. Audits are field-scoped: shared_version
// always audits file.pin_version on success (unchanged behavior); visibility
// audits file.visibility_change ONLY when the value actually changed.
func (h *Handlers) PatchFile(c *fiber.Ctx) error {
	nanoID := c.Params("id")
	// Cheap unlocked pre-check: fail fast on unknown/non-owner id or a bad body
	// before opening a tx. The write path below re-resolves under a row lock —
	// this first read is never relied on for the actual mutation.
	_, authzid, err := h.ownedFile(c, nanoID)
	if err != nil {
		return err
	}
	var req patchFileRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return apperr.Validation("invalid json body")
	}
	if req.SharedVersion == nil && req.Visibility == nil {
		return apperr.Validation("no fields to update")
	}

	var target int
	if req.SharedVersion != nil {
		target = *req.SharedVersion
		if target < 0 {
			return apperr.Validation("invalid shared_version")
		}
	}

	var newVisibility string
	if req.Visibility != nil {
		newVisibility = strings.TrimSpace(*req.Visibility)
		if verr := h.validateVisibilityValue(newVisibility); verr != nil {
			return verr
		}
	}

	// Resolve "follow latest" (target==0) and write both fields inside ONE
	// DB-only tx: GetOwnedForUpdate takes a row lock on file BEFORE reading
	// latest_version, so a concurrent republish can't advance the head between
	// the read and this write (that TOCTOU would pin a stale/expired serving
	// cache). The lock is acquired and released entirely inside this tx — it
	// never spans an external call (storage, etc).
	var oldVisibility string
	err = h.deps.DB.WithContext(c.UserContext()).Transaction(func(tx *gorm.DB) error {
		file, ferr := h.deps.Files.GetOwnedForUpdate(tx, nanoID, authzid)
		if ferr != nil {
			if errors.Is(ferr, gorm.ErrRecordNotFound) {
				return apperr.NotFound("not found")
			}
			return ferr
		}
		oldVisibility = file.Visibility

		if req.SharedVersion != nil {
			resolve := target
			if target == 0 {
				resolve = file.LatestVersion
			}
			serving, verr := h.deps.Versions.GetByVersionTx(tx, nanoID, resolve)
			if verr != nil {
				if errors.Is(verr, gorm.ErrRecordNotFound) {
					return apperr.Validation("version does not exist")
				}
				return verr
			}
			if e := h.deps.Files.SetSharedVersion(tx, nanoID, target, serving, authzid); e != nil {
				return e
			}
		}

		if req.Visibility != nil {
			if e := h.deps.Files.SetVisibility(tx, nanoID, newVisibility, authzid); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		var appErr *apperr.Error
		if errors.As(err, &appErr) {
			return appErr
		}
		return apperr.Internal("update failed")
	}

	if req.SharedVersion != nil {
		h.auditFileDetails(c, authzid, nanoID, "file.pin_version", fmt.Sprintf(`{"shared_version":%d}`, target))
		logStateChange(c, "file.pin_version", nanoID, ctxlog.OutcomeSuccess)
	}
	if req.Visibility != nil && newVisibility != oldVisibility {
		h.auditFileDetails(c, authzid, nanoID, "file.visibility_change",
			fmt.Sprintf(`{"old":%q,"new":%q}`, oldVisibility, newVisibility))
		logStateChange(c, "file.visibility_change", nanoID, ctxlog.OutcomeSuccess)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// RestoreVersion appends a historical version's bytes as a new head version.
func (h *Handlers) RestoreVersion(c *fiber.Ctx) error {
	nanoID := c.Params("id")
	file, authzid, err := h.ownedFile(c, nanoID)
	if err != nil {
		return err
	}
	// Restoring writes a new head version behind the share link, same as
	// republish — an expired page stays a 404 (indistinguishable from an
	// owner-scope miss) rather than being resurrected via restore.
	if fileExpired(file) {
		return apperr.NotFound("not found")
	}
	v, err := c.ParamsInt("v")
	if err != nil || v < 1 {
		return apperr.Validation("invalid version")
	}
	ver, err := h.deps.Versions.GetByVersion(c.UserContext(), nanoID, v)
	if err != nil {
		return apperr.NotFound("not found")
	}
	rc, err := h.deps.Storage.GetObject(c.UserContext(), ver.ObjectKey)
	if err != nil {
		return apperr.Storage("could not load version content")
	}
	body, err := io.ReadAll(io.LimitReader(rc, h.deps.Cfg.Upload.MaxFileSize+1))
	_ = rc.Close()
	if err != nil {
		return apperr.Storage("could not load version content")
	}
	if int64(len(body)) > h.deps.Cfg.Upload.MaxFileSize {
		return apperr.Validation("file too large")
	}
	return h.publishNewVersion(c, file, body, ver.Title, authzid, "file.restore")
}
