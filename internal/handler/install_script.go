package handler

import (
	"context"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Xm798/placard/internal/ghrelease"
	"github.com/Xm798/placard/internal/logger"
)

const (
	// cliVersionTTL is how long a resolved CLI version is reused. The install
	// scripts resolve the newest release themselves when the server hands them
	// none, so this cache only decides how quickly a fresh release starts being
	// pinned — not whether an install works.
	cliVersionTTL = 15 * time.Minute
	// cliVersionRetryTTL bounds the outbound traffic an unauthenticated
	// /install.sh can generate while GitHub is unreachable or rate-limiting.
	cliVersionRetryTTL = time.Minute
	// cliVersionTimeout keeps a slow GitHub from holding the install script
	// hostage: past it the script is served unpinned and resolves the release
	// itself.
	cliVersionTimeout = 5 * time.Second
)

// GitHubCLIRelease is the production Deps.CLIRelease: the newest CLI release
// published on GitHub. main.go injects it; leaving Deps.CLIRelease nil (every
// test does) means the install scripts are served unpinned and resolve the
// release themselves.
func GitHubCLIRelease() func(context.Context) (string, error) {
	hc := &http.Client{Timeout: cliVersionTimeout}
	return func(ctx context.Context) (string, error) {
		rel, err := ghrelease.Latest(ctx, hc)
		return rel.Version, err
	}
}

// cliVersionResolver caches the newest published CLI version for the install
// scripts. An instance with no outbound network never gets one, and that is a
// supported outcome: the scripts then resolve the release themselves.
type cliVersionResolver struct {
	lookup func(context.Context) (string, error)
	now    func() time.Time

	mu      sync.Mutex
	version string
	retryAt time.Time
}

func newCLIVersionResolver(lookup func(context.Context) (string, error)) *cliVersionResolver {
	if lookup == nil {
		return nil
	}
	return &cliVersionResolver{lookup: lookup, now: time.Now}
}

// Current returns the version to pin, or "" when none is known — including on
// a nil resolver, which is what an instance that was given no lookup has. A
// failed lookup keeps the last known version rather than dropping the pin: a
// stale pin still installs a working CLI.
func (r *cliVersionResolver) Current(ctx context.Context) string {
	if r == nil {
		return ""
	}
	// Holding the lock across the lookup is deliberate: concurrent installs
	// then share one request instead of each opening their own.
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.now().Before(r.retryAt) {
		return r.version
	}

	// Detached from the request: a visitor who hits Ctrl-C mid-download must
	// not cancel the lookup and leave the failure cached for everyone behind
	// them. The timeout still bounds it.
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cliVersionTimeout)
	defer cancel()
	v, err := r.lookup(lookupCtx)
	if err != nil {
		r.retryAt = r.now().Add(cliVersionRetryTTL)
		logger.Module("install").Warn("could not resolve the latest CLI release",
			zap.Error(err))
		return r.version
	}
	r.version, r.retryAt = v, r.now().Add(cliVersionTTL)
	return r.version
}
