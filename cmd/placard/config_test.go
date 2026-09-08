package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeBase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://placard.example.com", "https://placard.example.com"},
		{"https://placard.example.com/", "https://placard.example.com"},
		{"https://placard.example.com///", "https://placard.example.com"},
		{"HTTPS://PLACARD.EXAMPLE.COM", "https://placard.example.com"},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{" https://placard.example.com ", "https://placard.example.com"},
	}
	for _, tc := range cases {
		got, err := NormalizeBase(tc.in)
		if err != nil {
			t.Fatalf("NormalizeBase(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("NormalizeBase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeBaseRejectsBadInput(t *testing.T) {
	for _, in := range []string{"", "placard.example.com", "ftp://page", "https://"} {
		if _, err := NormalizeBase(in); err == nil {
			t.Errorf("NormalizeBase(%q) must fail", in)
		} else if ExitCode(err) != 2 {
			t.Errorf("NormalizeBase(%q) exit = %d, want 2 (usage)", in, ExitCode(err))
		}
	}
}

// testPaths resolves paths the way NewEnv does, with an empty environment: the
// stub Getenv is what keeps a developer's real $XDG_CONFIG_HOME from steering
// the suite at their actual credential file.
func testPaths(home string) Paths { return pathsWithXDG(home, "") }

func pathsWithXDG(home, xdg string) Paths {
	return NewPaths(home, func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return xdg
		}
		return ""
	})
}

func TestLoadConfigMissingFileIsEmpty(t *testing.T) {
	home := t.TempDir()
	cfg, err := LoadConfig(testPaths(home))
	if err != nil {
		t.Fatalf("LoadConfig on missing file must succeed, got %v", err)
	}
	if len(cfg.Tokens) != 0 {
		t.Fatalf("want empty config, got %v", cfg.Tokens)
	}
}

func TestNewPathsResolution(t *testing.T) {
	const home, xdg = "/home/u", "/opt/cfg"
	dotConfig := filepath.Join(home, ".config", "placard")
	for _, tc := range []struct {
		name, home, xdg string
		wantDir         string
	}{
		{"default is ~/.config", home, "", dotConfig},
		// A relative $XDG_CONFIG_HOME is invalid per the spec and must be ignored;
		// honoring one would drop credentials into whatever directory the CLI ran in.
		{"relative XDG ignored", home, "relative/config", dotConfig},
		{"absolute XDG wins", home, xdg, filepath.Join(xdg, "placard")},
		// os.UserHomeDir can fail; "" must not degrade into a relative path.
		{"no home, no XDG", "", "", ""},
		{"no home, XDG set", "", xdg, filepath.Join(xdg, "placard")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := pathsWithXDG(tc.home, tc.xdg)
			if got := p.ConfigDir(); got != tc.wantDir {
				t.Errorf("ConfigDir() = %q, want %q", got, tc.wantDir)
			}
			wantFile := ""
			if tc.wantDir != "" {
				wantFile = filepath.Join(tc.wantDir, "config.json")
			}
			if got := p.ConfigFile(); got != wantFile {
				t.Errorf("ConfigFile() = %q, want %q", got, wantFile)
			}
		})
	}
}

// The whole point of the XDG move: nothing is written straight into $HOME.
func TestSaveConfigPutsNothingInHomeRoot(t *testing.T) {
	home := t.TempDir()
	if err := SaveConfig(testPaths(home), &Config{Tokens: map[string]TokenEntry{}}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != ".config" {
			t.Errorf("home must contain only .config, found %q", e.Name())
		}
	}
}

func TestSaveConfigUnderXDGLeavesDotConfigAlone(t *testing.T) {
	home, xdg := t.TempDir(), t.TempDir()
	if err := SaveConfig(pathsWithXDG(home, xdg), &Config{Tokens: map[string]TokenEntry{}}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
		t.Error("with $XDG_CONFIG_HOME set, ~/.config must not be created")
	}
}

// With nowhere safe to write, refusing beats emitting a relative path.
func TestSaveConfigWithoutAnyRootIsLocalIO(t *testing.T) {
	p := pathsWithXDG("", "")
	cfg, err := LoadConfig(p)
	if err != nil || len(cfg.Tokens) != 0 {
		t.Fatalf("LoadConfig must degrade to empty, got %v / %v", cfg, err)
	}
	var e *Error
	if err := SaveConfig(p, &Config{Tokens: map[string]TokenEntry{}}); !asError(err, &e) || e.Code != "local_io" {
		t.Fatalf("want local_io, got %v", err)
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	cfg := &Config{Tokens: map[string]TokenEntry{
		"https://placard.example.com": {
			Token: "pl_abc", Base: "https://placard.example.com",
			Name: "CLI on mac", CreatedAt: "2026-08-01T00:00:00Z",
		},
	}}
	if err := SaveConfig(testPaths(home), cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err := LoadConfig(testPaths(home))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	entry, ok := got.Tokens["https://placard.example.com"]
	if !ok || entry.Token != "pl_abc" || entry.Base != "https://placard.example.com" {
		t.Fatalf("round trip lost data: %+v", got.Tokens)
	}
}

func TestSaveConfigPermissions(t *testing.T) {
	home := t.TempDir()
	p := testPaths(home)
	if err := SaveConfig(p, &Config{Tokens: map[string]TokenEntry{}}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	dirInfo, err := os.Stat(p.ConfigDir())
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o, want 700", perm)
	}
	fileInfo, err := os.Stat(p.ConfigFile())
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 600", perm)
	}
}

func TestSaveConfigLeavesNoTempFile(t *testing.T) {
	home := t.TempDir()
	p := testPaths(home)
	for i := 0; i < 3; i++ {
		if err := SaveConfig(p, &Config{Tokens: map[string]TokenEntry{}}); err != nil {
			t.Fatalf("SaveConfig: %v", err)
		}
	}
	entries, err := os.ReadDir(p.ConfigDir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	// Assert on temp-file residue specifically, not on the file count: the config
	// dir legitimately holds siblings (the pre-CLI token file until it is absorbed).
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("atomic write left a temp file behind: %s", e.Name())
		}
	}
	if _, serr := os.Stat(p.ConfigFile()); serr != nil {
		t.Fatalf("config.json must exist after SaveConfig: %v", serr)
	}
}

func TestLoadConfigMalformedIsLocalIO(t *testing.T) {
	home := t.TempDir()
	p := testPaths(home)
	if err := os.MkdirAll(p.ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(p)
	if err == nil {
		t.Fatal("malformed config must be an error, not a silent reset")
	}
	var e *Error
	if !asError(err, &e) || e.Code != "local_io" {
		t.Fatalf("want local_io, got %v", err)
	}
}
