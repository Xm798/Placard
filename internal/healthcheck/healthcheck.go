// Package healthcheck implements `placard-server healthcheck`, the liveness
// probe for containers whose runtime image has no shell, curl or wget to build
// one from.
package healthcheck

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Name is the argument that selects this subcommand.
const Name = "healthcheck"

// Path is the liveness endpoint. Readiness (/api/ready) is left out on
// purpose: a database or Redis outage must not get the container killed and
// restarted, which cannot fix either.
const Path = "/api/health"

// Timeout bounds the whole request, dial included.
const Timeout = 3 * time.Second

// URL is the liveness URL of a server listening on port. The probe always runs
// beside the server, so loopback is the right host regardless of base_url.
func URL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, Path)
}

// Probe returns nil only when url answers 200 within timeout.
func Probe(url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: unexpected status %s", url, resp.Status)
	}
	return nil
}
