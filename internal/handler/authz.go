package handler

import (
	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/authz"
	"github.com/Xm798/placard/internal/model"
)

// authorizeViewOrAudit wraps authz.View with the file.view_denied audit
// side effect meta.go and render.go both need on a denied verdict. Callers keep
// full control of their own response shape; this only removes the duplicated
// authorize+audit boilerplate.
func (h *Handlers) authorizeViewOrAudit(c *fiber.Ctx, file *model.File, authzid string) bool {
	ok := authz.View(file, authzid)
	if !ok {
		h.auditBestEffort(c, &model.AuditLog{
			Action:       "file.view_denied",
			Outcome:      "denied",
			Actor:        authzid,
			FileNanoID:   file.NanoID,
			ResourceType: "file",
			ResourceID:   file.NanoID,
		})
	}
	return ok
}
