package handler

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/devicecode"
	"github.com/Xm798/placard/internal/i18n"
)

// The pages below are rendered before any JavaScript runs, so Accept-Language
// is the only thing they have to go on. Each test pins both halves of that:
// the default (no header) is English, and a Chinese-speaking browser gets
// Chinese — asserting only one would let the page get stuck in one language.

const acceptLanguage = "Accept-Language"

// assertLanguage checks the page is in `want` and NOT in the other language.
// Absence matters as much as presence: a page that renders both bundles, or
// falls through to a half-translated one, still contains the right words.
func assertLanguage(t *testing.T, page string, want, other string) {
	t.Helper()
	if !strings.Contains(page, want) {
		t.Fatalf("page is missing %q: %.500s", want, page)
	}
	if strings.Contains(page, other) {
		t.Fatalf("page also carries the other language's %q: %.500s", other, page)
	}
}

func htmlLangAttr(t *testing.T, page string) string {
	t.Helper()
	const marker = `<html lang="`
	start := strings.Index(page, marker)
	if start < 0 {
		t.Fatalf("page has no <html lang>: %.200s", page)
	}
	rest := page[start+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("page has an unterminated <html lang>: %.200s", page)
	}
	return rest[:end]
}

// Every page pageLang decided must say so, or a shared cache is free to serve
// one reader's language to the next.
func TestLanguageDependentPagesVaryOnAcceptLanguage(t *testing.T) {
	app, deps := newTestApp(t)
	coded := publishHTML(t, app, codedPage)
	generateCode(t, app, coded)
	plain := publishHTML(t, app, `<!DOCTYPE html><html><head><title>Report</title></head><body>hi</body></html>`)
	iss, err := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{NameHint: "laptop"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	anon := anonApp(t, deps)

	cases := []struct {
		name string
		app  *fiber.App
		path string
	}{
		{"view shell", anon, "/s/" + plain},
		{"unlock prompt", anon, "/s/" + coded},
		{"logged out", app, "/auth/logged-out"},
		{"device confirm", app, "/auth/device?user_code=" + iss.UserCode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := tc.app.Test(httptest.NewRequest("GET", tc.path, nil), -1)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			if vary := resp.Header.Get("Vary"); !strings.Contains(vary, "Accept-Language") {
				t.Fatalf("Vary = %q, want it to name Accept-Language", vary)
			}
		})
	}
}

func TestViewShellFollowsAcceptLanguage(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, `<!DOCTYPE html><html><head><title>Report</title></head><body>hi</body></html>`)
	anon := anonApp(t, deps)

	en, zh := viewShellTextByLang[i18n.EN], viewShellTextByLang[i18n.ZhCN]

	t.Run("default", func(t *testing.T) {
		resp := anonGet(t, anon, "/s/"+id, nil)
		b, _ := io.ReadAll(resp.Body)
		page := string(b)
		assertLanguage(t, page, en.ErrorTitle, zh.ErrorTitle)
		if got := htmlLangAttr(t, page); got != "en" {
			t.Fatalf("<html lang> = %q, want en", got)
		}
	})

	t.Run("chinese", func(t *testing.T) {
		resp := anonGet(t, anon, "/s/"+id, map[string]string{acceptLanguage: "zh-CN,zh;q=0.9,en;q=0.8"})
		b, _ := io.ReadAll(resp.Body)
		page := string(b)
		assertLanguage(t, page, zh.ErrorTitle, en.ErrorTitle)
		if got := htmlLangAttr(t, page); got != "zh-CN" {
			t.Fatalf("<html lang> = %q, want zh-CN", got)
		}
	})

	// The inline blocks differ per language, so their hashes do too. A CSP
	// carrying the other language's hash blocks the page's own script — the
	// viewer would render an empty frame with nothing in the console to explain
	// it.
	t.Run("csp names this language's own hashes", func(t *testing.T) {
		resp := anonGet(t, anon, "/s/"+id, map[string]string{acceptLanguage: "zh-CN"})
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, viewShells[i18n.ZhCN].scriptHash) {
			t.Fatalf("zh shell CSP = %q, want the zh script hash", csp)
		}
		if strings.Contains(csp, viewShells[i18n.EN].scriptHash) {
			t.Fatalf("zh shell CSP names the en script hash: %q", csp)
		}
	})
}

func TestUnlockShellFollowsAcceptLanguage(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishHTML(t, app, codedPage)
	generateCode(t, app, id)
	anon := anonApp(t, deps)

	en, zh := unlockShellTextByLang[i18n.EN], unlockShellTextByLang[i18n.ZhCN]

	t.Run("default", func(t *testing.T) {
		resp := anonGet(t, anon, "/s/"+id, nil)
		b, _ := io.ReadAll(resp.Body)
		assertLanguage(t, string(b), en.Heading, zh.Heading)
	})

	t.Run("chinese", func(t *testing.T) {
		resp := anonGet(t, anon, "/s/"+id, map[string]string{acceptLanguage: "zh"})
		b, _ := io.ReadAll(resp.Body)
		assertLanguage(t, string(b), zh.Heading, en.Heading)
	})

	// Only the script block carries copy (the three error lines), so only its
	// hash differs per language; the <style> is identical and so is its hash.
	t.Run("csp names this language's own script hash", func(t *testing.T) {
		resp := anonGet(t, anon, "/s/"+id, map[string]string{acceptLanguage: "zh"})
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, unlockShells[i18n.ZhCN].scriptHash) {
			t.Fatalf("zh unlock CSP = %q, want the zh script hash", csp)
		}
		if strings.Contains(csp, unlockShells[i18n.EN].scriptHash) {
			t.Fatalf("zh unlock CSP names the en script hash: %q", csp)
		}
	})
}

// The sign-out landing is where a browser navigation ends, so it is the one
// page a reader sees with no session and no SPA loaded.
func TestLoggedOutPageFollowsAcceptLanguage(t *testing.T) {
	app, _ := newTestApp(t)
	en, zh := loggedOutTextByLang[i18n.EN], loggedOutTextByLang[i18n.ZhCN]

	cases := []struct {
		name         string
		header       string
		want, other  landingText
		wantHTMLLang string
	}{
		{"default", "", en, zh, "en"},
		{"chinese", "zh-CN,zh;q=0.9", zh, en, "zh-CN"},
		{"unspoken language falls back", "ko-KR,ko;q=0.9", en, zh, "en"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/auth/logged-out", nil)
			if tc.header != "" {
				req.Header.Set(acceptLanguage, tc.header)
			}
			resp, err := app.Test(req, -1)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			if resp.StatusCode != fiber.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			b, _ := io.ReadAll(resp.Body)
			page := string(b)
			assertLanguage(t, page, tc.want.Heading, tc.other.Heading)
			if !strings.Contains(page, tc.want.Action) {
				t.Fatalf("page is missing the sign-in link %q: %.500s", tc.want.Action, page)
			}
			if got := htmlLangAttr(t, page); got != tc.wantHTMLLang {
				t.Fatalf("<html lang> = %q, want %q", got, tc.wantHTMLLang)
			}
		})
	}
}

func TestDevicePageFollowsAcceptLanguage(t *testing.T) {
	app, deps := newTestApp(t)
	iss, err := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{NameHint: "laptop"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	en, zh := deviceTextByLang[i18n.EN], deviceTextByLang[i18n.ZhCN]

	page := func(t *testing.T, header string) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/auth/device?user_code="+iss.UserCode, nil)
		if header != "" {
			req.Header.Set(acceptLanguage, header)
		}
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	t.Run("default", func(t *testing.T) {
		p := page(t, "")
		assertLanguage(t, p, en.SubmitButton, zh.SubmitButton)
		// The radios are the only place the lifetime is named, so they have to
		// follow the language as well as the prose around them.
		assertLanguage(t, p, deviceTokenTTLOptions[0].label(en), deviceTokenTTLOptions[0].label(zh))
	})

	t.Run("chinese", func(t *testing.T) {
		p := page(t, "zh-TW,zh;q=0.9")
		assertLanguage(t, p, zh.SubmitButton, en.SubmitButton)
		assertLanguage(t, p, deviceTokenTTLOptions[0].label(zh), deviceTokenTTLOptions[0].label(en))
	})
}

// The shells render through text/template, which escapes nothing, so the copy
// has to be safe for the two contexts it lands in.
//
// In a single-quoted JS string literal an apostrophe or a backslash closes or
// escapes the literal and takes the whole inline script down with it — and the
// failure is silent: the CSP hash is computed from the same broken document so
// it still matches, the chrome renders, and the viewer simply never loads
// content with nothing in the console to explain it.
//
// In HTML text an unescaped angle bracket or ampersand injects markup. The
// rules are comments in pagetext.go; this is what enforces them.
func TestShellCopyIsSafeInItsRenderingContext(t *testing.T) {
	forbid := func(t *testing.T, where, chars, s string) {
		t.Helper()
		if strings.ContainsAny(s, chars) {
			t.Fatalf("%s copy %q contains one of %q", where, s, chars)
		}
	}
	const jsUnsafe = "'\\"
	const htmlUnsafe = "<>&"

	for lang, text := range viewShellTextByLang {
		where := "view shell " + string(lang)
		// ExternalTitle and ExternalBody reach both contexts: the dialog renders
		// them as HTML text, and the script rewrites them for a same-origin link.
		for _, s := range []string{text.LatestSuffix, text.SharedSuffix, text.ExternalTitle,
			text.ExternalBody, text.SharePageTitle, text.SharePageBody} {
			forbid(t, where, jsUnsafe, s)
		}
		for _, s := range []string{text.BrandHome, text.VersionSelect, text.FrameTitle,
			text.ErrorTitle, text.ErrorBody, text.HomeLink, text.ExternalTitle,
			text.ExternalBody, text.SessionNote, text.CancelButton, text.OpenLinkButton} {
			forbid(t, where, htmlUnsafe, s)
		}
	}
	for lang, text := range unlockShellTextByLang {
		where := "unlock shell " + string(lang)
		for _, s := range []string{text.CookieError, text.RateError, text.WrongError, text.NetworkError} {
			forbid(t, where, jsUnsafe, s)
		}
		for _, s := range []string{text.Title, text.Heading, text.Body, text.InputLabel,
			text.SubmitButton, text.HomeLink} {
			forbid(t, where, htmlUnsafe, s)
		}
	}
}
