package handler

import (
	"fmt"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/userctx"
)

// Meta handles GET /s/:id/meta. It returns ONLY the render metadata: after the
// GetActiveByNanoID (is_deleted=0 AND expires_at>=NOW()) check, it hands back the serving-cache title snapshot and the same-origin
// render_url (/s/:id/render). No object-store round-trip: the title was snapshotted
// into file_version.title at publish time and mirrored into file.title by the
// serving-cache invariant.
//
// The route is open to anonymous visitors: authorizeViewOrAudit decides, and
// authz.View lets an anonymous caller through for a `link` page only.
//
// ?v=N is an owner-only preview: it takes effect only when the requester IS
// the owner (authzid == create_user) AND the version row exists; everyone and
// everything else falls back to the serving version — never a 404, so version
// existence is not probeable. The response shape is identical either way
// (MetaResponse leaks no version info).
//
// The view record and audit are NOT taken here: they happen at /s/:id/render,
// the point where the content is actually read (meta can be skipped by a client
// that fetches /render directly).
//
// A page carrying a share code additionally requires the unlock ticket cookie
// (or the owner's own session): without it the answer is 403 share_code_required.
//
// On miss/expired/deleted/denied: return ONLY {expired:true} — no render_url,
// no title, no date — driving the viewer's generic failure state. A denied
// caller gets the identical body a missing id gets, so "this page exists but
// is private" is not a distinguishable answer.
func (h *Handlers) Meta(c *fiber.Ctx) error {
	id := c.Params("id")

	// Identity-dependent (a private page answers its owner and everyone else
	// differently), so no shared cache may reuse one visitor's response.
	c.Set("Cache-Control", "no-store")

	file, err := h.deps.Files.GetActiveByNanoID(c.UserContext(), id)
	if err != nil {
		return c.JSON(dto.ExpiredResponse{Expired: true})
	}

	// authorizeViewOrAudit runs before the ?v branch and before anything else
	// that could leak information — a denied caller must see the same
	// {expired:true} regardless of what ?v they passed.
	authzid := userctx.AuthzID(c)
	if !h.authorizeViewOrAudit(c, file, authzid) {
		return c.JSON(dto.ExpiredResponse{Expired: true})
	}
	// A locked page answers 403 rather than {expired:true}: the shell has
	// already shown this visitor the unlock form (the /s/:id body says the page
	// is code-protected), so there is nothing left to hide here, and the
	// distinct status is what tells the shell to send them back to it after the
	// ticket expires instead of dead-ending on the generic failure state.
	if h.shareCodeLocked(c, file, authzid) {
		return errShareCodeRequired()
	}

	title := file.Title
	renderURL := "/s/" + id + "/render"

	if v := c.QueryInt("v", 0); v > 0 && authzid != "" && authzid == file.CreateUser {
		if ver, verr := h.deps.Versions.GetByVersion(c.UserContext(), id, v); verr == nil {
			title = ver.Title
			renderURL = fmt.Sprintf("/s/%s/render?v=%d", id, v)
		}
	}

	return c.JSON(dto.MetaResponse{
		ID:        file.NanoID,
		Title:     title,
		Expired:   false,
		RenderURL: renderURL,
	})
}
