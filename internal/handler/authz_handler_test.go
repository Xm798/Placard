package handler

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/model"
)

// seedForeignFileVisibility is seedForeignFile plus an explicit visibility, so
// meta/render authz tests can seed files owned by someone other than the fixed
// test identity (testAuthzID).
func seedForeignFileVisibility(t *testing.T, deps Deps, nanoID, owner, visibility string) {
	t.Helper()
	h := strings.Repeat("ef", 32)
	f := &model.File{NanoID: nanoID, Title: "not yours", ObjectKey: "2026/07/" + nanoID + "-1.html",
		SizeBytes: 2, LatestVersion: 1, Visibility: visibility, ExpiresAt: neverSentinel,
		CreateUser: owner, UpdateUser: owner}
	if err := deps.DB.Create(f).Error; err != nil {
		t.Fatalf("seed foreign file: %v", err)
	}
	v := &model.FileVersion{NanoID: nanoID, Version: 1, ObjectKey: f.ObjectKey, SizeBytes: 2,
		ContentHash: h, Title: f.Title, CreateUser: owner}
	if err := deps.DB.Create(v).Error; err != nil {
		t.Fatalf("seed foreign version: %v", err)
	}
	if err := deps.Storage.PutObject(context.Background(), f.ObjectKey, strings.NewReader("<html><body>secret</body></html>"), htmlContentType); err != nil {
		t.Fatalf("seed foreign object: %v", err)
	}
}

func viewCountFor(t *testing.T, deps Deps, nanoID string) int64 {
	t.Helper()
	var n int64
	deps.DB.Model(&model.View{}).Where("file_nano_id = ?", nanoID).Count(&n)
	return n
}

func deniedAuditCountFor(t *testing.T, deps Deps, nanoID string) int64 {
	t.Helper()
	var n int64
	deps.DB.Model(&model.AuditLog{}).
		Where("action = ? AND outcome = ? AND file_nano_id = ?", "file.view_denied", "denied", nanoID).
		Count(&n)
	return n
}

func viewAuditCountFor(t *testing.T, deps Deps, nanoID string) int64 {
	t.Helper()
	var n int64
	deps.DB.Model(&model.AuditLog{}).Where("action = ? AND file_nano_id = ?", "file.view", nanoID).Count(&n)
	return n
}

// TestMetaPrivateNonOwnerIsExpired asserts a private file's meta is the same
// {expired:true} a missing id returns, so a non-owner cannot tell the page
// apart from one that was never published. The denial is still audited, and
// still records no view.
func TestMetaPrivateNonOwnerIsExpired(t *testing.T) {
	app, deps := newTestApp(t)
	seedForeignFileVisibility(t, deps, "mpriv001", "u_bob", model.VisibilityPrivate)

	code, b := doJSON(t, app, "GET", "/s/mpriv001/meta", "")
	if code != fiber.StatusOK {
		t.Fatalf("meta status = %d, want 200 (the miss body is a 200)", code)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["expired"] != true {
		t.Fatalf("meta body = %s, want {expired:true}", b)
	}
	for _, banned := range []string{"render_url", "title", "forbidden", "code", "message"} {
		if _, ok := m[banned]; ok {
			t.Errorf("denied meta leaked field %q: %s", banned, b)
		}
	}
	if n := deniedAuditCountFor(t, deps, "mpriv001"); n != 1 {
		t.Errorf("file.view_denied audit count = %d, want 1", n)
	}
	if n := viewCountFor(t, deps, "mpriv001"); n != 0 {
		t.Errorf("view rows = %d, want 0 on denial", n)
	}
}

// TestRenderPrivateNonOwnerIs404 asserts render on a private file for a
// non-owner is the same 404 a missing id gets — never a 403, which would
// confirm the page exists — and that no view/storage read happens.
func TestRenderPrivateNonOwnerIs404(t *testing.T) {
	app, deps := newTestApp(t)
	seedForeignFileVisibility(t, deps, "rpriv001", "u_bob", model.VisibilityPrivate)

	resp := renderIframe(t, app, "rpriv001")
	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("render status = %d, want 404", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"code":"not_found"`) {
		t.Errorf("render 404 body missing not_found code: %s", b)
	}
	if n := viewCountFor(t, deps, "rpriv001"); n != 0 {
		t.Errorf("view rows = %d, want 0 on denial", n)
	}
	if n := viewAuditCountFor(t, deps, "rpriv001"); n != 0 {
		t.Errorf("file.view audits = %d, want 0 on denial", n)
	}
	if n := deniedAuditCountFor(t, deps, "rpriv001"); n != 1 {
		t.Errorf("file.view_denied audit count = %d, want 1", n)
	}
}

// TestMetaExpiredSkipsAuthz asserts an expired file's meta returns
// {expired:true} without consulting authz at all — the GetActiveByNanoID miss
// check runs, and must keep running, BEFORE authorizeViewOrAudit, so an
// expired page never writes a denial audit.
func TestMetaExpiredSkipsAuthz(t *testing.T) {
	app, deps := newTestApp(t)
	seedForeignFileVisibility(t, deps, "mexp0001", "u_bob", model.VisibilityPrivate)
	if err := deps.DB.Model(&model.File{}).Where("nano_id = ?", "mexp0001").
		Update("expires_at", model.Timestamp(time.Now().Add(-time.Hour))).Error; err != nil {
		t.Fatalf("expire file: %v", err)
	}

	code, b := doJSON(t, app, "GET", "/s/mexp0001/meta", "")
	if code != fiber.StatusOK {
		t.Fatalf("status = %d", code)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["expired"] != true {
		t.Errorf("expired meta = %s, want expired:true", b)
	}
	if n := deniedAuditCountFor(t, deps, "mexp0001"); n != 0 {
		t.Errorf("file.view_denied audit count = %d, want 0 (expiry short-circuits authz)", n)
	}
}

// TestRenderExpiredSkipsAuthz is the render-side analog: expiry/miss is checked
// before authorizeViewOrAudit, so an expired page writes no denial audit.
func TestRenderExpiredSkipsAuthz(t *testing.T) {
	app, deps := newTestApp(t)
	seedForeignFileVisibility(t, deps, "rexp0001", "u_bob", model.VisibilityPrivate)
	if err := deps.DB.Model(&model.File{}).Where("nano_id = ?", "rexp0001").
		Update("expires_at", model.Timestamp(time.Now().Add(-time.Hour))).Error; err != nil {
		t.Fatalf("expire file: %v", err)
	}

	resp := renderIframe(t, app, "rexp0001")
	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("render status = %d, want 404", resp.StatusCode)
	}
	if n := deniedAuditCountFor(t, deps, "rexp0001"); n != 0 {
		t.Errorf("file.view_denied audit count = %d, want 0 (expiry short-circuits authz)", n)
	}
}

// TestMetaOwnerPrivateAllowed is the handler-level counterpart of
// authz_test.go's owner short-circuit: the fixed test identity publishes its
// own file, and an owner viewing their own private page must be allowed.
func TestMetaOwnerPrivateAllowed(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "30d")
	if err := deps.DB.Model(&model.File{}).Where("nano_id = ?", id).
		Update("visibility", model.VisibilityPrivate).Error; err != nil {
		t.Fatalf("set private: %v", err)
	}

	m := metaOf(t, app, "/s/"+id+"/meta")
	if m["expired"] != false {
		t.Fatalf("owner meta on own private file = %v, want allowed", m)
	}
}
