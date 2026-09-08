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
	placardskill "github.com/Xm798/placard/skills/placard"
)

func publishTitled(t *testing.T, app *fiber.App, html string) publishResp {
	t.Helper()
	code, body := doJSON(t, app, "POST", "/api/publish", `{"html":"`+html+`"}`)
	if code != fiber.StatusOK {
		t.Fatalf("publish = %d %s", code, body)
	}
	return decodePublish(t, body)
}

func TestListVersionsOwnerOnly(t *testing.T) {
	app, deps := newTestApp(t)
	pr := publishTitled(t, app, "<html><head><title>T1</title></head><body>one</body></html>")
	if code, body := republish(t, app, pr.ID, "<html><head><title>T2</title></head><body>two</body></html>", ""); code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, body)
	}
	code, body := doJSON(t, app, "GET", "/api/files/"+pr.ID+"/versions", "")
	if code != fiber.StatusOK {
		t.Fatalf("versions = %d %s", code, body)
	}
	var response struct {
		LatestVersion int `json:"latest_version"`
		SharedVersion int `json:"shared_version"`
		Versions      []struct {
			Version    int    `json:"version"`
			Title      string `json:"title"`
			SizeBytes  int64  `json:"size_bytes"`
			CreateTime string `json:"create_time"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if response.LatestVersion != 2 || response.SharedVersion != 0 || len(response.Versions) != 2 || response.Versions[0].Version != 2 || response.Versions[1].Version != 1 {
		t.Fatalf("versions = %+v", response)
	}
	if response.Versions[0].Title != "T2" || response.Versions[0].SizeBytes == 0 || response.Versions[0].CreateTime == "" {
		t.Fatalf("item fields wrong: %+v", response.Versions[0])
	}
	for _, leak := range []string{"object_key", "content_hash", "create_user", "nano_id"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("versions response leaks %q: %s", leak, body)
		}
	}
	seedForeignFile(t, deps, "vforeig1", "u_bob")
	if code, _ := doJSON(t, app, "GET", "/api/files/vforeig1/versions", ""); code != fiber.StatusNotFound {
		t.Fatalf("non-owner code = %d", code)
	}
	if code, _ := doJSON(t, app, "GET", "/api/files/nothere1/versions", ""); code != fiber.StatusNotFound {
		t.Fatalf("unknown code = %d", code)
	}
}

func TestPatchSharedVersionPinUnpin(t *testing.T) {
	app, deps := newTestApp(t)
	pr := publishTitled(t, app, "<html><head><title>T1</title></head><body>one</body></html>")
	if code, body := republish(t, app, pr.ID, "<html><head><title>T2</title></head><body>two</body></html>", ""); code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, body)
	}
	if code, _ := doJSON(t, app, "PATCH", "/api/files/"+pr.ID, `{"shared_version":1}`); code != fiber.StatusNoContent {
		t.Fatalf("pin = %d", code)
	}
	var file model.File
	if err := deps.DB.Where("nano_id = ?", pr.ID).First(&file).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	var v1 model.FileVersion
	if err := deps.DB.Where("nano_id = ? AND version = 1", pr.ID).First(&v1).Error; err != nil {
		t.Fatalf("read v1: %v", err)
	}
	if file.SharedVersion != 1 || file.ObjectKey != v1.ObjectKey || file.Title != "T1" {
		t.Fatalf("pin did not refresh serving cache: %+v (v1 object_key=%q)", file, v1.ObjectKey)
	}
	assertServingCache(t, deps, pr.ID)
	var audit model.AuditLog
	if err := deps.DB.Where("action = ? AND file_nano_id = ?", "file.pin_version", pr.ID).First(&audit).Error; err != nil || !strings.Contains(audit.Details, `"shared_version":1`) {
		t.Fatalf("pin audit = %+v, err = %v", audit, err)
	}
	for _, body := range []string{`{"shared_version":9}`, `{"shared_version":-1}`, `{}`} {
		if code, _ := doJSON(t, app, "PATCH", "/api/files/"+pr.ID, body); code != fiber.StatusBadRequest {
			t.Fatalf("invalid body %s = %d", body, code)
		}
	}
	if code, _ := doJSON(t, app, "PATCH", "/api/files/"+pr.ID, `{"shared_version":0}`); code != fiber.StatusNoContent {
		t.Fatalf("unpin = %d", code)
	}
	if err := deps.DB.Where("nano_id = ?", pr.ID).First(&file).Error; err != nil {
		t.Fatalf("re-read file: %v", err)
	}
	var v2 model.FileVersion
	if err := deps.DB.Where("nano_id = ? AND version = 2", pr.ID).First(&v2).Error; err != nil {
		t.Fatalf("read v2: %v", err)
	}
	if file.SharedVersion != 0 || file.ObjectKey != v2.ObjectKey {
		t.Fatalf("unpin wrong: %+v (v2 object_key=%q)", file, v2.ObjectKey)
	}
	assertServingCache(t, deps, pr.ID)
	seedForeignFile(t, deps, "pforeig1", "u_bob")
	if code, _ := doJSON(t, app, "PATCH", "/api/files/pforeig1", `{"shared_version":0}`); code != fiber.StatusNotFound {
		t.Fatalf("non-owner = %d", code)
	}
}

func TestRestoreCreatesNewHeadVersion(t *testing.T) {
	app, deps := newTestApp(t)
	pr := publishTitled(t, app, "<html><head><title>T1</title></head><body>one</body></html>")
	if code, body := republish(t, app, pr.ID, "<html><head><title>T2</title></head><body>two</body></html>", ""); code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, body)
	}
	code, body := doJSON(t, app, "POST", "/api/files/"+pr.ID+"/versions/1/restore", "")
	if code != fiber.StatusOK {
		t.Fatalf("restore = %d %s", code, body)
	}
	r3 := decodePublish(t, body)
	if r3.Version != 3 || r3.Title != "T1" || r3.ID != pr.ID {
		t.Fatalf("restore resp = %+v", r3)
	}
	if r3.SkillVersion != placardskill.SkillVersion {
		t.Fatalf("skill_version = %d, want %d", r3.SkillVersion, placardskill.SkillVersion)
	}
	var v3 model.FileVersion
	if err := deps.DB.Where("nano_id = ? AND version = 3", pr.ID).First(&v3).Error; err != nil {
		t.Fatalf("v3 row missing: %v", err)
	}
	rc, err := deps.Storage.GetObject(context.Background(), v3.ObjectKey)
	if err != nil {
		t.Fatalf("get v3 object: %v", err)
	}
	content, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !strings.Contains(string(content), "one") {
		t.Fatalf("v3 content = %q", content)
	}
	assertServingCache(t, deps, pr.ID)
	var audit model.AuditLog
	if err := deps.DB.Where("action = ? AND file_nano_id = ?", "file.restore", pr.ID).First(&audit).Error; err != nil {
		t.Fatalf("restore audit missing: %v", err)
	}
	code, body = doJSON(t, app, "POST", "/api/files/"+pr.ID+"/versions/1/restore", "")
	if code != fiber.StatusOK || decodePublish(t, body).Version != 3 {
		t.Fatalf("idempotent restore = %d %s", code, body)
	}
	if code, _ := doJSON(t, app, "POST", "/api/files/"+pr.ID+"/versions/99/restore", ""); code != fiber.StatusNotFound {
		t.Fatalf("unknown version = %d", code)
	}
	seedForeignFile(t, deps, "rforeig1", "u_bob")
	if code, _ := doJSON(t, app, "POST", "/api/files/rforeig1/versions/1/restore", ""); code != fiber.StatusNotFound {
		t.Fatalf("non-owner = %d", code)
	}
}

// TestRestoreExpiredIs404 asserts restoring an expired-but-not-yet-soft-deleted
// page 404s (final-review hardening) — restore, like republish, must not
// resurrect a page whose lifetime has already elapsed.
func TestRestoreExpiredIs404(t *testing.T) {
	app, deps := newTestApp(t)
	pr := publishTitled(t, app, "<html><head><title>T1</title></head><body>one</body></html>")
	if code, body := republish(t, app, pr.ID, "<html><head><title>T2</title></head><body>two</body></html>", ""); code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, body)
	}
	if err := deps.DB.Model(&model.File{}).
		Where("nano_id = ?", pr.ID).
		Update("expires_at", model.Timestamp(time.Now().Add(-time.Hour))).Error; err != nil {
		t.Fatalf("expire file: %v", err)
	}
	code, _ := doJSON(t, app, "POST", "/api/files/"+pr.ID+"/versions/1/restore", "")
	if code != fiber.StatusNotFound {
		t.Fatalf("restore expired code = %d, want 404", code)
	}
}

// TestRestoreOnPinnedPageKeepsServingCache: restoring while shared_version is
// pinned must still advance latest_version (a new head version is created),
// but the serving cache stays on the pinned row — restore is a publish-path
// mutation and AdvanceHead only refreshes object_key/size_bytes/title when
// shared_version = 0 (see repo.AdvanceHead).
func TestRestoreOnPinnedPageKeepsServingCache(t *testing.T) {
	app, deps := newTestApp(t)
	pr := publishTitled(t, app, "<html><head><title>T1</title></head><body>one</body></html>")
	if code, body := republish(t, app, pr.ID, "<html><head><title>T2</title></head><body>two</body></html>", ""); code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, body)
	}
	// Pin to the current latest (v2), then restore v1's (distinct) content —
	// restoring v2 itself would be a content-hash idempotent no-op (Task 3),
	// so v1 is the only choice that forces a genuinely new head version.
	if code, _ := doJSON(t, app, "PATCH", "/api/files/"+pr.ID, `{"shared_version":2}`); code != fiber.StatusNoContent {
		t.Fatalf("pin = %d", code)
	}
	var v2 model.FileVersion
	if err := deps.DB.Where("nano_id = ? AND version = 2", pr.ID).First(&v2).Error; err != nil {
		t.Fatalf("read v2: %v", err)
	}

	code, body := doJSON(t, app, "POST", "/api/files/"+pr.ID+"/versions/1/restore", "")
	if code != fiber.StatusOK {
		t.Fatalf("restore = %d %s", code, body)
	}
	r3 := decodePublish(t, body)
	if r3.Version != 3 {
		t.Fatalf("restore version = %d, want 3 (new head, even while pinned)", r3.Version)
	}

	var file model.File
	if err := deps.DB.Where("nano_id = ?", pr.ID).First(&file).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	if file.LatestVersion != 3 {
		t.Fatalf("latest_version = %d, want 3", file.LatestVersion)
	}
	if file.SharedVersion != 2 || file.ObjectKey != v2.ObjectKey {
		t.Fatalf("serving cache moved off pinned v2: %+v (v2 object_key=%q)", file, v2.ObjectKey)
	}
	assertServingCache(t, deps, pr.ID)
}

func TestFileListCarriesVersionPointers(t *testing.T) {
	app, _ := newTestApp(t)
	pr := publishTitled(t, app, "<html><head><title>T1</title></head><body>one</body></html>")
	if code, body := republish(t, app, pr.ID, "<html><head><title>T2</title></head><body>two</body></html>", ""); code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, body)
	}
	if code, _ := doJSON(t, app, "PATCH", "/api/files/"+pr.ID, `{"shared_version":1}`); code != fiber.StatusNoContent {
		t.Fatalf("pin failed")
	}
	code, body := doJSON(t, app, "GET", "/api/files", "")
	if code != fiber.StatusOK {
		t.Fatalf("list = %d %s", code, body)
	}
	var response struct {
		Files []struct {
			ID            string `json:"id"`
			LatestVersion int    `json:"latest_version"`
			SharedVersion int    `json:"shared_version"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(response.Files) != 1 || response.Files[0].LatestVersion != 2 || response.Files[0].SharedVersion != 1 {
		t.Fatalf("list = %+v", response.Files)
	}
}
