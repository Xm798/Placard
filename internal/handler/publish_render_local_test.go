package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/storage"
)

// TestPublishMetaRenderOverLocalStorage drives publish → meta → render against
// the on-disk backend rather than the in-memory stub: the bytes a self-hoster
// gets back must be the bytes they published, served as HTML from the same
// origin. It is also the one test that proves the object really left the
// process — the assertion on the file under the configured directory fails if
// publishing ever falls back to an in-memory store.
func TestPublishMetaRenderOverLocalStorage(t *testing.T) {
	dir := t.TempDir()
	objectStore, err := storage.NewLocalClient(config.LocalStorageConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewLocalClient: %v", err)
	}
	app, deps, _ := newTestAppStorage(t, objectStore)

	const body = `<!DOCTYPE html><html><head><title>Local Title</title></head><body>local storage page</body></html>`
	payload, err := json.Marshal(map[string]string{"html": body, "title": "Local Title", "expiry": "7d"})
	if err != nil {
		t.Fatalf("marshal publish body: %v", err)
	}
	code, respBody := doJSON(t, app, "POST", "/api/publish", string(payload))
	if code != fiber.StatusOK {
		t.Fatalf("publish status = %d, body = %s", code, respBody)
	}
	var pr struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &pr); err != nil {
		t.Fatalf("unmarshal publish resp: %v", err)
	}

	if code, b := doJSON(t, app, "GET", "/s/"+pr.ID+"/meta", ""); code != fiber.StatusOK {
		t.Fatalf("meta status = %d, body = %s", code, b)
	}

	req := httptest.NewRequest("GET", "/s/"+pr.ID+"/render", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("render request: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("render status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get(fiber.HeaderContentType); ct != htmlContentType {
		t.Errorf("render Content-Type = %q, want %q", ct, htmlContentType)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read render body: %v", err)
	}
	// The render proxy appends the link-relay script (withLinkRelay), so the
	// published document is the prefix of what is streamed back.
	if !strings.HasPrefix(string(got), body) {
		t.Errorf("rendered body = %q, want it to start with %q", got, body)
	}

	// The object key is a relative path under the configured directory, so the
	// published bytes are readable straight off the filesystem.
	file, err := deps.Files.GetActiveByNanoID(context.Background(), pr.ID)
	if err != nil {
		t.Fatalf("load file row: %v", err)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file.ObjectKey)))
	if err != nil {
		t.Fatalf("read stored object %q: %v", file.ObjectKey, err)
	}
	if string(onDisk) != body {
		t.Errorf("stored object = %q, want %q", onDisk, body)
	}

	// No temporary file is left behind by the write-then-rename put.
	entries, err := os.ReadDir(filepath.Dir(filepath.Join(dir, filepath.FromSlash(file.ObjectKey))))
	if err != nil {
		t.Fatalf("list object dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %q in object dir", e.Name())
		}
	}
}
