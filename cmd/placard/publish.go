// cmd/placard/publish.go
package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/htmlmeta"
)

type publishOpts struct {
	ID         string
	Title      string
	Expiry     string
	Visibility string
	Password   string
	Open       bool
}

func newPublishCmd(env *Env) *cobra.Command {
	var opts publishOpts
	cmd := &cobra.Command{
		Use:   "publish <file.html>",
		Short: "Publish a self-contained HTML file",
		Long: "Publishes a self-contained HTML file and returns the share link.\n\n" +
			"Note: the page's own <title> always takes precedence over --title — --title only applies when the page has no <title>.\n" +
			"--password auto returns a 6-digit share code once; it is shown again only in the web UI's share dialog.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPublish(cmd.Context(), env, args[0], opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.ID, "id", "", "Existing page id: update in place (link unchanged, appends a new version). With --id, --expiry / --visibility / --password are silently ignored by the server")
	f.StringVar(&opts.Title, "title", "", "Title. Ignored when the page has its own <title>")
	f.StringVar(&opts.Expiry, "expiry", "", "Expiry: <n>d|w|y, or never/permanent (default: never expires)")
	f.StringVar(&opts.Visibility, "visibility", "", "Visibility: private|link (default: account preference)")
	f.StringVar(&opts.Password, "password", "", `Gate the page behind a share code. Only "auto" is accepted — the server generates a 6-digit code and prints it once`)
	f.BoolVar(&opts.Open, "open", false, "Open in browser after publishing")
	return cmd
}

func runPublish(ctx context.Context, env *Env, path string, opts publishOpts) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return LocalIO("failed to read " + path + ": " + err.Error())
	}

	// The --title trap (spec §2.1(1)): the server's versionTitle always prefers
	// the page's own <title>. Detect it locally with the SHARED package so the
	// regex / unescape / 255-rune truncation can never drift from the server's.
	// This warning never blocks and never changes the exit code.
	if opts.Title != "" {
		if pageTitle := htmlmeta.Title(body); pageTitle != "" && pageTitle != opts.Title {
			env.Out.Warnf("warning: the page has its own <title> %q; the server will prefer it, so --title %q has no effect\n",
				pageTitle, opts.Title)
		}
	}
	if opts.ID != "" && (opts.Expiry != "" || opts.Visibility != "" || opts.Password != "") {
		env.Out.Warnf("warning: with --id the server silently ignores --expiry / --visibility / --password; the page keeps its existing expiry, visibility and share code\n")
	}

	c, err := env.AuthedClient()
	if err != nil {
		return err
	}
	fields := map[string]string{
		"id":         opts.ID,
		"title":      opts.Title,
		"expiry":     opts.Expiry,
		"visibility": opts.Visibility,
		"password":   opts.Password,
	}
	var resp dto.PublishResponse
	raw, err := c.DoMultipart(ctx, "/api/publish", fields, filepath.Base(path), body, &resp)
	if err != nil {
		return rateLimitHint(err, "reached the hourly publish limit (default 50/hour); please retry later")
	}

	if env.Out.JSON {
		if err := env.Out.EmitRaw(raw); err != nil {
			return err
		}
	} else {
		printPublishResult(env, resp)
	}

	if opts.Open {
		// Degradation never affects the publish verdict — the page is published.
		opened, oerr := env.OpenURL(env, resp.URL)
		if oerr != nil || !opened {
			env.Out.Warnf("No browser available; printed the link: %s\n", resp.URL)
		}
	}
	return nil
}

func printPublishResult(env *Env, resp dto.PublishResponse) {
	expires := "never expires"
	if resp.ExpiresAt != nil {
		expires = formatExpiry(*resp.ExpiresAt)
	}
	env.Out.Printf("Published: %s\n", env.Out.Bold(resp.URL))
	env.Out.Printf("  id       %s\n", resp.ID)
	env.Out.Printf("  title    %s\n", resp.Title)
	env.Out.Printf("  version  %d\n", resp.Version)
	env.Out.Printf("  expires  %s\n", expires)
	// Only ever present on the publish that minted it — the server never
	// returns the code again, so this line is the one chance to record it.
	if resp.ShareCode != "" {
		env.Out.Printf("  code     %s (visitors must enter this to open the page)\n", resp.ShareCode)
	}
}
