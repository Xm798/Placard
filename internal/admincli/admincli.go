// Package admincli implements `placard-server admin`, the recovery surface an
// operator reaches for when nobody can sign in to the admin page — a forgotten
// password, a withdrawn admin flag, an identity provider that has gone away.
//
// It talks to the database through the same repositories the HTTP handlers use
// and never through the API: the whole point is that it works when no session
// can be opened. The user-facing `placard` CLI carries none of this.
package admincli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/account"
	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/idgen"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/password"
	"github.com/Xm798/placard/internal/repo"
)

// Name is the argument that selects this subcommand.
const Name = "admin"

// listPageSize bounds one page of the account listing. `list` walks every page,
// so this is a memory bound rather than a limit on what is shown.
const listPageSize = 200

// Env is everything Run touches beyond its own arguments.
//
// OpenDB is injected rather than resolved here so a test drives a private
// database instead of whatever the host is configured with; it must apply
// migrations, because creating the first admin on an empty SQLite file is one
// of the situations this command exists for.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	OpenDB func(configPath string) (*gorm.DB, error)
}

// ErrUsage means the arguments named no command this package implements. The
// caller turns it into a non-zero exit after the usage text has been printed.
var ErrUsage = errors.New("admincli: usage")

const usage = `Usage: placard-server admin user <command> [flags]

Commands:
  create           create an account
  list             list accounts
  set-admin        grant or withdraw admin rights
  reset-password   set an account's password

Run a command with -h for its flags.`

// Run executes the arguments that followed "admin".
func Run(ctx context.Context, args []string, env Env) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(env.Stderr, usage)
		if len(args) == 0 {
			return ErrUsage
		}
		return nil
	}
	if args[0] != "user" {
		fmt.Fprintf(env.Stderr, "unknown command %q\n\n%s\n", args[0], usage)
		return ErrUsage
	}
	if len(args) == 1 {
		fmt.Fprintln(env.Stderr, usage)
		return ErrUsage
	}

	switch args[1] {
	case "create":
		return runCreate(ctx, args[2:], env)
	case "list":
		return runList(ctx, args[2:], env)
	case "set-admin":
		return runSetAdmin(ctx, args[2:], env)
	case "reset-password":
		return runResetPassword(ctx, args[2:], env)
	default:
		fmt.Fprintf(env.Stderr, "unknown command %q\n\n%s\n", args[1], usage)
		return ErrUsage
	}
}

// newFlagSet builds a flag set that reports errors on env.Stderr and carries
// the --config flag every command needs to find the database.
func newFlagSet(env Env, name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("admin user "+name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	configPath := fs.String("config", "", "path to config file (default: $PLACARD_CONFIG, else the built-in path)")
	return fs, configPath
}

func runCreate(ctx context.Context, args []string, env Env) error {
	fs, configPath := newFlagSet(env, "create")
	username := fs.String("username", "", "username to create (required)")
	email := fs.String("email", "", "email address")
	displayName := fs.String("display-name", "", "display name (defaults to the username)")
	isAdmin := fs.Bool("admin", false, "grant admin rights")
	pw := fs.String("password", "", "password; read from stdin when omitted")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *username == "" {
		return fmt.Errorf("--username is required")
	}
	name, err := account.ValidateUsername(*username)
	if err != nil {
		return err
	}
	addr, err := account.ValidateEmail(*email)
	if err != nil {
		return err
	}
	display, err := account.ValidateDisplayName(*displayName, name)
	if err != nil {
		return err
	}
	plaintext, err := resolvePassword(env, *pw)
	if err != nil {
		return err
	}
	hash, err := password.Hash(plaintext)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	db, err := env.OpenDB(*configPath)
	if err != nil {
		return err
	}

	user := &model.User{
		ID:                idgen.Generate(model.UserIDLen),
		Username:          name,
		Email:             addr,
		DisplayName:       display,
		PasswordHash:      hash,
		IsAdmin:           *isAdmin,
		DefaultVisibility: model.VisibilityLink,
		FirstLoginAt:      model.Never,
		LastLoginAt:       model.Never,
		LastActiveAt:      model.Never,
	}
	// The account and the credential it logs in with go in together, the same
	// way registration writes them — an account with no way to sign into it is
	// exactly what this command exists to avoid producing.
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Create(user).Error; e != nil {
			if errors.Is(e, gorm.ErrDuplicatedKey) {
				return fmt.Errorf("username or email already in use")
			}
			return e
		}
		return tx.Create(&model.UserIdentity{
			Provider: model.ProviderLocal,
			Subject:  user.ID,
			UserID:   user.ID,
		}).Error
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(env.Stdout, "created %s (%s)\n", user.Username, user.ID)
	return nil
}

func runList(ctx context.Context, args []string, env Env) error {
	fs, configPath := newFlagSet(env, "list")
	asJSON := fs.Bool("json", false, "emit the same JSON the admin API returns")
	if err := fs.Parse(args); err != nil {
		return err
	}

	db, err := env.OpenDB(*configPath)
	if err != nil {
		return err
	}
	users := repo.NewUserRepo(db)

	total, err := users.Count(ctx)
	if err != nil {
		return err
	}
	items := make([]dto.AdminUserItem, 0, total)
	for offset := 0; ; offset += listPageSize {
		page, err := users.List(ctx, offset, listPageSize)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		for _, u := range page {
			items = append(items, dto.AdminUserItemFrom(u))
		}
	}

	if *asJSON {
		body, err := json.Marshal(dto.AdminUsersResponse{
			Users:    items,
			Total:    total,
			Page:     1,
			PageSize: len(items),
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(env.Stdout, string(body))
		return err
	}

	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "USERNAME\tDISPLAY NAME\tEMAIL\tADMIN\tDISABLED\tLAST LOGIN\tID")
	for _, item := range items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			item.Username, item.DisplayName, dash(item.Email),
			yesNo(item.IsAdmin), yesNo(item.Disabled), stamp(item.LastLoginAt), item.ID)
	}
	return w.Flush()
}

func runSetAdmin(ctx context.Context, args []string, env Env) error {
	fs, configPath := newFlagSet(env, "set-admin")
	name := fs.String("username", "", "username or email of the account (required)")
	revoke := fs.Bool("revoke", false, "withdraw admin rights instead of granting them")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("--username is required")
	}

	db, err := env.OpenDB(*configPath)
	if err != nil {
		return err
	}
	users := repo.NewUserRepo(db)

	user, err := lookup(ctx, users, *name)
	if err != nil {
		return err
	}
	if _, err := users.SetAdmin(ctx, user.ID, !*revoke); err != nil {
		return err
	}

	if *revoke {
		fmt.Fprintf(env.Stdout, "%s is no longer an admin\n", user.Username)
	} else {
		fmt.Fprintf(env.Stdout, "%s is now an admin\n", user.Username)
	}
	return nil
}

func runResetPassword(ctx context.Context, args []string, env Env) error {
	fs, configPath := newFlagSet(env, "reset-password")
	name := fs.String("username", "", "username or email of the account (required)")
	pw := fs.String("password", "", "new password; read from stdin when omitted")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("--username is required")
	}
	plaintext, err := resolvePassword(env, *pw)
	if err != nil {
		return err
	}
	hash, err := password.Hash(plaintext)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	db, err := env.OpenDB(*configPath)
	if err != nil {
		return err
	}
	users := repo.NewUserRepo(db)

	user, err := lookup(ctx, users, *name)
	if err != nil {
		return err
	}
	if _, err := users.SetPasswordHash(ctx, user.ID, hash); err != nil {
		return err
	}
	// An account provisioned through a provider has no local credential row,
	// and the password just written would be one nothing consults.
	if err := repo.NewUserIdentityRepo(db).EnsureLocal(ctx, user.ID); err != nil {
		return err
	}

	fmt.Fprintf(env.Stdout, "password reset for %s\n", user.Username)
	return nil
}

// lookup resolves the account a command names. It accepts a username or an
// email address, the same pair the login form accepts, so an operator reading
// a name off the account page does not have to know which one it is.
func lookup(ctx context.Context, users *repo.UserRepo, identifier string) (*model.User, error) {
	user, err := users.GetByIdentifier(ctx, identifier)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("no account matches %q", identifier)
	}
	return user, err
}

// resolvePassword returns the flag value, or the first line of stdin when the
// flag was omitted.
//
// The typed password is echoed: reading it without an echo needs terminal
// control this command deliberately does not take, since it also has to work
// under `docker exec` and in a provisioning script's pipe. An operator who
// minds should pipe the password in rather than type it.
func resolvePassword(env Env, flagValue string) (string, error) {
	pw := flagValue
	if pw == "" {
		if env.Stdin == nil {
			return "", fmt.Errorf("no password given and no input to read one from")
		}
		fmt.Fprint(env.Stderr, "Password: ")
		line, err := bufio.NewReader(env.Stdin).ReadString('\n')
		if err != nil && (err != io.EOF || line == "") {
			return "", fmt.Errorf("read password: %w", err)
		}
		fmt.Fprintln(env.Stderr)
		pw = strings.TrimRight(line, "\r\n")
	}
	if err := account.ValidatePassword(pw); err != nil {
		return "", err
	}
	return pw, nil
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func stamp(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.UTC().Format("2006-01-02 15:04")
}
