package logger

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/Xm798/placard/internal/config"
)

// captureStdout swaps os.Stdout for a pipe while fn runs and returns what fn
// wrote there. The core reads os.Stdout at construction time, so anything built
// inside fn is captured.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	out := <-done
	_ = r.Close()
	return out
}

// A configured file sink adds a copy, it does not move the log off stdout:
// `docker logs` on a container that fails to start is the one place an
// operator can always reach.
func TestFileSinkKeepsConsole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "placard.log")
	cfg := config.LogConfig{Level: "info", File: path, MaxSize: 1, MaxBackups: 1}

	stdout := captureStdout(t, func() {
		log := zap.New(newCore(cfg))
		log.Info("in-both")
		_ = log.Sync()
	})

	if !strings.Contains(stdout, "in-both") {
		t.Errorf("stdout should carry the entry alongside the file, got %q", stdout)
	}
	// The console copy is for a human reading a terminal or `docker logs`;
	// JSON there would be the file's job done twice.
	if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Errorf("console sink should not be JSON-encoded, got %q", stdout)
	}
	// captureStdout hands the core a pipe, never a terminal.
	if strings.Contains(stdout, "\x1b[") {
		t.Errorf("console sink should not colour a non-terminal stream, got %q", stdout)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(b), "in-both") {
		t.Errorf("log file missing the entry, got %q", b)
	}
	// The file sink is what a collector consumes, so it must be JSON.
	if !strings.HasPrefix(strings.TrimSpace(string(b)), "{") {
		t.Errorf("log file is not JSON-encoded, got %q", b)
	}
}

// With no file configured (local dev) the console remains the sink, otherwise
// `make run` would emit nothing at all.
func TestNoFileSinkKeepsConsole(t *testing.T) {
	stdout := captureStdout(t, func() {
		log := zap.New(newCore(config.LogConfig{Level: "info"}))
		log.Info("on-stdout")
		_ = log.Sync()
	})

	if !strings.Contains(stdout, "on-stdout") {
		t.Errorf("stdout should carry the entry when log.file is empty, got %q", stdout)
	}
}

func TestLevelIsHonoredAndFallsBackToInfo(t *testing.T) {
	if got := newCore(config.LogConfig{Level: "warn"}); got.Enabled(zapcore.InfoLevel) {
		t.Error("info should be disabled at level=warn")
	}
	// An unparseable level must not silence the service.
	core := newCore(config.LogConfig{Level: "not-a-level"})
	if !core.Enabled(zapcore.InfoLevel) {
		t.Error("info should be enabled after falling back from a bad level")
	}
	if core.Enabled(zapcore.DebugLevel) {
		t.Error("debug should be disabled after falling back to info")
	}
}
