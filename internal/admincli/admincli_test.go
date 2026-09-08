package admincli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/password"
	"github.com/Xm798/placard/internal/repo"
	"github.com/Xm798/placard/internal/testutil"
)

// cli is one command run against a private database — the empty SQLite file an
// operator points the subcommand at when nobody can sign in.
type cli struct {
	db     *gorm.DB
	stdout bytes.Buffer
	stderr bytes.Buffer
	stdin  string
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	return &cli{db: testutil.OpenTestDB(t)}
}

func (c *cli) run(args ...string) error {
	c.stdout.Reset()
	c.stderr.Reset()
	return Run(context.Background(), args, Env{
		Stdout: &c.stdout,
		Stderr: &c.stderr,
		Stdin:  strings.NewReader(c.stdin),
		OpenDB: func(string) (*gorm.DB, error) { return c.db, nil },
	})
}

func (c *cli) users() *repo.UserRepo { return repo.NewUserRepo(c.db) }

// An admin created on an empty database carries everything a login needs: the
// normalized username, a verifiable password hash, the admin flag, and the
// local credential row without which the password would be a column nothing
// consults.
func TestCreateProducesALoginableAdmin(t *testing.T) {
	c := newCLI(t)

	if err := c.run("user", "create", "--username", "Alice", "--email", "Alice@Example.com",
		"--display-name", "Alice", "--admin", "--password", "correct-horse"); err != nil {
		t.Fatalf("create: %v (%s)", err, c.stderr.String())
	}

	ctx := context.Background()
	user, err := c.users().GetByIdentifier(ctx, "alice")
	if err != nil {
		t.Fatalf("load created account: %v", err)
	}
	if !user.IsAdmin || user.Disabled {
		t.Errorf("account = admin:%v disabled:%v, want admin and enabled", user.IsAdmin, user.Disabled)
	}
	if user.Email == nil || *user.Email != "alice@example.com" {
		t.Errorf("email = %v, want the normalized address", user.Email)
	}
	if !password.Verify(user.PasswordHash, "correct-horse") {
		t.Error("stored hash does not verify the password that was set")
	}
	if _, err := repo.NewUserIdentityRepo(c.db).GetByProviderSubject(ctx, model.ProviderLocal, user.ID); err != nil {
		t.Fatalf("local identity: %v", err)
	}
	if !strings.Contains(c.stdout.String(), "alice") {
		t.Errorf("stdout = %q, want the created username", c.stdout.String())
	}
}

// With no --password the command reads one from stdin, which is how a
// provisioning script pipes it in.
func TestCreateReadsThePasswordFromStdin(t *testing.T) {
	c := newCLI(t)
	c.stdin = "correct-horse\n"

	if err := c.run("user", "create", "--username", "alice"); err != nil {
		t.Fatalf("create: %v (%s)", err, c.stderr.String())
	}
	user, err := c.users().GetByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("load created account: %v", err)
	}
	if !password.Verify(user.PasswordHash, "correct-horse") {
		t.Error("password read from stdin does not verify")
	}
	if user.IsAdmin {
		t.Error("account is admin without --admin")
	}
	if user.DisplayName != "alice" {
		t.Errorf("display name = %q, want the username", user.DisplayName)
	}
}

// The credential rules are the API's rules: an account this command creates has
// to be one the login form can resolve.
func TestCreateEnforcesTheSharedCredentialRules(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"username too short", []string{"user", "create", "--username", "al", "--password", "correct-horse"}},
		{"username with @", []string{"user", "create", "--username", "al@ice", "--password", "correct-horse"}},
		{"password too short", []string{"user", "create", "--username", "alice", "--password", "short"}},
		{"invalid email", []string{"user", "create", "--username", "alice", "--email", "not-an-address", "--password", "correct-horse"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCLI(t)
			if err := c.run(tc.args...); err == nil {
				t.Fatal("command succeeded, want a rule violation")
			}
			var n int64
			if err := c.db.Model(&model.User{}).Count(&n).Error; err != nil {
				t.Fatalf("count users: %v", err)
			}
			if n != 0 {
				t.Errorf("accounts created = %d, want 0", n)
			}
		})
	}
}

func TestCreateRefusesADuplicateUsername(t *testing.T) {
	c := newCLI(t)
	if err := c.run("user", "create", "--username", "alice", "--password", "correct-horse"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := c.run("user", "create", "--username", "ALICE", "--password", "correct-horse"); err == nil {
		t.Fatal("second create succeeded, want a conflict")
	}
}

func TestSetAdminGrantsAndRevokes(t *testing.T) {
	c := newCLI(t)
	if err := c.run("user", "create", "--username", "alice", "--password", "correct-horse"); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := c.run("user", "set-admin", "--username", "alice"); err != nil {
		t.Fatalf("set-admin: %v (%s)", err, c.stderr.String())
	}
	if user, _ := c.users().GetByUsername(context.Background(), "alice"); !user.IsAdmin {
		t.Error("is_admin = false after set-admin")
	}

	if err := c.run("user", "set-admin", "--username", "alice", "--revoke"); err != nil {
		t.Fatalf("set-admin --revoke: %v", err)
	}
	if user, _ := c.users().GetByUsername(context.Background(), "alice"); user.IsAdmin {
		t.Error("is_admin = true after --revoke")
	}

	if err := c.run("user", "set-admin", "--username", "nobody"); err == nil {
		t.Error("set-admin on an unknown account succeeded, want an error")
	}
}

// A reset retires the old password, and gives a provider-only account the local
// credential row that makes the new one usable.
func TestResetPassword(t *testing.T) {
	c := newCLI(t)
	ctx := context.Background()

	if err := c.run("user", "create", "--username", "alice", "--password", "correct-horse"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.run("user", "reset-password", "--username", "alice", "--password", "battery-staple"); err != nil {
		t.Fatalf("reset-password: %v (%s)", err, c.stderr.String())
	}
	user, _ := c.users().GetByUsername(ctx, "alice")
	if password.Verify(user.PasswordHash, "correct-horse") {
		t.Error("the retired password still verifies")
	}
	if !password.Verify(user.PasswordHash, "battery-staple") {
		t.Error("the new password does not verify")
	}

	sso := &model.User{
		ID:                "ssoonlyaccount01",
		Username:          "carol",
		DisplayName:       "Carol",
		DefaultVisibility: model.VisibilityLink,
		FirstLoginAt:      model.Never,
		LastLoginAt:       model.Never,
		LastActiveAt:      model.Never,
	}
	if err := c.users().Create(ctx, sso); err != nil {
		t.Fatalf("seed sso account: %v", err)
	}
	if err := c.run("user", "reset-password", "--username", "carol", "--password", "battery-staple"); err != nil {
		t.Fatalf("reset-password on the sso account: %v", err)
	}
	if _, err := repo.NewUserIdentityRepo(c.db).GetByProviderSubject(ctx, model.ProviderLocal, sso.ID); err != nil {
		t.Fatalf("local identity after reset: %v", err)
	}
}

// `list --json` emits the admin API's own body, so an operator reading it on
// the host sees the same fields the page does.
func TestListJSONMatchesTheAPIShape(t *testing.T) {
	c := newCLI(t)
	if err := c.run("user", "create", "--username", "alice", "--email", "alice@example.com", "--admin", "--password", "correct-horse"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.run("user", "create", "--username", "bob", "--password", "correct-horse"); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := c.run("user", "list", "--json"); err != nil {
		t.Fatalf("list --json: %v (%s)", err, c.stderr.String())
	}
	var body dto.AdminUsersResponse
	if err := json.Unmarshal(c.stdout.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal %s: %v", c.stdout.String(), err)
	}
	if body.Total != 2 || len(body.Users) != 2 {
		t.Fatalf("list = %d of %d, want 2 of 2", len(body.Users), body.Total)
	}
	byName := map[string]dto.AdminUserItem{}
	for _, u := range body.Users {
		byName[u.Username] = u
	}
	alice, ok := byName["alice"]
	if !ok {
		t.Fatalf("alice missing from %s", c.stdout.String())
	}
	if !alice.IsAdmin || !alice.HasPassword || alice.Email != "alice@example.com" {
		t.Errorf("alice = %+v, want the created account", alice)
	}
	if alice.LastLoginAt != nil {
		t.Errorf("last_login_at = %v, want null for an account that never logged in", alice.LastLoginAt)
	}
	// The listing is an account inventory, never a credential dump.
	for _, banned := range []string{"password_hash", "avatar_source_url"} {
		if bytes.Contains(c.stdout.Bytes(), []byte(banned)) {
			t.Errorf("list --json leaked %q: %s", banned, c.stdout.String())
		}
	}
}

func TestListHumanOutputNamesEveryAccount(t *testing.T) {
	c := newCLI(t)
	if err := c.run("user", "create", "--username", "alice", "--admin", "--password", "correct-horse"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.run("user", "list"); err != nil {
		t.Fatalf("list: %v (%s)", err, c.stderr.String())
	}
	out := c.stdout.String()
	for _, want := range []string{"USERNAME", "alice", "yes", "never"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestUnknownCommandReportsUsage(t *testing.T) {
	c := newCLI(t)
	for _, args := range [][]string{{}, {"nonsense"}, {"user"}, {"user", "nonsense"}} {
		if err := c.run(args...); err == nil {
			t.Errorf("run(%v) succeeded, want a usage error", args)
		}
		if !strings.Contains(c.stderr.String(), "Usage: placard-server admin user") {
			t.Errorf("run(%v) stderr = %q, want the usage text", args, c.stderr.String())
		}
	}
}
