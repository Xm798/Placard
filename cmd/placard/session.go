// cmd/placard/session.go
package main

import (
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// meResponse decodes GET /api/me. There is no DTO for it server-side (the
// handler returns a fiber.Map), so the shape lives here. On the PAT channel
// DisplayName and AvatarURL are ALWAYS empty.
type meResponse struct {
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
	AuthzID     string `json:"authz_id"`
}

func newLogoutCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Clear stored credentials (current base only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Without a base there is nothing to clear, and reporting success
			// would tell the user a credential is gone while it sits on disk.
			if err := env.RequireBase(); err != nil {
				return err
			}
			cfg, err := LoadConfig(env.Paths)
			if err != nil {
				return err
			}
			// Per-base: logging out of staging must not log you out of prod.
			delete(cfg.Tokens, env.Base)
			if cfg.DefaultBase == env.Base {
				cfg.DefaultBase = cfg.soleBase()
			}
			if err := SaveConfig(env.Paths, cfg); err != nil {
				return err
			}

			// The three unbound sources are outside this file's reach; saying so
			// avoids "I logged out but it still works" reports.
			if strings.TrimSpace(env.Getenv("PLACARD_TOKEN")) != "" {
				env.Out.Warnf("Note: the PLACARD_TOKEN environment variable is still set and will still be used; unset it manually\n")
			}
			env.Out.Printf("Cleared local credentials for %s\n", env.Base)
			return env.Out.Emit(map[string]any{"base": env.Base, "cleared": true})
		},
	}
}

func newWhoamiCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show current identity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := env.AuthedClient()
			if err != nil {
				return err
			}
			var me meResponse
			raw, err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/me", nil, nil, &me)
			if err != nil {
				return err
			}
			if env.Out.JSON {
				return env.Out.EmitRaw(raw)
			}
			env.Out.Printf("base        %s\n", env.Base)
			// Older servers and users without a cached profile may return an empty
			// display_name, so never render a blank name field.
			if me.DisplayName != "" {
				env.Out.Printf("name        %s\n", me.DisplayName)
			}
			env.Out.Printf("authz_id    %s\n", me.AuthzID)
			if me.DisplayName == "" {
				env.Out.Printf("\n(The server returned no display profile for this identity; only authz_id is shown)\n")
			}
			return nil
		},
	}
}
