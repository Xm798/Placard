// cmd/placard/files.go
package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Xm798/placard/internal/dto"
)

const (
	// lsPageSize is the server's maximum page_size: use it so auto-pagination
	// costs the fewest round trips.
	lsPageSize = 100
	// lsMaxPages caps auto-pagination at 5000 rows. Hitting it WARNS — silent
	// truncation would read as "that's all my pages" (spec §2.3).
	lsMaxPages = 50
)

type lsOpts struct {
	Limit    int
	Page     int
	PageSize int
}

func newLsCmd(env *Env) *cobra.Command {
	var opts lsOpts
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List your pages (auto-paginates by default)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			manual := cmd.Flags().Changed("page") || cmd.Flags().Changed("page-size")
			return runLs(cmd.Context(), env, opts, manual)
		},
	}
	f := cmd.Flags()
	f.IntVar(&opts.Limit, "limit", 0, "Take only the first n rows (stop once enough, no further paging)")
	f.IntVar(&opts.Page, "page", 1, "Escape hatch: explicit page number, disables auto-pagination")
	f.IntVar(&opts.PageSize, "page-size", 20, "Escape hatch: explicit page size (max 100), disables auto-pagination")
	return cmd
}

func runLs(ctx context.Context, env *Env, opts lsOpts, manual bool) error {
	c, err := env.AuthedClient()
	if err != nil {
		return err
	}

	// Escape hatch: one raw request, server body passed through untouched.
	if manual {
		q := url.Values{}
		q.Set("page", strconv.Itoa(opts.Page))
		q.Set("page_size", strconv.Itoa(opts.PageSize))
		var resp dto.FileListResponse
		raw, err := c.DoJSON(ctx, http.MethodGet, "/api/files", q, nil, &resp)
		if err != nil {
			return err
		}
		if env.Out.JSON {
			return env.Out.EmitRaw(raw)
		}
		printFileTable(env, resp.Files, resp.Total)
		return nil
	}

	files, total, err := fetchAllFiles(ctx, env, c, opts.Limit)
	if err != nil {
		return err
	}
	if env.Out.JSON {
		// page/page_size are meaningless after merging, so they are dropped
		// (spec §2.2 table): the shape is {"files":[...],"total":N}.
		return env.Out.Emit(struct {
			Files []dto.FileListItem `json:"files"`
			Total int64              `json:"total"`
		}{Files: files, Total: total})
	}
	printFileTable(env, files, total)
	return nil
}

// fetchAllFiles walks page=1,2,3... at page_size=100 until it has `total` rows
// (or `limit` rows when limit > 0). The server sorts by create_time DESC,id DESC
// with no cursor, so a page published mid-walk can duplicate or skip a boundary
// row — a known, accepted trade-off with no compensation (spec §2.3).
func fetchAllFiles(ctx context.Context, env *Env, c *Client, limit int) ([]dto.FileListItem, int64, error) {
	var all []dto.FileListItem
	var total int64
	for page := 1; page <= lsMaxPages; page++ {
		q := url.Values{}
		q.Set("page", strconv.Itoa(page))
		q.Set("page_size", strconv.Itoa(lsPageSize))
		var resp dto.FileListResponse
		if _, err := c.DoJSON(ctx, http.MethodGet, "/api/files", q, nil, &resp); err != nil {
			return nil, 0, err
		}
		total = resp.Total
		all = append(all, resp.Files...)
		if limit > 0 && len(all) >= limit {
			return all[:limit], total, nil
		}
		if len(resp.Files) == 0 || int64(len(all)) >= total {
			return all, total, nil
		}
		if page == lsMaxPages {
			env.Out.Warnf("warning: reached the auto-pagination cap of %d pages (%d rows); results may be incomplete. Use --page / --page-size to paginate manually\n",
				lsMaxPages, lsMaxPages*lsPageSize)
		}
	}
	return all, total, nil
}

func printFileTable(env *Env, files []dto.FileListItem, total int64) {
	rows := make([][]string, 0, len(files))
	for _, f := range files {
		shared := "latest" // shared_version 0 means "follow latest"; the magic
		if f.SharedVersion != 0 {
			shared = strconv.Itoa(f.SharedVersion) // number is never shown to humans
		}
		rows = append(rows, []string{
			f.ID,
			f.Title,
			strconv.Itoa(f.LatestVersion),
			shared,
			f.Visibility,
			strconv.FormatInt(f.ViewCount, 10),
			f.CreateTime.UTC().Format("2006-01-02"),
		})
	}
	env.Out.Table([]string{"ID", "TITLE", "VERSION", "SHARED", "VISIBILITY", "VIEWS", "CREATED"}, rows)
	env.Out.Printf("\nTotal: %d pages\n", total)
}

func newRmCmd(env *Env) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm <id>",
		Short: "Delete a page (asks for confirmation; use -y to skip)",
		Long: "Deletes a page. The link and EVERY version die immediately and cannot be recovered.\n\n" +
			"At a terminal this asks for confirmation first. When stdin is not a terminal (scripts,\n" +
			"agents, CI, or a pipe) there is nobody to ask, so -y is required rather than assumed —\n" +
			"piping `echo y` deliberately does not count as consent.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRm(cmd.Context(), env, args[0], yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt (required when there is no terminal)")
	return cmd
}

func runRm(ctx context.Context, env *Env, id string, yes bool) error {
	c, err := env.AuthedClient()
	if err != nil {
		return err
	}

	if !yes {
		// Deletion is unrecoverable, so nobody to ask means refusal, not assumed
		// consent. Usage (exit 2) rather than share_add's Conflict (exit 1): no
		// data condition failed here, the invocation is just incomplete.
		if !env.CanPrompt() {
			return Usage("refusing to delete without confirmation: stdin is not a terminal. Re-run with -y to confirm")
		}
		// Echoing back the id the user just typed confirms nothing, so name the
		// page. Same owner-only 404 as DELETE, so this leaks no existence.
		if !confirm(env, rmPrompt(ctx, c, id)) {
			return Conflict("aborted; nothing was deleted")
		}
	}

	// DELETE returns 204 with no body, so --json gets a synthesized
	// minimal object (spec §2.2 rule 2's only exception).
	if _, err := c.DoJSON(ctx, http.MethodDelete, "/api/files/"+url.PathEscape(id), nil, nil, nil); err != nil {
		return err
	}
	env.Out.Printf("Deleted: %s\n", id)
	return env.Out.Emit(map[string]any{"id": id, "deleted": true})
}

// rmPrompt describes what is about to be destroyed. A failed lookup degrades to
// the bare id rather than aborting: the prompt is an aid, and the DELETE is
// still authoritative about whether the id exists.
func rmPrompt(ctx context.Context, c *Client, id string) string {
	subject := id
	var resp dto.FileVersionsResponse
	if _, err := c.DoJSON(ctx, http.MethodGet,
		"/api/files/"+url.PathEscape(id)+"/versions", nil, nil, &resp); err == nil && len(resp.Versions) > 0 {
		plural := ""
		if len(resp.Versions) > 1 {
			plural = "s"
		}
		// %q keeps CJK readable but escapes control bytes, so a title cannot
		// smuggle ANSI escapes into the prompt.
		subject = fmt.Sprintf("%q (%s, %d version%s)", resp.Versions[0].Title, id, len(resp.Versions), plural)
	}
	return fmt.Sprintf("Delete %s permanently? This cannot be undone. [y/N] ", subject)
}

// confirm asks a yes/no question on stderr and reads one line from stdin.
// Callers must gate it on env.CanPrompt(). Anything but y/yes is a decline, so
// a stray control byte or EOF fails safe.
func confirm(env *Env, prompt string) bool {
	env.Out.Warnf("%s", prompt)
	line, err := bufio.NewReader(env.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
