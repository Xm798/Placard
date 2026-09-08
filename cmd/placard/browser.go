// cmd/placard/browser.go
package main

import (
	"os/exec"
	"runtime"
)

// browserRunner encapsulates every OS interaction of "open a URL" so the
// degradation matrix is testable without a real browser.
type browserRunner struct {
	Getenv func(string) string
	GOOS   string
	Look   func(string) (string, error)
	Run    func(name string, args ...string) error
}

func newBrowserRunner(getenv func(string) string) browserRunner {
	return browserRunner{
		Getenv: getenv,
		GOOS:   runtime.GOOS,
		Look:   exec.LookPath,
		Run: func(name string, args ...string) error {
			return exec.Command(name, args...).Start()
		},
	}
}

// Open tries, in order: $BROWSER, then the platform default. It returns
// (false, nil) — NOT an error — whenever there is no usable browser: no
// graphical session, missing command, or a non-zero exit. Callers print the URL
// instead and keep exit code 0 (spec §2.4).
func (b browserRunner) Open(rawurl string) (bool, error) {
	if cmd := b.Getenv("BROWSER"); cmd != "" {
		if _, err := b.Look(cmd); err != nil {
			return false, nil
		}
		if err := b.Run(cmd, rawurl); err != nil {
			return false, nil
		}
		return true, nil
	}
	// An SSH session is a headless signal on every platform.
	if b.Getenv("SSH_CONNECTION") != "" {
		return false, nil
	}

	var name string
	var args []string
	switch b.GOOS {
	case "darwin":
		name, args = "open", []string{rawurl}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", rawurl}
	default: // linux and the other unixes
		if b.Getenv("DISPLAY") == "" && b.Getenv("WAYLAND_DISPLAY") == "" {
			return false, nil
		}
		name, args = "xdg-open", []string{rawurl}
	}
	if _, err := b.Look(name); err != nil {
		return false, nil
	}
	if err := b.Run(name, args...); err != nil {
		return false, nil
	}
	return true, nil
}

// openURL is the single entry point used by `open`, `publish --open` and
// `login`. Commands must reach it through Env.OpenURL, never call it directly:
// that field is the seam TestMain overrides so no test can spawn a real
// browser. It is a var (not a func) so that override covers Env values built by
// NewEnv too.
var openURL = func(env *Env, rawurl string) (bool, error) {
	return newBrowserRunner(env.Getenv).Open(rawurl)
}
