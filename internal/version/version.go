// Package version holds build-time metadata injected via -ldflags -X. Defaults
// (dev/unknown) apply to `go run` and bare `go build`; the Makefile `build`
// target and the Dockerfile build stage inject real values, so a running
// instance can be identified for ops triage.
package version

import "strings"

var (
	// Version is the git describe output (tag or shorthand commit, with -dirty
	// suffix).
	Version = "dev"
	// BuildTime is the UTC ISO 8601 build timestamp. Logged at startup only —
	// never exposed on /api/version to avoid handing attackers a build-time
	// pointer.
	BuildTime = "unknown"
	// GitCommit is the short commit hash.
	GitCommit = "unknown"
	// GitBranch is the source branch name.
	GitBranch = "unknown"
)

// Public returns the version exposed by /api/version. Master builds include
// their short commit so a deployment maps unambiguously to source.
func Public() string {
	branch := strings.TrimPrefix(GitBranch, "refs/heads/")
	if branch == "master" && GitCommit != "" && GitCommit != "unknown" {
		return "master-" + GitCommit
	}
	return Version
}
