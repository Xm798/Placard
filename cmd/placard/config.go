// cmd/placard/config.go
package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// TokenEntry is one stored PAT plus the base URL it is bound to. Base is
// stored explicitly (not only as the map key) so a hand-edited or
// differently-normalized file is caught by the pre-flight comparison in
// creds.go instead of silently shipping a prod token to staging (spec §4.1
// rule 1).
type TokenEntry struct {
	Token     string `json:"token"`
	Base      string `json:"base"`
	Name      string `json:"name,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Config is the credential store (see Paths.Config). Tokens is keyed by
// normalized base URL so prod and staging credentials coexist (spec §4.2).
// There is deliberately no profile concept.
type Config struct {
	Tokens map[string]TokenEntry `json:"tokens"`
	// DefaultBase is the server used when neither --base nor PLACARD_URL says
	// otherwise: the one most recently logged into. Placard ships no default
	// instance, so without it every command after `login` would need --base.
	// It selects a server, never a credential — the per-base binding in
	// Tokens still decides which token may be sent (rule 1).
	DefaultBase string `json:"default_base,omitempty"`
}

// Paths resolves the CLI's config locations, following the XDG Base Directory
// spec: the config root is $XDG_CONFIG_HOME when set, else ~/.config — never a
// dotdir dumped straight into the home directory. It covers the config root
// only; the self-update install dir comes from os.Executable() (see updater).
// It is a value so tests pin both inputs instead of reading the real env.
type Paths struct {
	// configHome is the XDG config root (…/.config), NOT the placard dir.
	configHome string
	// home is os.UserHomeDir(), kept for locations that predate XDG and for
	// future non-config roots.
	home string
}

// NewPaths resolves the config root from the environment. Per the XDG spec a
// relative $XDG_CONFIG_HOME is invalid and must be ignored — honoring one would
// scatter credential files across whatever directory the CLI happened to run in.
//
// Validity is filepath.IsAbs, which is itself platform-dependent: on Windows it
// demands a volume name, so the POSIX-style value MSYS2/Git Bash shells export
// is rejected there and resolution falls back to %USERPROFILE%\.config.
func NewPaths(home string, getenv func(string) string) Paths {
	if v := strings.TrimSpace(getenv("XDG_CONFIG_HOME")); filepath.IsAbs(v) {
		return Paths{configHome: v, home: home}
	}
	if home != "" {
		return Paths{configHome: filepath.Join(home, ".config"), home: home}
	}
	return Paths{home: home}
}

// join builds a path under the placard config dir, or returns "" when no
// config root is resolvable — so no caller can end up with a relative path.
func (p Paths) join(elem ...string) string {
	if p.configHome == "" {
		return ""
	}
	return filepath.Join(append([]string{p.configHome, "placard"}, elem...)...)
}

// ConfigDir is the placard config directory, or "" when neither
// $XDG_CONFIG_HOME nor a home directory is known (--token still works).
func (p Paths) ConfigDir() string { return p.join() }

// ConfigFile is the credential store: <config root>/placard/config.json,
// i.e. ~/.config/placard/config.json unless $XDG_CONFIG_HOME redirects it.
func (p Paths) ConfigFile() string { return p.join("config.json") }

// soleBase is the base of the only stored login, or "" when there are none or
// several. It is what a bare command falls back to when no server was named:
// one login is an unambiguous answer, and guessing between several would send
// a command to whichever base map iteration happened to reach first.
func (c *Config) soleBase() string {
	if len(c.Tokens) != 1 {
		return ""
	}
	for base := range c.Tokens {
		return base
	}
	return ""
}

// LoadConfig returns an empty config when the file does not exist, and a
// local_io error when it exists but cannot be parsed — never a silent reset,
// which would look like "my login vanished".
func LoadConfig(p Paths) (*Config, error) {
	// An unresolvable path is "" here, which os.ReadFile reports as IsNotExist —
	// the same degrade-to-empty branch below, so it needs no separate guard.
	path := p.ConfigFile()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{Tokens: map[string]TokenEntry{}}, nil
	}
	if err != nil {
		return nil, LocalIO("failed to read " + path + ": " + err.Error())
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, LocalIO(path + " is not valid JSON: " + err.Error() +
			" (fix or delete the file, then run: placard login)")
	}
	if cfg.Tokens == nil {
		cfg.Tokens = map[string]TokenEntry{}
	}
	return &cfg, nil
}

// SaveConfig writes atomically: temp file in the SAME directory (a cross-device
// rename fails), 0600, then os.Rename. Never truncate-in-place — a crash there
// leaves half a JSON and loses every instance's credentials (spec §4.3).
// Last writer wins; there is deliberately no lock.
func SaveConfig(p Paths, cfg *Config) error {
	dir := p.ConfigDir()
	if dir == "" {
		return LocalIO("cannot determine a config directory: neither $XDG_CONFIG_HOME nor a home directory is set" +
			" (pass --token, or set the PLACARD_TOKEN environment variable)")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return LocalIO("failed to create " + dir + ": " + err.Error())
	}
	// MkdirAll respects umask, so an existing/looser directory is tightened.
	if err := os.Chmod(dir, 0o700); err != nil {
		return LocalIO("failed to set permissions on " + dir + ": " + err.Error())
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return LocalIO("failed to serialize config: " + err.Error())
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return LocalIO("failed to create temp file: " + err.Error())
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return LocalIO("failed to set temp file permissions: " + err.Error())
	}
	if _, err := tmp.Write(b); err != nil {
		cleanup()
		return LocalIO("failed to write temp file: " + err.Error())
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return LocalIO("failed to close temp file: " + err.Error())
	}
	if err := os.Rename(tmpName, p.ConfigFile()); err != nil {
		_ = os.Remove(tmpName)
		return LocalIO("failed to replace config file: " + err.Error())
	}
	return nil
}

// NormalizeBase canonicalizes a base URL: trimmed, http/https only, lowercase
// scheme+host, trailing slashes stripped. It is both the config map key and the
// binding-comparison key, so the two can never disagree over formatting alone.
func NormalizeBase(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", Usage("base URL must not be empty")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", Usage("invalid base URL: " + err.Error())
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", Usage("base URL must start with http:// or https://, got: " + raw)
	}
	if u.Host == "" {
		return "", Usage("base URL is missing a host, got: " + raw)
	}
	u.Scheme = scheme
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}
