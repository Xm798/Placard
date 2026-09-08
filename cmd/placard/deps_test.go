package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoServerSideImports is the dependency-boundary guard from spec §1.1.
// internal/web embeds the whole SPA (//go:embed all:dist); repo/storage/handler
// drag in GORM, Redis, the S3 SDK and Fiber. Importing any of them has NO
// compile-time signal — it only shows up as a much larger binary that nobody
// notices. CI runs the same check as a shell one-liner in the cli:test job.
func TestNoServerSideImports(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	forbidden := []string{
		"github.com/Xm798/placard/internal/web",
		"github.com/Xm798/placard/internal/repo",
		"github.com/Xm798/placard/internal/storage",
		"github.com/Xm798/placard/internal/handler",
	}
	for _, line := range strings.Split(string(out), "\n") {
		for _, f := range forbidden {
			if strings.TrimSpace(line) == f {
				t.Errorf("cmd/placard must not import %s", f)
			}
		}
	}
}
