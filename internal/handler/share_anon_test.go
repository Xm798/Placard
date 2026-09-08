package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/httpx"
	"github.com/Xm798/placard/internal/middleware"
	"github.com/Xm798/placard/internal/model"
)

// anonApp mounts the routes a second time over the SAME deps as the caller's
// authenticated app, with no identity injected. It is the view a visitor with
// no account gets of a page published through the authenticated app — the
// production equivalent of a request that carries no session cookie.
func anonApp(t *testing.T, deps Deps) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(middleware.RequestID())
	Register(app, deps)
	return app
}

func anonGet(t *testing.T, app *fiber.App, path string, headers map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func anonShell(t *testing.T, app *fiber.App, id string) (int, string) {
	t.Helper()
	resp := anonGet(t, app, "/s/"+id, nil)
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func anonMeta(t *testing.T, app *fiber.App, id string) (int, map[string]any) {
	t.Helper()
	resp := anonGet(t, app, "/s/"+id+"/meta", map[string]string{"Sec-Fetch-Mode": "cors"})
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("meta body %s: %v", b, err)
	}
	return resp.StatusCode, m
}

func anonRender(t *testing.T, app *fiber.App, id string) (int, string) {
	t.Helper()
	resp := anonGet(t, app, "/s/"+id+"/render", map[string]string{"Sec-Fetch-Dest": "iframe"})
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// publishHTML publishes raw HTML through the authenticated app and returns its
// nano id, so a test can control the <title> / <meta name=description> the
// publish path snapshots.
func publishHTML(t *testing.T, app *fiber.App, html string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"html": html, "expiry": "never"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	code, b := doJSON(t, app, "POST", "/api/publish", string(body))
	if code != fiber.StatusOK {
		t.Fatalf("publish = %d %s", code, b)
	}
	return decodePublish(t, b).ID
}

// --- link pages are open to anyone holding the link ---

// TestAnonSeesLinkPage walks the whole no-cookie path a pasted link takes:
// shell, meta, and the rendered bytes.
func TestAnonSeesLinkPage(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, "<!DOCTYPE html><html><head><title>公开页</title></head><body>hello anon</body></html>")
	anon := anonApp(t, deps)

	if code, body := anonShell(t, anon, id); code != fiber.StatusOK || !strings.Contains(body, "<iframe") {
		t.Fatalf("shell = %d (iframe present: %v)", code, strings.Contains(body, "<iframe"))
	}
	code, m := anonMeta(t, anon, id)
	if code != fiber.StatusOK || m["expired"] != false || m["title"] != "公开页" {
		t.Fatalf("meta = %d %v", code, m)
	}
	if m["render_url"] != "/s/"+id+"/render" {
		t.Fatalf("render_url = %v", m["render_url"])
	}
	rcode, rbody := anonRender(t, anon, id)
	if rcode != fiber.StatusOK || !strings.Contains(rbody, "hello anon") {
		t.Fatalf("render = %d %q", rcode, rbody)
	}
}

// TestAnonPrivateIsIndistinguishableFromMissing pins the whole point of the
// 404: a private page, an expired page and an id that never existed must all
// answer identically on all three routes, so nothing about them is probeable.
func TestAnonPrivateIsIndistinguishableFromMissing(t *testing.T) {
	app, deps := newTestApp(t)
	anon := anonApp(t, deps)

	private := publishHTML(t, app, "<!DOCTYPE html><html><head><title>秘密</title></head><body>secret</body></html>")
	if code, b := doJSON(t, app, "PATCH", "/api/files/"+private, `{"visibility":"private"}`); code != fiber.StatusNoContent {
		t.Fatalf("set private = %d %s", code, b)
	}
	expired := publishHTML(t, app, "<!DOCTYPE html><html><head><title>过期</title></head><body>gone</body></html>")
	if err := deps.DB.Model(&model.File{}).Where("nano_id = ?", expired).
		Update("expires_at", model.Timestamp(time.Now().Add(-time.Hour))).Error; err != nil {
		t.Fatalf("expire: %v", err)
	}

	for _, tc := range []struct{ name, id string }{
		{"private", private},
		{"expired", expired},
		{"never existed", "zzzzzzzz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := anonShell(t, anon, tc.id)
			if code != fiber.StatusNotFound {
				t.Errorf("shell = %d, want 404", code)
			}
			if strings.Contains(body, "og:") {
				t.Errorf("shell leaked Open Graph tags: %s", body)
			}
			mcode, m := anonMeta(t, anon, tc.id)
			if mcode != fiber.StatusOK || len(m) != 1 || m["expired"] != true {
				t.Errorf("meta = %d %v, want 200 {expired:true}", mcode, m)
			}
			if rcode, _ := anonRender(t, anon, tc.id); rcode != fiber.StatusNotFound {
				t.Errorf("render = %d, want 404", rcode)
			}
		})
	}
}

// TestAnonPrivateShellLeaksNoTitle: the 404 shell is the generic body, so the
// page's own title never reaches someone who may not read it.
func TestAnonPrivateShellLeaksNoTitle(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, "<!DOCTYPE html><html><head><title>机密标题</title></head><body>x</body></html>")
	if code, b := doJSON(t, app, "PATCH", "/api/files/"+id, `{"visibility":"private"}`); code != fiber.StatusNoContent {
		t.Fatalf("set private = %d %s", code, b)
	}

	_, body := anonShell(t, anonApp(t, deps), id)
	if strings.Contains(body, "机密标题") {
		t.Fatalf("404 shell leaked the title: %s", body)
	}
}

// TestOwnerStillReachesOwnPrivatePage: collapsing denial into 404 must not cost
// the owner access to their own page.
func TestOwnerStillReachesOwnPrivatePage(t *testing.T) {
	app, _ := newTestApp(t)
	id := publishHTML(t, app, "<!DOCTYPE html><html><head><title>我的私有页</title></head><body>mine</body></html>")
	if code, b := doJSON(t, app, "PATCH", "/api/files/"+id, `{"visibility":"private"}`); code != fiber.StatusNoContent {
		t.Fatalf("set private = %d %s", code, b)
	}

	resp := anonGet(t, app, "/s/"+id, nil) // app injects the owner identity
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("owner shell = %d, want 200", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `content="我的私有页"`) {
		t.Fatalf("owner shell missing its own og:title: %s", b)
	}
}

// --- Open Graph ---

func TestViewShellOpenGraph(t *testing.T) {
	app, deps := newTestApp(t)
	anon := anonApp(t, deps)

	withDesc := publishHTML(t, app, `<!DOCTYPE html><html><head><title>标题</title>`+
		`<meta name="description" content="页面描述"></head><body>x</body></html>`)
	noDesc := publishHTML(t, app, `<!DOCTYPE html><html><head><title>只有标题</title></head><body>x</body></html>`)

	_, body := anonShell(t, anon, withDesc)
	for _, want := range []string{
		`<meta property="og:type" content="website">`,
		`<meta property="og:url" content="https://placard.example.com/s/` + withDesc + `">`,
		`<meta property="og:title" content="标题">`,
		`<meta property="og:description" content="页面描述">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("shell missing %s", want)
		}
	}

	// No <meta name=description> on the page: og:description repeats the title
	// rather than being omitted, so a preview card is never half-empty.
	_, body = anonShell(t, anon, noDesc)
	if !strings.Contains(body, `<meta property="og:description" content="只有标题">`) {
		t.Errorf("shell did not fall back to the title for og:description: %s", body)
	}
}

// TestViewShellOpenGraphEscapes: title and description are user content
// reaching the shell's own markup, and are the only such values on the page.
func TestViewShellOpenGraphEscapes(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, `<!DOCTYPE html><html><head><title>a"&gt;&lt;script&gt;x</title>`+
		`<meta name="description" content="d&quot;&gt;bad"></head><body>x</body></html>`)

	_, body := anonShell(t, anonApp(t, deps), id)
	if strings.Contains(body, `<script>x`) {
		t.Errorf("og:title escaped out of its attribute: %s", body)
	}
	if !strings.Contains(body, `content="a&#34;&gt;&lt;script&gt;x"`) {
		t.Errorf("og:title not attribute-escaped: %s", body)
	}
	if !strings.Contains(body, `content="d&#34;&gt;bad"`) {
		t.Errorf("og:description not attribute-escaped: %s", body)
	}
}

// TestViewShellKeepsSingleInlineBlocks: splicing the Open Graph block in must
// not introduce a second inline <script>/<style>, or the CSP hashes computed
// over the template would stop admitting the shell's own code.
func TestViewShellKeepsSingleInlineBlocks(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, `<!DOCTYPE html><html><head><title>t</title></head><body>x</body></html>`)

	resp := anonGet(t, anonApp(t, deps), "/s/"+id, nil)
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	if strings.Count(body, "<script>") != 1 || strings.Count(body, "<style>") != 1 {
		t.Fatalf("inline blocks = %d script / %d style, want 1 each",
			strings.Count(body, "<script>"), strings.Count(body, "<style>"))
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'sha256-") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("shell CSP must hash-admit the inline script, got %q", csp)
	}
}

// --- view accounting ---

// TestAnonRenderCountsViews: anonymous reads land on the one empty-viewer row
// per file, and the cron recompute totals the same number the render path
// incremented.
func TestAnonRenderCountsViews(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, "<!DOCTYPE html><html><head><title>t</title></head><body>x</body></html>")
	anon := anonApp(t, deps)

	for i := 0; i < 3; i++ {
		if code, _ := anonRender(t, anon, id); code != fiber.StatusOK {
			t.Fatalf("render %d = %d", i, code)
		}
	}

	var rows []model.View
	if err := deps.DB.Where("file_nano_id = ?", id).Find(&rows).Error; err != nil {
		t.Fatalf("read views: %v", err)
	}
	if len(rows) != 1 || rows[0].Viewer != "" || rows[0].ViewCount != 3 {
		t.Fatalf("view rows = %+v, want one empty-viewer row with count 3", rows)
	}

	if _, err := deps.Files.RecomputeViewCounts(context.Background()); err != nil {
		t.Fatalf("recompute: %v", err)
	}
	var f model.File
	if err := deps.DB.Where("nano_id = ?", id).First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	if f.ViewCount != 3 {
		t.Fatalf("file.view_count = %d, want 3 (cron recompute must agree)", f.ViewCount)
	}
}

// --- per-IP budget for anonymous renders ---

// limitedDeps returns deps with an anonymous per-IP budget of limit requests.
func limitedDeps(deps Deps, limit int) Deps {
	deps.AnonRenderLimiter = middleware.AnonOnly(
		middleware.NewIPLimiter(middleware.NewMemoryCounter(), limit, "anonrender").Handler())
	return deps
}

// TestAnonShareRoutesRateLimited: the budget covers all three share routes, not
// just /render. Each one costs a database read before it can answer, and /meta
// writes a denial audit row, so an unmetered route is a write loop an anonymous
// caller can run against a page they cannot read.
func TestAnonShareRoutesRateLimited(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, "<!DOCTYPE html><html><head><title>t</title></head><body>x</body></html>")

	for _, tc := range []struct {
		name string
		get  func(*testing.T, *fiber.App, string) (int, string)
	}{
		{"shell", anonShell},
		{"render", anonRender},
		{"meta", func(t *testing.T, a *fiber.App, id string) (int, string) {
			code, _ := anonMeta(t, a, id)
			return code, ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const limit = 2
			anon := anonApp(t, limitedDeps(deps, limit))
			for i := 0; i < limit; i++ {
				if code, _ := tc.get(t, anon, id); code != fiber.StatusOK {
					t.Fatalf("request %d = %d, want 200 (within budget)", i, code)
				}
			}
			if code, _ := tc.get(t, anon, id); code != fiber.StatusTooManyRequests {
				t.Fatalf("over budget = %d, want 429", code)
			}
		})
	}
}

// TestOwnerExemptFromAnonBudget: same limiter, same IP, but an identity — an
// owner never competes with the visitors of a page they published.
func TestOwnerExemptFromAnonBudget(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, "<!DOCTYPE html><html><head><title>t</title></head><body>x</body></html>")

	owner := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	owner.Use(middleware.RequestID())
	owner.Use(injectTestIdentity)
	Register(owner, limitedDeps(deps, 1))

	for i := 0; i < 4; i++ {
		if code, _ := anonRender(t, owner, id); code != fiber.StatusOK {
			t.Fatalf("owner render %d = %d, want 200 (exempt from the anonymous budget)", i, code)
		}
	}
}

// TestAnonViewIsCountedButNotAudited: audit_log has no retention sweep, so a
// row per anonymous page view would grow without bound while naming nobody.
// The view row still counts the read.
func TestAnonViewIsCountedButNotAudited(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, "<!DOCTYPE html><html><head><title>t</title></head><body>x</body></html>")

	if code, _ := anonRender(t, anonApp(t, deps), id); code != fiber.StatusOK {
		t.Fatalf("anon render = %d", code)
	}
	if n := viewAuditCountFor(t, deps, id); n != 0 {
		t.Errorf("file.view audits after an anonymous read = %d, want 0", n)
	}
	if n := viewCountFor(t, deps, id); n != 1 {
		t.Errorf("view rows = %d, want 1 (the read is still counted)", n)
	}

	// A named reader is still audited — that row attributes the read to an actor.
	if resp := anonGet(t, app, "/s/"+id+"/render", map[string]string{"Sec-Fetch-Dest": "iframe"}); resp.StatusCode != fiber.StatusOK {
		t.Fatalf("owner render = %d", resp.StatusCode)
	}
	if n := viewAuditCountFor(t, deps, id); n != 1 {
		t.Errorf("file.view audits after a signed-in read = %d, want 1", n)
	}
}

// --- description snapshot ---

// TestDescriptionFollowsServingVersion: og:description is read from the file
// row, so the snapshot has to travel the same serving-cache path the title does
// — through a republish, and through a pin back to an older version.
func TestDescriptionFollowsServingVersion(t *testing.T) {
	app, deps := newTestApp(t)
	anon := anonApp(t, deps)

	page := func(desc string) string {
		return `<!DOCTYPE html><html><head><title>t</title>` +
			`<meta name="description" content="` + desc + `"></head><body>x</body></html>`
	}
	id := publishHTML(t, app, page("v1 描述"))
	if code, b := republish(t, app, id, page("v2 描述"), ""); code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}
	assertServingCache(t, deps, id)
	if _, body := anonShell(t, anon, id); !strings.Contains(body, `content="v2 描述"`) {
		t.Fatalf("republish did not refresh the description: %s", body)
	}

	if code, b := doJSON(t, app, "PATCH", "/api/files/"+id, `{"shared_version":1}`); code != fiber.StatusNoContent {
		t.Fatalf("pin = %d %s", code, b)
	}
	assertServingCache(t, deps, id)
	if _, body := anonShell(t, anon, id); !strings.Contains(body, `content="v1 描述"`) {
		t.Fatalf("pin did not restore the v1 description: %s", body)
	}
}

// TestShareRoutesAreUncacheable: every share route's body depends on who is
// asking, so a shared cache must never serve one visitor's copy to the next —
// that would hand an anonymous visitor the owner's view of a private page.
func TestShareRoutesAreUncacheable(t *testing.T) {
	app, _ := newTestApp(t)
	id := publishHTML(t, app, "<!DOCTYPE html><html><head><title>t</title></head><body>x</body></html>")

	for _, path := range []string{"/s/" + id, "/s/" + id + "/meta", "/s/" + id + "/render"} {
		resp := anonGet(t, app, path, map[string]string{"Sec-Fetch-Dest": "iframe"})
		if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s Cache-Control = %q, want no-store", path, cc)
		}
	}
}

// TestViewShellPlaceholderNeverServed: the marker openGraphTags is spliced at
// must not survive into any response, viewable or not.
func TestViewShellPlaceholderNeverServed(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, "<!DOCTYPE html><html><head><title>t</title></head><body>x</body></html>")

	for _, tc := range []struct {
		name string
		app  *fiber.App
		id   string
	}{
		{"viewable", anonApp(t, deps), id},
		{"not viewable", anonApp(t, deps), "zzzzzzzz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, body := anonShell(t, tc.app, tc.id); strings.Contains(body, ogPlaceholder) {
				t.Fatalf("shell served the %s marker", ogPlaceholder)
			}
		})
	}
}

// TestAnonLinkPageThroughRealAuthMiddleware is the acceptance criterion taken
// literally: a request with no cookie at all, through the production auth
// middleware rather than a test identity injector, reaches a `link` page's
// shell and its rendered bytes.
func TestAnonLinkPageThroughRealAuthMiddleware(t *testing.T) {
	app, deps, _ := newSessionTestApp(t)
	seedForeignFileVisibility(t, deps, "anonreal", "u_bob", model.VisibilityLink)

	if code, body := anonShell(t, app, "anonreal"); code != fiber.StatusOK ||
		!strings.Contains(body, `<meta property="og:title" content="not yours">`) {
		t.Fatalf("shell = %d, og:title present: %v", code, strings.Contains(body, "og:title"))
	}
	if code, body := anonRender(t, app, "anonreal"); code != fiber.StatusOK ||
		!strings.Contains(body, "secret") {
		t.Fatalf("render = %d %q", code, body)
	}

	// The same request against a private page is the 404 a missing id gets.
	seedForeignFileVisibility(t, deps, "anonpriv", "u_bob", model.VisibilityPrivate)
	if code, _ := anonShell(t, app, "anonpriv"); code != fiber.StatusNotFound {
		t.Fatalf("private shell = %d, want 404", code)
	}
	if code, _ := anonRender(t, app, "anonpriv"); code != fiber.StatusNotFound {
		t.Fatalf("private render = %d, want 404", code)
	}
}
