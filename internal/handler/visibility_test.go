package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/model"
)

// seedUserDefaultVisibility creates authzID's user row, then stamps
// default_visibility to v via SetDefaultVisibility — bypassing
// model.ValidVisibility so tests can also exercise an out-of-domain stored
// value (the fallback-to-link branch).
func seedUserDefaultVisibility(t *testing.T, deps Deps, authzID, v string) {
	t.Helper()
	seedUserRow(t, deps.DB, model.User{ID: authzID, DisplayName: "x"})
	if _, err := deps.Users.SetDefaultVisibility(context.Background(), authzID, v); err != nil {
		t.Fatalf("set default visibility: %v", err)
	}
}

func fileVisibility(t *testing.T, deps Deps, nanoID string) string {
	t.Helper()
	var f model.File
	if err := deps.DB.Where("nano_id = ?", nanoID).First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	return f.Visibility
}

// publishWithVisibility POSTs /api/publish (JSON channel) with an explicit
// visibility field and returns the (status, nano id).
func publishWithVisibility(t *testing.T, app *fiber.App, visibility string) (int, string) {
	t.Helper()
	body := `{"html":"<html><body>hi</body></html>","title":"t","expiry":"30d","visibility":"` + visibility + `"}`
	code, b := doJSON(t, app, "POST", "/api/publish", body)
	if code != fiber.StatusOK {
		return code, ""
	}
	return code, decodePublish(t, b).ID
}

// --- resolveVisibility: param wins ---

func TestPublishVisibilityParamWins(t *testing.T) {
	app, deps := newTestApp(t)
	// No user row exists — the fallback would be "link" — yet the explicit
	// param must win outright.
	code, id := publishWithVisibility(t, app, model.VisibilityPrivate)
	if code != fiber.StatusOK {
		t.Fatalf("publish = %d", code)
	}
	if v := fileVisibility(t, deps, id); v != model.VisibilityPrivate {
		t.Fatalf("visibility = %q, want private", v)
	}
}

func TestPublishVisibilityInvalidParamIs400(t *testing.T) {
	app, _ := newTestApp(t)
	code, _ := publishWithVisibility(t, app, "bogus")
	if code != fiber.StatusBadRequest {
		t.Fatalf("code = %d, want 400", code)
	}
}

// "restricted" was a defined value before the roster feature was removed; it
// must now be rejected like any other unknown string, on both channels.
func TestPublishRestrictedIs400(t *testing.T) {
	app, _ := newTestApp(t)
	code, _ := publishWithVisibility(t, app, "restricted")
	if code != fiber.StatusBadRequest {
		t.Fatalf("code = %d, want 400", code)
	}
}

// --- multipart channel carries the same param ---

func TestPublishMultipartVisibilityParam(t *testing.T) {
	app, deps := newTestApp(t)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "page.html")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write([]byte("<html><body>multipart</body></html>")); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_ = w.WriteField("title", "mp title")
	_ = w.WriteField("expiry", "30d")
	_ = w.WriteField("visibility", model.VisibilityPrivate)
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/publish", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var pr publishResp
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v := fileVisibility(t, deps, pr.ID); v != model.VisibilityPrivate {
		t.Fatalf("visibility = %q, want private", v)
	}
}

// --- default resolution from user.default_visibility ---

func TestPublishVisibilityDefaultFromUser(t *testing.T) {
	app, deps := newTestApp(t)
	seedUserDefaultVisibility(t, deps, testAuthzID, model.VisibilityPrivate)

	code, id := publishWithVisibilityOmitted(t, app)
	if code != fiber.StatusOK {
		t.Fatalf("publish = %d", code)
	}
	if v := fileVisibility(t, deps, id); v != model.VisibilityPrivate {
		t.Fatalf("visibility = %q, want private (from user default)", v)
	}
}

// publishWithVisibilityOmitted publishes with no visibility field at all
// (distinct from an empty-string field, though the handler treats both the
// same via TrimSpace) and returns (status, nano id).
func publishWithVisibilityOmitted(t *testing.T, app *fiber.App) (int, string) {
	t.Helper()
	code, b := doJSON(t, app, "POST", "/api/publish",
		`{"html":"<html><body>hi</body></html>","title":"t","expiry":"30d"}`)
	if code != fiber.StatusOK {
		return code, ""
	}
	return code, decodePublish(t, b).ID
}

func TestPublishVisibilityFallbackLinkWhenUserRowMissing(t *testing.T) {
	app, deps := newTestApp(t)
	code, id := publishWithVisibilityOmitted(t, app)
	if code != fiber.StatusOK {
		t.Fatalf("publish = %d, want 200 (fallback must never 500)", code)
	}
	if v := fileVisibility(t, deps, id); v != model.VisibilityLink {
		t.Fatalf("visibility = %q, want link (fallback)", v)
	}
}

// TestPublishVisibilityFallbackLinkWhenUserDefaultOutOfDomain covers a stored
// default_visibility that is neither private nor link (should never happen
// through the validated PUT /api/prefs path, but resolveVisibility must not
// trust the column blindly) — still falls back to link.
func TestPublishVisibilityFallbackLinkWhenUserDefaultOutOfDomain(t *testing.T) {
	app, deps := newTestApp(t)
	seedUserDefaultVisibility(t, deps, testAuthzID, "restricted")

	code, id := publishWithVisibilityOmitted(t, app)
	if code != fiber.StatusOK {
		t.Fatalf("publish = %d", code)
	}
	if v := fileVisibility(t, deps, id); v != model.VisibilityLink {
		t.Fatalf("visibility = %q, want link (out-of-domain default never honored)", v)
	}
}

// --- republish/restore never touch visibility ---

func TestRepublishDoesNotChangeVisibility(t *testing.T) {
	app, deps := newTestApp(t)
	_, id := publishWithVisibility(t, app, model.VisibilityPrivate)

	if code, b := republish(t, app, id, "<html><body>v2</body></html>", ""); code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}
	if v := fileVisibility(t, deps, id); v != model.VisibilityPrivate {
		t.Fatalf("visibility after republish = %q, want private (unchanged)", v)
	}
}

func TestRestoreDoesNotChangeVisibility(t *testing.T) {
	app, deps := newTestApp(t)
	_, id := publishWithVisibility(t, app, model.VisibilityPrivate)
	if code, b := republish(t, app, id, "<html><body>v2</body></html>", ""); code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}
	if code, b := doJSON(t, app, "POST", "/api/files/"+id+"/versions/1/restore", ""); code != fiber.StatusOK {
		t.Fatalf("restore = %d %s", code, b)
	}
	if v := fileVisibility(t, deps, id); v != model.VisibilityPrivate {
		t.Fatalf("visibility after restore = %q, want private (unchanged)", v)
	}
}

// --- PatchFile: multi-field refactor ---

func TestPatchVisibilityOnly(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "30d") // default visibility: link (no user row)

	if code, b := doJSON(t, app, "PATCH", "/api/files/"+id, `{"visibility":"private"}`); code != fiber.StatusNoContent {
		t.Fatalf("patch = %d %s", code, b)
	}
	if v := fileVisibility(t, deps, id); v != model.VisibilityPrivate {
		t.Fatalf("visibility = %q, want private", v)
	}
	var audit model.AuditLog
	if err := deps.DB.Where("action = ? AND file_nano_id = ?", "file.visibility_change", id).First(&audit).Error; err != nil {
		t.Fatalf("visibility_change audit missing: %v", err)
	}
	if audit.Details != `{"old":"link","new":"private"}` {
		t.Fatalf("audit details = %q", audit.Details)
	}
}

func TestPatchSharedVersionAndVisibilityTogether(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "30d")
	if code, b := republish(t, app, id, "<html><head><title>T2</title></head><body>two</body></html>", ""); code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}

	code, b := doJSON(t, app, "PATCH", "/api/files/"+id, `{"shared_version":1,"visibility":"private"}`)
	if code != fiber.StatusNoContent {
		t.Fatalf("patch = %d %s", code, b)
	}

	var f model.File
	if err := deps.DB.Where("nano_id = ?", id).First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	if f.SharedVersion != 1 || f.Visibility != model.VisibilityPrivate {
		t.Fatalf("file = %+v, want shared_version 1 / visibility private", f)
	}
	assertServingCache(t, deps, id)

	var pinAudit, visAudit model.AuditLog
	if err := deps.DB.Where("action = ? AND file_nano_id = ?", "file.pin_version", id).First(&pinAudit).Error; err != nil {
		t.Fatalf("pin audit missing: %v", err)
	}
	if err := deps.DB.Where("action = ? AND file_nano_id = ?", "file.visibility_change", id).First(&visAudit).Error; err != nil {
		t.Fatalf("visibility_change audit missing: %v", err)
	}
}

func TestPatchNoFieldsIs400(t *testing.T) {
	app, _ := newTestApp(t)
	id := publishOne(t, app, "30d")
	for _, body := range []string{`{}`, `{"shared_version":null,"visibility":null}`} {
		if code, b := doJSON(t, app, "PATCH", "/api/files/"+id, body); code != fiber.StatusBadRequest {
			t.Fatalf("body %s = %d %s, want 400", body, code, b)
		}
	}
}

func TestPatchInvalidVisibilityIs400(t *testing.T) {
	app, _ := newTestApp(t)
	id := publishOne(t, app, "30d")
	if code, _ := doJSON(t, app, "PATCH", "/api/files/"+id, `{"visibility":"bogus"}`); code != fiber.StatusBadRequest {
		t.Fatalf("code = %d, want 400", code)
	}
}

func TestPatchRestrictedIs400(t *testing.T) {
	app, _ := newTestApp(t)
	id := publishOne(t, app, "30d")
	if code, _ := doJSON(t, app, "PATCH", "/api/files/"+id, `{"visibility":"restricted"}`); code != fiber.StatusBadRequest {
		t.Fatalf("code = %d, want 400", code)
	}
}

// TestPatchVisibilityNoAuditWhenUnchanged asserts setting visibility to its
// current value is a silent no-op audit-wise — only an actual change writes
// file.visibility_change.
func TestPatchVisibilityNoAuditWhenUnchanged(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "30d") // default: link

	if code, b := doJSON(t, app, "PATCH", "/api/files/"+id, `{"visibility":"link"}`); code != fiber.StatusNoContent {
		t.Fatalf("patch = %d %s", code, b)
	}
	var n int64
	deps.DB.Model(&model.AuditLog{}).
		Where("action = ? AND file_nano_id = ?", "file.visibility_change", id).Count(&n)
	if n != 0 {
		t.Fatalf("visibility_change audit count = %d, want 0 (value unchanged)", n)
	}
}

func TestPatchVisibilityNonOwnerIs404(t *testing.T) {
	app, deps := newTestApp(t)
	seedForeignFile(t, deps, "pvforeig", "u_bob")
	if code, _ := doJSON(t, app, "PATCH", "/api/files/pvforeig", `{"visibility":"private"}`); code != fiber.StatusNotFound {
		t.Fatalf("code = %d, want 404", code)
	}
}

// --- List DTO exposes visibility ---

func TestFileListIncludesVisibility(t *testing.T) {
	app, _ := newTestApp(t)
	_, idPriv := publishWithVisibility(t, app, model.VisibilityPrivate)
	_, idLink := publishWithVisibility(t, app, model.VisibilityLink)

	code, b := doJSON(t, app, "GET", "/api/files", "")
	if code != fiber.StatusOK {
		t.Fatalf("list = %d %s", code, b)
	}
	var resp struct {
		Files []struct {
			ID         string `json:"id"`
			Visibility string `json:"visibility"`
		} `json:"files"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := map[string]string{}
	for _, f := range resp.Files {
		got[f.ID] = f.Visibility
	}
	if got[idPriv] != model.VisibilityPrivate {
		t.Errorf("visibility[%s] = %q, want private", idPriv, got[idPriv])
	}
	if got[idLink] != model.VisibilityLink {
		t.Errorf("visibility[%s] = %q, want link", idLink, got[idLink])
	}
}
