package main

import (
	"testing"
)

const testBase = "https://placard.example.com"

func seedConfig(t *testing.T, home, base, token string) {
	t.Helper()
	cfg := &Config{Tokens: map[string]TokenEntry{
		base: {Token: token, Base: base, Name: "CLI on test", CreatedAt: "2026-08-01T00:00:00Z"},
	}}
	if err := SaveConfig(testPaths(home), cfg); err != nil {
		t.Fatalf("seedConfig: %v", err)
	}
}

// seedConfigEntry adds one more login to an existing store, for the cases that
// turn on how many there are.
func seedConfigEntry(t *testing.T, home, base, token string) {
	t.Helper()
	cfg, err := LoadConfig(testPaths(home))
	if err != nil {
		t.Fatalf("seedConfigEntry: %v", err)
	}
	cfg.Tokens[base] = TokenEntry{Token: token, Base: base, Name: "CLI on test", CreatedAt: "2026-08-01T00:00:00Z"}
	if err := SaveConfig(testPaths(home), cfg); err != nil {
		t.Fatalf("seedConfigEntry: %v", err)
	}
}

func TestCredentialPriorityFlagWinsEverything(t *testing.T) {
	home := t.TempDir()
	seedConfig(t, home, testBase, "pl_config")
	got, err := ResolveCredential(CredInput{
		Base: testBase, FlagToken: "pl_flag", EnvToken: "pl_env", Paths: testPaths(home),
	})
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if got.Token != "pl_flag" || got.Source != SourceFlag {
		t.Fatalf("got %+v, want pl_flag/flag", got)
	}
}

func TestCredentialPriorityEnvBeatsConfig(t *testing.T) {
	home := t.TempDir()
	seedConfig(t, home, testBase, "pl_config")
	got, err := ResolveCredential(CredInput{Base: testBase, EnvToken: "pl_env", Paths: testPaths(home)})
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if got.Token != "pl_env" || got.Source != SourceEnv {
		t.Fatalf("got %+v, want pl_env/env", got)
	}
}

// spec §4.1 rule 2: an explicit --base disables the unbound source, so a prod
// PAT in the environment can never be sent to a staging base.
func TestExplicitBaseRefusesUnboundSources(t *testing.T) {
	home := t.TempDir()
	_, err := ResolveCredential(CredInput{
		Base:         "https://staging.placard.example.com",
		BaseExplicit: true,
		FlagToken:    "",
		EnvToken:     "pl_prod_from_env",
		Paths:        testPaths(home),
	})
	if err == nil {
		t.Fatal("explicit --base must not fall back to PLACARD_TOKEN")
	}
	var e *Error
	if !asError(err, &e) || e.Code != "no_credentials" {
		t.Fatalf("want no_credentials, got %v", err)
	}
	if ExitCode(err) != 3 {
		t.Fatalf("exit = %d, want 3", ExitCode(err))
	}
}

// --token is the one exception: it is explicit per-invocation intent, and the
// spec keeps it usable with --base (CI passes both together).
func TestExplicitBaseStillAcceptsFlagToken(t *testing.T) {
	home := t.TempDir()
	got, err := ResolveCredential(CredInput{
		Base: "https://staging.placard.example.com", BaseExplicit: true,
		FlagToken: "pl_flag", Paths: testPaths(home),
	})
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if got.Source != SourceFlag {
		t.Fatalf("got %+v, want flag", got)
	}
}

func TestExplicitBaseUsesConfigEntryForThatBase(t *testing.T) {
	home := t.TempDir()
	staging := "https://staging.placard.example.com"
	cfg := &Config{Tokens: map[string]TokenEntry{
		testBase: {Token: "pl_prod", Base: testBase},
		staging:  {Token: "pl_staging", Base: staging},
	}}
	if err := SaveConfig(testPaths(home), cfg); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveCredential(CredInput{Base: staging, BaseExplicit: true, Paths: testPaths(home)})
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if got.Token != "pl_staging" {
		t.Fatalf("got %q, want pl_staging", got.Token)
	}
}

// spec §4.1 rule 1: a hand-edited config whose entry.base disagrees with its
// key is refused, not warned about.
func TestBaseBindingMismatchIsRefused(t *testing.T) {
	home := t.TempDir()
	cfg := &Config{Tokens: map[string]TokenEntry{
		testBase: {Token: "pl_prod", Base: "https://staging.placard.example.com"},
	}}
	if err := SaveConfig(testPaths(home), cfg); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveCredential(CredInput{Base: testBase, Paths: testPaths(home)})
	if err == nil {
		t.Fatal("mismatched binding must be refused")
	}
	var e *Error
	if !asError(err, &e) || e.Code != "base_mismatch" {
		t.Fatalf("want base_mismatch, got %v", err)
	}
	if ExitCode(err) != 3 {
		t.Fatalf("exit = %d, want 3", ExitCode(err))
	}
}

func TestNoCredentialsAtAll(t *testing.T) {
	home := t.TempDir()
	_, err := ResolveCredential(CredInput{Base: testBase, Paths: testPaths(home)})
	var e *Error
	if !asError(err, &e) || e.Code != "no_credentials" {
		t.Fatalf("want no_credentials, got %v", err)
	}
	if !contains(e.Message, "placard login") {
		t.Fatalf("message must point at login, got %q", e.Message)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
