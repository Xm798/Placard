package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/admincli"
	"github.com/Xm798/placard/internal/dto"
)

// runAdmin executes the server's `admin` subcommand against the fixture's
// database and returns what it wrote to stdout.
func runAdmin(t *testing.T, a *localAuthApp, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := admincli.Run(context.Background(), args, admincli.Env{
		Stdout: &stdout,
		Stderr: &stderr,
		Stdin:  strings.NewReader(""),
		OpenDB: func(string) (*gorm.DB, error) { return a.deps.DB, nil },
	})
	if err != nil {
		t.Fatalf("admin %v: %v (%s)", args, err, stderr.String())
	}
	return stdout.String()
}

// The lockout recovery path end to end: an operator creates an admin on an
// instance with no accounts, and that account signs in over HTTP and reaches
// the admin surface.
func TestAdminCLICreatesAnAccountTheServerCanLogIn(t *testing.T) {
	a := newLocalAuthApp(t)

	runAdmin(t, a, "user", "create", "--username", "root", "--email", "root@example.com",
		"--display-name", "Root", "--admin", "--password", "correct-horse")

	code, cookie := a.login(t, "root", "correct-horse")
	if code != fiber.StatusOK || cookie == nil {
		t.Fatalf("login as the CLI-created admin = %d, want 200 with a session", code)
	}
	if code, _ := a.get(t, "/api/admin/users", cookie); code != fiber.StatusOK {
		t.Errorf("CLI-created admin GET /api/admin/users = %d, want 200", code)
	}
	// The email is a login identifier like any other account's.
	if code, _ := a.login(t, "root@example.com", "correct-horse"); code != fiber.StatusOK {
		t.Errorf("login by email = %d, want 200", code)
	}
}

// `admin user list --json` and GET /api/admin/users describe the same accounts
// the same way — the subcommand is a second view of the admin surface, not a
// second definition of it.
func TestAdminCLIListMatchesTheAPI(t *testing.T) {
	f := newAdminFixture(t)

	raw := runAdmin(t, f.localAuthApp, "user", "list", "--json")
	var fromCLI dto.AdminUsersResponse
	if err := json.Unmarshal([]byte(raw), &fromCLI); err != nil {
		t.Fatalf("unmarshal CLI output %s: %v", raw, err)
	}

	code, body := f.get(t, "/api/admin/users", f.adminCookie)
	if code != fiber.StatusOK {
		t.Fatalf("GET /api/admin/users = %d, want 200", code)
	}
	var fromAPI dto.AdminUsersResponse
	if err := json.Unmarshal(body, &fromAPI); err != nil {
		t.Fatalf("unmarshal API body %s: %v", body, err)
	}

	if fromCLI.Total != fromAPI.Total {
		t.Errorf("total = %d (CLI) vs %d (API)", fromCLI.Total, fromAPI.Total)
	}
	if !reflect.DeepEqual(fromCLI.Users, fromAPI.Users) {
		t.Errorf("rows differ:\nCLI %+v\nAPI %+v", fromCLI.Users, fromAPI.Users)
	}
	// The listing an operator reads on the host names no caller, because there
	// is none.
	if fromCLI.Self != "" {
		t.Errorf("CLI self = %q, want empty", fromCLI.Self)
	}
}

// An admin locked out of their own instance gets back in through the host: the
// subcommand restores the flag and the password the admin page could not.
func TestAdminCLIRestoresALockedOutAdmin(t *testing.T) {
	f := newAdminFixture(t)

	// Another admin demoted them, and they no longer know the password.
	if code := f.patchUser(t, f.admin.ID, `{"is_admin":false}`, f.adminCookie); code != fiber.StatusBadRequest {
		t.Fatalf("self-demote = %d, want 400", code)
	}
	if code := f.patchUser(t, f.user.ID, `{"is_admin":true}`, f.adminCookie); code != fiber.StatusNoContent {
		t.Fatalf("promote the second account = %d, want 204", code)
	}
	if code := f.patchUser(t, f.admin.ID, `{"is_admin":false}`, f.userCookie); code != fiber.StatusNoContent {
		t.Fatalf("demote the first admin = %d, want 204", code)
	}
	if code, _ := f.get(t, "/api/admin/users", f.adminCookie); code != fiber.StatusForbidden {
		t.Fatalf("demoted admin GET users = %d, want 403", code)
	}

	runAdmin(t, f.localAuthApp, "user", "set-admin", "--username", "alice")
	runAdmin(t, f.localAuthApp, "user", "reset-password", "--username", "alice", "--password", "battery-staple")

	code, cookie := f.login(t, "alice", "battery-staple")
	if code != fiber.StatusOK || cookie == nil {
		t.Fatalf("login after the CLI reset = %d, want 200 with a session", code)
	}
	if code, _ := f.get(t, "/api/admin/users", cookie); code != fiber.StatusOK {
		t.Errorf("restored admin GET users = %d, want 200", code)
	}
}
