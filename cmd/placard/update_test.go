package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestIsLocalBuildCoversAllFourVersionShapes(t *testing.T) {
	local := []string{
		"dev",               // go run / bare go build
		"",                  // ldflags forgotten
		"master-3577643",    // version.Public() leaking in (defensive)
		"v0.6.3-2-g3577643", // make build via git describe
		"v0.6.3-2-g3577643-dirty",
		"0.1.0-3-gabcdef", // describe without the v prefix
		"v0.6.3",          // server tag shape: CLI releases never carry v
	}
	for _, v := range local {
		if !isLocalBuild(v) {
			t.Errorf("isLocalBuild(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"1.2.3", "0.1.0", "10.0.1"} {
		if isLocalBuild(v) {
			t.Errorf("isLocalBuild(%q) = true, want false (goreleaser injects a bare semver)", v)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2.4", "1.2.3", 1},
		{"1.2.3", "1.2.4", -1},
		{"1.10.0", "1.9.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.2", "1.2.0", 0},
		{"1.2.3-rc.1", "1.2.3", -1},
		{"1.2.3", "1.2.3-rc.1", 1},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

type fakeUpdater struct {
	latest    string
	latestErr error
	applied   []string
	applyErr  error
}

func (f *fakeUpdater) Latest(context.Context) (string, error) { return f.latest, f.latestErr }
func (f *fakeUpdater) Apply(_ context.Context, v string) error {
	if f.applyErr != nil {
		return f.applyErr
	}
	f.applied = append(f.applied, v)
	return nil
}

func withUpdater(t *testing.T, u Updater, current string) (*Env, *strings.Builder, *strings.Builder) {
	t.Helper()
	prevNew, prevVer := newUpdater, currentVersion
	newUpdater = func(*Env) Updater { return u }
	currentVersion = func() string { return current }
	t.Cleanup(func() { newUpdater, currentVersion = prevNew, prevVer })

	home := t.TempDir()
	var stdout, stderr strings.Builder
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	return env, &stdout, &stderr
}

func TestUpdateInstallsNewerVersion(t *testing.T) {
	u := &fakeUpdater{latest: "1.3.0"}
	env, _, _ := withUpdater(t, u, "1.2.0")
	if err := runCLI(env, "--json", "update"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(u.applied) != 1 || u.applied[0] != "1.3.0" {
		t.Fatalf("applied = %v", u.applied)
	}
}

func TestUpdateAlreadyLatestIsSuccess(t *testing.T) {
	u := &fakeUpdater{latest: "1.2.0"}
	env, stdout, _ := withUpdater(t, u, "1.2.0")
	if err := runCLI(env, "--json", "update"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(u.applied) != 0 {
		t.Fatal("nothing to do")
	}
	var got struct {
		Current string `json:"current"`
		Latest  string `json:"latest"`
		Updated bool   `json:"updated"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &got); err != nil {
		t.Fatalf("unmarshal: %v (%q)", err, stdout.String())
	}
	if got.Updated || got.Current != "1.2.0" || got.Latest != "1.2.0" || got.Reason == "" {
		t.Fatalf("got %+v", got)
	}
}

// spec §8 hardening 1: refuse downgrades, and name BOTH versions.
func TestUpdateRefusesDowngrade(t *testing.T) {
	u := &fakeUpdater{latest: "1.1.0"}
	env, _, _ := withUpdater(t, u, "1.2.0")
	err := runCLI(env, "update")
	if err == nil {
		t.Fatal("a lower remote version must be refused, not silently installed")
	}
	if !strings.Contains(err.Error(), "1.1.0") || !strings.Contains(err.Error(), "1.2.0") {
		t.Fatalf("both versions must be named, got %q", err.Error())
	}
	if len(u.applied) != 0 {
		t.Fatal("nothing may be installed")
	}
}

func TestUpdateForceAllowsDowngrade(t *testing.T) {
	u := &fakeUpdater{latest: "1.1.0"}
	env, _, _ := withUpdater(t, u, "1.2.0")
	if err := runCLI(env, "update", "--force"); err != nil {
		t.Fatalf("--force is an explicit human request: %v", err)
	}
	if len(u.applied) != 1 || u.applied[0] != "1.1.0" {
		t.Fatalf("applied = %v", u.applied)
	}
}

// spec §8.2: a local build skips the check and exits 0.
func TestUpdateOnLocalBuildSkipsAndExitsZero(t *testing.T) {
	u := &fakeUpdater{latest: "1.3.0"}
	env, stdout, _ := withUpdater(t, u, "v0.6.3-2-g3577643")
	if err := runCLI(env, "--json", "update"); err != nil {
		t.Fatalf("local build must exit 0, got %v", err)
	}
	if len(u.applied) != 0 {
		t.Fatal("a local build must not be replaced without --force")
	}
	var got struct {
		Updated bool   `json:"updated"`
		Reason  string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(stdout.String()), &got)
	if got.Updated || !strings.Contains(got.Reason, "local build") {
		t.Fatalf("got %+v", got)
	}
}

func TestUpdateLocalBuildWithForceInstalls(t *testing.T) {
	u := &fakeUpdater{latest: "1.3.0"}
	env, _, _ := withUpdater(t, u, "dev")
	if err := runCLI(env, "update", "--force"); err != nil {
		t.Fatalf("update --force: %v", err)
	}
	if len(u.applied) != 1 {
		t.Fatalf("applied = %v", u.applied)
	}
}

// The distribution line has not landed yet: the default updater must say so
// clearly rather than panic on a nil interface.
func TestUpdateWithoutADistributionBackendIsAClearError(t *testing.T) {
	prevNew, prevVer := newUpdater, currentVersion
	newUpdater = defaultNewUpdater
	// Must pin a release version: under `go test` the injected version is "dev",
	// which isLocalBuild short-circuits before the updater is ever consulted.
	currentVersion = func() string { return "1.2.0" }
	t.Cleanup(func() { newUpdater, currentVersion = prevNew, prevVer })

	home := t.TempDir()
	var stdout, stderr strings.Builder
	env := NewEnv(&stdout, &stderr, strings.NewReader(""), home,
		func(string) string { return "" }, testClock, false)
	err := runCLI(env, "update")
	if err == nil || !strings.Contains(err.Error(), "self-update is not enabled") {
		t.Fatalf("got %v", err)
	}
	if ExitCode(err) != 1 {
		t.Fatalf("exit = %d, want 1", ExitCode(err))
	}
}

func TestUpdateApplyFailurePropagates(t *testing.T) {
	u := &fakeUpdater{latest: "1.3.0", applyErr: errors.New("sha256 verification failed")}
	env, _, _ := withUpdater(t, u, "1.2.0")
	err := runCLI(env, "update")
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("got %v", err)
	}
}
