package ctxlog

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is where this test walks from: internal/ctxlog -> repo root.
const repoRoot = "../.."

// scannedDirs are the trees that must stay free of zap.Duration.
var scannedDirs = []string{"internal", "cmd"}

// knownDurationSites are the zap.Duration call sites that predate this rule.
// It is now empty: every site has been renamed, so the rule has no exceptions
// left and any new zap.Duration is a straight failure.
//
// The mechanism stays because it is a ratchet, not an exemption: it may only
// shrink. Should a site ever have to be listed again, a file listed here that
// no longer uses zap.Duration fails the test too, so the entry cannot rot after
// its rename lands — which is how internal/middleware/accesslog.go came off
// this list once "latency" became duration_ms (design §10).
var knownDurationSites = []string{}

// TestNoZapDuration enforces the field rule from the observability design:
// zap.Duration serializes as float seconds, which is ambiguous beside the
// millisecond fields used everywhere else and cannot be aggregated consistently
// in ELK. Use ctxlog.DurMS / ctxlog.TTFBMS, or zap.Int64 with the unit in the
// field name.
//
// The scan is token-based, so prose mentioning zap.Duration (this file, doc
// comments) does not trip it — only real code does.
func TestNoZapDuration(t *testing.T) {
	known := make(map[string]bool, len(knownDurationSites))
	for _, p := range knownDurationSites {
		known[p] = false // flipped to true once a hit is seen
	}

	for _, dir := range scannedDirs {
		root := filepath.Join(repoRoot, dir)
		prefix := filepath.ToSlash(repoRoot) + "/"
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(filepath.ToSlash(path), prefix)
			if d.IsDir() {
				return nil
			}
			// Non-test sources only; _test.go files (including this one) are exempt.
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !usesZapDuration(src) {
				return nil
			}
			if _, ok := known[rel]; ok {
				known[rel] = true
				return nil
			}
			t.Errorf("%s uses zap.Duration; use ctxlog.DurMS/TTFBMS or zap.Int64 with the unit in the field name", rel)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	for path, seen := range known {
		if !seen {
			t.Errorf("%s is listed in knownDurationSites but no longer uses zap.Duration; drop the entry", path)
		}
	}
}

// usesZapDuration reports whether src contains the token sequence
// `zap` `.` `Duration` in code. Comments and string literals are skipped by the
// scanner, so documentation about the rule is not a violation of it.
func usesZapDuration(src []byte) bool {
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	s.Init(file, src, nil /* ignore scan errors */, 0)

	// Rolling window over the last two significant tokens.
	var prev2, prev1 string
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return false
		}
		cur := lit
		if cur == "" {
			cur = tok.String()
		}
		if prev2 == "zap" && prev1 == "." && cur == "Duration" {
			return true
		}
		prev2, prev1 = prev1, cur
	}
}
