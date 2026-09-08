package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/htmlmeta"
	"github.com/Xm798/placard/internal/idgen"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/userctx"
	placardskill "github.com/Xm798/placard/skills/placard"
)

// publishRequest is the JSON body form of POST /api/publish. ID, when set,
// switches to the update-in-place path (a new version behind the same link).
type publishRequest struct {
	ID         string `json:"id"`
	HTML       string `json:"html"`
	Title      string `json:"title"`
	Expiry     string `json:"expiry"`
	Visibility string `json:"visibility"`
	Password   string `json:"password"`
}

// passwordAuto is the only accepted value of the publish password field: the
// server mints the code, a caller never supplies one. There is no request shape
// in which six digits a person picked are safer than six random ones.
const passwordAuto = "auto"

// Publish handles POST /api/publish (JSON or multipart). Without id: read+
// validate HTML (server-side 10MB + type check, never trusting the frontend) →
// nanoid (uk_nano_id conflict retry ≤3) → storage PutObject versioned key → commit
// File + its v1 version row in one tx. With id: owner-scoped re-publish — a
// new version behind the SAME link via publishNewVersion (CAS head advance;
// expiry, visibility and password are ignored, the page keeps its lifetime and
// its access settings). password="auto" on a NEW page mints a share code in the
// same transaction and returns the plaintext once. On failure best-effort
// enqueue PendingObjectDelete OUTSIDE the failing tx. Audits file.publish /
// file.republish (actor=AuthzID, IP=ClientIP).
func (h *Handlers) Publish(c *fiber.Ctx) error {
	authzid := userctx.AuthzID(c)
	if authzid == "" {
		return apperr.Unauthorized()
	}

	var (
		body       []byte
		id         string
		title      string
		expiry     string
		visibility string
		password   string
		declaredCT string
		filename   string
	)

	maxFileSize := h.maxUploadBytes(c.UserContext())

	if isMultipart(c) {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			return apperr.Validation("file is required")
		}
		// Server-side 10MB enforcement before reading the body.
		if fileHeader.Size > maxFileSize {
			return apperr.Validation("file too large")
		}
		f, err := fileHeader.Open()
		if err != nil {
			return apperr.Validation("cannot read file")
		}
		defer f.Close()
		body, err = io.ReadAll(io.LimitReader(f, maxFileSize+1))
		if err != nil {
			return apperr.Validation("cannot read file")
		}
		if int64(len(body)) > maxFileSize {
			return apperr.Validation("file too large")
		}
		declaredCT = fileHeader.Header.Get("Content-Type")
		filename = filepath.Base(fileHeader.Filename)
		id = c.FormValue("id")
		title = c.FormValue("title")
		expiry = c.FormValue("expiry")
		visibility = c.FormValue("visibility")
		password = c.FormValue("password")
	} else {
		var req publishRequest
		if err := json.Unmarshal(c.Body(), &req); err != nil {
			return apperr.Validation("invalid json body")
		}
		body = []byte(req.HTML)
		if int64(len(body)) > maxFileSize {
			return apperr.Validation("file too large")
		}
		// No trusted content type on the JSON channel — rely on body sniffing
		// (the server never trusts a frontend-declared type).
		declaredCT = ""
		id = req.ID
		title = req.Title
		expiry = req.Expiry
		visibility = req.Visibility
		password = req.Password
	}

	// The custom title is optional; when omitted, use the uploaded filename.
	if strings.TrimSpace(title) == "" {
		title = filename
	}

	if len(bytes.TrimSpace(body)) == 0 {
		return apperr.Validation("empty content")
	}
	if !looksLikeHTML(declaredCT, body) {
		return apperr.New("validation", 415, "content is not HTML")
	}
	password = strings.TrimSpace(password)
	if password != "" && password != passwordAuto {
		return apperr.Validation(`password must be "auto"`)
	}

	// Update-in-place: id names an existing page. Owner-scope miss (non-owner
	// or unknown id) is an indistinguishable 404. expiry is deliberately
	// ignored — the page keeps its original lifetime.
	if strings.TrimSpace(id) != "" {
		file, err := h.deps.Files.GetOwned(c.UserContext(), strings.TrimSpace(id), authzid)
		if err != nil {
			return apperr.NotFound("not found")
		}
		// An expired page cannot be republished — it stays a 404, indistinguishable
		// from an owner-scope miss, even though the row is still live (not yet
		// soft-deleted by the cleanup cron).
		if fileExpired(file) {
			return apperr.NotFound("not found")
		}
		return h.publishNewVersion(c, file, body, title, authzid, "file.republish")
	}

	expiresAt, err := parseFileExpiry(expiry)
	if err != nil {
		return apperr.Validation("invalid expiry")
	}

	titleSnap := versionTitle(body, title)
	descSnap := htmlmeta.Description(body)
	sum := sha256.Sum256(body)
	contentHash := hex.EncodeToString(sum[:])

	// Visibility resolution applies ONLY to a brand-new file — republish above
	// already returned, and restore (publishNewVersion) never calls resolveVisibility
	// either, so an existing page's visibility is never touched by either path.
	resolvedVisibility, err := h.resolveVisibility(c.UserContext(), visibility, authzid)
	if err != nil {
		return err
	}

	// Generate nanoid with uk_nano_id collision retry (≤3). Upload to storage first,
	// then commit DB; on any post-upload failure enqueue the orphan for cron.
	var (
		nanoID string
		key    string
	)
	for attempt := 0; attempt < nanoIDMaxRetries; attempt++ {
		nanoID = idgen.Generate(nanoIDLen)
		key = objectKey(nanoID, 1)

		if err := h.deps.Storage.PutObject(c.UserContext(), key, bytes.NewReader(body), htmlContentType); err != nil {
			return apperr.Storage("storage upload failed")
		}

		file := &model.File{
			NanoID:        nanoID,
			Title:         titleSnap,
			Description:   descSnap,
			ObjectKey:     key,
			SizeBytes:     int64(len(body)),
			LatestVersion: 1,
			ExpiresAt:     expiresAt,
			Visibility:    resolvedVisibility,
			CreateUser:    authzid,
			UpdateUser:    authzid,
		}
		// Seal the code onto the row before it is inserted, so the page and its
		// code commit together — a link that is briefly open before the code
		// lands is the one thing --password auto exists to avoid.
		var shareCode string
		if password == passwordAuto {
			minted, merr := h.mintShareCode(file)
			if merr != nil {
				h.enqueueOrphan(c.UserContext(), key, model.ReasonPublishRollback, authzid)
				return apperr.Internal("could not generate a share code")
			}
			shareCode = minted
		}

		err := h.deps.DB.WithContext(c.UserContext()).Transaction(func(tx *gorm.DB) error {
			if e := h.deps.Files.Insert(tx, file); e != nil {
				return e
			}
			return h.deps.Versions.Insert(tx, &model.FileVersion{
				NanoID: nanoID, Version: 1, ObjectKey: key, SizeBytes: int64(len(body)),
				ContentHash: contentHash, Title: titleSnap, Description: descSnap,
				CreateUser: authzid,
			})
		})
		if err == nil {
			h.auditFile(c, authzid, nanoID, "file.publish")
			logStateChange(c, "file.publish", nanoID, ctxlog.OutcomeSuccess)
			resp := h.publishResponse(file.NanoID, file.Title, 1, file.ExpiresAt, file.CreateTime)
			if shareCode != "" {
				h.auditFile(c, authzid, nanoID, "file.share_code_set")
				// The plaintext travels exactly once. Afterwards only the
				// owner's own file list carries it.
				resp.ShareCode = shareCode
			}
			return c.JSON(resp)
		}

		if isUniqueConflict(err) {
			// nanoid collided: orphan the just-uploaded object and retry.
			h.enqueueOrphan(c.UserContext(), key, model.ReasonNanoidConflict, authzid)
			continue
		}

		// DB commit failed for another reason: orphan and abort.
		h.enqueueOrphan(c.UserContext(), key, model.ReasonPublishRollback, authzid)
		return apperr.Internal("publish failed")
	}

	// Exhausted nanoid retries.
	return apperr.Internal("could not allocate id")
}

// validateVisibilityValue validates an explicit (caller-supplied) visibility
// value. Shared by resolveVisibility's param branch and PatchFile so the two
// entry points can never drift.
func (h *Handlers) validateVisibilityValue(v string) error {
	if !model.ValidVisibility(v) {
		return apperr.Validation("invalid visibility")
	}
	return nil
}

// resolveVisibility decides the visibility for a brand-new publish (never
// called for republish/restore, which keep the existing file's visibility
// untouched). Three-tier priority: an explicit non-empty param wins (validated
// via validateVisibilityValue); absent a param, the caller's stored
// user.default_visibility preference wins IF it is in the domain; otherwise —
// no user row, a lookup error, or an out-of-domain stored value — falls back to
// link. The fallback path never surfaces as a 500: a missing Users dep or a DB
// error degrades to link rather than failing the publish.
func (h *Handlers) resolveVisibility(ctx context.Context, param, authzID string) (string, error) {
	param = strings.TrimSpace(param)
	if param != "" {
		if err := h.validateVisibilityValue(param); err != nil {
			return "", err
		}
		return param, nil
	}
	if h.deps.Users == nil {
		return model.VisibilityLink, nil
	}
	u, err := h.deps.Users.Get(ctx, authzID)
	if err != nil {
		return model.VisibilityLink, nil
	}
	if model.ValidVisibility(u.DefaultVisibility) {
		return u.DefaultVisibility, nil
	}
	return model.VisibilityLink, nil
}

// errCASMiss marks a lost publish race: the head moved between the unlocked
// read and the AdvanceHead CAS. Retried with a fresh read, never surfaced.
var errCASMiss = errors.New("publish head CAS miss")

// publishNewVersion appends body as the next version of file. Flow per spec:
// unlocked head read → candidate = latest+1 → storage PutObject BEFORE the DB tx
// (never hold a row lock across an external call) → short tx {INSERT version
// row + CAS AdvanceHead}. uk_nano_version conflict or CAS miss orphans the
// upload (reason version_conflict) and retries on a fresh head read, ≤
// nanoIDMaxRetries. Content identical to the current latest (SHA-256, non-NULL)
// returns idempotently without uploading or writing. auditAction is
// file.republish or file.restore; the version lands in AuditLog.Details JSON.
func (h *Handlers) publishNewVersion(c *fiber.Ctx, file *model.File, body []byte, reqTitle, authzid, auditAction string) error {
	sum := sha256.Sum256(body)
	contentHash := hex.EncodeToString(sum[:])
	fallback := strings.TrimSpace(reqTitle)
	if fallback == "" {
		fallback = file.Title
	}
	titleSnap := versionTitle(body, fallback)
	descSnap := htmlmeta.Description(body)

	for attempt := 0; attempt < nanoIDMaxRetries; attempt++ {
		if attempt > 0 {
			// Lost a race: re-read the head (GetOwned re-checks owner + liveness).
			fresh, err := h.deps.Files.GetOwned(c.UserContext(), file.NanoID, authzid)
			if err != nil {
				return apperr.NotFound("not found")
			}
			file = fresh
		}

		latest, err := h.deps.Versions.GetByVersion(c.UserContext(), file.NanoID, file.LatestVersion)
		if err != nil {
			return apperr.Internal("publish failed")
		}
		if latest.ContentHash != "" && latest.ContentHash == contentHash {
			// Identical to the current latest: idempotent return. Backfilled rows
			// (empty hash) never match.
			return c.JSON(h.publishResponse(file.NanoID, latest.Title, latest.Version, file.ExpiresAt, latest.CreateTime))
		}

		candidate := file.LatestVersion + 1
		key, err := objectVersionKey(file.NanoID, candidate)
		if err != nil {
			return apperr.Internal("publish failed")
		}
		if err := h.deps.Storage.PutObject(c.UserContext(), key, bytes.NewReader(body), htmlContentType); err != nil {
			return apperr.Storage("storage upload failed")
		}

		ver := &model.FileVersion{
			NanoID: file.NanoID, Version: candidate, ObjectKey: key,
			SizeBytes: int64(len(body)), ContentHash: contentHash,
			Title: titleSnap, Description: descSnap, CreateUser: authzid,
		}
		err = h.deps.DB.WithContext(c.UserContext()).Transaction(func(tx *gorm.DB) error {
			if e := h.deps.Versions.Insert(tx, ver); e != nil {
				return e
			}
			n, e := h.deps.Files.AdvanceHead(tx, file.NanoID, candidate, ver, authzid)
			if e != nil {
				return e
			}
			if n != 1 {
				return errCASMiss
			}
			return nil
		})
		if err == nil {
			h.auditFileDetails(c, authzid, file.NanoID, auditAction,
				fmt.Sprintf(`{"version":%d}`, candidate))
			// auditAction is "file.republish" or "file.restore" — the same
			// value the audit row gets, so the two never name it differently.
			logStateChange(c, auditAction, file.NanoID, ctxlog.OutcomeSuccess)
			return c.JSON(h.publishResponse(file.NanoID, ver.Title, candidate, file.ExpiresAt, ver.CreateTime))
		}
		if isUniqueConflict(err) || errors.Is(err, errCASMiss) {
			// Another publish won the slot: orphan the uploaded object and retry
			// on a fresh head read.
			h.enqueueOrphan(c.UserContext(), key, model.ReasonVersionConflict, authzid)
			continue
		}
		h.enqueueOrphan(c.UserContext(), key, model.ReasonPublishRollback, authzid)
		return apperr.Internal("publish failed")
	}
	return apperr.Internal("publish conflict retries exhausted")
}

// publishResponse builds the publish-shaped success body shared by publish
// and restore, so response-shape invariants (including skill_version) live
// in one place.
func (h *Handlers) publishResponse(nanoID, title string, version int, expiresAt, createTime time.Time) dto.PublishResponse {
	return dto.PublishResponse{
		ID:           nanoID,
		URL:          h.shareURL(nanoID),
		Title:        title,
		Version:      version,
		ExpiresAt:    dto.NullableExpiry(expiresAt),
		CreateTime:   createTime,
		SkillVersion: placardskill.SkillVersion,
	}
}

// versionTitle computes the immutable title snapshot stored on a version row
// (and mirrored into the serving cache): the page's own <title> wins — it is
// what a visitor actually sees — falling back to the caller-provided title.
//
// Its description counterpart is htmlmeta.Description called directly: a
// description has no caller-supplied fallback (no API field carries one), and
// the empty result is meaningful — the share shell falls back to the title
// when rendering og:description.
func versionTitle(body []byte, fallback string) string {
	if t := htmlmeta.Title(body); t != "" {
		return t
	}
	return fallback
}

// enqueueOrphan best-effort records an orphaned object for cron reclamation. Failures
// are swallowed: even if this write fails, the cron orphan reconciliation
// (storage object not referenced by DB) reclaims it.
func (h *Handlers) enqueueOrphan(ctx context.Context, key, reason, authzid string) {
	if h.deps.Pending == nil {
		return
	}
	_ = h.deps.Pending.Insert(ctx, key, reason, authzid)
}

// auditFile records a file.* audit entry (best-effort). Shared by the publish,
// view, and delete paths — they differ only in action and the file nano id.
func (h *Handlers) auditFile(c *fiber.Ctx, authzid, nanoID, action string) {
	h.auditFileDetails(c, authzid, nanoID, action, "")
}

// auditFileDetails is auditFile plus a validated-JSON details payload (version
// numbers etc.) — used by the versioning mutations (republish/restore/pin).
// details == "" stores the column's zero value, matching plain auditFile calls.
func (h *Handlers) auditFileDetails(c *fiber.Ctx, authzid, nanoID, action, details string) {
	h.auditBestEffort(c, &model.AuditLog{
		Action:       action,
		Actor:        authzid,
		ActorName:    displayName(c),
		FileNanoID:   nanoID,
		ResourceType: "file",
		ResourceID:   nanoID,
		Details:      details,
	})
}

// shareURL builds the canonical /s/:id share URL on the primary origin
// (Server.BaseURL, trailing slash trimmed).
func (h *Handlers) shareURL(nanoID string) string {
	return strings.TrimRight(h.deps.Cfg.Server.BaseURL, "/") + "/s/" + nanoID
}

// isMultipart reports whether the request is multipart/form-data.
func isMultipart(c *fiber.Ctx) bool {
	return bytes.Contains(bytes.ToLower(c.Request().Header.ContentType()), []byte("multipart/form-data"))
}

// isUniqueConflict reports whether err is a unique-constraint violation. It
// checks both the translated gorm error (requires TranslateError) and the
// driver message, so it works regardless of the scaffold's gorm config. The
// two dialects word it differently — SQLite "UNIQUE constraint failed: ...",
// Postgres "duplicate key value violates unique constraint ...".
func isUniqueConflict(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "unique constraint")
}

// truncateUA bounds the User-Agent length stored in audit (untrusted input).
func truncateUA(ua string) string {
	return truncateString(ua, 255)
}

// displayName returns the request identity's display name snapshot, or "".
func displayName(c *fiber.Ctx) string {
	id, ok := userctx.Get(c)
	if !ok {
		return ""
	}
	return id.DisplayName
}

// maxUploadBytes is the upload cap in force right now: the setting table's
// value, which an admin edits without a restart, falling back to the config
// seed when the table is unreadable or unwired.
//
// fasthttp's BodyLimit is still set from the config value at boot and cannot
// change while the process runs, so it remains a hard outer bound: lowering the
// setting takes effect here immediately, while raising it past the configured
// value needs a restart to matter.
func (h *Handlers) maxUploadBytes(ctx context.Context) int64 {
	configured := h.deps.Cfg.Upload.MaxFileSize
	if h.deps.Settings == nil {
		return configured
	}
	size, err := h.deps.Settings.GetInt64(ctx, model.SettingUploadMaxFileSize, configured)
	if err != nil || size <= 0 {
		return configured
	}
	return size
}
