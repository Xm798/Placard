package repo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/repo"
	"github.com/Xm798/placard/internal/testutil"
)

func seedAccount(t *testing.T, r *repo.UserRepo, id, username, email string) *model.User {
	t.Helper()
	u := &model.User{
		ID: id, Username: username, DisplayName: username,
		DefaultVisibility: model.VisibilityLink,
		FirstLoginAt:      model.Never, LastLoginAt: model.Never, LastActiveAt: model.Never,
	}
	if email != "" {
		u.Email = &email
	}
	if err := r.Create(context.Background(), u); err != nil {
		t.Fatalf("create user %q: %v", username, err)
	}
	return u
}

// One identifier column or the other, matched case-insensitively — that is what
// lets a login form accept either without asking which one was typed.
func TestGetByIdentifierMatchesUsernameOrEmail(t *testing.T) {
	users := repo.NewUserRepo(testutil.OpenTestDB(t))
	seedAccount(t, users, "u_alice", "Alice", "Alice@Example.com")
	ctx := context.Background()

	for _, identifier := range []string{"alice", "ALICE", " alice ", "alice@example.com", "Alice@Example.COM"} {
		u, err := users.GetByIdentifier(ctx, identifier)
		if err != nil {
			t.Fatalf("GetByIdentifier(%q): %v", identifier, err)
		}
		if u.ID != "u_alice" {
			t.Errorf("GetByIdentifier(%q) = %q, want u_alice", identifier, u.ID)
		}
	}
	if _, err := users.GetByIdentifier(ctx, "bob"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("unknown identifier err = %v, want ErrRecordNotFound", err)
	}
	if _, err := users.GetByIdentifier(ctx, ""); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("empty identifier err = %v, want ErrRecordNotFound", err)
	}
}

// An account with no email stores NULL, so the unique index has to admit more
// than one of them — otherwise the second password-only account is rejected.
func TestEmaillessAccountsCoexist(t *testing.T) {
	db := testutil.OpenTestDB(t)
	users := repo.NewUserRepo(db)
	seedAccount(t, users, "u_a", "alice", "")
	seedAccount(t, users, "u_b", "bob", "")

	var n int64
	if err := db.Model(&model.User{}).Count(&n).Error; err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	if n != 2 {
		t.Fatalf("accounts = %d, want 2", n)
	}
}

// Any stops at the first row, so it must still answer correctly on both sides
// of "the instance has no accounts".
func TestAnyReportsWhetherAnAccountExists(t *testing.T) {
	db := testutil.OpenTestDB(t)
	users := repo.NewUserRepo(db)
	ctx := context.Background()

	if any, err := users.Any(ctx); err != nil || any {
		t.Fatalf("Any(empty) = %v, %v; want false, nil", any, err)
	}
	seedAccount(t, users, "u_a", "alice", "")
	if any, err := users.Any(ctx); err != nil || !any {
		t.Fatalf("Any(seeded) = %v, %v; want true, nil", any, err)
	}
}

func TestActiveReportsDisabledAndMissing(t *testing.T) {
	db := testutil.OpenTestDB(t)
	users := repo.NewUserRepo(db)
	seedAccount(t, users, "u_alice", "alice", "")
	ctx := context.Background()

	if active, err := users.Active(ctx, "u_alice"); err != nil || !active {
		t.Fatalf("Active(enabled) = %v, %v; want true, nil", active, err)
	}
	if active, err := users.Active(ctx, "u_ghost"); err != nil || active {
		t.Fatalf("Active(missing) = %v, %v; want false, nil", active, err)
	}
	if err := db.Model(&model.User{}).Where("id = ?", "u_alice").Update("disabled", true).Error; err != nil {
		t.Fatalf("disable: %v", err)
	}
	if active, err := users.Active(ctx, "u_alice"); err != nil || active {
		t.Fatalf("Active(disabled) = %v, %v; want false, nil", active, err)
	}
}

// first_login_at is set once and then held; last_login_at moves every time.
func TestStampLoginPinsFirstLogin(t *testing.T) {
	users := repo.NewUserRepo(testutil.OpenTestDB(t))
	seedAccount(t, users, "u_alice", "alice", "")
	ctx := context.Background()

	first := time.Now().Add(-time.Hour)
	if err := users.StampLogin(ctx, "u_alice", first); err != nil {
		t.Fatalf("StampLogin: %v", err)
	}
	second := time.Now()
	if err := users.StampLogin(ctx, "u_alice", second); err != nil {
		t.Fatalf("StampLogin: %v", err)
	}

	u, err := users.Get(ctx, "u_alice")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !u.FirstLoginAt.Equal(model.Timestamp(first)) {
		t.Errorf("first_login_at = %v, want %v", u.FirstLoginAt, model.Timestamp(first))
	}
	if !u.LastLoginAt.Equal(model.Timestamp(second)) {
		t.Errorf("last_login_at = %v, want %v", u.LastLoginAt, model.Timestamp(second))
	}
}

// The config file seeds the settings once. A restart must not undo an admin's
// change, which is what makes the table — not the file — authoritative.
func TestSeedDefaultsDoesNotOverwrite(t *testing.T) {
	settings := repo.NewSettingRepo(testutil.OpenTestDB(t))
	ctx := context.Background()
	defaults := map[string]string{
		model.SettingRegistrationOpen:  "false",
		model.SettingUploadMaxFileSize: "10485760",
	}

	if err := settings.SeedDefaults(ctx, defaults); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := settings.Set(ctx, model.SettingRegistrationOpen, "true"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := settings.SeedDefaults(ctx, defaults); err != nil {
		t.Fatalf("re-seed: %v", err)
	}

	open, err := settings.GetBool(ctx, model.SettingRegistrationOpen, false)
	if err != nil {
		t.Fatalf("GetBool: %v", err)
	}
	if !open {
		t.Error("re-seeding reverted an admin's change")
	}
	size, err := settings.GetInt64(ctx, model.SettingUploadMaxFileSize, 0)
	if err != nil || size != 10485760 {
		t.Errorf("upload size = %d, %v; want 10485760, nil", size, err)
	}
}

// A value nobody can parse reads as the caller's default rather than failing
// the request that consulted it.
func TestSettingFallsBackOnUnparsableValue(t *testing.T) {
	settings := repo.NewSettingRepo(testutil.OpenTestDB(t))
	ctx := context.Background()
	if err := settings.Set(ctx, model.SettingRegistrationOpen, "yes-please"); err != nil {
		t.Fatalf("set: %v", err)
	}
	open, err := settings.GetBool(ctx, model.SettingRegistrationOpen, true)
	if err != nil {
		t.Fatalf("GetBool: %v", err)
	}
	if !open {
		t.Error("unparsable value did not fall back to the caller's default")
	}
	missing, err := settings.GetInt64(ctx, "no.such.key", 42)
	if err != nil || missing != 42 {
		t.Errorf("missing key = %d, %v; want 42, nil", missing, err)
	}
}

// (provider, subject) is unique, so an upstream identity can never be claimed
// by a second account.
func TestUserIdentityUniquePerProviderSubject(t *testing.T) {
	db := testutil.OpenTestDB(t)
	users := repo.NewUserRepo(db)
	identities := repo.NewUserIdentityRepo(db)
	seedAccount(t, users, "u_a", "alice", "")
	seedAccount(t, users, "u_b", "bob", "")
	ctx := context.Background()

	if err := identities.Create(ctx, &model.UserIdentity{
		Provider: model.ProviderLocal, Subject: "u_a", UserID: "u_a",
	}); err != nil {
		t.Fatalf("create identity: %v", err)
	}
	err := identities.Create(ctx, &model.UserIdentity{
		Provider: model.ProviderLocal, Subject: "u_a", UserID: "u_b",
	})
	// errors.Is, not ==: the Postgres driver wraps the sentinel around its own
	// SQLSTATE message while SQLite returns it bare.
	if !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("duplicate identity err = %v, want ErrDuplicatedKey", err)
	}

	got, err := identities.ListByUser(ctx, "u_a")
	if err != nil || len(got) != 1 {
		t.Fatalf("ListByUser = %v, %v; want one row", got, err)
	}
}
