package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files under testdata/")

// assertGolden compares got against testdata/<name>. Run with -update to
// rewrite. Every golden-producing test must pin: the clock (testClock), the id
// (fixed by the httptest server), colour (--no-color) and table width
// (PLACARD_TABLE_WIDTH), per spec §12.2.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run: go test ./cmd/placard/ -update)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestGoldenHarnessRoundTrips(t *testing.T) {
	// Guards the harness itself: without this, a broken assertGolden would make
	// every later golden test vacuously pass under -update. t.Chdir restores the
	// working directory automatically at the end of the test.
	t.Chdir(t.TempDir())

	*updateGolden = true
	assertGolden(t, "harness.golden", []byte("hello\n"))
	*updateGolden = false
	assertGolden(t, "harness.golden", []byte("hello\n"))
}
