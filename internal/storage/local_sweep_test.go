package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xm798/placard/internal/config"
)

// newSweepable builds a local backend and hands back its root so a test can
// plant files the Client API cannot express.
func newSweepable(t *testing.T) (TempSweeper, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := NewLocalClient(config.LocalStorageConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewLocalClient: %v", err)
	}
	sw, ok := c.(TempSweeper)
	if !ok {
		t.Fatal("local backend does not implement TempSweeper")
	}
	return sw, dir
}

// plant writes a file at rel with the given mtime, creating parents.
func plant(t *testing.T, root, rel string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// A temp file a crash stranded is reclaimed; a real object next to it is not.
func TestLocalSweepTemp_RemovesStrandedTempsOnly(t *testing.T) {
	sw, root := newSweepable(t)

	stranded := plant(t, root, "ab/cd/.tmp-123456", 48*time.Hour)
	nested := plant(t, root, ".tmp-987654", 48*time.Hour)
	object := plant(t, root, "ab/cd/page.html", 48*time.Hour)
	dotted := plant(t, root, "ab/cd/.hidden", 48*time.Hour)

	n, err := sw.SweepTemp(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("SweepTemp: %v", err)
	}
	if n != 2 {
		t.Errorf("swept %d files, want 2", n)
	}
	if exists(t, stranded) || exists(t, nested) {
		t.Error("a stranded temp file survived the sweep")
	}
	if !exists(t, object) {
		t.Error("SweepTemp deleted a real object")
	}
	if !exists(t, dotted) {
		t.Error("SweepTemp deleted a dotfile that is not one of ours")
	}
}

// The cutoff is what keeps the sweep from deleting a temp file another process
// is still writing into — a PutObject in flight right now.
func TestLocalSweepTemp_LeavesRecentTempsAlone(t *testing.T) {
	sw, root := newSweepable(t)
	inflight := plant(t, root, "ab/.tmp-inflight", time.Minute)

	n, err := sw.SweepTemp(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("SweepTemp: %v", err)
	}
	if n != 0 {
		t.Errorf("swept %d files, want 0", n)
	}
	if !exists(t, inflight) {
		t.Error("SweepTemp deleted a temp file younger than the cutoff")
	}
}

// The name the sweep matches has to be the one PutObject actually creates, or
// the whole step reclaims nothing.
func TestLocalSweepTemp_MatchesWhatPutObjectStages(t *testing.T) {
	dir := t.TempDir()
	c, err := NewLocalClient(config.LocalStorageConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewLocalClient: %v", err)
	}
	// A PutObject whose body fails mid-copy leaves the staged file behind in
	// exactly the way a SIGKILL would.
	if err := c.PutObject(context.Background(), "ab/cd/page.html", failingReader{}, htmlType); err == nil {
		t.Fatal("PutObject with a failing body returned no error")
	}
	// PutObject's own defer removes it, so re-stage one by hand under the same
	// name pattern and prove the matcher agrees with os.CreateTemp's output.
	f, err := os.CreateTemp(filepath.Join(dir, "ab", "cd"), ".tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	_ = f.Close()
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(name, old, old); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(name), tempPrefix) {
		t.Fatalf("os.CreateTemp produced %q, which tempPrefix %q does not match", filepath.Base(name), tempPrefix)
	}

	n, err := c.(TempSweeper).SweepTemp(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("SweepTemp: %v", err)
	}
	if n != 1 || exists(t, name) {
		t.Errorf("swept %d files and exists=%v, want 1 and false", n, exists(t, name))
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errBoom }

var errBoom = os.ErrInvalid
