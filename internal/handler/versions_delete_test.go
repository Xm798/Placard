package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/model"
)

// TestDeleteFileEnqueuesAllVersions: deleting a multi-version page queues every
// version's object key (reason user_delete) atomically with the soft-delete.
func TestDeleteFileEnqueuesAllVersions(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "never")
	if code, b := republish(t, app, id, "<html><head><title>V2</title></head><body>two</body></html>", ""); code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}

	req := httptest.NewRequest("DELETE", "/api/files/"+id, nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", resp.StatusCode)
	}

	var rows []model.PendingObjectDelete
	if err := deps.DB.Order("object_key").Find(&rows).Error; err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("pending rows = %d, want 2 (both version keys)", len(rows))
	}
	for _, r := range rows {
		if r.Reason != model.ReasonUserDelete {
			t.Fatalf("reason = %q, want user_delete", r.Reason)
		}
	}
	var f model.File
	if err := deps.DB.Where("nano_id = ?", id).First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	if f.IsDeleted != 1 {
		t.Fatalf("is_deleted = %d, want 1", f.IsDeleted)
	}
}
