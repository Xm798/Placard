package handler

import (
	"path"
	"strings"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/Xm798/placard/installer"
	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/middleware"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/userctx"
	"github.com/Xm798/placard/internal/web"
	placardskill "github.com/Xm798/placard/skills/placard"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// ListFiles handles GET /api/files. Owner-only, offset-paginated
// (?page=&page_size=), newest first. view_count is exposed here (owner DTO),
// unlike the public meta path which never leaks it. Each page's plaintext
// share code rides along for a SESSION caller only — the owner has to be able
// to read back what they hand out, and this is the only surface that shows it.
func (h *Handlers) ListFiles(c *fiber.Ctx) error {
	authzid := userctx.AuthzID(c)
	if authzid == "" {
		return apperr.Unauthorized()
	}

	page := max(1, c.QueryInt("page", 1))
	pageSize := c.QueryInt("page_size", defaultPageSize)
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	pageSize = min(pageSize, maxPageSize)
	offset := (page - 1) * pageSize

	total, err := h.deps.Files.CountOwned(c.UserContext(), authzid)
	if err != nil {
		return apperr.Internal("could not list files")
	}
	files, err := h.deps.Files.ListOwned(c.UserContext(), authzid, offset, pageSize)
	if err != nil {
		return apperr.Internal("could not list files")
	}

	// The share code is the one field this list withholds from the PAT
	// channel. `placard ls` is a legitimate token caller, but a token exists to
	// publish pages, and BrowserOnly already refuses to let one stand in for
	// its owner's session on the share routes — handing it the codes to every
	// coded page would give a leaked token more than that gate refuses.
	showShareCode := middleware.CookieChannel(c)

	items := make([]dto.FileListItem, 0, len(files))
	for _, f := range files {
		var shareCode string
		if showShareCode {
			shareCode, _ = h.activeShareCode(&f)
		}
		items = append(items, dto.FileListItem{
			ID:            f.NanoID,
			Title:         f.Title,
			URL:           h.shareURL(f.NanoID),
			ViewCount:     f.ViewCount,
			LatestVersion: f.LatestVersion,
			SharedVersion: f.SharedVersion,
			Visibility:    f.Visibility,
			ShareCode:     shareCode,
			CreateTime:    f.CreateTime,
			ExpiresAt:     dto.NullableExpiry(f.ExpiresAt),
		})
	}
	return c.JSON(dto.FileListResponse{
		Files: items, Total: total, Page: page, PageSize: pageSize,
	})
}

// DeleteFile handles DELETE /api/files/:id (nano_id). Owner-only: a non-owner or
// unknown id is an indistinguishable 404 (no existence confirmation). On hit it
// enqueues EVERY version's object key (reason user_delete — all enjoy the retention
// window), soft-deletes the file and clears its view rows in ONE tx, then audits
// file.delete. The link is dead immediately; objects are reclaimed by cron
// after the retention window.
func (h *Handlers) DeleteFile(c *fiber.Ctx) error {
	authzid := userctx.AuthzID(c)
	if authzid == "" {
		return apperr.Unauthorized()
	}

	nanoID := c.Params("id")
	file, err := h.deps.Files.GetOwned(c.UserContext(), nanoID, authzid)
	if err != nil {
		// Non-owner or unknown id → indistinguishable 404 (no existence leak).
		return apperr.NotFound("not found")
	}

	keys, err := h.deps.Versions.ReclaimKeys(c.UserContext(), nanoID, file.ObjectKey)
	if err != nil {
		return apperr.Internal("delete failed")
	}

	err = h.deps.DB.WithContext(c.UserContext()).Transaction(func(tx *gorm.DB) error {
		if e := h.deps.Pending.InsertBatch(tx, keys, model.ReasonUserDelete, authzid); e != nil {
			return e
		}
		if e := h.deps.Files.MarkDeleted(tx, nanoID, authzid); e != nil {
			return e
		}
		return h.deps.Views.DeleteByFile(tx, nanoID)
	})
	if err != nil {
		return apperr.Internal("delete failed")
	}

	h.auditFile(c, authzid, nanoID, "file.delete")
	logStateChange(c, "file.delete", nanoID, ctxlog.OutcomeSuccess)
	return c.SendStatus(fiber.StatusNoContent)
}

// AppPage serves the built React SPA shell (GET /, /files, /settings, /docs,
// and the unauthenticated /login and /register). The SPA switches sections
// client-side; all paths return the same index.html, which loads its hashed
// module bundle from same-origin /assets/*. CSP forbids inline script
// (script-src 'self') while allowing inline style for the bundled stylesheet's
// runtime needs.
func (h *Handlers) AppPage(c *fiber.Ctx) error {
	html, err := web.DistFS.ReadFile("dist/index.html")
	if err != nil {
		// dist not built (only .gitkeep present) — surface a clear 500.
		return apperr.Internal("frontend not built: dist/index.html missing")
	}
	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	setCommonSecurityHeaders(c)
	c.Set("Content-Security-Policy", "default-src 'self'; "+
		"style-src 'self' 'unsafe-inline'; script-src 'self'; "+
		"img-src 'self' data:; frame-ancestors 'self'; base-uri 'none'")
	return c.Send(html)
}

// SkillDoc serves the embedded Claude Code skill definition (GET /skill.md).
// Unauthenticated by design (middleware builtinSkips): agents fetch it via
// curl/WebFetch to self-install, and it is pure documentation — no secrets.
func (h *Handlers) SkillDoc(c *fiber.Ctx) error {
	return serveMarkdown(c, placardskill.SkillMD)
}

// InstallDoc serves the embedded one-time install guide (GET /install.md),
// same unauthenticated markdown contract as SkillDoc.
func (h *Handlers) InstallDoc(c *fiber.Ctx) error {
	return serveMarkdown(c, placardskill.InstallMD)
}

// InstallScript serves the CLI install script (GET /install.sh).
// Unauthenticated by design (middleware builtinSkips) — it is the branded
// `curl -fsSL https://<server>/install.sh | sh` entry point and carries no
// secrets. nosniff is mandatory: without it a browser could be talked into
// rendering the script as HTML.
//
// The script is served with the newest published CLI release pinned into it,
// so an install from this instance is reproducible; an instance that cannot
// reach GitHub serves it unpinned and the script resolves the release itself.
func (h *Handlers) InstallScript(c *fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, "text/x-shellscript; charset=utf-8")
	c.Set("X-Content-Type-Options", "nosniff")
	return c.Send(installer.PinShell(h.latestCLIVersion(c)))
}

func (h *Handlers) latestCLIVersion(c *fiber.Ctx) string {
	return h.cliVersion.Current(c.UserContext())
}

// InstallScriptPS serves the PowerShell install script (GET /install.ps1),
// same contract as InstallScript.
func (h *Handlers) InstallScriptPS(c *fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, "text/x-powershell; charset=utf-8")
	c.Set("X-Content-Type-Options", "nosniff")
	return c.Send(installer.PinPowerShell(h.latestCLIVersion(c)))
}

func serveMarkdown(c *fiber.Ctx, doc []byte) error {
	c.Set(fiber.HeaderContentType, "text/markdown; charset=utf-8")
	c.Set("X-Content-Type-Options", "nosniff")
	return c.Send(doc)
}

// Assets serves the SPA's hashed static assets (GET /assets/*) from the
// embedded dist FS. Filenames are content-hashed, so responses are immutable
// and cached for a year. Paths are cleaned and confined to dist/assets — any
// traversal (or a miss) is an indistinguishable 404. No app CSP here: assets
// are subresources, not documents.
func (h *Handlers) Assets(c *fiber.Ctx) error {
	rel := c.Params("*")
	// Reject traversal: clean and confine to the assets subtree.
	clean := path.Clean("/" + rel)
	if strings.Contains(rel, "..") || clean == "/" {
		return apperr.NotFound("not found")
	}
	data, err := web.DistFS.ReadFile("dist/assets" + clean)
	if err != nil {
		return apperr.NotFound("not found")
	}
	c.Set(fiber.HeaderContentType, assetContentType(clean))
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Cache-Control", "public, max-age=31536000, immutable")
	return c.Send(data)
}

// assetContentType maps a hashed asset's extension to its Content-Type.
func assetContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".woff2":
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}
