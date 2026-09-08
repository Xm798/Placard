package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/httpx"
	"github.com/Xm798/placard/internal/middleware"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/repo"
	"github.com/Xm798/placard/internal/session"
	"github.com/Xm798/placard/internal/storage"
	"github.com/Xm798/placard/internal/testutil"
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

const authTestOrigin = "https://placard.example.com"

// localAuthApp is the fixture the local-account tests drive: the real auth
// middleware (session + PAT channels, account-status check) and the real CSRF
// middleware in front of the real routes, over a private database. Nothing is
// injected — a test that wants an identity has to register or log in for one,
// which is the whole point here.
type localAuthApp struct {
	app      *fiber.App
	deps     Deps
	sessions session.Store
	settings *repo.SettingRepo
	users    *repo.UserRepo
}

func newLocalAuthApp(t *testing.T) *localAuthApp {
	t.Helper()

	db := testutil.OpenTestDB(t)
	sessions := newEphemeral(t, db, time.Hour, 2*time.Hour).sessions

	cfg := &config.Config{}
	cfg.Upload.MaxFileSize = 10 << 20
	cfg.Server.SecretKey = "test-secret-key"
	cfg.Token.MaxTTLDays = 365
	cfg.Server.BaseURL = authTestOrigin
	cfg.CSRF.AllowedOrigins = []string{authTestOrigin}
	cfg.Auth.Session.AbsoluteTTL = 2 * time.Hour

	users := repo.NewUserRepo(db)
	settings := repo.NewSettingRepo(db)
	deps := Deps{
		DB:            db,
		Storage:       storage.NewStubClient(),
		Cfg:           cfg,
		Files:         repo.NewFileRepo(db),
		Tokens:        repo.NewTokenRepo(db),
		Views:         repo.NewViewRepo(db),
		Audit:         repo.NewAuditRepo(db),
		Pending:       repo.NewPendingObjectDeleteRepo(db),
		Versions:      repo.NewFileVersionRepo(db),
		Users:         users,
		Identities:    repo.NewUserIdentityRepo(db),
		Settings:      settings,
		FailLimiter:   middleware.NewIPLimiter(nil, 3, "authfail-test"),
		AuthLimiter:   middleware.NewIPLimiter(nil, 200, "authroute-test").Handler(),
		Sessions:      sessions,
		SessionCookie: testSessionCookie,
	}

	a := &localAuthApp{deps: deps, sessions: sessions, settings: settings, users: users}
	a.remount(t)
	return a
}

// remount rebuilds the app from the current deps, so a test can swap one
// collaborator (a tighter limiter, say) and keep the rest of the fixture.
func (a *localAuthApp) remount(t *testing.T) {
	t.Helper()
	h := New(a.deps)
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(middleware.RequestID())
	app.Use(middleware.NewAuth(middleware.AuthOptions{
		Sessions:       a.sessions,
		CookieName:     testSessionCookie,
		TokenValidator: h.TokenValidator(),
		AccountStatus:  a.users.Active,
	}))
	app.Use(middleware.CSRF(a.deps.Cfg.CSRF))
	h.Mount(app)
	a.app = app
}

// post issues a same-origin XHR the CSRF middleware accepts, optionally
// carrying a session cookie, and returns the status, body and any session
// cookie the response set.
func (a *localAuthApp) post(t *testing.T, path, body string, cookie *http.Cookie) (int, []byte, *http.Cookie) {
	t.Helper()
	return a.write(t, "POST", path, body, cookie)
}

func (a *localAuthApp) write(t *testing.T, method, path, body string, cookie *http.Cookie) (int, []byte, *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", authTestOrigin)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Requested-With", "fetch")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := a.app.Test(req, -1)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	var set *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == testSessionCookie {
			set = c
		}
	}
	return resp.StatusCode, b, set
}

func (a *localAuthApp) get(t *testing.T, path string, cookie *http.Cookie) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := a.app.Test(req, -1)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

type accountBody struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	IsAdmin     bool   `json:"is_admin"`
}

func (a *localAuthApp) register(t *testing.T, body string) (int, accountBody, *http.Cookie) {
	t.Helper()
	code, raw, cookie := a.post(t, "/api/auth/register", body, nil)
	var acct accountBody
	if code == fiber.StatusCreated {
		if err := json.Unmarshal(raw, &acct); err != nil {
			t.Fatalf("unmarshal register response %s: %v", raw, err)
		}
	}
	return code, acct, cookie
}

func (a *localAuthApp) login(t *testing.T, identifier, pw string) (int, *http.Cookie) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"identifier": identifier, "password": pw})
	if err != nil {
		t.Fatalf("marshal login body: %v", err)
	}
	code, _, cookie := a.post(t, "/api/auth/login", string(body), nil)
	return code, cookie
}

// --- registration ---------------------------------------------------------

// The first account on an empty instance is admitted regardless of the
// registration switch and becomes the admin; the second is refused while the
// switch is off, and admitted once an admin turns it on.
func TestRegisterFirstUserBecomesAdminThenSwitchGoverns(t *testing.T) {
	a := newLocalAuthApp(t)

	code, acct, cookie := a.register(t, `{"username":"alice","email":"Alice@Example.com","password":"correct-horse","display_name":"Alice"}`)
	if code != fiber.StatusCreated {
		t.Fatalf("first register status = %d, want 201", code)
	}
	if !acct.IsAdmin {
		t.Error("first account is_admin = false, want true")
	}
	if acct.Username != "alice" || acct.Email != "alice@example.com" {
		t.Errorf("account = %+v, want normalized username/email", acct)
	}
	if cookie == nil || cookie.Value == "" {
		t.Fatal("register set no session cookie")
	}

	// The registration switch defaults to closed, and the instance is no
	// longer empty, so the second one is refused.
	code, _, _ = a.post(t, "/api/auth/register", `{"username":"bob","password":"correct-horse"}`, nil)
	if code != fiber.StatusForbidden {
		t.Fatalf("second register status = %d, want 403", code)
	}

	if err := a.settings.Set(context.Background(), model.SettingRegistrationOpen, "true"); err != nil {
		t.Fatalf("open registration: %v", err)
	}
	code, acct, _ = a.register(t, `{"username":"bob","password":"correct-horse"}`)
	if code != fiber.StatusCreated {
		t.Fatalf("register after opening the switch = %d, want 201", code)
	}
	if acct.IsAdmin {
		t.Error("second account is_admin = true, want false")
	}
}

// Registration writes the account and its "local" credential together — an
// account with no way to log into it must never exist.
func TestRegisterWritesLocalIdentity(t *testing.T) {
	a := newLocalAuthApp(t)
	code, acct, _ := a.register(t, `{"username":"alice","password":"correct-horse"}`)
	if code != fiber.StatusCreated {
		t.Fatalf("register status = %d, want 201", code)
	}

	id, err := a.deps.Identities.GetByProviderSubject(context.Background(), model.ProviderLocal, acct.ID)
	if err != nil {
		t.Fatalf("local identity lookup: %v", err)
	}
	if id.UserID != acct.ID {
		t.Errorf("identity user_id = %q, want %q", id.UserID, acct.ID)
	}
}

func TestRegisterRejectsDuplicateAndInvalidInput(t *testing.T) {
	a := newLocalAuthApp(t)
	if code, _, _ := a.register(t, `{"username":"alice","email":"alice@example.com","password":"correct-horse"}`); code != fiber.StatusCreated {
		t.Fatalf("seed register status = %d, want 201", code)
	}
	if err := a.settings.Set(context.Background(), model.SettingRegistrationOpen, "true"); err != nil {
		t.Fatalf("open registration: %v", err)
	}

	cases := []struct {
		name string
		body string
		want int
	}{
		{"duplicate username", `{"username":"Alice","password":"correct-horse"}`, fiber.StatusConflict},
		{"duplicate email", `{"username":"bob","email":"ALICE@example.com","password":"correct-horse"}`, fiber.StatusConflict},
		{"short username", `{"username":"ab","password":"correct-horse"}`, fiber.StatusBadRequest},
		{"username with at sign", `{"username":"bob@example.com","password":"correct-horse"}`, fiber.StatusBadRequest},
		{"short password", `{"username":"bob","password":"short"}`, fiber.StatusBadRequest},
		{"bad email", `{"username":"bob","email":"not-an-email","password":"correct-horse"}`, fiber.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, _ := a.post(t, "/api/auth/register", tc.body, nil)
			if code != tc.want {
				t.Errorf("status = %d, want %d", code, tc.want)
			}
		})
	}
}

// --- login ----------------------------------------------------------------

// Either identifier resolves the same account, and enough wrong passwords trip
// the per-IP failed-authn budget.
func TestLoginByUsernameOrEmailAndFailureBudget(t *testing.T) {
	a := newLocalAuthApp(t)
	if code, _, _ := a.register(t, `{"username":"alice","email":"alice@example.com","password":"correct-horse"}`); code != fiber.StatusCreated {
		t.Fatalf("seed register status = %d, want 201", code)
	}

	for _, identifier := range []string{"alice", "ALICE@example.com"} {
		code, cookie := a.login(t, identifier, "correct-horse")
		if code != fiber.StatusOK {
			t.Fatalf("login as %q status = %d, want 200", identifier, code)
		}
		if cookie == nil || cookie.Value == "" {
			t.Fatalf("login as %q set no session cookie", identifier)
		}
	}

	// The limiter admits `limit` failures and rejects once the count passes it.
	var got int
	for i := 0; i < 6; i++ {
		got, _ = a.login(t, "alice", "wrong-password")
		if got == fiber.StatusTooManyRequests {
			break
		}
		if got != fiber.StatusUnauthorized {
			t.Fatalf("failed login status = %d, want 401 or 429", got)
		}
	}
	if got != fiber.StatusTooManyRequests {
		t.Fatalf("repeated bad passwords never produced 429 (last status %d)", got)
	}
}

// A wrong password and an identifier nobody has must be indistinguishable.
func TestLoginUnknownIdentifierLooksLikeWrongPassword(t *testing.T) {
	a := newLocalAuthApp(t)
	if code, _, _ := a.register(t, `{"username":"alice","password":"correct-horse"}`); code != fiber.StatusCreated {
		t.Fatalf("seed register status = %d, want 201", code)
	}

	codeUnknown, bodyUnknown, _ := a.post(t, "/api/auth/login", `{"identifier":"nobody","password":"correct-horse"}`, nil)
	codeWrong, bodyWrong, _ := a.post(t, "/api/auth/login", `{"identifier":"alice","password":"nope-nope"}`, nil)
	if codeUnknown != fiber.StatusUnauthorized || codeWrong != fiber.StatusUnauthorized {
		t.Fatalf("statuses = %d / %d, want 401 for both", codeUnknown, codeWrong)
	}
	if string(bodyUnknown) != string(bodyWrong) {
		t.Errorf("bodies differ:\n unknown: %s\n wrong:   %s", bodyUnknown, bodyWrong)
	}
}

func TestLoginStampsLoginTimes(t *testing.T) {
	a := newLocalAuthApp(t)
	code, acct, _ := a.register(t, `{"username":"alice","password":"correct-horse"}`)
	if code != fiber.StatusCreated {
		t.Fatalf("register status = %d, want 201", code)
	}
	if code, _ := a.login(t, "alice", "correct-horse"); code != fiber.StatusOK {
		t.Fatalf("login status = %d, want 200", code)
	}

	u, err := a.users.Get(context.Background(), acct.ID)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if model.IsNever(u.LastLoginAt) || model.IsNever(u.FirstLoginAt) {
		t.Errorf("login timestamps still at the sentinel: first=%v last=%v", u.FirstLoginAt, u.LastLoginAt)
	}
}

// --- disabled accounts ----------------------------------------------------

// Disabling an account has to reach the credentials that already exist, not
// only the next login attempt.
func TestDisabledUserIsRefusedOnEveryChannel(t *testing.T) {
	a := newLocalAuthApp(t)
	code, acct, cookie := a.register(t, `{"username":"alice","password":"correct-horse"}`)
	if code != fiber.StatusCreated {
		t.Fatalf("register status = %d, want 201", code)
	}

	// Mint a PAT while the account is still enabled.
	patCode, patBody, _ := a.post(t, "/api/tokens", `{"name":"cli","expiry":"30d"}`, cookie)
	if patCode != fiber.StatusOK {
		t.Fatalf("create token status = %d, body = %s", patCode, patBody)
	}
	var minted struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(patBody, &minted); err != nil {
		t.Fatalf("unmarshal token: %v", err)
	}

	// The session works before the account is disabled — otherwise the
	// assertions below would pass for the wrong reason.
	if code, _ := a.get(t, "/api/me", cookie); code != fiber.StatusOK {
		t.Fatalf("pre-disable /api/me status = %d, want 200", code)
	}

	if err := a.deps.DB.Model(&model.User{}).Where("id = ?", acct.ID).
		Update("disabled", true).Error; err != nil {
		t.Fatalf("disable account: %v", err)
	}

	if code, _ := a.get(t, "/api/me", cookie); code != fiber.StatusUnauthorized {
		t.Errorf("existing session /api/me status = %d, want 401", code)
	}

	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+minted.Token)
	resp, err := a.app.Test(req, -1)
	if err != nil {
		t.Fatalf("PAT /api/me: %v", err)
	}
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Errorf("PAT /api/me status = %d, want 401", resp.StatusCode)
	}

	if code, _ := a.login(t, "alice", "correct-horse"); code != fiber.StatusUnauthorized {
		t.Errorf("disabled login status = %d, want 401", code)
	}
}

// --- status ---------------------------------------------------------------

func TestAuthStatusReportsSetupThenPolicy(t *testing.T) {
	a := newLocalAuthApp(t)

	code, body := a.get(t, "/api/auth/status", nil)
	if code != fiber.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	var st struct {
		RegistrationOpen bool `json:"registration_open"`
		SetupRequired    bool `json:"setup_required"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if !st.SetupRequired || !st.RegistrationOpen {
		t.Fatalf("empty instance status = %+v, want both true", st)
	}

	if code, _, _ := a.register(t, `{"username":"alice","password":"correct-horse"}`); code != fiber.StatusCreated {
		t.Fatalf("register status = %d, want 201", code)
	}
	_, body = a.get(t, "/api/auth/status", nil)
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if st.SetupRequired || st.RegistrationOpen {
		t.Fatalf("seeded instance status = %+v, want both false", st)
	}
}

// The login and registration pages, and the bundle they load, must be reachable
// with no session — otherwise there is no way to obtain one.
func TestUnauthenticatedCanReachLoginSurfaces(t *testing.T) {
	a := newLocalAuthApp(t)
	for _, path := range []string{"/api/auth/status", "/assets/does-not-exist.js"} {
		code, _ := a.get(t, path, nil)
		if code == fiber.StatusUnauthorized || code == fiber.StatusFound {
			t.Errorf("GET %s status = %d, want the route's own answer, not an auth gate", path, code)
		}
	}
}

// A user row is what prefs and the file list read; an account created by
// registration must satisfy them immediately, with no separate provisioning
// step between registering and using the app.
func TestRegisteredAccountCanUsePrefs(t *testing.T) {
	a := newLocalAuthApp(t)
	code, _, cookie := a.register(t, `{"username":"alice","password":"correct-horse"}`)
	if code != fiber.StatusCreated {
		t.Fatalf("register status = %d, want 201", code)
	}

	putCode, body, _ := a.write(t, "PUT", "/api/prefs", `{"default_visibility":"private"}`, cookie)
	if putCode != fiber.StatusNoContent {
		t.Fatalf("PUT /api/prefs status = %d (%s), want 204", putCode, body)
	}
}

// The upload cap is a runtime setting: lowering it in the table takes effect on
// the next publish, with no restart.
func TestUploadCapFollowsTheSettingTable(t *testing.T) {
	a := newLocalAuthApp(t)
	code, _, cookie := a.register(t, `{"username":"alice","password":"correct-horse"}`)
	if code != fiber.StatusCreated {
		t.Fatalf("register status = %d, want 201", code)
	}

	page := `{"html":"<!DOCTYPE html><html><body>` + strings.Repeat("x", 2048) + `</body></html>"}`
	if pubCode, body, _ := a.post(t, "/api/publish", page, cookie); pubCode != fiber.StatusOK {
		t.Fatalf("publish under the configured cap = %d (%s), want 200", pubCode, body)
	}

	if err := a.settings.Set(context.Background(), model.SettingUploadMaxFileSize, "512"); err != nil {
		t.Fatalf("lower the cap: %v", err)
	}
	pubCode, body, _ := a.post(t, "/api/publish", page, cookie)
	if pubCode != fiber.StatusBadRequest {
		t.Fatalf("publish over the lowered cap = %d (%s), want 400", pubCode, body)
	}
}

// Every password attempt costs an argon2 hash, so the endpoint is capped on
// requests — not only on failures, which a caller sending correct-looking
// requests would never trip.
func TestAuthRoutesAreRateLimitedPerIP(t *testing.T) {
	a := newLocalAuthApp(t)
	a.deps.AuthLimiter = middleware.NewIPLimiter(nil, 2, "authroute-capped").Handler()
	a.remount(t)

	var last int
	for i := 0; i < 5; i++ {
		last, _, _ = a.post(t, "/api/auth/login", `{"identifier":"alice","password":"correct-horse"}`, nil)
		if last == fiber.StatusTooManyRequests {
			break
		}
	}
	if last != fiber.StatusTooManyRequests {
		t.Fatalf("repeated login attempts never produced 429 (last status %d)", last)
	}
}

// --- the one-time bootstrap claim -----------------------------------------

// A marker left over from accounts that were all removed must not lock the
// instance out of ever creating an admin again.
func TestClaimBootstrapTakesOverAStaleMarker(t *testing.T) {
	db := testutil.OpenTestDB(t)
	settings := repo.NewSettingRepo(db)
	if err := settings.Set(context.Background(), model.SettingBootstrapAt, "2000-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed stale marker: %v", err)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(onlyAccount("u_new", "newadmin")).Error; err != nil {
			return err
		}
		return New(Deps{Settings: settings}).claimBootstrap(tx)
	})
	if err != nil {
		t.Fatalf("claimBootstrap over a stale marker: %v", err)
	}

	value, ok, err := settings.Get(context.Background(), model.SettingBootstrapAt)
	if err != nil || !ok {
		t.Fatalf("read marker: %v (found=%v)", err, ok)
	}
	if value == "2000-01-01T00:00:00Z" {
		t.Error("marker still holds the stale timestamp, want it refreshed")
	}
}

// With the marker held and another account already present, this registration
// was not the first one.
func TestClaimBootstrapRejectsWhenTheSlotIsTaken(t *testing.T) {
	db := testutil.OpenTestDB(t)
	settings := repo.NewSettingRepo(db)
	if err := settings.Set(context.Background(), model.SettingBootstrapAt, "2000-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	if err := db.Create(onlyAccount("u_first", "first")).Error; err != nil {
		t.Fatalf("seed winner: %v", err)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(onlyAccount("u_second", "second")).Error; err != nil {
			return err
		}
		return New(Deps{Settings: settings}).claimBootstrap(tx)
	})
	if !errors.Is(err, errBootstrapLost) {
		t.Fatalf("claimBootstrap = %v, want errBootstrapLost", err)
	}
}

// An instance emptied out of band bootstraps a fresh admin over HTTP, rather
// than answering "registration is closed" forever.
func TestRegisterAfterEveryAccountIsRemoved(t *testing.T) {
	a := newLocalAuthApp(t)
	if code, acct, _ := a.register(t, `{"username":"alice","password":"correct-horse"}`); code != fiber.StatusCreated {
		t.Fatalf("first register status = %d, want 201", code)
	} else if err := a.deps.DB.Where("id = ?", acct.ID).Delete(&model.User{}).Error; err != nil {
		t.Fatalf("remove the only account: %v", err)
	}

	code, acct, _ := a.register(t, `{"username":"bob","password":"correct-horse"}`)
	if code != fiber.StatusCreated {
		t.Fatalf("re-bootstrap status = %d, want 201", code)
	}
	if !acct.IsAdmin {
		t.Error("re-bootstrapped account is_admin = false, want true")
	}
}

func onlyAccount(id, username string) *model.User {
	return &model.User{
		ID: id, Username: username, DisplayName: username,
		DefaultVisibility: model.VisibilityLink,
		FirstLoginAt:      model.Never, LastLoginAt: model.Never, LastActiveAt: model.Never,
	}
}

// --- input normalization --------------------------------------------------

// RFC 5322 also admits display-name forms, so two spellings of one mailbox must
// not become two accounts the unique index cannot see are the same.
func TestRegisterStoresTheParsedEmailAddress(t *testing.T) {
	a := newLocalAuthApp(t)
	code, acct, _ := a.register(t, `{"username":"alice","email":"Alice Example <Alice@Example.com>","password":"correct-horse"}`)
	if code != fiber.StatusCreated {
		t.Fatalf("register status = %d, want 201", code)
	}
	if acct.Email != "alice@example.com" {
		t.Errorf("stored email = %q, want the parsed address", acct.Email)
	}

	if err := a.settings.Set(context.Background(), model.SettingRegistrationOpen, "true"); err != nil {
		t.Fatalf("open registration: %v", err)
	}
	dup, _, _ := a.post(t, "/api/auth/register", `{"username":"bob","email":"alice@example.com","password":"correct-horse"}`, nil)
	if dup != fiber.StatusConflict {
		t.Errorf("second account on the same mailbox = %d, want 409", dup)
	}
}

// The username charset promises ASCII digits, and the length bound is measured
// in bytes — a non-ASCII digit would satisfy neither.
func TestRegisterRejectsNonASCIIDigitsInUsername(t *testing.T) {
	a := newLocalAuthApp(t)
	code, _, _ := a.post(t, "/api/auth/register", `{"username":"ali٣e","password":"correct-horse"}`, nil)
	if code != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}
