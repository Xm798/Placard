// cmd/placard/update.go
package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/Xm798/placard/internal/version"
)

// Updater is the seam between this file (command shell, version policy) and the
// distribution line (download, fail-closed sha256, atomic replace). See the
// distribution plan; the signatures here are the contract.
type Updater interface {
	Latest(ctx context.Context) (string, error)
	Apply(ctx context.Context, version string) error
}

// unavailableUpdater is what a build without the distribution backend gets. It
// fails with a sentence a user can act on instead of a nil-interface panic.
type unavailableUpdater struct{}

func (unavailableUpdater) Latest(context.Context) (string, error) {
	return "", Conflict("self-update is not enabled in this build (missing distribution backend). " +
		"Reinstall with: curl -fsSL https://<your placard server>/install.sh | sh")
}

func (unavailableUpdater) Apply(context.Context, string) error {
	return Conflict("self-update is not enabled in this build (missing distribution backend)")
}

func defaultNewUpdater(*Env) Updater { return unavailableUpdater{} }

// newUpdater is overridden by the distribution line's init() (update_source.go);
// currentVersion is a variable so tests can pin the running version.
//
// MAINTAINERS: once update_source.go lands, the default is NO LONGER
// unavailableUpdater — its init() replaces it at package load, in tests too.
// Any new update-related test must therefore SET newUpdater explicitly (as
// every test below does) instead of assuming what the default is.
var (
	newUpdater     = defaultNewUpdater
	currentVersion = cliVersion
)

// isLocalBuild recognizes every non-release version string the CLI can carry
// (spec §8.2), including the v prefix that marks a SERVER tag: goreleaser
// injects a bare semver (1.2.3) for the CLI, so anything starting with v
// cannot be a CLI release. The server decides the same question the same way
// when it enforces min_cli_version.
func isLocalBuild(v string) bool { return !version.IsRelease(v) }

// compareVersions orders two release versions; see version.Compare.
func compareVersions(a, b string) int { return version.Compare(a, b) }

func newUpdateCmd(env *Env) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the CLI to the latest version",
		Long: "Updates the CLI to the latest version.\n\n" +
			"Refuses downgrades: the remote version must be strictly higher than the current one; use --force to override (including downgrades and local builds).\n" +
			"The sha256 check is fail-closed: a missing or mismatched checksum always fails, with no skip option — " +
			"self-update is unattended, and silently installing an unverified binary is worse than failing to install.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdate(cmd.Context(), env, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Skip version comparison and force install (allows downgrades and local builds)")
	return cmd
}

func runUpdate(ctx context.Context, env *Env, force bool) error {
	current := currentVersion()
	up := newUpdater(env)

	if isLocalBuild(current) && !force {
		reason := "local build detected (" + current + "); skipped update check. Use --force to install the latest version"
		env.Out.Printf("%s\n", reason)
		return env.Out.Emit(map[string]any{
			"current": current, "latest": "", "updated": false, "reason": reason,
		})
	}

	latest, err := up.Latest(ctx)
	if err != nil {
		return err
	}

	if !force {
		switch compareVersions(latest, current) {
		case 0:
			reason := "already up to date (" + current + ")"
			env.Out.Printf("%s\n", reason)
			return env.Out.Emit(map[string]any{
				"current": current, "latest": latest, "updated": false, "reason": reason,
			})
		case -1:
			// Refusing beats installing: whoever can retag a release could
			// otherwise downgrade every user onto a known-bad build, and a
			// downgrade attack needs no forged signature.
			return &Error{Code: "conflict", Exit: 1,
				Message: "remote version (" + latest + ") is lower than current (" + current +
					"); refused to install. Use --force to downgrade"}
		}
	}

	env.Out.Printf("Updating: %s → %s\n", current, latest)
	if err := up.Apply(ctx, latest); err != nil {
		return err
	}
	env.Out.Printf("Updated to %s (takes effect on next run)\n", latest)
	return env.Out.Emit(map[string]any{
		"current": current, "latest": latest, "updated": true, "reason": "",
	})
}
