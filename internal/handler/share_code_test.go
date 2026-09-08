package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/httpx"
	"github.com/Xm798/placard/internal/i18n"
	"github.com/Xm798/placard/internal/middleware"
	"github.com/Xm798/placard/internal/sharecode"
	"github.com/Xm798/placard/internal/userctx"
)

// unlockPrompt is the access-code prompt's heading in the default language —
// test requests carry no Accept-Language.
var unlockPrompt = unlockShellTextByLang[i18n.EN].Heading

const codedPage = `<!DOCTYPE html><html><head><title>季度复盘</title>` +
	`<meta name="description" content="内部数据，勿外传"></head><body>secret body</body></html>`

// publishAs publishes html through the real-auth fixture as the holder of
// cookie, and returns the new page's id.
func (a *localAuthApp) publishAs(t *testing.T, cookie *http.Cookie, html string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"html": html, "expiry": "never"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	status, raw, _ := a.post(t, "/api/publish", string(body), cookie)
	if status != fiber.StatusOK {
		t.Fatalf("publish = %d %s", status, raw)
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return resp.ID
}

// generateCode mints a share code for id through the owner-facing endpoint and
// returns the plaintext.
func generateCode(t *testing.T, app *fiber.App, id string) string {
	t.Helper()
	code, body := doJSON(t, app, "POST", "/api/files/"+id+"/share-code", "")
	if code != fiber.StatusOK {
		t.Fatalf("generate share code = %d %s", code, body)
	}
	var resp struct {
		ShareCode string `json:"share_code"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if !sharecode.Valid(resp.ShareCode) {
		t.Fatalf("share_code = %q, want 6 digits", resp.ShareCode)
	}
	return resp.ShareCode
}

// unlock submits code to the anonymous app and returns the status plus the
// ticket cookie the response set (nil when it set none).
func unlock(t *testing.T, app *fiber.App, id, code string) (int, *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest("POST", "/s/"+id+"/unlock", strings.NewReader(`{"code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	want := sharecode.CookieName(true, id)
	for _, c := range resp.Cookies() {
		if c.Name == want {
			return resp.StatusCode, c
		}
	}
	return resp.StatusCode, nil
}

func anonGetWithCookie(t *testing.T, app *fiber.App, path string, cookie *http.Cookie, headers map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Fiber matches group middleware by raw string prefix, so mounting the share
// routes under app.Group("/s") silently puts BrowserOnly and the anonymous
// visitor budget in front of /settings and /skill.md too. Both must answer
// without spending a share-link request.
func TestShareMiddlewareDoesNotReachSiblingPaths(t *testing.T) {
	app, deps := newTestApp(t)
	// A budget of zero: any share-route middleware that runs rejects at once.
	deps.AnonRenderLimiter = middleware.AnonOnly(
		middleware.NewIPLimiter(middleware.NewMemoryCounter(), 0, "sharescopetest").Handler())
	anon := anonApp(t, deps)

	for _, path := range []string{"/settings", "/skill.md"} {
		if code, _ := anonGetWithCookie(t, anon, path, nil, nil); code == fiber.StatusTooManyRequests {
			t.Fatalf("%s ran the share-link middleware", path)
		}
	}
	// The budget really is in force where it belongs.
	id := publishHTML(t, app, codedPage)
	if code, _ := anonShell(t, anon, id); code != fiber.StatusTooManyRequests {
		t.Fatalf("/s/:id = %d, want 429 — the share middleware is not wired", code)
	}
}

// --- the gate ------------------------------------------------------------

// TestShareCodeGatesTheAnonymousReadPaths walks the whole visitor journey: a
// coded page shows the prompt, meta and render refuse, the right code opens
// all three.
func TestShareCodeGatesTheAnonymousReadPaths(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, codedPage)
	code := generateCode(t, app, id)
	anon := anonApp(t, deps)

	shellCode, shell := anonShell(t, anon, id)
	if shellCode != fiber.StatusOK || !strings.Contains(shell, unlockPrompt) {
		t.Fatalf("locked shell = %d, prompt present: %v", shellCode, strings.Contains(shell, unlockPrompt))
	}
	if strings.Contains(shell, "<iframe") {
		t.Fatal("locked shell rendered the viewer iframe")
	}
	if mc, _ := anonMeta(t, anon, id); mc != fiber.StatusForbidden {
		t.Fatalf("locked meta = %d, want 403", mc)
	}
	if rc, _ := anonRender(t, anon, id); rc != fiber.StatusForbidden {
		t.Fatalf("locked render = %d, want 403", rc)
	}

	status, cookie := unlock(t, anon, id, code)
	if status != fiber.StatusNoContent || cookie == nil {
		t.Fatalf("unlock = %d, cookie set: %v", status, cookie != nil)
	}

	mc, meta := anonGetWithCookie(t, anon, "/s/"+id+"/meta", cookie, map[string]string{"Sec-Fetch-Mode": "cors"})
	if mc != fiber.StatusOK || !strings.Contains(meta, `"expired":false`) {
		t.Fatalf("unlocked meta = %d %s", mc, meta)
	}
	rc, rendered := anonGetWithCookie(t, anon, "/s/"+id+"/render", cookie, map[string]string{"Sec-Fetch-Dest": "iframe"})
	if rc != fiber.StatusOK || !strings.Contains(rendered, "secret body") {
		t.Fatalf("unlocked render = %d %q", rc, rendered)
	}
	sc, unlockedShell := anonGetWithCookie(t, anon, "/s/"+id, cookie, nil)
	if sc != fiber.StatusOK || !strings.Contains(unlockedShell, "<iframe") {
		t.Fatalf("unlocked shell = %d, iframe present: %v", sc, strings.Contains(unlockedShell, "<iframe"))
	}
}

// A wrong code is 403 and, once the per-file+IP budget is spent, 429 — the
// online guessing rate is the only thing protecting six digits.
func TestWrongShareCodeIs403ThenRateLimited(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, codedPage)
	code := generateCode(t, app, id)

	const budget = 3
	deps.ShareCodeLimiter = middleware.NewIPLimiter(middleware.NewMemoryCounter(), budget, "sharecodetest")
	anon := anonApp(t, deps)

	wrong := "000000"
	if wrong == code {
		wrong = "111111"
	}
	for i := 0; i <= budget; i++ {
		if status, _ := unlock(t, anon, id, wrong); status != fiber.StatusForbidden {
			t.Fatalf("attempt %d = %d, want 403", i+1, status)
		}
	}
	if status, _ := unlock(t, anon, id, wrong); status != fiber.StatusTooManyRequests {
		t.Fatalf("over budget = %d, want 429", status)
	}
	// The budget outranks a correct code too: an attacker must not be able to
	// keep guessing past it just because the last guess happened to land.
	if status, _ := unlock(t, anon, id, code); status != fiber.StatusTooManyRequests {
		t.Fatalf("correct code while rate limited = %d, want 429", status)
	}
}

// The budget is keyed on the file as well as the address, so guessing at one
// page never locks a visitor out of another.
func TestShareCodeBudgetIsPerFile(t *testing.T) {
	app, deps := newTestApp(t)
	first := publishHTML(t, app, codedPage)
	second := publishHTML(t, app, codedPage)
	generateCode(t, app, first)
	secondCode := generateCode(t, app, second)

	deps.ShareCodeLimiter = middleware.NewIPLimiter(middleware.NewMemoryCounter(), 1, "sharecodetest")
	anon := anonApp(t, deps)

	for i := 0; i < 3; i++ {
		unlock(t, anon, first, "000000")
	}
	if status, cookie := unlock(t, anon, second, secondCode); status != fiber.StatusNoContent || cookie == nil {
		t.Fatalf("second page unlock = %d, cookie set: %v", status, cookie != nil)
	}
}

// Regenerating the code is how an owner revokes access: the version bump has to
// invalidate every ticket already handed out.
func TestRegeneratingTheCodeInvalidatesOldTickets(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, codedPage)
	first := generateCode(t, app, id)
	anon := anonApp(t, deps)

	_, oldCookie := unlock(t, anon, id, first)
	if mc, _ := anonGetWithCookie(t, anon, "/s/"+id+"/meta", oldCookie, map[string]string{"Sec-Fetch-Mode": "cors"}); mc != fiber.StatusOK {
		t.Fatalf("meta before rotation = %d, want 200", mc)
	}

	second := generateCode(t, app, id)
	if second == first {
		t.Fatal("regenerate returned the same code")
	}
	if mc, _ := anonGetWithCookie(t, anon, "/s/"+id+"/meta", oldCookie, map[string]string{"Sec-Fetch-Mode": "cors"}); mc != fiber.StatusForbidden {
		t.Fatalf("meta with stale ticket = %d, want 403", mc)
	}
	if rc, _ := anonGetWithCookie(t, anon, "/s/"+id+"/render", oldCookie, map[string]string{"Sec-Fetch-Dest": "iframe"}); rc != fiber.StatusForbidden {
		t.Fatalf("render with stale ticket = %d, want 403", rc)
	}
	if status, _ := unlock(t, anon, id, first); status != fiber.StatusForbidden {
		t.Fatalf("old code after rotation = %d, want 403", status)
	}

	_, newCookie := unlock(t, anon, id, second)
	if mc, _ := anonGetWithCookie(t, anon, "/s/"+id+"/meta", newCookie, map[string]string{"Sec-Fetch-Mode": "cors"}); mc != fiber.StatusOK {
		t.Fatalf("meta with the new ticket = %d, want 200", mc)
	}
}

func TestClearingTheCodeReopensThePage(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, codedPage)
	code := generateCode(t, app, id)
	anon := anonApp(t, deps)

	if status, body := doJSON(t, app, "DELETE", "/api/files/"+id+"/share-code", ""); status != fiber.StatusNoContent {
		t.Fatalf("clear = %d %s", status, body)
	}
	if mc, _ := anonMeta(t, anon, id); mc != fiber.StatusOK {
		t.Fatalf("meta after clear = %d, want 200", mc)
	}
	// Nothing left to unlock: the endpoint must not confirm which pages once
	// had a code.
	if status, _ := unlock(t, anon, id, code); status != fiber.StatusNotFound {
		t.Fatalf("unlock after clear = %d, want 404", status)
	}
}

// The owner never has to type their own code.
func TestOwnerReadsTheirCodedPageWithoutUnlocking(t *testing.T) {
	app, _ := newTestApp(t)
	id := publishHTML(t, app, codedPage)
	generateCode(t, app, id)

	req := httptest.NewRequest("GET", "/s/"+id+"/meta", nil)
	req.Header.Set("Sec-Fetch-Mode", "cors")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("owner meta = %d, want 200", resp.StatusCode)
	}
}

// A `private` page keeps answering 404, code or no code: the share code is a
// second gate behind authz.View, never a replacement for it.
func TestPrivateCodedPageStays404ForStrangers(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, codedPage)
	code := generateCode(t, app, id)
	if status, b := doJSON(t, app, "PATCH", "/api/files/"+id, `{"visibility":"private"}`); status != fiber.StatusNoContent {
		t.Fatalf("set private = %d %s", status, b)
	}
	anon := anonApp(t, deps)

	if sc, body := anonShell(t, anon, id); sc != fiber.StatusNotFound || strings.Contains(body, unlockPrompt) {
		t.Fatalf("private coded shell = %d, prompt shown: %v", sc, strings.Contains(body, unlockPrompt))
	}
	if status, _ := unlock(t, anon, id, code); status != fiber.StatusNotFound {
		t.Fatalf("unlock on a private page = %d, want 404", status)
	}
}

// --- Open Graph ----------------------------------------------------------

// Pasting a coded link into a chat must preview the title and nothing else:
// og:description is a sentence lifted out of the page the code gates.
func TestLockedPageOpenGraphIsTitleOnly(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, codedPage)
	generateCode(t, app, id)
	anon := anonApp(t, deps)

	_, shell := anonShell(t, anon, id)
	if !strings.Contains(shell, `<meta property="og:title" content="季度复盘">`) {
		t.Fatalf("og:title missing from %q", shell)
	}
	if strings.Contains(shell, "og:description") || strings.Contains(shell, "内部数据") {
		t.Fatal("locked page leaked og:description")
	}
}

// The unlock prompt is a different document from the viewer shell, so it has to
// be served under its own inline hashes — the shell's would silently block its
// style and its submit script.
func TestLockedPageServesItsOwnCSPHashes(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, codedPage)
	generateCode(t, app, id)
	anon := anonApp(t, deps)

	resp := anonGet(t, anon, "/s/"+id, nil)
	csp := resp.Header.Get("Content-Security-Policy")
	unlock, shell := unlockShells[i18n.EN], viewShells[i18n.EN]
	if !strings.Contains(csp, unlock.scriptHash) || !strings.Contains(csp, unlock.styleHash) {
		t.Fatalf("locked shell CSP = %q, want the unlock page's hashes", csp)
	}
	if strings.Contains(csp, shell.scriptHash) {
		t.Fatalf("locked shell CSP names the viewer shell's script hash: %q", csp)
	}
}

// --- the code never leaks ------------------------------------------------

// The owner's own file list is the only place the plaintext appears.
func TestShareCodeIsVisibleOnlyToTheOwner(t *testing.T) {
	f := newAdminFixture(t)
	id := f.publishAs(t, f.userCookie, codedPage)

	status, raw, _ := f.write(t, "POST", "/api/files/"+id+"/share-code", "", f.userCookie)
	if status != fiber.StatusOK {
		t.Fatalf("generate = %d %s", status, raw)
	}
	var minted struct {
		ShareCode string `json:"share_code"`
	}
	if err := json.Unmarshal(raw, &minted); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}

	if _, own := f.get(t, "/api/files", f.userCookie); !strings.Contains(string(own), minted.ShareCode) {
		t.Fatalf("owner's own list is missing the code: %s", own)
	}

	// Everything a non-owner (here, the instance admin) can reach.
	for _, path := range []string{"/api/files", "/api/admin/users", "/s/" + id, "/s/" + id + "/meta"} {
		_, body := f.get(t, path, f.adminCookie)
		if strings.Contains(string(body), minted.ShareCode) {
			t.Fatalf("%s leaked the share code: %s", path, body)
		}
	}
}

// `placard ls` runs over a PAT, and it must not be a way to read back the codes
// to every coded page the owner has — that is more than BrowserOnly refuses a
// token on the share routes themselves.
func TestShareCodeIsWithheldFromThePATChannel(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, codedPage)
	code := generateCode(t, app, id)

	patApp := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	patApp.Use(middleware.RequestID())
	patApp.Use(func(c *fiber.Ctx) error {
		userctx.Set(c, userctx.Identity{AuthzID: testAuthzID, AuthChannel: userctx.ChannelPAT})
		return c.Next()
	})
	Register(patApp, deps)

	status, body := doJSON(t, patApp, "GET", "/api/files", "")
	if status != fiber.StatusOK {
		t.Fatalf("token list = %d %s", status, body)
	}
	// The list itself still works — only the code is missing from it.
	if !strings.Contains(string(body), id) {
		t.Fatalf("token list dropped the page itself: %s", body)
	}
	if strings.Contains(string(body), code) {
		t.Fatalf("token list leaked the share code: %s", body)
	}
}

// --- key rotation --------------------------------------------------------

// A code sealed under a key the instance no longer has cannot be shown to its
// owner or checked against a submission, so the page reads as uncoded rather
// than as permanently unreachable. Losing the key must not lock an owner out of
// their own content.
func TestUndecryptableCodeReadsAsNoCode(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, codedPage)
	code := generateCode(t, app, id)

	deps.Cfg.Server.SecretKey = "a-rotated-secret-key"
	rotated := anonApp(t, deps)

	if mc, _ := anonMeta(t, rotated, id); mc != fiber.StatusOK {
		t.Fatalf("meta after key rotation = %d, want 200 (page reads as uncoded)", mc)
	}
	if rc, body := anonRender(t, rotated, id); rc != fiber.StatusOK || !strings.Contains(body, "secret body") {
		t.Fatalf("render after key rotation = %d %q", rc, body)
	}
	if status, _ := unlock(t, rotated, id, code); status != fiber.StatusNotFound {
		t.Fatalf("unlock after key rotation = %d, want 404", status)
	}
	// The owner's list shows no code either — there is nothing left to show.
	_, list := doJSON(t, app, "GET", "/api/files", "")
	if strings.Contains(string(list), `"share_code"`) {
		t.Fatalf("owner list reported an unreadable code: %s", list)
	}
}

// --- publish --password auto ---------------------------------------------

func TestPublishWithPasswordAutoMintsACode(t *testing.T) {
	app, deps := newTestApp(t)
	body, err := json.Marshal(map[string]string{"html": codedPage, "expiry": "never", "password": "auto"})
	if err != nil {
		t.Fatal(err)
	}
	status, raw := doJSON(t, app, "POST", "/api/publish", string(body))
	if status != fiber.StatusOK {
		t.Fatalf("publish = %d %s", status, raw)
	}
	var resp struct {
		ID        string `json:"id"`
		ShareCode string `json:"share_code"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if !sharecode.Valid(resp.ShareCode) {
		t.Fatalf("share_code = %q, want 6 digits", resp.ShareCode)
	}

	anon := anonApp(t, deps)
	if mc, _ := anonMeta(t, anon, resp.ID); mc != fiber.StatusForbidden {
		t.Fatalf("meta = %d, want 403 — the code was not in force from the first request", mc)
	}
	if s, cookie := unlock(t, anon, resp.ID, resp.ShareCode); s != fiber.StatusNoContent || cookie == nil {
		t.Fatalf("unlock with the minted code = %d", s)
	}
}

func TestPublishRejectsAChosenPassword(t *testing.T) {
	app, _ := newTestApp(t)
	body, _ := json.Marshal(map[string]string{"html": codedPage, "password": "123456"})
	if status, raw := doJSON(t, app, "POST", "/api/publish", string(body)); status != fiber.StatusBadRequest {
		t.Fatalf("publish = %d %s, want 400", status, raw)
	}
}

// --- ownership -----------------------------------------------------------

// Both mutations are owner-only, and a stranger's id is indistinguishable from
// one that does not exist.
func TestShareCodeMutationsAreOwnerOnly(t *testing.T) {
	f := newAdminFixture(t)
	id := f.publishAs(t, f.userCookie, codedPage)

	for _, method := range []string{"POST", "DELETE"} {
		if status, _, _ := f.write(t, method, "/api/files/"+id+"/share-code", "", f.adminCookie); status != fiber.StatusNotFound {
			t.Fatalf("%s as a non-owner = %d, want 404", method, status)
		}
	}
	if status, _, _ := f.write(t, "POST", "/api/files/zzzzzzzz/share-code", "", f.userCookie); status != fiber.StatusNotFound {
		t.Fatalf("unknown id = %d, want 404", status)
	}
}
