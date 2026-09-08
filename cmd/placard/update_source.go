// cmd/placard/update_source.go
package main

import (
	"context"
	"errors"

	"github.com/Xm798/placard/cmd/placard/updater"
)

// releaseUpdater is the distribution backend behind the Updater seam declared in
// update.go. Everything about WHICH version to install (local builds, version
// comparison, refusing downgrades, --force) is decided there; this type only
// fetches and installs what it is told to.
//
// It prints nothing: all user-facing output and the --json envelope are
// produced by runUpdate through env.Out.
type releaseUpdater struct{}

var _ Updater = releaseUpdater{}

func (releaseUpdater) Latest(ctx context.Context) (string, error) {
	v, err := updater.FetchLatest(ctx)
	if err != nil {
		return "", Network(err.Error())
	}
	return v, nil
}

func (releaseUpdater) Apply(ctx context.Context, version string) error {
	return classifyApplyError(updater.Apply(ctx, version))
}

// classifyApplyError maps the updater package's error classes onto the CLI's
// typed errors, so the exit code and the --json envelope code are right without
// anyone matching on message text.
func classifyApplyError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, updater.ErrVerification):
		// Own code: a checksum failure is a security event, not a flaky
		// download. Monitoring and CI must be able to tell them apart. Code
		// "error" would be wrong twice over: it is reserved for server-envelope
		// passthrough and unclassified cobra runtime errors, and the CLI never
		// mints it itself.
		return &Error{Code: "verification", Message: err.Error(), Exit: 1}
	case errors.Is(err, updater.ErrReplace):
		return LocalIO(err.Error())
	default:
		return Network(err.Error())
	}
}

// init replaces the default (unavailableUpdater) wiring. Package-level vars are
// initialized before init runs, so this always wins.
//
// Any test that wants the "self-update not built in" behaviour must set
// newUpdater = defaultNewUpdater explicitly — which cmd/placard/update_test.go
// already does.
func init() {
	newUpdater = func(*Env) Updater { return releaseUpdater{} }
}
