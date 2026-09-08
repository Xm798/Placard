// cmd/placard/root.go
package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Xm798/placard/internal/version"
)

// baseHint is the wording every "which server?" error ends with. Placard has
// no default instance: a fork of it is somebody's own deployment, and guessing
// an address would send a token somewhere the user never named.
const baseHint = "pass --base https://placard.your-domain.example, set PLACARD_URL, " +
	"or run `placard login --base <url>` once to remember it"

// cliVersion reports the raw injected version. NEVER version.Public(): its
// master-<commit> special case is a server-ops affordance that is neither
// semver nor a local-build marker, and it would break update comparison
// (spec §8.2).
func cliVersion() string { return version.Version }

type globalFlags struct {
	base    string
	token   string
	json    bool
	noColor bool
}

// Env is everything a command may touch that is not the command's own flags.
// Every field is injectable so tests pin the clock, the home directory, the
// environment and the writers (spec §12.2).
type Env struct {
	Out          *Out
	Base         string
	BaseExplicit bool
	Paths        Paths
	Now          func() time.Time
	Stdin        io.Reader
	// IsTTY is stdout's terminal-ness and decides COLOUR only. Whether a human
	// can be asked a question is StdinTTY's job — see CanPrompt.
	IsTTY bool
	// StdinTTY is stdin's terminal-ness. Prompting must key off this, not
	// IsTTY: with `echo y | placard rm <id>` stdout is still a terminal, so
	// IsTTY would happily read the consent off the pipe.
	StdinTTY bool
	Getenv   func(string) string
	HTTP     *http.Client

	// Sleep and OpenURL are injected so login is testable without real waiting
	// or a real browser.
	Sleep   func(time.Duration)
	OpenURL func(*Env, string) (bool, error)

	flags globalFlags
}

func NewEnv(stdout, stderr io.Writer, stdin io.Reader, home string, getenv func(string) string, now func() time.Time, isTTY bool) *Env {
	return &Env{
		Out:     NewOut(stdout, stderr, false, false, defaultTableWidth),
		Paths:   NewPaths(home, getenv),
		Now:     now,
		Stdin:   stdin,
		IsTTY:   isTTY,
		Getenv:  getenv,
		HTTP:    &http.Client{Timeout: defaultHTTPTimeout},
		Sleep:   time.Sleep,
		OpenURL: openURL,
	}
}

// CanPrompt reports whether an interactive question may be asked: a real
// terminal on stdin to answer it, and human output so --json never blocks on
// stdin nor breaks its one-JSON-value-on-stdout contract. Every confirm() call
// must be gated on this.
func (e *Env) CanPrompt() bool { return e.StdinTTY && !e.Out.JSON }

// RequireBase refuses, with a usage error naming every way to supply one, when
// no server was resolved. Commands that talk to a server call it before
// anything else; `placard update`, `--help` and `--version` never need one.
func (e *Env) RequireBase() error {
	if e.Base == "" {
		return Usage("no Placard server configured: " + baseHint)
	}
	return nil
}

// AuthedClient resolves credentials for the effective base and returns a client
// with the Bearer header set. Failure is no_credentials or base_mismatch, both
// exit 3.
func (e *Env) AuthedClient() (*Client, error) {
	if err := e.RequireBase(); err != nil {
		return nil, err
	}
	cred, err := ResolveCredential(CredInput{
		Base:         e.Base,
		BaseExplicit: e.BaseExplicit,
		FlagToken:    e.flags.token,
		EnvToken:     e.Getenv("PLACARD_TOKEN"),
		Paths:        e.Paths,
	})
	if err != nil {
		return nil, err
	}
	return NewClient(e.Base, cred.Token, e.HTTP), nil
}

// AnonClient is for the device-code endpoints, which are unauthenticated by
// definition — login has no token yet.
func (e *Env) AnonClient() (*Client, error) {
	if err := e.RequireBase(); err != nil {
		return nil, err
	}
	return NewClient(e.Base, "", e.HTTP), nil
}

func newRootCmd(env *Env) *cobra.Command {
	root := &cobra.Command{
		Use:   "placard",
		Short: "Publish self-contained HTML pages and get a share link",
		// Errors and usage are printed by main via Out, so cobra must stay quiet
		// or every failure would be reported twice, half of it on the wrong stream.
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       cliVersion(),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return env.applyGlobals(cmd)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetOut(env.Out.Stdout)
	root.SetErr(env.Out.Stderr)
	root.SetVersionTemplate("placard {{.Version}}\n")

	f := root.PersistentFlags()
	f.StringVar(&env.flags.base, "base", "", "Server base URL (env: PLACARD_URL; defaults to the server of your last login)")
	f.StringVar(&env.flags.token, "token", "",
		"Personal access token (CI only; leaks into shell history and ps. For interactive use, run: placard login)")
	f.BoolVar(&env.flags.json, "json", false, "Output machine-readable JSON (stdout is always valid JSON; diagnostics go to stderr)")
	f.BoolVar(&env.flags.noColor, "no-color", false, "Disable colored output (env: NO_COLOR)")

	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return Usage(err.Error())
	})

	root.AddCommand(
		newPublishCmd(env),
		newLsCmd(env),
		newRmCmd(env),
		newOpenCmd(env),
		newVersionCmd(env),
		newLoginCmd(env),
		newLogoutCmd(env),
		newWhoamiCmd(env),
		newUpdateCmd(env),
	)
	return root
}

// applyGlobals resolves the effective base and rebuilds Out with the final
// json/color/width settings. It runs before every command body.
func (e *Env) applyGlobals(cmd *cobra.Command) error {
	e.BaseExplicit = cmd.Flags().Changed("base") || cmd.Root().PersistentFlags().Changed("base")

	raw := e.flags.base
	if raw == "" {
		raw = e.Getenv("PLACARD_URL")
	}
	if raw == "" {
		// The server of the last successful login, or the only one there is —
		// the fallback covers a config written before default_base existed. A
		// parse failure is ignored here and reported by credential resolution,
		// which is where a broken config file is actionable; `placard update`
		// must not need one.
		if cfg, cerr := LoadConfig(e.Paths); cerr == nil {
			raw = cfg.DefaultBase
			if raw == "" {
				raw = cfg.soleBase()
			}
		}
	}
	// An unset base is not an error yet: update, --help and --version have no
	// server to talk to. RequireBase is what refuses, where it matters.
	if raw != "" {
		base, err := NormalizeBase(raw)
		if err != nil {
			return err
		}
		e.Base = base
	}

	color := e.IsTTY && !e.flags.noColor && e.Getenv("NO_COLOR") == "" && !e.flags.json
	e.Out = NewOut(e.Out.Stdout, e.Out.Stderr, e.flags.json, color, tableWidthFromEnv(e.Getenv))
	return nil
}

// Run executes the CLI with the given argv (no program name) and returns a
// typed error. main turns it into an exit code; tests call it directly.
func Run(env *Env, args []string) error { return RunContext(context.Background(), env, args) }

// RunContext is Run with a caller-supplied context, so main can wire SIGINT to
// the device-code poller's cancellation path.
func RunContext(ctx context.Context, env *Env, args []string) error {
	root := newRootCmd(env)
	root.SetArgs(args)
	return classifyCobraError(root.ExecuteContext(ctx))
}

// isTTY reports whether f is a character device. Used only to decide colour
// (stdout) and whether a command may prompt (stdin) — never to decide
// golden-test output shape.
func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
