package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The install scripts reach the network only through curl, so a curl stub on
// PATH is enough to drive install.sh end to end — release list, asset
// selection, checksum verification and the install itself — against fixtures.
const fakeCurl = `#!/bin/sh
url=""
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -H) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
case "$url" in
  https://api.github.com/*)
    file="$FIXTURES/releases-${url##*page=}.json"
    [ -f "$file" ] || { echo '[]'; exit 0; } ;;
  https://github.com/*/releases/download/cli/v*)
    file="$FIXTURES/${url##*/releases/download/cli/v}" ;;
  *) echo "fake curl: unexpected url $url" >&2; exit 22 ;;
esac
[ -f "$file" ] || { echo "fake curl: no fixture for $url" >&2; exit 22; }
if [ -n "$out" ]; then cp "$file" "$out"; else cat "$file"; fi
`

// releasesJSON mirrors the GitHub response, download URLs included: the
// server's own tags share the list, and the newest CLI release is a prerelease
// that must be skipped. The release body deliberately names another release's
// tag, which is the shape that would fool a looser match.
const releasesJSON = `[
  {"tag_name": "v9.9.9", "draft": false, "prerelease": false, "body": "server release", "assets": []},
  {"tag_name": "cli/v2.0.0-rc.1", "draft": false, "prerelease": true, "assets": [
    {"name": "%[1]s", "browser_download_url": "%[4]s/v2.0.0-rc.1/%[1]s"},
    {"name": "checksums.txt", "browser_download_url": "%[4]s/v2.0.0-rc.1/checksums.txt"}]},
  {"tag_name": "cli/v1.2.3", "draft": false, "prerelease": false, "body": "supersedes cli/v0.9.0", "assets": [
    {"name": "%[2]s", "browser_download_url": "%[4]s/v1.2.3/%[2]s"},
    {"name": "checksums.txt", "browser_download_url": "%[4]s/v1.2.3/checksums.txt"}]},
  {"tag_name": "cli/v0.9.0", "draft": false, "prerelease": false, "assets": [
    {"name": "%[3]s", "browser_download_url": "%[4]s/v0.9.0/%[3]s"},
    {"name": "checksums.txt", "browser_download_url": "%[4]s/v0.9.0/checksums.txt"}]}
]`

// downloadPrefix is how GitHub spells a download URL for a tag containing a
// slash: verbatim, not percent-encoded.
const downloadPrefix = "https://github.com/Xm798/placard/releases/download/cli"

// artifactName mirrors install.sh's own os/arch mapping.
func artifactName(t *testing.T, version string) string {
	t.Helper()
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return fmt.Sprintf("placard-%s-%s-%s", version, runtime.GOOS, arch)
}

// scriptRunner writes the fixtures one install.sh run needs and returns the
// command that runs it.
type scriptRunner struct {
	dir      string // fixture root
	binDir   string // where the CLI is installed
	script   string // the install.sh under test
	fixtures string
}

func newScriptRunner(t *testing.T, script []byte, corruptChecksum bool) *scriptRunner {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is the POSIX installer")
	}
	root := t.TempDir()
	fixtures := filepath.Join(root, "fixtures")
	pathDir := filepath.Join(root, "path")
	for _, d := range []string{fixtures, pathDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, v := range []string{"2.0.0-rc.1", "1.2.3", "0.9.0"} {
		name := artifactName(t, v)
		payload := []byte("#!/bin/sh\necho \"placard " + v + "\"\n")
		sum := sha256.Sum256(payload)
		digest := hex.EncodeToString(sum[:])
		if corruptChecksum {
			digest = strings.Repeat("0", 64)
		}
		dir := filepath.Join(fixtures, v)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, name), payload)
		write(t, filepath.Join(dir, "checksums.txt"), []byte(digest+"  "+name+"\n"))
	}
	write(t, filepath.Join(fixtures, "releases-1.json"), []byte(fmt.Sprintf(releasesJSON,
		artifactName(t, "2.0.0-rc.1"), artifactName(t, "1.2.3"), artifactName(t, "0.9.0"), downloadPrefix)))

	curl := filepath.Join(pathDir, "curl")
	write(t, curl, []byte(fakeCurl))
	if err := os.Chmod(curl, 0o755); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(root, "install.sh")
	write(t, scriptPath, script)

	return &scriptRunner{
		dir:      root,
		binDir:   filepath.Join(root, "bin"),
		script:   scriptPath,
		fixtures: fixtures,
	}
}

func write(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *scriptRunner) run(t *testing.T, extraEnv ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", r.script)
	cmd.Env = append([]string{
		"PATH=" + filepath.Join(r.dir, "path") + ":" + os.Getenv("PATH"),
		"HOME=" + r.dir,
		"FIXTURES=" + r.fixtures,
		"PLACARD_BIN_DIR=" + r.binDir,
		"SHELL=/bin/sh",
	}, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *scriptRunner) installed(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.binDir, "placard"))
	if err != nil {
		return ""
	}
	return string(b)
}

// The newest STABLE cli/v release wins: not the server's own v9.9.9 tag, and
// not the newer prerelease.
func TestInstallShPicksTheNewestStableCLIRelease(t *testing.T) {
	r := newScriptRunner(t, Script, false)
	out, err := r.run(t)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	if !strings.Contains(r.installed(t), "placard 1.2.3") {
		t.Fatalf("installed the wrong release; output:\n%s", out)
	}
}

// A pinned script installs that release, taking its assets from that release's
// own entry — checksums.txt is named the same in every one of them.
func TestInstallShHonoursThePinnedVersion(t *testing.T) {
	r := newScriptRunner(t, PinShell("0.9.0"), false)
	out, err := r.run(t)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	if !strings.Contains(r.installed(t), "placard 0.9.0") {
		t.Fatalf("pinned version was not installed; output:\n%s", out)
	}
}

// PLACARD_CLI_VERSION outranks the pin the server injected.
func TestInstallShEnvVersionOutranksThePin(t *testing.T) {
	r := newScriptRunner(t, PinShell("0.9.0"), false)
	out, err := r.run(t, "PLACARD_CLI_VERSION=1.2.3")
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	if !strings.Contains(r.installed(t), "placard 1.2.3") {
		t.Fatalf("PLACARD_CLI_VERSION was ignored; output:\n%s", out)
	}
}

// Fail-closed: a mismatching digest aborts before anything is installed.
func TestInstallShAbortsOnChecksumMismatch(t *testing.T) {
	r := newScriptRunner(t, Script, true)
	out, err := r.run(t)
	if err == nil {
		t.Fatalf("install.sh must fail on a checksum mismatch; output:\n%s", out)
	}
	if !strings.Contains(out, "SHA256 mismatch") {
		t.Errorf("output must name the failure:\n%s", out)
	}
	if got := r.installed(t); got != "" {
		t.Errorf("nothing may be installed after a checksum failure, got %q", got)
	}
}

// Server and CLI releases share one list, so the newest CLI release can sit
// past the first page. Stopping at page one would report that the CLI has no
// releases at all.
func TestInstallShWalksPastAPageOfServerReleases(t *testing.T) {
	r := newScriptRunner(t, Script, false)
	// Move every CLI release to page two and fill page one with server tags.
	cli, err := os.ReadFile(filepath.Join(r.fixtures, "releases-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.fixtures, "releases-2.json"), cli)
	var serverTags []string
	for i := 0; i < 100; i++ {
		serverTags = append(serverTags,
			fmt.Sprintf(`{"tag_name": "v1.0.%d", "draft": false, "prerelease": false, "assets": []}`, i))
	}
	write(t, filepath.Join(r.fixtures, "releases-1.json"),
		[]byte("["+strings.Join(serverTags, ",")+"]"))

	out, err := r.run(t)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	if !strings.Contains(r.installed(t), "placard 1.2.3") {
		t.Fatalf("the release on page two was not found; output:\n%s", out)
	}
}
