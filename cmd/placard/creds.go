// cmd/placard/creds.go
package main

import "strings"

// CredSource records which of the three priority levels produced the token, so
// a surprising identity is traceable when resolution is under investigation.
type CredSource string

const (
	SourceFlag   CredSource = "flag"
	SourceEnv    CredSource = "env"
	SourceConfig CredSource = "config"
)

type Credential struct {
	Token  string
	Source CredSource
}

// CredInput is everything credential resolution may look at. BaseExplicit is
// true ONLY when --base was passed on the command line: PLACARD_URL alone
// counts as "not explicit" so the existing agent flows (which set
// PLACARD_URL + PLACARD_TOKEN together) keep working (spec §4.1 rule 2).
type CredInput struct {
	Base         string // already normalized
	BaseExplicit bool
	FlagToken    string
	EnvToken     string
	Paths        Paths
}

// ResolveCredential implements the three-level priority with two guards:
//
//	rule 1 — a config entry must be bound to the effective base; a mismatch is
//	         REFUSED (base_mismatch), never warned about;
//	rule 2 — an explicit --base disables the one unbound source (PLACARD_TOKEN),
//	         so a prod PAT cannot leak into a staging access log.
//	         --token stays allowed: it is per-invocation intent, and CI passes
//	         --base and --token together.
func ResolveCredential(in CredInput) (*Credential, error) {
	if t := strings.TrimSpace(in.FlagToken); t != "" {
		return &Credential{Token: t, Source: SourceFlag}, nil
	}
	if !in.BaseExplicit {
		if t := strings.TrimSpace(in.EnvToken); t != "" {
			return &Credential{Token: t, Source: SourceEnv}, nil
		}
	}

	cfg, err := LoadConfig(in.Paths)
	if err != nil {
		return nil, err
	}
	if entry, ok := cfg.Tokens[in.Base]; ok && strings.TrimSpace(entry.Token) != "" {
		bound := entry.Base
		if bound == "" {
			bound = in.Base // pre-binding entries are treated as bound to their key
		}
		normalized, nerr := NormalizeBase(bound)
		if nerr != nil || normalized != in.Base {
			return nil, BaseMismatch(
				"the stored token is bound to " + bound + " but the effective base is " + in.Base +
					"; refused to send. Run: placard login --base " + in.Base)
		}
		return &Credential{Token: strings.TrimSpace(entry.Token), Source: SourceConfig}, nil
	}

	if in.BaseExplicit {
		return nil, NoCredentials(
			"not logged in for " + in.Base + ". An explicit --base disables fallback to PLACARD_TOKEN" +
				" (to prevent sending one environment's token to another)." +
				" Run: placard login --base " + in.Base)
	}
	return nil, NoCredentials(
		"no credentials found. Run: placard login, or set the PLACARD_TOKEN environment variable")
}
