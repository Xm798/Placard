// cmd/placard/output.go
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"
	"unicode/utf8"
)

// defaultTableWidth is the fixed rendering width. The CLI NEVER reads the real
// terminal width: golden tests must not depend on the runner's terminal
// (spec §12.2 item 4).
const defaultTableWidth = 100

// Out is the single output sink. Two invariants (spec §2.2):
//   - in --json mode stdout carries exactly one valid JSON value, success or
//     failure; every human-readable line is suppressed or routed to stderr;
//   - warnings ALWAYS go to stderr, in both modes.
type Out struct {
	Stdout io.Writer
	Stderr io.Writer
	JSON   bool
	Color  bool
	Width  int
}

func NewOut(stdout, stderr io.Writer, jsonMode, color bool, width int) *Out {
	if width <= 0 {
		width = defaultTableWidth
	}
	return &Out{Stdout: stdout, Stderr: stderr, JSON: jsonMode, Color: color, Width: width}
}

// Emit writes v as the command's JSON result. No-op in human mode, so callers
// can always call it right after their Printf block.
func (o *Out) Emit(v any) error {
	if !o.JSON {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return LocalIO("cannot encode JSON output: " + err.Error())
	}
	_, err = fmt.Fprintln(o.Stdout, string(b))
	return err
}

// EmitRaw passes the server's response bytes through untouched (spec §2.2 rule
// 2: no field renaming, no magic-number translation). It validates first so a
// non-JSON body (proxy error page, HTML) can never break the stdout contract.
func (o *Out) EmitRaw(raw []byte) error {
	if !o.JSON {
		return nil
	}
	if !json.Valid(raw) {
		return Network("server response is not valid JSON; refused to write to stdout")
	}
	_, err := fmt.Fprintln(o.Stdout, string(raw))
	return err
}

// Printf writes a human line to stdout; suppressed entirely in --json mode.
func (o *Out) Printf(format string, a ...any) {
	if o.JSON {
		return
	}
	fmt.Fprintf(o.Stdout, format, a...)
}

// Warnf always writes to stderr, in both modes.
func (o *Out) Warnf(format string, a ...any) {
	fmt.Fprintf(o.Stderr, format, a...)
}

// Table renders a fixed-width column table. Suppressed in --json mode.
// Column widths come from content (display width, rune-aware), padded with two
// spaces; the last column is never padded so trailing whitespace stays out of
// golden files.
func (o *Out) Table(headers []string, rows [][]string) {
	if o.JSON {
		return
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && utf8.RuneCountInString(cell) > widths[i] {
				widths[i] = utf8.RuneCountInString(cell)
			}
		}
	}
	writeRow := func(cells []string) {
		for i, cell := range cells {
			if i == len(cells)-1 {
				fmt.Fprint(o.Stdout, cell)
				continue
			}
			pad := widths[i] - utf8.RuneCountInString(cell) + 2
			fmt.Fprint(o.Stdout, cell)
			for p := 0; p < pad; p++ {
				fmt.Fprint(o.Stdout, " ")
			}
		}
		fmt.Fprintln(o.Stdout)
	}
	writeRow(headers)
	for _, row := range rows {
		writeRow(row)
	}
}

// EmitError renders the terminal error. In --json mode it is the command's
// single stdout JSON value; in human mode it is one stderr line.
func (o *Out) EmitError(err error) {
	if err == nil {
		return
	}
	if o.JSON {
		b, merr := json.Marshal(errorEnvelope(err))
		if merr != nil {
			b = []byte(`{"error":{"code":"error","message":"cannot encode error"}}`)
		}
		fmt.Fprintln(o.Stdout, string(b))
		return
	}
	fmt.Fprintln(o.Stderr, "placard: "+err.Error())
}

func (o *Out) Bold(s string) string {
	if !o.Color {
		return s
	}
	return "\x1b[1m" + s + "\x1b[0m"
}

// tableWidthFromEnv reads PLACARD_TABLE_WIDTH, falling back to the fixed
// default. Deliberately env-driven so tests pin it (spec §12.2 item 4).
func tableWidthFromEnv(getenv func(string) string) int {
	v := getenv("PLACARD_TABLE_WIDTH")
	if v == "" {
		return defaultTableWidth
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return defaultTableWidth
	}
	return n
}

// expiryLayout is how the CLI renders an absolute expiry: UTC, second
// precision, explicitly zoned. Shared so "when does this expire" reads the same
// whether it comes from a publish, a login, or anything added later — these
// strings are user-facing and golden-tested, so a per-call-site copy means
// changing the format is a hunt.
const expiryLayout = "2006-01-02 15:04:05Z"

// formatExpiry renders t in expiryLayout. Callers decide what an absent expiry
// means: publish says "never expires", login omits the line entirely.
func formatExpiry(t time.Time) string {
	return t.UTC().Format(expiryLayout)
}
