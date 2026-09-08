package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/model"
)

func metaOf(t *testing.T, app *fiber.App, path string) map[string]any {
	t.Helper()
	code, b := doJSON(t, app, "GET", path, "")
	if code != fiber.StatusOK {
		t.Fatalf("meta %s = %d %s", path, code, b)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal meta: %v", err)
	}
	return m
}

// TestMetaTitleFromServingCacheNoStorage: meta serves the title snapshot straight
// from the serving cache — deleting the storage object must not affect it.
func TestMetaTitleFromServingCacheNoStorage(t *testing.T) {
	app, deps := newTestApp(t)
	pr := publishTitled(t, app, "<html><head><title>Snap</title></head><body>x</body></html>")

	var f model.File
	if err := deps.DB.Where("nano_id = ?", pr.ID).First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	_ = deps.Storage.DeleteObject(context.Background(), f.ObjectKey) // meta must not need the object anymore

	m := metaOf(t, app, "/s/"+pr.ID+"/meta")
	if m["title"] != "Snap" {
		t.Fatalf("title = %v, want the stored snapshot", m["title"])
	}
	if m["render_url"] != "/s/"+pr.ID+"/render" {
		t.Fatalf("render_url = %v", m["render_url"])
	}
}

func TestMetaOwnerVersionPreview(t *testing.T) {
	app, _ := newTestApp(t)
	pr := publishTitled(t, app, "<html><head><title>T1</title></head><body>one</body></html>")
	if code, b := republish(t, app, pr.ID, "<html><head><title>T2</title></head><body>two</body></html>", ""); code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}

	m := metaOf(t, app, "/s/"+pr.ID+"/meta?v=1")
	if m["title"] != "T1" || m["render_url"] != "/s/"+pr.ID+"/render?v=1" {
		t.Fatalf("owner preview meta = %v", m)
	}
	// Nonexistent v falls back to the serving version — no 404, no leak.
	m = metaOf(t, app, "/s/"+pr.ID+"/meta?v=99")
	if m["render_url"] != "/s/"+pr.ID+"/render" || m["title"] != "T2" {
		t.Fatalf("invalid-v meta = %v, want serving fallback", m)
	}
}

func TestMetaNonOwnerVersionParamIgnored(t *testing.T) {
	app, deps := newTestApp(t)
	seedForeignFile(t, deps, "mforeig1", "u_bob")
	m := metaOf(t, app, "/s/mforeig1/meta?v=1")
	if m["render_url"] != "/s/mforeig1/render" {
		t.Fatalf("non-owner ?v must be ignored, got %v", m["render_url"])
	}
}

func TestRenderOwnerPreviewServesVersionAndSkipsView(t *testing.T) {
	app, deps := newTestApp(t)
	pr := publishTitled(t, app, "<html><head><title>T1</title></head><body>one</body></html>")
	if code, b := republish(t, app, pr.ID, "<html><head><title>T2</title></head><body>two</body></html>", ""); code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}

	// Owner preview of v1: old content, NO view record, NO file.view audit.
	req := httptest.NewRequest("GET", "/s/"+pr.ID+"/render?v=1", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != fiber.StatusOK || !strings.Contains(string(body), "one") {
		t.Fatalf("preview = %d %q, want v1 content", resp.StatusCode, body)
	}
	var n int64
	deps.DB.Model(&model.View{}).Where("file_nano_id = ?", pr.ID).Count(&n)
	if n != 0 {
		t.Fatalf("view rows = %d, want 0 (owner preview must not count)", n)
	}
	deps.DB.Model(&model.AuditLog{}).Where("action = ? AND file_nano_id = ?", "file.view", pr.ID).Count(&n)
	if n != 0 {
		t.Fatalf("file.view audits = %d, want 0 on preview", n)
	}

	// Plain render still serves the head and records the view.
	req = httptest.NewRequest("GET", "/s/"+pr.ID+"/render", nil)
	resp, err = app.Test(req, -1)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "two") {
		t.Fatalf("plain render = %q, want v2 content", body)
	}
	deps.DB.Model(&model.View{}).Where("file_nano_id = ?", pr.ID).Count(&n)
	if n != 1 {
		t.Fatalf("view rows = %d, want 1 after a normal render", n)
	}
}

func TestRenderNonOwnerVersionParamServesSharedVersion(t *testing.T) {
	app, deps := newTestApp(t)
	seedForeignFile(t, deps, "rforeig2", "u_bob")
	if err := deps.Storage.PutObject(context.Background(), "2026/07/rforeig2-1.html",
		strings.NewReader("<html><body>bobshared</body></html>"), htmlContentType); err != nil {
		t.Fatalf("seed object: %v", err)
	}

	req := httptest.NewRequest("GET", "/s/rforeig2/render?v=1", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != fiber.StatusOK || !strings.Contains(string(body), "bobshared") {
		t.Fatalf("render = %d %q, want the shared serving version", resp.StatusCode, body)
	}
	// Not an owner preview → the view IS recorded.
	var n int64
	deps.DB.Model(&model.View{}).Where("file_nano_id = ?", "rforeig2").Count(&n)
	if n != 1 {
		t.Fatalf("view rows = %d, want 1", n)
	}
}
