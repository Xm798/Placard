// cmd/placard/update_source_test.go
package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Xm798/placard/cmd/placard/updater"
)

// The whole point of update_source.go is the init() wiring; assert it took effect,
// or a build could silently ship with self-update disabled.
func TestInitWiresReleaseUpdater(t *testing.T) {
	got := newUpdater(nil)
	if _, ok := got.(releaseUpdater); !ok {
		t.Fatalf("newUpdater returned %T, want releaseUpdater — init() did not take effect", got)
	}
}

func TestClassifyApplyError(t *testing.T) {
	cases := []struct {
		name     string
		in       error
		wantCode string
	}{
		// A security failure must stay distinguishable from a retryable one.
		{"verification", fmt.Errorf("%w: sha256 mismatch", updater.ErrVerification), "verification"},
		{"replace", fmt.Errorf("%w: permission denied", updater.ErrReplace), "local_io"},
		{"anything else is network", errors.New("dial tcp: connection refused"), "network"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e *Error
			if !errors.As(classifyApplyError(tc.in), &e) {
				t.Fatalf("want *Error so the exit code is right, got %T", tc.in)
			}
			if e.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", e.Code, tc.wantCode)
			}
			if e.Exit != 1 {
				t.Fatalf("exit = %d, want 1", e.Exit)
			}
		})
	}
}

func TestClassifyApplyErrorNilStaysNil(t *testing.T) {
	if err := classifyApplyError(nil); err != nil {
		t.Fatalf("nil must stay nil, got %v", err)
	}
}
