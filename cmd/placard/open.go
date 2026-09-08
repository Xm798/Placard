// cmd/placard/open.go
package main

import (
	"github.com/spf13/cobra"
)

func newOpenCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "open <id>",
		Short: "Open the share link in a local browser",
		Long: "Opens <base>/s/<id> in the local browser.\n\n" +
			"Note: /s/* is browser-only (a PAT can never read page content), so the CLI can only launch the browser,\n" +
			"not print the page to the terminal. In a headless environment it prints the link and still exits 0.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			shareURL := env.Base + "/s/" + id
			opened, err := env.OpenURL(env, shareURL)
			if err != nil {
				return err
			}
			// The URL goes to stdout in human mode either way: degradation is a
			// normal outcome, not a failure.
			env.Out.Printf("%s\n", shareURL)
			if !opened {
				env.Out.Warnf("No browser available; printed the link instead\n")
			}
			return env.Out.Emit(map[string]any{"id": id, "url": shareURL, "opened": opened})
		},
	}
}
