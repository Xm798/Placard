// cmd/placard/versions.go
package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Xm798/placard/internal/dto"
)

// patchFileRequest is PATCH /api/files/:id. Both fields are pointers because
// the server requires AT LEAST ONE and treats absence as "leave alone";
// shared_version 0 is a meaningful value (follow latest), so a non-pointer int
// would be indistinguishable from "not set".
type patchFileRequest struct {
	SharedVersion *int    `json:"shared_version,omitempty"`
	Visibility    *string `json:"visibility,omitempty"`
}

func newVersionCmd(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Version history: list / pin / restore",
		RunE: func(c *cobra.Command, _ []string) error {
			return c.Help()
		},
	}
	cmd.AddCommand(newVersionLsCmd(env), newVersionPinCmd(env), newVersionRestoreCmd(env))
	return cmd
}

func newVersionLsCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "ls <id>",
		Short: "List version history (newest first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := env.AuthedClient()
			if err != nil {
				return err
			}
			var resp dto.FileVersionsResponse
			raw, err := c.DoJSON(cmd.Context(), http.MethodGet,
				"/api/files/"+url.PathEscape(args[0])+"/versions", nil, nil, &resp)
			if err != nil {
				return err
			}
			if env.Out.JSON {
				return env.Out.EmitRaw(raw)
			}
			printVersions(env, resp)
			return nil
		},
	}
}

func printVersions(env *Env, resp dto.FileVersionsResponse) {
	rows := make([][]string, 0, len(resp.Versions))
	for _, v := range resp.Versions {
		marker := ""
		switch {
		case v.Version == resp.LatestVersion && resp.SharedVersion == 0:
			marker = "← latest"
		case v.Version == resp.SharedVersion:
			marker = "← shared"
		case v.Version == resp.LatestVersion:
			marker = "← latest"
		}
		rows = append(rows, []string{
			strconv.Itoa(v.Version),
			v.Title,
			humanSize(v.SizeBytes),
			v.CreateTime.UTC().Format("2006-01-02"),
			marker,
		})
	}
	env.Out.Table([]string{"VERSION", "TITLE", "SIZE", "CREATED", ""}, rows)
	// shared_version 0 is the server's "follow latest" magic number; humans
	// never see the 0 (spec §2.1(3)).
	if resp.SharedVersion == 0 {
		env.Out.Printf("\nCurrently sharing: latest (version %d)\n", resp.LatestVersion)
	} else {
		env.Out.Printf("\nCurrently sharing: pinned version %d (latest is %d)\n", resp.SharedVersion, resp.LatestVersion)
	}
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// parsePinTarget maps the user-facing `latest` keyword onto the server's
// shared_version = 0. A literal 0 is rejected: it is an internal magic number,
// not something a user should type (spec §2.1(3)).
func parsePinTarget(s string) (int, error) {
	if strings.EqualFold(strings.TrimSpace(s), "latest") {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 {
		return 0, Usage("version must be a positive integer or \"latest\"; got: " + s)
	}
	return n, nil
}

func newVersionPinCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "pin <id> <n|latest>",
		Short: "Pin the share link to a version (latest = follow newest)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			target, err := parsePinTarget(args[1])
			if err != nil {
				return err
			}
			c, err := env.AuthedClient()
			if err != nil {
				return err
			}
			body := patchFileRequest{SharedVersion: &target}
			if _, err := c.DoJSON(cmd.Context(), http.MethodPatch,
				"/api/files/"+url.PathEscape(id), nil, body, nil); err != nil {
				return err
			}
			if target == 0 {
				env.Out.Printf("%s now follows latest\n", id)
			} else {
				env.Out.Printf("%s pinned to version %d\n", id, target)
			}
			return env.Out.Emit(map[string]any{"id": id, "shared_version": target})
		},
	}
}

func newVersionRestoreCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "restore <id> <n>",
		Short: "Re-publish an old version's content as a new version",
		Long: "Re-publishes an old version's content as a new version.\n\n" +
			"The response shape is identical to publish, and it shares the same hourly publish budget.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			n, err := strconv.Atoi(strings.TrimSpace(args[1]))
			if err != nil || n < 1 {
				return Usage("restore requires a concrete version number (positive integer); got: " + args[1])
			}
			c, err := env.AuthedClient()
			if err != nil {
				return err
			}
			var resp dto.PublishResponse
			raw, err := c.DoJSON(cmd.Context(), http.MethodPost,
				"/api/files/"+url.PathEscape(id)+"/versions/"+strconv.Itoa(n)+"/restore", nil, nil, &resp)
			if err != nil {
				return rateLimitHint(err, "reached the hourly publish limit (default 50/hour, shared with publish); please retry later")
			}
			if env.Out.JSON {
				return env.Out.EmitRaw(raw)
			}
			env.Out.Printf("Re-published version %d content as version %d\n", n, resp.Version)
			printPublishResult(env, resp)
			return nil
		},
	}
}
