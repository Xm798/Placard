// cmd/placard/login.go
package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Xm798/placard/internal/dto"
)

const (
	// pollMaxInterval caps the slow_down ratchet (spec §3.8).
	pollMaxInterval = 30 * time.Second
	// pollNetworkRetries is the number of CONSECUTIVE transient failures
	// tolerated before giving up, with 1s/2s/4s backoff.
	pollNetworkRetries = 3
	// pollGrace is added to expires_in so the last poll still lands inside the
	// server's TTL window.
	pollGrace = 10 * time.Second
)

// devicePoller polls POST /auth/device/token. Everything time-related is
// injected so the ratchet and the timeout are testable without real sleeping.
type devicePoller struct {
	Client      *Client
	Interval    time.Duration
	MaxInterval time.Duration
	Deadline    time.Time
	Now         func() time.Time
	Sleep       func(time.Duration)
}

// Poll runs until the code is approved, expires, the deadline passes, the
// context is cancelled, or three consecutive transport failures pile up.
//
// The four flow states arrive as HTTP 200 with a status field; only genuine
// errors (rate limiting, Redis down, missing route) use the error envelope.
func (p *devicePoller) Poll(ctx context.Context, deviceCode string) (*dto.DeviceTokenResponse, error) {
	interval := p.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	maxInterval := p.MaxInterval
	if maxInterval <= 0 {
		maxInterval = pollMaxInterval
	}
	failures := 0

	for {
		if err := ctx.Err(); err != nil {
			return nil, &Error{Code: "network", Message: "canceled", Exit: 1}
		}
		if p.Now().After(p.Deadline) {
			return nil, &Error{
				Code:    "network",
				Message: "timed out waiting for authorization (the code is valid for 180 seconds). Run: placard login again",
				Exit:    3,
			}
		}
		p.Sleep(interval)
		if err := ctx.Err(); err != nil {
			return nil, &Error{Code: "network", Message: "canceled", Exit: 1}
		}

		var resp dto.DeviceTokenResponse
		_, err := p.Client.DoJSON(ctx, http.MethodPost, "/auth/device/token", nil,
			dto.DeviceTokenRequest{DeviceCode: deviceCode}, &resp)
		if err != nil {
			// A missing route means the server predates the device flow: that is
			// terminal, retrying cannot help.
			var e *Error
			if asError(err, &e) && (e.Code == "server_too_old" || e.Code == "unauthorized") {
				return nil, err
			}
			failures++
			if failures >= pollNetworkRetries+1 {
				return nil, err
			}
			// 1s, 2s, 4s
			p.Sleep(time.Duration(1<<(failures-1)) * time.Second)
			continue
		}
		failures = 0

		switch resp.Status {
		case "approved":
			return &resp, nil
		case "expired":
			return nil, &Error{
				Code:    "expired",
				Message: "authorization code expired (valid for 180 seconds). Run: placard login again",
				Exit:    3,
			}
		case "slow_down":
			// Ratchet: multiply by 1.5, cap, and never lower again.
			interval = interval * 3 / 2
			if interval > maxInterval {
				interval = maxInterval
			}
		case "pending":
			// keep the current interval
		default:
			// An unknown status means the CLI cannot safely continue; conflict is
			// the "CLI refuses to proceed" code (see the error-code table).
			return nil, Conflict("server returned an unknown authorization status: " + resp.Status)
		}
	}
}

func newLoginCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate via browser and save a PAT",
		Long: "Logs in via the device-code flow: the browser opens on a confirmation page that already carries the authorization code, so you only have to confirm.\n\n" +
			"Verify before confirming: if the code shown in the browser is not the one printed here, close the page immediately — someone else started that login.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLogin(cmd.Context(), env)
		},
	}
}

func runLogin(ctx context.Context, env *Env) error {
	anon, err := env.AnonClient()
	if err != nil {
		return err
	}

	// The hostname becomes the token's name on /settings ("CLI on <host>"), so
	// a user with several machines can tell their tokens apart and spot one
	// they did not create. Report it as-is: sanitizing is the SERVER's job
	// (it does not trust this value), and doing it here too would only make the
	// two implementations drift. An error just means no hostname — the server
	// falls back to "unknown".
	host, herr := os.Hostname()
	if herr != nil {
		host = ""
	}

	var code dto.DeviceCodeResponse
	if _, err := anon.DoJSON(ctx, http.MethodPost, "/auth/device/code", nil,
		dto.DeviceCodeRequest{Hostname: host}, &code); err != nil {
		const fallback = " Contact your administrator to upgrade or configure the server, or provide a PAT created on the web /settings page via --token / PLACARD_TOKEN."
		var e *Error
		if asError(err, &e) && e.Code == "permission_denied" {
			return ServerError(e.Code,
				"The Placard server rejected CLI login (permission_denied). This is a server-side compatibility or configuration problem, not a local file permission error."+fallback, e.Exit)
		}
		return asServerTooOld(err,
			"This Placard server does not support CLI login (missing device-code endpoint)."+fallback, 3)
	}

	// The user_code is still printed even though the URL carries it: it is what
	// the user compares against the browser before confirming. The device_code
	// is NEVER printed — whoever holds it can redeem the token (spec §3.4).
	//
	// OpenURI owns the complete-vs-bare choice (see its doc); this only picks
	// wording to match whichever page the user will land on.
	openURI := code.OpenURI()
	env.Out.Warnf("\n  Authorization code: %s\n\n", env.Out.Bold(code.UserCode))
	if code.VerificationURIComplete != "" {
		env.Out.Warnf("Confirm in your browser: %s\n", openURI)
	} else {
		env.Out.Warnf("Open %s in your browser, type the code above, and confirm.\n", openURI)
	}
	env.Out.Warnf("Verify the code shown in the browser matches the one here; if it does not, close the page immediately.\n\n")

	if opened, _ := env.OpenURL(env, openURI); !opened {
		env.Out.Warnf("No browser available; open the link above manually.\n")
	}

	expiresIn := time.Duration(code.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = 180 * time.Second
	}
	interval := time.Duration(code.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	poller := &devicePoller{
		Client:      anon,
		Interval:    interval,
		MaxInterval: pollMaxInterval,
		Deadline:    env.Now().Add(expiresIn + pollGrace),
		Now:         env.Now,
		Sleep:       env.Sleep,
	}
	tok, err := poller.Poll(ctx, code.DeviceCode)
	if err != nil {
		return err
	}

	cfg, err := LoadConfig(env.Paths)
	if err != nil {
		return err
	}
	// The server just logged into becomes the one later commands talk to when
	// they are given none. Logging into a second instance repoints it; the
	// tokens of the first are untouched and still reachable with --base.
	cfg.DefaultBase = env.Base
	cfg.Tokens[env.Base] = TokenEntry{
		Token:     tok.Token,
		Base:      env.Base, // the binding checked before every request (rule 1)
		Name:      tok.Name,
		CreatedAt: env.Now().UTC().Format(time.RFC3339),
	}
	if err := SaveConfig(env.Paths, cfg); err != nil {
		return err
	}

	// Confirm with the freshly stored token so a broken credential surfaces now
	// rather than on the user's next command.
	authed := NewClient(env.Base, tok.Token, env.HTTP)
	var me meResponse
	if _, err := authed.DoJSON(ctx, http.MethodGet, "/api/me", nil, nil, &me); err != nil {
		return err
	}

	env.Out.Printf("Login successful: %s\n", env.Base)
	env.Out.Printf("  authz_id    %s\n", me.AuthzID)
	env.Out.Printf("  token name  %s\n", tok.Name)
	// The lifetime is picked in the browser, so the terminal is where it gets
	// verified: printing it is how the user notices they approved 180 days when
	// they meant 1. Omitted when absent rather than printed as "never" — a server
	// predating the field is not a server granting an eternal token.
	if tok.ExpiresAt != nil {
		env.Out.Printf("  expires at  %s\n", formatExpiry(*tok.ExpiresAt))
	}
	// Non-empty: SaveConfig above already failed out if the path was unresolvable.
	env.Out.Printf("  credentials saved to %s (mode 0600)\n", env.Paths.ConfigFile())
	// The token plaintext is deliberately absent from the JSON envelope.
	out := map[string]any{
		"base":       env.Base,
		"authz_id":   me.AuthzID,
		"token_name": tok.Name,
	}
	if tok.ExpiresAt != nil {
		out["expires_at"] = tok.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return env.Out.Emit(out)
}
