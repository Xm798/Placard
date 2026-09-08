// Package installer embeds the Placard CLI install scripts so the server can
// serve them at GET /install.sh and GET /install.ps1 under the product's own
// domain.
//
// (go:embed cannot reference files outside the package directory, hence this
// small package instead of embedding from internal/handler — same reason
// skills/placard exists.)
//
// install.sh and install.ps1 in this directory are the SINGLE SOURCE for both
// the served copies and the copies CI attaches to a CLI release.
// Never fork either.
package installer

import (
	"bytes"
	_ "embed"
	"strings"

	"github.com/Xm798/placard/internal/version"
)

//go:embed install.sh
var Script []byte

//go:embed install.ps1
var ScriptPS []byte

func init() {
	// Fail fast: an empty or non-shell install.sh is a build defect, not a
	// runtime condition — serving it would hand every user a no-op "installer".
	if !strings.HasPrefix(string(Script), "#!") {
		panic("installer: install.sh must start with a shebang")
	}
	// Same guard for install.ps1: it must start with '#' (the '#Requires'
	// directive or a comment). An empty or malformed embed is a build defect.
	if len(ScriptPS) == 0 || ScriptPS[0] != '#' {
		panic("installer: install.ps1 must start with '#'")
	}
	// A rename of either version line would silently stop the server from
	// pinning anything, and nothing downstream would notice.
	if bytes.Count(Script, []byte(shellVersionMarker)) != 1 {
		panic("installer: install.sh must declare " + shellVersionMarker + " exactly once")
	}
	if bytes.Count(ScriptPS, []byte(psVersionMarker)) != 1 {
		panic("installer: install.ps1 must declare " + psVersionMarker + " exactly once")
	}
}

// Pin markers. Each install script declares the version it installs on one
// exact line, which a server rewrites when it serves the script so that an
// install from an instance lands on the CLI release that instance resolved.
// The scripts fall back to resolving the newest release themselves, which is
// what an install straight from GitHub does.
const (
	shellVersionMarker = `DEFAULT_CLI_VERSION=""`
	psVersionMarker    = `$DefaultCliVersion = ''`
)

// PinShell returns install.sh with its version line pinned to v. An empty or
// non-release v returns the script untouched, so the script resolves the
// newest release itself rather than being handed something it cannot install.
func PinShell(v string) []byte {
	return pin(Script, shellVersionMarker, `DEFAULT_CLI_VERSION="`+v+`"`, v)
}

// PinPowerShell is PinShell for install.ps1.
func PinPowerShell(v string) []byte {
	return pin(ScriptPS, psVersionMarker, `$DefaultCliVersion = '`+v+`'`, v)
}

func pin(script []byte, marker, replacement, v string) []byte {
	if !version.IsRelease(v) {
		return script
	}
	return bytes.Replace(script, []byte(marker), []byte(replacement), 1)
}
