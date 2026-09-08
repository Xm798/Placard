package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/model"
)

// adminFixture is one admin plus one ordinary user, both logged in — the two
// principals every test below needs before it can say anything about the admin
// surface.
type adminFixture struct {
	*localAuthApp
	admin       accountBody
	adminCookie *http.Cookie
	user        accountBody
	userCookie  *http.Cookie
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	a := newLocalAuthApp(t)

	code, admin, adminCookie := a.register(t, `{"username":"alice","email":"alice@example.com","password":"correct-horse","display_name":"Alice"}`)
	if code != fiber.StatusCreated || !admin.IsAdmin {
		t.Fatalf("seed admin = %d %+v, want 201 with is_admin", code, admin)
	}
	if err := a.settings.Set(context.Background(), model.SettingRegistrationOpen, "true"); err != nil {
		t.Fatalf("open registration: %v", err)
	}
	code, user, userCookie := a.register(t, `{"username":"bob","password":"correct-horse"}`)
	if code != fiber.StatusCreated || user.IsAdmin {
		t.Fatalf("seed user = %d %+v, want 201 without is_admin", code, user)
	}

	return &adminFixture{localAuthApp: a, admin: admin, adminCookie: adminCookie, user: user, userCookie: userCookie}
}

func (f *adminFixture) patchUser(t *testing.T, id, body string, cookie *http.Cookie) int {
	t.Helper()
	code, _, _ := f.write(t, "PATCH", "/api/admin/users/"+id, body, cookie)
	return code
}

func (f *adminFixture) users(t *testing.T, cookie *http.Cookie) (int, []map[string]any, string) {
	t.Helper()
	code, raw := f.get(t, "/api/admin/users", cookie)
	if code != fiber.StatusOK {
		return code, nil, ""
	}
	var body struct {
		Users []map[string]any `json:"users"`
		Self  string           `json:"self"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal users %s: %v", raw, err)
	}
	return code, body.Users, body.Self
}

// --- authorization --------------------------------------------------------

// Every admin route answers a signed-in non-admin with 403, and the same
// requests succeed for an admin.
func TestAdminRoutesRefuseNonAdmin(t *testing.T) {
	f := newAdminFixture(t)

	cases := []struct {
		method, path, body string
	}{
		{"GET", "/api/admin/users", ""},
		{"PATCH", "/api/admin/users/" + f.admin.ID, `{"disabled":true}`},
		{"POST", "/api/admin/users/" + f.admin.ID + "/password", `{"password":"correct-horse"}`},
		{"DELETE", "/api/admin/users/" + f.admin.ID, ""},
		{"GET", "/api/admin/settings", ""},
		{"PATCH", "/api/admin/settings", `{"registration_open":true}`},
	}
	for _, tc := range cases {
		var code int
		if tc.method == "GET" {
			code, _ = f.get(t, tc.path, f.userCookie)
		} else {
			code, _, _ = f.write(t, tc.method, tc.path, tc.body, f.userCookie)
		}
		if code != fiber.StatusForbidden {
			t.Errorf("%s %s as non-admin = %d, want 403", tc.method, tc.path, code)
		}
	}

	code, users, self := f.users(t, f.adminCookie)
	if code != fiber.StatusOK {
		t.Fatalf("GET /api/admin/users as admin = %d, want 200", code)
	}
	if len(users) != 2 {
		t.Fatalf("admin user list = %d rows, want 2", len(users))
	}
	if self != f.admin.ID {
		t.Errorf("self = %q, want the calling admin %q", self, f.admin.ID)
	}
}

// An admin whose flag was withdrawn loses the surface on the session they
// already hold — the gate reads the user row, not a snapshot taken at login.
func TestAdminDemotionAppliesToTheExistingSession(t *testing.T) {
	f := newAdminFixture(t)

	if code := f.patchUser(t, f.user.ID, `{"is_admin":true}`, f.adminCookie); code != fiber.StatusNoContent {
		t.Fatalf("promote = %d, want 204", code)
	}
	if code, _ := f.get(t, "/api/admin/users", f.userCookie); code != fiber.StatusOK {
		t.Fatalf("promoted user GET users = %d, want 200", code)
	}

	if code := f.patchUser(t, f.user.ID, `{"is_admin":false}`, f.adminCookie); code != fiber.StatusNoContent {
		t.Fatalf("demote = %d, want 204", code)
	}
	if code, _ := f.get(t, "/api/admin/users", f.userCookie); code != fiber.StatusForbidden {
		t.Errorf("demoted user GET users = %d, want 403", code)
	}
}

// The admin API is a browser surface: a personal access token is refused
// before the admin check even runs.
func TestAdminRoutesRefusePAT(t *testing.T) {
	f := newAdminFixture(t)

	plaintext := patPrefix + "admin-surface-token"
	if err := f.deps.Tokens.Insert(context.Background(), &model.Token{
		TokenHash:  hashToken(f.deps.Cfg.Server.SecretKey, plaintext),
		UserID:     f.admin.ID,
		Name:       "cli",
		ExpiresAt:  neverSentinel,
		LastUsedAt: model.Never,
		CreateUser: f.admin.ID,
	}); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/admin/users", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	resp, err := f.app.Test(req, -1)
	if err != nil {
		t.Fatalf("GET /api/admin/users with PAT: %v", err)
	}
	if resp.StatusCode != fiber.StatusForbidden {
		t.Errorf("admin list over PAT = %d, want 403", resp.StatusCode)
	}
}

// --- account operations ---------------------------------------------------

// Disabling takes effect on the session that already exists and on the next
// login; enabling gives both back.
func TestAdminDisableLocksOutImmediately(t *testing.T) {
	f := newAdminFixture(t)

	if code, _ := f.get(t, "/api/me", f.userCookie); code != fiber.StatusOK {
		t.Fatalf("user GET /api/me before disable = %d, want 200", code)
	}

	if code := f.patchUser(t, f.user.ID, `{"disabled":true}`, f.adminCookie); code != fiber.StatusNoContent {
		t.Fatalf("disable = %d, want 204", code)
	}
	if code, _ := f.get(t, "/api/me", f.userCookie); code != fiber.StatusUnauthorized {
		t.Errorf("disabled user GET /api/me = %d, want 401", code)
	}
	if code, _ := f.login(t, "bob", "correct-horse"); code != fiber.StatusUnauthorized {
		t.Errorf("disabled user login = %d, want 401", code)
	}

	if code := f.patchUser(t, f.user.ID, `{"disabled":false}`, f.adminCookie); code != fiber.StatusNoContent {
		t.Fatalf("enable = %d, want 204", code)
	}
	if code, cookie := f.login(t, "bob", "correct-horse"); code != fiber.StatusOK || cookie == nil {
		t.Errorf("re-enabled user login = %d, want 200 with a session", code)
	}
}

// A reset replaces the password: the old one stops working and the new one is
// what the account logs in with.
func TestAdminResetPasswordRetiresTheOldOne(t *testing.T) {
	f := newAdminFixture(t)

	code, _, _ := f.write(t, "POST", "/api/admin/users/"+f.user.ID+"/password",
		`{"password":"battery-staple"}`, f.adminCookie)
	if code != fiber.StatusNoContent {
		t.Fatalf("reset password = %d, want 204", code)
	}

	if code, _ := f.login(t, "bob", "correct-horse"); code != fiber.StatusUnauthorized {
		t.Errorf("login with the retired password = %d, want 401", code)
	}
	if code, cookie := f.login(t, "bob", "battery-staple"); code != fiber.StatusOK || cookie == nil {
		t.Errorf("login with the new password = %d, want 200 with a session", code)
	}
}

// An account that signed in only through a provider gets a local identity row
// along with the password, so the reset is a usable login method rather than a
// column nothing reads.
func TestAdminResetPasswordGivesAnOIDCOnlyAccountALocalIdentity(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()

	sso := &model.User{
		ID:                "oidconlyuser0001",
		Username:          "carol",
		DisplayName:       "Carol",
		DefaultVisibility: model.VisibilityLink,
		FirstLoginAt:      model.Never,
		LastLoginAt:       model.Never,
		LastActiveAt:      model.Never,
	}
	if err := f.deps.Users.Create(ctx, sso); err != nil {
		t.Fatalf("seed sso account: %v", err)
	}

	code, _, _ := f.write(t, "POST", "/api/admin/users/"+sso.ID+"/password",
		`{"password":"battery-staple"}`, f.adminCookie)
	if code != fiber.StatusNoContent {
		t.Fatalf("reset password = %d, want 204", code)
	}

	if _, err := f.deps.Identities.GetByProviderSubject(ctx, model.ProviderLocal, sso.ID); err != nil {
		t.Fatalf("local identity after reset: %v", err)
	}
	if code, cookie := f.login(t, "carol", "battery-staple"); code != fiber.StatusOK || cookie == nil {
		t.Errorf("login after reset = %d, want 200 with a session", code)
	}
}

// The three ways an admin could lock themselves out of the surface that would
// undo it are all refused; the harmless self-edits are not.
func TestAdminCannotLockThemselvesOut(t *testing.T) {
	f := newAdminFixture(t)

	if code := f.patchUser(t, f.admin.ID, `{"disabled":true}`, f.adminCookie); code != fiber.StatusBadRequest {
		t.Errorf("self-disable = %d, want 400", code)
	}
	if code := f.patchUser(t, f.admin.ID, `{"is_admin":false}`, f.adminCookie); code != fiber.StatusBadRequest {
		t.Errorf("self-demote = %d, want 400", code)
	}
	if code, _, _ := f.write(t, "DELETE", "/api/admin/users/"+f.admin.ID, "", f.adminCookie); code != fiber.StatusBadRequest {
		t.Errorf("self-delete = %d, want 400", code)
	}

	if code := f.patchUser(t, f.admin.ID, `{"disabled":false}`, f.adminCookie); code != fiber.StatusNoContent {
		t.Errorf("self-enable = %d, want 204", code)
	}
	if code, _ := f.get(t, "/api/admin/users", f.adminCookie); code != fiber.StatusOK {
		t.Errorf("admin still on the surface = %d, want 200", code)
	}
}

func TestAdminPatchUnknownUserIsNotFound(t *testing.T) {
	f := newAdminFixture(t)
	if code := f.patchUser(t, "nosuchuser000001", `{"disabled":true}`, f.adminCookie); code != fiber.StatusNotFound {
		t.Errorf("patch unknown user = %d, want 404", code)
	}
	if code, _, _ := f.write(t, "DELETE", "/api/admin/users/nosuchuser000001", "", f.adminCookie); code != fiber.StatusNotFound {
		t.Errorf("delete unknown user = %d, want 404", code)
	}
}

// Deleting an account takes its credentials with it, kills its pages, and
// queues every object under the user_delete retention window rather than
// removing bytes on the spot.
func TestAdminDeleteUserRemovesCredentialsAndQueuesObjects(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()

	code, raw, _ := f.post(t, "/api/publish", `{"html":"<html><body>bob</body></html>","title":"bob page"}`, f.userCookie)
	if code != fiber.StatusOK && code != fiber.StatusCreated {
		t.Fatalf("publish as user = %d (%s), want 2xx", code, raw)
	}
	var published struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("unmarshal publish response %s: %v", raw, err)
	}

	if err := f.deps.Tokens.Insert(ctx, &model.Token{
		TokenHash:  hashToken(f.deps.Cfg.Server.SecretKey, patPrefix+"bob-token"),
		UserID:     f.user.ID,
		Name:       "cli",
		ExpiresAt:  neverSentinel,
		LastUsedAt: model.Never,
		CreateUser: f.user.ID,
	}); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	if err := f.deps.Users.SetAvatar(ctx, f.user.ID, "https://cdn.example.com/bob.png", "avatars/"+f.user.ID); err != nil {
		t.Fatalf("set avatar: %v", err)
	}

	if code, _, _ := f.write(t, "DELETE", "/api/admin/users/"+f.user.ID, "", f.adminCookie); code != fiber.StatusNoContent {
		t.Fatalf("delete user = %d, want 204", code)
	}

	if _, err := f.deps.Users.Get(ctx, f.user.ID); err == nil {
		t.Error("user row still present after delete")
	}
	if code, _ := f.get(t, "/api/me", f.userCookie); code != fiber.StatusUnauthorized {
		t.Errorf("deleted user GET /api/me = %d, want 401", code)
	}
	identities, err := f.deps.Identities.ListByUser(ctx, f.user.ID)
	if err != nil {
		t.Fatalf("list identities: %v", err)
	}
	if len(identities) != 0 {
		t.Errorf("identities after delete = %d, want 0", len(identities))
	}
	tokens, err := f.deps.Tokens.ListByUser(ctx, f.user.ID)
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("tokens after delete = %d, want 0", len(tokens))
	}

	var file model.File
	if err := f.deps.DB.Where("nano_id = ?", published.ID).First(&file).Error; err != nil {
		t.Fatalf("load published file: %v", err)
	}
	if file.IsDeleted != 1 {
		t.Errorf("file is_deleted = %d, want 1", file.IsDeleted)
	}

	// Nothing is reclaimed yet: every queued key carries the user_delete
	// reason, which the cleanup cron holds back for the retention window.
	var queued []model.PendingObjectDelete
	if err := f.deps.DB.Find(&queued).Error; err != nil {
		t.Fatalf("load pending deletes: %v", err)
	}
	keys := map[string]string{}
	for _, row := range queued {
		keys[row.ObjectKey] = row.Reason
	}
	if reason, ok := keys[file.ObjectKey]; !ok || reason != model.ReasonUserDelete {
		t.Errorf("page object %q queued as %q, want reason %q", file.ObjectKey, reason, model.ReasonUserDelete)
	}
	if reason, ok := keys["avatars/"+f.user.ID]; !ok || reason != model.ReasonUserDelete {
		t.Errorf("avatar object queued as %q, want reason %q", reason, model.ReasonUserDelete)
	}

	fresh, err := f.deps.Pending.ListPending(ctx, 3, time.Now().Add(-24*time.Hour), 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(fresh) != 0 {
		t.Errorf("reclaimable rows inside the retention window = %d, want 0", len(fresh))
	}
}

// --- instance settings ----------------------------------------------------

// A settings change is in force on the next request: the registration switch
// admits the account it refused a moment earlier, and the upload cap rejects
// the body it accepted.
func TestAdminSettingsApplyWithoutRestart(t *testing.T) {
	f := newAdminFixture(t)

	if err := f.settings.Set(context.Background(), model.SettingRegistrationOpen, "false"); err != nil {
		t.Fatalf("close registration: %v", err)
	}
	if code, _, _ := f.post(t, "/api/auth/register", `{"username":"carol","password":"correct-horse"}`, nil); code != fiber.StatusForbidden {
		t.Fatalf("register while closed = %d, want 403", code)
	}

	code, raw, _ := f.write(t, "PATCH", "/api/admin/settings", `{"registration_open":true}`, f.adminCookie)
	if code != fiber.StatusOK {
		t.Fatalf("patch settings = %d (%s), want 200", code, raw)
	}
	var settings struct {
		RegistrationOpen  bool  `json:"registration_open"`
		OIDCAutoProvision bool  `json:"oidc_auto_provision"`
		UploadMaxFileSize int64 `json:"upload_max_file_size"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("unmarshal settings %s: %v", raw, err)
	}
	if !settings.RegistrationOpen {
		t.Error("patch response registration_open = false, want true")
	}
	if code, _, _ := f.post(t, "/api/auth/register", `{"username":"carol","password":"correct-horse"}`, nil); code != fiber.StatusCreated {
		t.Errorf("register after opening = %d, want 201", code)
	}

	page := `{"html":"<html><body>hello</body></html>"}`
	if code, _, _ := f.post(t, "/api/publish", page, f.userCookie); code != fiber.StatusOK {
		t.Fatalf("publish under the configured cap = %d, want 200", code)
	}
	if code, _, _ := f.write(t, "PATCH", "/api/admin/settings", `{"upload_max_file_size":8}`, f.adminCookie); code != fiber.StatusOK {
		t.Fatalf("lower the upload cap = %d, want 200", code)
	}
	if code, _, _ := f.post(t, "/api/publish", page, f.userCookie); code != fiber.StatusBadRequest {
		t.Errorf("publish over the lowered cap = %d, want 400", code)
	}

	code, raw = f.get(t, "/api/admin/settings", f.adminCookie)
	if code != fiber.StatusOK {
		t.Fatalf("GET settings = %d, want 200", code)
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("unmarshal settings %s: %v", raw, err)
	}
	if settings.UploadMaxFileSize != 8 || !settings.RegistrationOpen {
		t.Errorf("settings = %+v, want the patched values", settings)
	}
}

func TestAdminSettingsRejectEmptyAndInvalidPatches(t *testing.T) {
	f := newAdminFixture(t)

	if code, _, _ := f.write(t, "PATCH", "/api/admin/settings", `{}`, f.adminCookie); code != fiber.StatusBadRequest {
		t.Errorf("empty patch = %d, want 400", code)
	}
	if code, _, _ := f.write(t, "PATCH", "/api/admin/settings", `{"upload_max_file_size":0}`, f.adminCookie); code != fiber.StatusBadRequest {
		t.Errorf("zero upload cap = %d, want 400", code)
	}
	if code := f.patchUser(t, f.user.ID, `{}`, f.adminCookie); code != fiber.StatusBadRequest {
		t.Errorf("empty user patch = %d, want 400", code)
	}
}

// The admin user list is a whitelist: no password hash, and no avatar source
// URL (which encodes a hash of the account's email under the Gravatar
// fallback). Share codes land on the file table in a later change and must
// never reach this surface either.
func TestAdminUserListLeaksNothingCredentialShaped(t *testing.T) {
	f := newAdminFixture(t)

	code, raw := f.get(t, "/api/admin/users", f.adminCookie)
	if code != fiber.StatusOK {
		t.Fatalf("GET users = %d, want 200", code)
	}
	for _, banned := range []string{"password_hash", "avatar_source_url", "share_code"} {
		if bytes.Contains(raw, []byte(banned)) {
			t.Errorf("admin user list leaked %q: %s", banned, raw)
		}
	}
}
