// Package updater is the distribution-side half of `placard update`: it
// resolves the published version, downloads that binary, verifies its sha256
// and atomically replaces the running executable. The new binary takes effect
// on the NEXT invocation.
//
// This package is MECHANISM ONLY. Version policy — isLocalBuild, version
// comparison, refusing downgrades, --force — lives in cmd/placard/update.go
// on the CLI side, and must not be duplicated here.
//
// Verification is fail-closed with NO escape hatch. install.sh offers
// PLACARD_SKIP_CHECKSUM=1 because a human is watching the warning scroll by;
// self-update is unattended, and silently installing an unverified binary is
// worse than not updating at all.
//
// Stated plainly: checksums.txt is an asset of the same GitHub release as the
// binaries, so this defends against transport corruption and against downgrade
// attacks — not against tampering by someone holding write access to the
// repository's releases.
package updater

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Xm798/placard/internal/ghrelease"
)

// Error classes, so the caller can map failures onto the CLI's typed errors
// without matching on message text. Anything not wrapped in one of these is a
// fetch/network failure.
var (
	// ErrVerification means the binary could not be proven authentic: the
	// checksums file was unreachable or unparsable, had no entry for this
	// artifact, or the digest did not match. All of these abort the update.
	ErrVerification = errors.New("verification failed")
	// ErrReplace means the download succeeded and verified but the executable
	// could not be replaced (permissions, locked file, read-only mount).
	ErrReplace = errors.New("replacing the executable failed")
)

// FetchLatest returns the version of the newest `cli/v*` GitHub release. It
// applies no policy: comparing it against the running version is the caller's
// job.
func FetchLatest(ctx context.Context) (string, error) {
	rel, err := ghrelease.Latest(ctx, textClient)
	if err != nil {
		return "", err
	}
	return rel.Version, nil
}

// Apply downloads the given version for this os/arch, verifies its sha256
// against the same release's checksums.txt and atomically replaces the running
// executable. The caller has already decided that this version SHOULD be
// installed.
func Apply(ctx context.Context, version string) error {
	target, err := resolveSelfPath()
	if err != nil {
		return fmt.Errorf("%w: %s", ErrReplace, err)
	}
	return applyTo(ctx, version, target)
}

func applyTo(ctx context.Context, version, target string) error {
	name, err := artifactName(version)
	if err != nil {
		return err
	}

	rel, err := ghrelease.Find(ctx, textClient, version)
	if err != nil {
		return err
	}
	binaryURL, ok := rel.Assets[name]
	if !ok {
		return fmt.Errorf("release %s%s has no asset named %s (this platform is not published)",
			ghrelease.TagPrefix, version, name)
	}
	data, err := fetchBinary(ctx, binaryURL)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", name, err)
	}

	// Fail-closed: a release without a reachable checksums.txt aborts the
	// update. Do NOT turn this into a warning — "make one request fail" must
	// not be a way to disable verification.
	checksumsURL, ok := rel.Assets["checksums.txt"]
	if !ok {
		return fmt.Errorf("%w: release %s%s publishes no checksums.txt",
			ErrVerification, ghrelease.TagPrefix, version)
	}
	body, err := fetchText(ctx, checksumsURL)
	if err != nil {
		return fmt.Errorf("%w: fetching checksums: %s", ErrVerification, err)
	}
	expected, err := checksumFor(body, name)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrVerification, err)
	}
	if err := verifySHA256(data, expected); err != nil {
		return fmt.Errorf("%w: %s: %s", ErrVerification, name, err)
	}

	if err := writeFileAtomic(target, data, 0o755); err != nil {
		return fmt.Errorf("%w: %s: %s", ErrReplace, target, err)
	}
	return nil
}

// artifactName builds the release artifact name for this platform, matching
// .goreleaser.cli.yml, install.sh and install.ps1.
func artifactName(version string) (string, error) {
	osName, archName, err := platformSuffix()
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("placard-%s-%s-%s", version, osName, archName)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name, nil
}

// resolveSelfPath returns the symlink-resolved path of the running binary so
// the replacement lands on the real file, not a Homebrew-style symlink.
func resolveSelfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating the running executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolving symlinks for %s: %w", exe, err)
	}
	// EvalSymlinks only guarantees the target was not a symlink at the instant
	// it resolved. Between that and the write there is a TOCTOU window in which
	// the resolved path itself can be swapped for a symlink pointing elsewhere,
	// which would make the "atomic replace" write the new binary to an
	// attacker-chosen location. Re-Lstat and demand a regular file. This does
	// not close the window (that needs openat-level primitives) but it rules
	// out every shape seen in practice, for one syscall.
	fi, err := os.Lstat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspecting %s: %w", resolved, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("refusing to replace %s: not a regular file (mode %s)", resolved, fi.Mode())
	}
	return resolved, nil
}

// platformSuffix mirrors the (os, arch) mapping in install.sh / install.ps1.
func platformSuffix() (string, string, error) {
	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "arm64"
	default:
		return "", "", fmt.Errorf("unsupported architecture: %s", runtime.GOARCH)
	}
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		return runtime.GOOS, arch, nil
	default:
		return "", "", fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}

// checksumFor reads goreleaser's `<sha256>  <name>` format.
func checksumFor(body, binaryName string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		fields := strings.Fields(strings.TrimSpace(scanner.Text()))
		if len(fields) >= 2 && fields[len(fields)-1] == binaryName {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum entry for %s", binaryName)
}
