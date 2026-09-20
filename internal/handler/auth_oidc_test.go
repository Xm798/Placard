package handler

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/oidc"
)

const testProvider = "corp"

// oidcApp is localAuthApp with a configured provider pointed at a fake issuer.
// Everything else is the real thing: the real routes, the real auth and CSRF
// middleware, a real database.
type oidcApp struct {
	*localAuthApp
	issuer *fakeIssuer
}

func newOIDCApp(t *testing.T) *oidcApp {
	t.Helper()
	base := newLocalAuthApp(t)
	issuer := newFakeIssuer(t)

	base.deps.Cfg.Auth.OIDC = []config.OIDCProviderConfig{{
		Name:         testProvider,
		DisplayName:  "Company SSO",
		Issuer:       issuer.URL(),
		ClientID:     "placard-test",
		ClientSecret: "s3cret",
	}}
	base.deps.Cfg.Auth.OIDCAutoProvision = true
	base.deps.OIDC = oidc.NewRegistry(base.deps.Cfg.Auth.OIDC,
		authTestOrigin+OIDCCallbackPath, issuer.server.Client())
	base.remount(t)
	return &oidcApp{localAuthApp: base, issuer: issuer}
}

// startAuthorize drives GET /auth/oidc/start and returns the authorize URL the
// server redirected to plus the session cookie the flow was stored on.
func (a *oidcApp) startAuthorize(t *testing.T, query string, cookie *http.Cookie) (*url.URL, *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest("GET", "/auth/oidc/start?provider="+testProvider+query, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := a.app.Test(req, -1)
	if err != nil {
		t.Fatalf("GET /auth/oidc/start: %v", err)
	}
	if resp.StatusCode != fiber.StatusFound {
		t.Fatalf("start status = %d, want 302", resp.StatusCode)
	}
	authURL, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	set := cookie
	for _, c := range resp.Cookies() {
		if c.Name == testSessionCookie {
			set = c
		}
	}
	return authURL, set
}

// start is startAuthorize reduced to the two flow secrets most tests need.
func (a *oidcApp) start(t *testing.T, query string, cookie *http.Cookie) (state, nonce string, set *http.Cookie) {
	t.Helper()
	authURL, set := a.startAuthorize(t, query, cookie)
	q := authURL.Query()
	return q.Get("state"), q.Get("nonce"), set
}

// callback drives the provider's redirect back to the server.
func (a *oidcApp) callback(t *testing.T, query string, cookie *http.Cookie) (int, string, *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest("GET", OIDCCallbackPath+"?"+query, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := a.app.Test(req, -1)
	if err != nil {
		t.Fatalf("GET %s: %v", OIDCCallbackPath, err)
	}
	var set *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == testSessionCookie && c.Value != "" {
			set = c
		}
	}
	return resp.StatusCode, resp.Header.Get("Location"), set
}

// signIn runs a whole login: start, register the code at the issuer, call back.
func (a *oidcApp) signIn(t *testing.T, code string, claims map[string]interface{}) (int, *http.Cookie) {
	t.Helper()
	state, nonce, cookie := a.start(t, "", nil)
	a.issuer.issue(code, nonce, claims)
	status, _, session := a.callback(t, "code="+code+"&state="+url.QueryEscape(state), cookie)
	return status, session
}

func claimSet(sub string, extra map[string]interface{}) map[string]interface{} {
	claims := map[string]interface{}{"sub": sub}
	for k, v := range extra {
		claims[k] = v
	}
	return claims
}

func (a *oidcApp) userBySubject(t *testing.T, sub string) *model.User {
	t.Helper()
	identity, err := a.deps.Identities.GetByProviderSubject(context.Background(), testProvider, sub)
	if err != nil {
		t.Fatalf("identity for subject %q: %v", sub, err)
	}
	user, err := a.deps.Users.Get(context.Background(), identity.UserID)
	if err != nil {
		t.Fatalf("user for subject %q: %v", sub, err)
	}
	return user
}

// --- provisioning ---------------------------------------------------------

// A subject nobody has seen before is provisioned into a new account, with the
// username taken from preferred_username and the verified address stored.
func TestOIDCLoginProvisionsNewAccount(t *testing.T) {
	a := newOIDCApp(t)

	status, session := a.signIn(t, "code-1", claimSet("sub-alice", map[string]interface{}{
		"preferred_username": "Alice",
		"email":              "alice@example.com",
		"email_verified":     true,
		"name":               "Alice Example",
	}))
	if status != fiber.StatusFound {
		t.Fatalf("callback status = %d, want 302", status)
	}
	if session == nil || session.Value == "" {
		t.Fatal("callback set no session cookie")
	}

	user := a.userBySubject(t, "sub-alice")
	if user.Username != "alice" {
		t.Errorf("username = %q, want %q", user.Username, "alice")
	}
	if user.Email == nil || *user.Email != "alice@example.com" {
		t.Errorf("email = %v, want alice@example.com", user.Email)
	}
	if user.DisplayName != "Alice Example" {
		t.Errorf("display_name = %q, want %q", user.DisplayName, "Alice Example")
	}
	if user.PasswordHash != "" {
		t.Error("an SSO-provisioned account must have no password hash")
	}
	// The first account on an empty instance is the admin whichever channel
	// created it — otherwise an SSO-only instance has no way to reach /admin.
	if !user.IsAdmin {
		t.Error("first account is_admin = false, want true")
	}

	// The session the callback issued is a real one: it authenticates.
	code, body := a.get(t, "/api/me", session)
	if code != fiber.StatusOK {
		t.Fatalf("GET /api/me after SSO login = %d (%s), want 200", code, body)
	}
}

// A verified address that already belongs to a local account links to it
// instead of creating a second account for the same person.
func TestOIDCLoginLinksByVerifiedEmail(t *testing.T) {
	a := newOIDCApp(t)
	_, acct, _ := a.register(t, `{"username":"alice","email":"alice@example.com","password":"correct-horse"}`)

	status, session := a.signIn(t, "code-1", claimSet("sub-alice", map[string]interface{}{
		"preferred_username": "different",
		"email":              "Alice@Example.com",
		"email_verified":     true,
	}))
	if status != fiber.StatusFound || session == nil {
		t.Fatalf("callback status = %d, cookie = %v, want 302 with a session", status, session)
	}

	user := a.userBySubject(t, "sub-alice")
	if user.ID != acct.ID {
		t.Fatalf("linked to user %q, want the existing account %q", user.ID, acct.ID)
	}
	if user.Username != "alice" {
		t.Errorf("username = %q, want the existing account's name", user.Username)
	}
}

// An unverified address is a claim, not a proof: it must not reach the account
// that holds it. With auto-provisioning on, the login gets its own new account.
func TestOIDCLoginIgnoresUnverifiedEmail(t *testing.T) {
	a := newOIDCApp(t)
	_, acct, _ := a.register(t, `{"username":"alice","email":"alice@example.com","password":"correct-horse"}`)

	status, _ := a.signIn(t, "code-1", claimSet("sub-impostor", map[string]interface{}{
		"preferred_username": "alice",
		"email":              "alice@example.com",
		"email_verified":     false,
	}))
	if status != fiber.StatusFound {
		t.Fatalf("callback status = %d, want 302", status)
	}

	user := a.userBySubject(t, "sub-impostor")
	if user.ID == acct.ID {
		t.Fatal("an unverified email address linked to an existing account")
	}
	if user.Email != nil {
		t.Errorf("email = %v, want nil — an unverified address is never stored", user.Email)
	}
	if user.Username == "alice" {
		t.Error("username collided with the existing account instead of getting a suffix")
	}
	if !strings.HasPrefix(user.Username, "alice") {
		t.Errorf("username = %q, want it derived from %q", user.Username, "alice")
	}
}

// A taken preferred_username gets a numeric suffix rather than failing the
// login.
func TestOIDCUsernameCollisionGetsSuffix(t *testing.T) {
	a := newOIDCApp(t)
	if code, _, _ := a.register(t, `{"username":"alice","password":"correct-horse"}`); code != fiber.StatusCreated {
		t.Fatalf("seed register status = %d, want 201", code)
	}

	if status, _ := a.signIn(t, "code-1", claimSet("sub-1", map[string]interface{}{
		"preferred_username": "alice",
	})); status != fiber.StatusFound {
		t.Fatalf("first SSO login status = %d, want 302", status)
	}
	if status, _ := a.signIn(t, "code-2", claimSet("sub-2", map[string]interface{}{
		"preferred_username": "alice",
	})); status != fiber.StatusFound {
		t.Fatalf("second SSO login status = %d, want 302", status)
	}

	first := a.userBySubject(t, "sub-1").Username
	second := a.userBySubject(t, "sub-2").Username
	if first != "alice2" || second != "alice3" {
		t.Errorf("usernames = %q, %q; want alice2, alice3", first, second)
	}
}

// With auto-provisioning off, an unknown subject is refused and no account
// appears.
func TestOIDCLoginRefusedWhenAutoProvisionDisabled(t *testing.T) {
	a := newOIDCApp(t)
	if err := a.settings.Set(context.Background(), model.SettingOIDCAutoProvision, "false"); err != nil {
		t.Fatalf("disable auto provisioning: %v", err)
	}

	status, session := a.signIn(t, "code-1", claimSet("sub-alice", map[string]interface{}{
		"preferred_username": "alice",
	}))
	if status != fiber.StatusForbidden {
		t.Fatalf("callback status = %d, want 403", status)
	}
	if session != nil {
		t.Error("a refused SSO login set a session cookie")
	}
	if _, err := a.deps.Identities.GetByProviderSubject(context.Background(), testProvider, "sub-alice"); err == nil {
		t.Fatal("a refused SSO login created an identity")
	}
	if any, err := a.deps.Users.Any(context.Background()); err != nil || any {
		t.Fatalf("users exist after a refused login (any=%v, err=%v)", any, err)
	}
}

// --- callback verification ------------------------------------------------

// A state that does not match the stored flow is refused, and nothing is
// created — this is the CSRF check on the callback.
func TestOIDCCallbackRejectsStateMismatch(t *testing.T) {
	a := newOIDCApp(t)
	_, nonce, cookie := a.start(t, "", nil)
	a.issuer.issue("code-1", nonce, claimSet("sub-alice", nil))

	status, _, session := a.callback(t, "code=code-1&state=not-the-state", cookie)
	if status != fiber.StatusBadRequest {
		t.Fatalf("callback status = %d, want 400", status)
	}
	if session != nil {
		t.Error("a state mismatch set a session cookie")
	}
	if any, err := a.deps.Users.Any(context.Background()); err != nil || any {
		t.Fatalf("a state mismatch created an account (any=%v, err=%v)", any, err)
	}
}

// An id token whose nonce is not the one this flow started with is refused:
// it is a token minted for some other authorization request.
func TestOIDCCallbackRejectsNonceMismatch(t *testing.T) {
	a := newOIDCApp(t)
	state, _, cookie := a.start(t, "", nil)
	a.issuer.issue("code-1", "some-other-nonce", claimSet("sub-alice", nil))

	status, _, session := a.callback(t, "code=code-1&state="+url.QueryEscape(state), cookie)
	if status != fiber.StatusBadRequest {
		t.Fatalf("callback status = %d, want 400", status)
	}
	if session != nil {
		t.Error("a nonce mismatch set a session cookie")
	}
	if any, err := a.deps.Users.Any(context.Background()); err != nil || any {
		t.Fatalf("a nonce mismatch created an account (any=%v, err=%v)", any, err)
	}
}

// The authorization request carries an S256 challenge and the redemption the
// matching verifier, so a code captured out of the redirect cannot be spent
// without the server-side flow record it was minted against.
func TestOIDCLoginUsesPKCE(t *testing.T) {
	a := newOIDCApp(t)
	authURL, cookie := a.startAuthorize(t, "", nil)
	q := authURL.Query()
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", got)
	}
	challenge := q.Get("code_challenge")
	if challenge == "" {
		t.Fatal("authorize url carries no code_challenge")
	}

	a.issuer.issue("code-1", q.Get("nonce"), claimSet("sub-alice", nil))
	status, _, _ := a.callback(t, "code=code-1&state="+url.QueryEscape(q.Get("state")), cookie)
	if status != fiber.StatusFound {
		t.Fatalf("callback status = %d, want 302", status)
	}

	verifier := a.issuer.verifiers["code-1"]
	sum := sha256.Sum256([]byte(verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); want != challenge {
		t.Errorf("code_verifier %q hashes to %q, want the challenge %q", verifier, want, challenge)
	}
}

// A flow record from before PKCE has no verifier to redeem with, so the login
// in flight across the upgrade is dropped rather than exchanged.
func TestOIDCCallbackRejectsFlowWithoutVerifier(t *testing.T) {
	a := newOIDCApp(t)
	state, nonce, cookie := a.start(t, "", nil)
	a.issuer.issue("code-1", nonce, claimSet("sub-alice", nil))

	ctx := context.Background()
	d, err := a.deps.Sessions.Get(ctx, cookie.Value)
	if err != nil {
		t.Fatalf("read the pending session: %v", err)
	}
	d.OIDC.Verifier = ""
	if err := a.deps.Sessions.Update(ctx, cookie.Value, d); err != nil {
		t.Fatalf("rewrite the flow: %v", err)
	}

	status, _, session := a.callback(t, "code=code-1&state="+url.QueryEscape(state), cookie)
	if status != fiber.StatusBadRequest {
		t.Fatalf("callback status = %d, want 400", status)
	}
	if session != nil {
		t.Error("a flow with no verifier set a session cookie")
	}
	if _, ok := a.issuer.verifiers["code-1"]; ok {
		t.Error("the code was redeemed without a verifier")
	}
	if any, err := a.deps.Users.Any(ctx); err != nil || any {
		t.Fatalf("a flow with no verifier created an account (any=%v, err=%v)", any, err)
	}
}

// An id token signed by a key the issuer's JWKS does not publish is refused.
func TestOIDCCallbackRejectsBadSignature(t *testing.T) {
	a := newOIDCApp(t)
	state, nonce, cookie := a.start(t, "", nil)

	// The issuer keeps publishing its own JWKS but signs this id token with a
	// key that is not in it.
	forger := newFakeIssuer(t)
	a.issuer.signKey = forger.key
	a.issuer.issue("code-1", nonce, claimSet("sub-alice", nil))

	status, _, session := a.callback(t, "code=code-1&state="+url.QueryEscape(state), cookie)
	if status != fiber.StatusBadRequest {
		t.Fatalf("callback status = %d, want 400", status)
	}
	if session != nil {
		t.Error("an unverifiable id token set a session cookie")
	}
	if any, err := a.deps.Users.Any(context.Background()); err != nil || any {
		t.Fatalf("an unverifiable id token created an account (any=%v, err=%v)", any, err)
	}
}

// A flow is consumed by the first callback that reads it, so replaying the
// same redirect finds nothing to verify against.
func TestOIDCCallbackFlowIsSingleUse(t *testing.T) {
	a := newOIDCApp(t)
	state, nonce, cookie := a.start(t, "", nil)
	a.issuer.issue("code-1", nonce, claimSet("sub-alice", nil))
	query := "code=code-1&state=" + url.QueryEscape(state)

	if status, _, _ := a.callback(t, query, cookie); status != fiber.StatusFound {
		t.Fatalf("first callback status = %d, want 302", status)
	}
	if status, _, _ := a.callback(t, query, cookie); status != fiber.StatusBadRequest {
		t.Fatalf("replayed callback status = %d, want 400", status)
	}
}

// A disabled account cannot be signed into through SSO either, and gets the
// same indistinguishable 401 the password path answers with.
func TestOIDCLoginRefusesDisabledAccount(t *testing.T) {
	a := newOIDCApp(t)
	if status, _ := a.signIn(t, "code-1", claimSet("sub-alice", nil)); status != fiber.StatusFound {
		t.Fatalf("initial SSO login status = %d, want 302", status)
	}
	user := a.userBySubject(t, "sub-alice")
	if err := a.deps.DB.Model(&model.User{}).Where("id = ?", user.ID).
		Update("disabled", true).Error; err != nil {
		t.Fatalf("disable account: %v", err)
	}

	status, session := a.signIn(t, "code-2", claimSet("sub-alice", nil))
	if status != fiber.StatusUnauthorized {
		t.Fatalf("callback status = %d, want 401", status)
	}
	if session != nil {
		t.Error("a disabled account got a session cookie")
	}
}

// The post-login destination is confined to this origin.
func TestOIDCStartRejectsOffSiteRedirect(t *testing.T) {
	a := newOIDCApp(t)
	state, nonce, cookie := a.start(t, "&redirect="+url.QueryEscape("//evil.example/steal"), nil)
	a.issuer.issue("code-1", nonce, claimSet("sub-alice", nil))

	status, location, _ := a.callback(t, "code=code-1&state="+url.QueryEscape(state), cookie)
	if status != fiber.StatusFound {
		t.Fatalf("callback status = %d, want 302", status)
	}
	if location != "/" {
		t.Errorf("redirected to %q, want the default %q", location, "/")
	}
}

// safeRedirectPath must see what the browser will see, not what the string
// looks like: TAB/LF/CR are stripped before a URL is parsed, and a backslash
// reads as a slash.
func TestSafeRedirectPath(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"", ""},
		{"/files", "/files"},
		{"/files?page=2#top", "/files?page=2#top"},
		{"//evil.example", ""},
		{"/\\evil.example", ""},
		{"\\\\evil.example", ""},
		{"https://evil.example/x", ""},
		// Stripped by the browser before parsing, leaving "//evil.example".
		{"/\t/evil.example", ""},
		{"/\n/evil.example", ""},
		{"/\r/evil.example", ""},
		{"/\t\t//evil.example", ""},
		// Longer than the column can hold alongside the rest of the flow.
		{"/" + strings.Repeat("a", redirectMaxLen), ""},
	}
	for _, tc := range cases {
		if got := safeRedirectPath(tc.raw); got != tc.want {
			t.Errorf("safeRedirectPath(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// --- linking and unlinking ------------------------------------------------

// A signed-in user runs the same flow to bind a provider to the account they
// already hold; signing in with that subject afterwards lands on it.
func TestOIDCManualLinkThenLoginLandsOnSameAccount(t *testing.T) {
	a := newOIDCApp(t)
	_, acct, cookie := a.register(t, `{"username":"alice","password":"correct-horse"}`)

	state, nonce, linkCookie := a.start(t, "&redirect=%2Fsettings", cookie)
	if linkCookie.Value != cookie.Value {
		t.Fatal("starting a link flow replaced the caller's session")
	}
	a.issuer.issue("code-1", nonce, claimSet("sub-alice", map[string]interface{}{
		"preferred_username": "someone-else",
	}))
	status, location, _ := a.callback(t, "code=code-1&state="+url.QueryEscape(state), cookie)
	if status != fiber.StatusFound {
		t.Fatalf("link callback status = %d, want 302", status)
	}
	if location != "/settings" {
		t.Errorf("link redirected to %q, want /settings", location)
	}
	if user := a.userBySubject(t, "sub-alice"); user.ID != acct.ID {
		t.Fatalf("identity bound to %q, want the signed-in account %q", user.ID, acct.ID)
	}

	// Signing in fresh with that subject reaches the same account rather than
	// provisioning a second one.
	status, session := a.signIn(t, "code-2", claimSet("sub-alice", nil))
	if status != fiber.StatusFound || session == nil {
		t.Fatalf("SSO login after linking = %d, cookie = %v, want 302 with a session", status, session)
	}
	if user := a.userBySubject(t, "sub-alice"); user.ID != acct.ID {
		t.Fatalf("SSO login resolved to %q, want %q", user.ID, acct.ID)
	}
}

// An identity already bound elsewhere is not moved by a link attempt.
func TestOIDCManualLinkRefusesSubjectBoundElsewhere(t *testing.T) {
	a := newOIDCApp(t)
	if status, _ := a.signIn(t, "code-1", claimSet("sub-alice", nil)); status != fiber.StatusFound {
		t.Fatalf("SSO login status = %d, want 302", status)
	}
	owner := a.userBySubject(t, "sub-alice")

	if err := a.settings.Set(context.Background(), model.SettingRegistrationOpen, "true"); err != nil {
		t.Fatalf("open registration: %v", err)
	}
	_, _, cookie := a.register(t, `{"username":"bob","password":"correct-horse"}`)

	state, nonce, _ := a.start(t, "", cookie)
	a.issuer.issue("code-2", nonce, claimSet("sub-alice", nil))
	status, _, _ := a.callback(t, "code=code-2&state="+url.QueryEscape(state), cookie)
	if status != fiber.StatusConflict {
		t.Fatalf("link callback status = %d, want 409", status)
	}
	if user := a.userBySubject(t, "sub-alice"); user.ID != owner.ID {
		t.Fatalf("identity moved to %q, want it to stay on %q", user.ID, owner.ID)
	}
}

// Unlinking is refused while it would leave the account with no way in, and
// allowed once another method exists.
func TestOIDCUnlinkKeepsLastLoginMethod(t *testing.T) {
	a := newOIDCApp(t)
	if status, _ := a.signIn(t, "code-1", claimSet("sub-alice", nil)); status != fiber.StatusFound {
		t.Fatalf("SSO login status = %d, want 302", status)
	}
	_, session := a.signIn(t, "code-2", claimSet("sub-alice", nil))

	// The provisioned account has no password, so this provider is the only
	// credential it has.
	status, body, _ := a.write(t, "DELETE", "/api/auth/identities/"+testProvider, "", session)
	if status != fiber.StatusBadRequest {
		t.Fatalf("unlink of the last method = %d (%s), want 400", status, body)
	}

	var listed dto.IdentitiesResponse
	code, raw := a.get(t, "/api/auth/identities", session)
	if code != fiber.StatusOK {
		t.Fatalf("GET /api/auth/identities = %d (%s), want 200", code, raw)
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("unmarshal identities %s: %v", raw, err)
	}
	if len(listed.Identities) != 1 || listed.Identities[0].Provider != testProvider {
		t.Fatalf("identities = %+v, want the one provider", listed.Identities)
	}
	if listed.Identities[0].CanUnlink {
		t.Error("can_unlink = true for the only login method")
	}
	if listed.HasPassword {
		t.Error("has_password = true for an SSO-provisioned account")
	}

	// Give the account a second credential and the same call succeeds.
	if err := a.deps.DB.Model(&model.User{}).Where("id = ?", a.userBySubject(t, "sub-alice").ID).
		Update("password_hash", "argon2id$placeholder").Error; err != nil {
		t.Fatalf("set password hash: %v", err)
	}
	status, body, _ = a.write(t, "DELETE", "/api/auth/identities/"+testProvider, "", session)
	if status != fiber.StatusNoContent {
		t.Fatalf("unlink with a password set = %d (%s), want 204", status, body)
	}
	if _, err := a.deps.Identities.GetByProviderSubject(context.Background(), testProvider, "sub-alice"); err == nil {
		t.Fatal("the identity survived a successful unlink")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("identity lookup after unlink: %v", err)
	}
}

// The callback is unauthenticated, so it re-checks the account itself: a
// session that outlived its account being disabled cannot add a credential.
func TestOIDCManualLinkRefusesDisabledAccount(t *testing.T) {
	a := newOIDCApp(t)
	_, acct, cookie := a.register(t, `{"username":"alice","password":"correct-horse"}`)

	state, nonce, _ := a.start(t, "", cookie)
	if err := a.deps.DB.Model(&model.User{}).Where("id = ?", acct.ID).
		Update("disabled", true).Error; err != nil {
		t.Fatalf("disable account: %v", err)
	}
	a.issuer.issue("code-1", nonce, claimSet("sub-alice", nil))

	status, _, _ := a.callback(t, "code=code-1&state="+url.QueryEscape(state), cookie)
	if status != fiber.StatusUnauthorized {
		t.Fatalf("link callback status = %d, want 401", status)
	}
	if _, err := a.deps.Identities.GetByProviderSubject(context.Background(), testProvider, "sub-alice"); err == nil {
		t.Fatal("a disabled account linked an identity")
	}
}

// A failed sign-in is a browser navigation, so it must land on a readable page
// with a way back — not on the JSON envelope the API endpoints return. The
// status stays the one the check produced.
func TestOIDCFailureRendersABrowserPage(t *testing.T) {
	a := newOIDCApp(t)
	_, nonce, cookie := a.start(t, "", nil)
	a.issuer.issue("code-1", nonce, claimSet("sub-alice", nil))

	req := httptest.NewRequest("GET", OIDCCallbackPath+"?code=code-1&state=wrong", nil)
	req.AddCookie(cookie)
	resp, err := a.app.Test(req, -1)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `href="/login"`) {
		t.Error("the failure page offers no way back to sign in")
	}
	// The page must not describe which check rejected the request.
	if strings.Contains(string(body), "state") || strings.Contains(string(body), "nonce") {
		t.Errorf("the failure page names the check that failed: %s", body)
	}
}

// --- login page -----------------------------------------------------------

// The unauthenticated status probe describes the configured providers and
// nothing else about them.
func TestAuthStatusListsProviders(t *testing.T) {
	a := newOIDCApp(t)
	code, raw := a.get(t, "/api/auth/status", nil)
	if code != fiber.StatusOK {
		t.Fatalf("GET /api/auth/status = %d, want 200", code)
	}
	var status dto.AuthStatusResponse
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatalf("unmarshal status %s: %v", raw, err)
	}
	if len(status.OIDCProviders) != 1 {
		t.Fatalf("providers = %+v, want exactly one", status.OIDCProviders)
	}
	if status.OIDCProviders[0].Name != testProvider || status.OIDCProviders[0].DisplayName != "Company SSO" {
		t.Errorf("provider = %+v, want the configured name and label", status.OIDCProviders[0])
	}
	if strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), a.issuer.URL()) {
		t.Errorf("status body leaked provider credentials or endpoints: %s", raw)
	}
}

// An instance with no provider configured serves no OIDC routes at all.
func TestOIDCRoutesAbsentWithoutProviders(t *testing.T) {
	a := newLocalAuthApp(t)
	req := httptest.NewRequest("GET", "/auth/oidc/start?provider=corp", nil)
	resp, err := a.app.Test(req, -1)
	if err != nil {
		t.Fatalf("GET /auth/oidc/start: %v", err)
	}
	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("start status = %d on a password-only instance, want 404", resp.StatusCode)
	}
}
