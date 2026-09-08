package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/devicecode"
	"github.com/Xm798/placard/internal/i18n"
)

func TestDeviceCodeResponseShape(t *testing.T) {
	app, deps := newTestApp(t)

	status, body := doJSON(t, app, "POST", "/auth/device/code", `{"hostname":"MacBook-Pro"}`)
	if status != 200 {
		t.Fatalf("status = %d body = %s", status, body)
	}
	var out struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if len(out.DeviceCode) != devicecode.DeviceCodeLen {
		t.Fatalf("device_code len = %d, want %d", len(out.DeviceCode), devicecode.DeviceCodeLen)
	}
	if len(out.UserCode) != devicecode.UserCodeLen {
		t.Fatalf("user_code len = %d, want %d", len(out.UserCode), devicecode.UserCodeLen)
	}
	if out.ExpiresIn != 180 || out.Interval != 5 {
		t.Fatalf("expires_in=%d interval=%d, want 180/5", out.ExpiresIn, out.Interval)
	}
	want := deps.Cfg.Server.BaseURL + "/auth/device"
	if out.VerificationURI != want {
		t.Fatalf("verification_uri = %q, want %q", out.VerificationURI, want)
	}
	if wantComplete := want + "?user_code=" + out.UserCode; out.VerificationURIComplete != wantComplete {
		t.Fatalf("verification_uri_complete = %q, want %q", out.VerificationURIComplete, wantComplete)
	}
	// The hostname from the body must actually reach the record, sanitized.
	// Without this the whole sanitize path is dead code the moment a client
	// stops sending a body — and every token in /settings reads "CLI on unknown".
	_, rec, err := deps.DeviceCodes.ByUserCode(t.Context(), out.UserCode)
	if err != nil {
		t.Fatalf("ByUserCode: %v", err)
	}
	if rec.NameHint != "MacBook-Pro" {
		t.Fatalf("NameHint = %q, want %q", rec.NameHint, "MacBook-Pro")
	}
}

// TestDeviceCodeWithoutBodyFallsBackToUnknown pins backward compatibility: the
// hostname field is optional, so a client that sends no body at all still gets
// a working authorization rather than a 400.
func TestDeviceCodeWithoutBodyFallsBackToUnknown(t *testing.T) {
	app, deps := newTestApp(t)
	status, body := doJSON(t, app, "POST", "/auth/device/code", "")
	if status != 200 {
		t.Fatalf("status = %d body = %s", status, body)
	}
	var out struct {
		UserCode string `json:"user_code"`
	}
	_ = json.Unmarshal(body, &out)
	_, rec, err := deps.DeviceCodes.ByUserCode(t.Context(), out.UserCode)
	if err != nil {
		t.Fatalf("ByUserCode: %v", err)
	}
	if rec.NameHint != "unknown" {
		t.Fatalf("NameHint = %q, want unknown", rec.NameHint)
	}
}

// TestDeviceCodeBareVerificationURIStaysBare: the complete URI carries the
// code (that is the point), but the bare one must stay clean — it is the form a
// user reads off to a phone, and it is the fallback a client uses when it wants
// the manual-entry page.
func TestDeviceCodeBareVerificationURIStaysBare(t *testing.T) {
	app, _ := newTestApp(t)
	_, body := doJSON(t, app, "POST", "/auth/device/code", `{}`)
	var out struct {
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
	}
	_ = json.Unmarshal(body, &out)
	if strings.Contains(out.VerificationURI, out.UserCode) ||
		strings.Contains(strings.ToLower(out.VerificationURI), "user_code") {
		t.Fatalf("verification_uri %q must not carry the code", out.VerificationURI)
	}
	if !strings.Contains(out.VerificationURIComplete, out.UserCode) {
		t.Fatalf("verification_uri_complete %q must carry the code", out.VerificationURIComplete)
	}
}

// TestDevicePagePrefillsUserCodeFromQuery is the functional guard for the
// one-click shape: the CLI now opens ?user_code=, and if the input stops being
// prefilled the user is silently back to transcribing a code by hand.
func TestDevicePagePrefillsUserCodeFromQuery(t *testing.T) {
	app, deps := newTestApp(t)
	iss, err := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{NameHint: "laptop"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest("GET", "/auth/device?user_code="+iss.UserCode, nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	page := string(raw)
	if !strings.Contains(page, `id="user_code" name="user_code"`) {
		t.Fatalf("no user_code input in the page: %.400s", page)
	}
	if !strings.Contains(page, `value="`+iss.UserCode+`"`) {
		t.Fatalf("user_code %q not prefilled: %.600s", iss.UserCode, page)
	}
	// The requester block is what a suspicious user reads; it must survive too.
	if !strings.Contains(page, "laptop") {
		t.Fatalf("requester hostname missing: %.600s", page)
	}
}

// TestDevicePageDoesNotEchoMalformedUserCode: the query value is
// attacker-controlled, so only codes shaped like a real one get reflected into
// the input. html/template escapes the attribute either way — this keeps the
// page from asserting that some arbitrary string is a code awaiting approval.
func TestDevicePageDoesNotEchoMalformedUserCode(t *testing.T) {
	app, _ := newTestApp(t)
	for _, bad := range []string{`"><script>x</script>`, "SHORT", "ZZZZZZZZZZZZZZZZ", "ABCD0O1I"} {
		req := httptest.NewRequest("GET", "/auth/device?user_code="+url.QueryEscape(bad), nil)
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(raw), `value="`+bad+`"`) {
			t.Fatalf("malformed code %q was echoed into the input", bad)
		}
		if !strings.Contains(string(raw), `value=""`) {
			t.Fatalf("input should be empty for %q: %.400s", bad, raw)
		}
	}
}

// TestApproveRejectedSubmissionKeepsTheCode: a rejected approval re-renders,
// and the code has to come back with it — an emptied field after a transient
// failure reads as "start over" and the flow's 180s budget does not allow much
// of that.
func TestApproveRejectedSubmissionKeepsTheCode(t *testing.T) {
	app, deps := newTestApp(t)
	_, token := renderDevicePage(t, app)

	// A well-formed code that no longer exists: exactly the expiry case.
	const gone = "K7M2X9PQ"
	status, body := approveForm(t, app, deps.Cfg.Server.BaseURL, token, gone, nil)
	if status != 400 {
		t.Fatalf("status = %d, want 400", status)
	}
	if !strings.Contains(string(body), `value="`+gone+`"`) {
		t.Fatalf("rejected submission dropped the code: %.600s", body)
	}
}

// The confirmation page's three mutually exclusive status lines, read out of
// the copy the page itself renders from. Test requests carry no
// Accept-Language, so the page comes back in the default language.
var (
	deviceLineCompare = string(deviceTextByLang[i18n.EN].CompareWarning)
	deviceLineExpired = deviceTextByLang[i18n.EN].ExpiredWarning
	deviceLineManual  = deviceTextByLang[i18n.EN].ManualWarning
)

// assertOnlyStatusLine pins both halves of the page's wording invariant at once:
// the line that applies renders, and the others do not. Asserting only the
// absence of the wrong line would let a state render nothing at all, which is
// how the user ends up with no next step.
//
// want == "" asserts that no status line renders — legitimate only where the
// inline error already carries the whole message; the caller is then responsible
// for asserting that error is present, or the state says nothing to anyone.
func assertOnlyStatusLine(t *testing.T, page, want string) {
	t.Helper()
	for _, line := range []string{deviceLineCompare, deviceLineExpired, deviceLineManual} {
		switch got := strings.Contains(page, line); {
		case line == want && !got:
			t.Fatalf("status line %q is missing: %.800s", line, page)
		case line != want && got:
			t.Fatalf("status line %q renders where it should not: %.800s", line, page)
		}
	}
}

// TestDeviceConfirmStatusLineMatchesState: UserCode used to mean two things at
// once — "prefill this value" and "this code is live" — so a rejected
// submission rendered the inline error AND the compare-your-terminal line,
// telling the user to
// verify a code the server had just said it does not have, while suppressing
// the line that says what to do next. Known splits the two meanings; this pins
// each state to the one true line, or to the inline error alone where that
// error is the whole answer.
func TestDeviceConfirmStatusLineMatchesState(t *testing.T) {
	// A well-formed code the store does not have: the expiry case.
	const gone = "K7M2X9PQ"

	t.Run("live code awaiting approval", func(t *testing.T) {
		app, deps := newTestApp(t)
		iss, err := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{NameHint: "laptop"})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		req := httptest.NewRequest("GET", "/auth/device?user_code="+iss.UserCode, nil)
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		assertOnlyStatusLine(t, string(raw), deviceLineCompare)
	})

	t.Run("rejected submission", func(t *testing.T) {
		app, deps := newTestApp(t)
		_, token := renderDevicePage(t, app)
		status, body := approveForm(t, app, deps.Cfg.Server.BaseURL, token, gone, nil)
		if status != 400 {
			t.Fatalf("status = %d, want 400", status)
		}
		page := string(body)
		// The inline error is the whole answer here: it names the cause and the
		// next step, so no status line may repeat it back.
		if !strings.Contains(page, `class="err"`) {
			t.Fatalf("no inline error on a rejected submission: %.800s", page)
		}
		assertOnlyStatusLine(t, page, "")
		if !strings.Contains(page, `value="`+gone+`"`) {
			t.Fatalf("rejected code not echoed back: %.800s", page)
		}
	})

	// Reachable without any submission: the CLI's link is opened after the 180s
	// budget ran out. There is a prefilled value but nothing behind it, so the
	// page must neither ask for a comparison nor read as an empty form.
	t.Run("expired code on a fresh GET", func(t *testing.T) {
		app, _ := newTestApp(t)
		req := httptest.NewRequest("GET", "/auth/device?user_code="+gone, nil)
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		page := string(raw)
		if strings.Contains(page, `class="err"`) {
			t.Fatalf("a fresh GET is not a rejection and must not show one: %.800s", page)
		}
		assertOnlyStatusLine(t, page, deviceLineExpired)
		if !strings.Contains(page, `value="`+gone+`"`) {
			t.Fatalf("well-formed code should still prefill: %.800s", page)
		}
	})

	// The manual-entry page: an older CLI opening the bare verification_uri.
	t.Run("no code at all", func(t *testing.T) {
		app, _ := newTestApp(t)
		// Not renderDevicePage: that helper drains the body to grab the token.
		resp, err := app.Test(httptest.NewRequest("GET", "/auth/device", nil), -1)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		assertOnlyStatusLine(t, string(raw), deviceLineManual)
	})
}

// TestDeviceCodeCapacityReturns503: the mint endpoint is unauthenticated, so
// per-IP limiting is not enough (shared office NAT forces it to be loose).
// Filling Redis would evict sessions and log the whole site out, so the global
// cap must surface as 503 rather than another key.
func TestDeviceCodeCapacityReturns503(t *testing.T) {
	app, deps := newTestApp(t)
	for i := 0; i < devicecode.MaxOutstanding; i++ {
		if _, err := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{}); err != nil {
			t.Fatalf("seed #%d: %v", i, err)
		}
	}
	status, body := doJSON(t, app, "POST", "/auth/device/code", `{}`)
	if status != 503 {
		t.Fatalf("status = %d body = %s, want 503", status, body)
	}
}

func TestSanitizeHostname(t *testing.T) {
	tests := []struct{ in, want string }{
		{"MacBook-Pro", "MacBook-Pro"},
		{"host.example_1", "host.example_1"},
		{"我的电脑", "unknown"},
		{"<script>alert(1)</script>", "scriptalert1script"},
		{"", "unknown"},
		{strings.Repeat("a", 100), strings.Repeat("a", 64)},
	}
	for _, tt := range tests {
		if got := sanitizeHostname(tt.in); got != tt.want {
			t.Fatalf("sanitizeHostname(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDeviceTokenName(t *testing.T) {
	if got := deviceTokenName("张三的电脑"); got != "CLI on unknown" {
		t.Fatalf("deviceTokenName = %q, want %q", got, "CLI on unknown")
	}
}

// approveForm posts the confirmation form the way a real browser does:
// same-origin, with Origin and Sec-Fetch-Site, and WITHOUT X-Requested-With
// (a script-src 'none' page cannot set custom headers).
//
// The Origin below is SIMULATED — httptest never derives headers the way a
// browser does, so this helper cannot prove a browser actually sends one. What
// guarantees that is the page's own Referrer-Policy, asserted separately in
// TestDevicePageSecurityHeaders.
// It delegates to postApproveForm; mutate runs after the body is encoded, which
// is why a form FIELD cannot be added through it (see approveFormTTL).
func approveForm(t *testing.T, app *fiber.App, origin, csrfToken, userCode string,
	mutate func(*http.Request)) (int, []byte) {
	t.Helper()
	form := url.Values{}
	form.Set("csrf_token", csrfToken)
	form.Set("user_code", userCode)
	return postApproveForm(t, app, origin, form, mutate)
}

// approveFormTTL is approveForm plus an explicit token_ttl field.
//
// A separate entry point rather than an extra approveForm parameter so every
// existing call site keeps posting NO token_ttl at all — that is the
// backward-compatible shape (an older cached page, or a client that never
// learned the field) and it has to stay exercised.
func approveFormTTL(t *testing.T, app *fiber.App, origin, csrfToken, userCode, ttl string) (int, []byte) {
	t.Helper()
	form := url.Values{}
	form.Set("csrf_token", csrfToken)
	form.Set("user_code", userCode)
	form.Set("token_ttl", ttl)
	return postApproveForm(t, app, origin, form, nil)
}

// postApproveForm is the single place the browser-shaped POST is built, so what
// counts as "what a browser sends" cannot drift between the two entry points.
func postApproveForm(t *testing.T, app *fiber.App, origin string, form url.Values,
	mutate func(*http.Request)) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest("POST", "/auth/device/approve", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if mutate != nil {
		mutate(req)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// csrfTokenRe pulls the synchronizer token out of the rendered page.
var csrfTokenRe = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// TestDevicePageOffersTTLChoices: the lifetime control is the only place the
// approver can see what they are granting, so every allowlist entry must reach
// the page and exactly one must be preselected. A control that renders with
// nothing checked posts an empty token_ttl, which silently takes the default —
// i.e. the user's apparent choice would not be the one applied.
func TestDevicePageOffersTTLChoices(t *testing.T) {
	app, _ := newTestApp(t)
	// Not renderDevicePage: that helper drains the body to grab the CSRF token.
	resp, err := app.Test(httptest.NewRequest("GET", "/auth/device", nil), -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	page := string(raw)

	for _, opt := range deviceTokenTTLOptions {
		if !strings.Contains(page, `value="`+opt.Value+`"`) {
			t.Fatalf("TTL option %q missing from the page: %.900s", opt.Value, page)
		}
	}
	checked := regexp.MustCompile(`value="([^"]+)" checked`).FindAllStringSubmatch(page, -1)
	if len(checked) != 1 {
		t.Fatalf("want exactly one checked TTL radio, got %d: %.900s", len(checked), page)
	}
	if checked[0][1] != deviceTokenTTLDefaultValue {
		t.Fatalf("preselected TTL = %q, want %q", checked[0][1], deviceTokenTTLDefaultValue)
	}
	// The prose must not name a fixed lifetime any more: the radios decide it,
	// so the default's label may appear exactly once — inside its own radio.
	defaultLabel := deviceTokenTTLOptions[len(deviceTokenTTLOptions)-1].label(deviceTextByLang[i18n.EN])
	if n := strings.Count(page, defaultLabel); n != 1 {
		t.Fatalf("lifetime %q appears %d times, want 1 (the radio): %.900s", defaultLabel, n, page)
	}
}

// TestApproveHonoursTheChosenTTL walks the whole path the choice travels:
// form → Redis record → minted PAT. Asserting only the record would miss the
// redemption endpoint ignoring it, which is where the lifetime actually applies.
func TestApproveHonoursTheChosenTTL(t *testing.T) {
	app, deps := newTestApp(t)
	iss, err := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{NameHint: "laptop"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, token := renderDevicePage(t, app)

	status, body := approveFormTTL(t, app, deps.Cfg.Server.BaseURL, token, iss.UserCode, "7d")
	if status != 200 {
		t.Fatalf("status = %d body = %s, want 200", status, body)
	}
	// The receipt has to name the granted lifetime, not a generic success line.
	if !strings.Contains(string(body), "7 days") {
		t.Fatalf("approved page does not echo the granted lifetime: %.600s", body)
	}

	_, body = postDeviceToken(t, app, iss.DeviceCode)
	var out struct {
		Status    string     `json:"status"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	_ = json.Unmarshal(body, &out)
	if out.Status != "approved" {
		t.Fatalf("redemption = %s", body)
	}
	if out.ExpiresAt == nil {
		t.Fatal("expires_at is null; the device PAT must not be permanent")
	}
	if d := time.Until(*out.ExpiresAt); d > 8*24*time.Hour || d < 6*24*time.Hour {
		t.Fatalf("expires_at is %v away, want ~7d (the chosen lifetime)", d)
	}
}

// TestApproveRejectsTTLOutsideTheAllowlist: the allowlist is the entire policy
// for a credential minted through an UNAUTHENTICATED endpoint. A submitted
// value can only be off-list by bypassing the rendered form, so it must be
// refused outright — clamping it to the default would grant a lifetime nobody
// chose, and honouring it would make the ceiling decorative.
func TestApproveRejectsTTLOutsideTheAllowlist(t *testing.T) {
	for _, ttl := range []string{"3650d", "365d", "never", "permanent", "0d", "-30d", "180"} {
		t.Run(ttl, func(t *testing.T) {
			app, deps := newTestApp(t)
			iss, err := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{})
			if err != nil {
				t.Fatalf("seed: %v", err)
			}
			_, token := renderDevicePage(t, app)

			status, body := approveFormTTL(t, app, deps.Cfg.Server.BaseURL, token, iss.UserCode, ttl)
			if status != 400 {
				t.Fatalf("status = %d body = %.400s, want 400", status, body)
			}
			// Refused means NOT approved: the flow must still be redeemable only
			// after a legitimate approval.
			if _, err := deps.DeviceCodes.Consume(t.Context(), iss.DeviceCode); err == nil {
				t.Fatal("record was approved despite an off-list lifetime")
			}
		})
	}
}

// TestApproveWithoutTTLFieldKeepsTheOldLifetime: a page cached before this
// control shipped posts no token_ttl at all. That must keep working and grant
// exactly what the flow granted when the lifetime was hard-coded — a rejection
// here would break logins during a rollout.
func TestApproveWithoutTTLFieldKeepsTheOldLifetime(t *testing.T) {
	app, deps := newTestApp(t)
	iss, err := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, token := renderDevicePage(t, app)

	status, body := approveForm(t, app, deps.Cfg.Server.BaseURL, token, iss.UserCode, nil)
	if status != 200 {
		t.Fatalf("status = %d body = %.400s, want 200", status, body)
	}
	rec, err := deps.DeviceCodes.Consume(t.Context(), iss.DeviceCode)
	if err != nil {
		t.Fatalf("Consume after approve: %v", err)
	}
	if rec.TokenTTL != deviceTokenTTLDefaultValue {
		t.Fatalf("TokenTTL = %q, want the %q default", rec.TokenTTL, deviceTokenTTLDefaultValue)
	}
}

// TestApproveRejectionKeepsTheChosenTTL: the 180s budget does not allow redoing
// the whole form, so a rejection for an unrelated reason (a dead code here) must
// bring the picked lifetime back with it.
//
// The subtests cover both re-render paths. The un-normalized case is the one
// that bites: parseDeviceTokenTTL trims and lower-cases, so " 30D " is accepted
// and the flow proceeds — but the template marks the checked radio by string
// equality, so echoing the RAW value would match no option and render the
// control with NOTHING checked. A control in that state posts no token_ttl at
// all, silently granting the default instead of what the user picked.
func TestApproveRejectionKeepsTheChosenTTL(t *testing.T) {
	for _, tc := range []struct {
		name, submitted, wantChecked string
	}{
		{"exact value", "30d", "30d"},
		{"needs normalizing", " 30D ", "30d"},
		// Off-list is refused before the code lookup; the re-render must still
		// leave exactly one radio checked, so the retry cannot post an empty field.
		{"off-list falls back to the default", "3650d", deviceTokenTTLDefaultValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, deps := newTestApp(t)
			_, token := renderDevicePage(t, app)

			status, body := approveFormTTL(t, app, deps.Cfg.Server.BaseURL, token, "K7M2X9PQ", tc.submitted)
			if status != 400 {
				t.Fatalf("status = %d, want 400", status)
			}
			page := string(body)
			if !strings.Contains(page, `value="`+tc.wantChecked+`" checked`) {
				t.Fatalf("submitted %q: want %q checked on the re-render: %.900s",
					tc.submitted, tc.wantChecked, page)
			}
			// Exactly one, or the retry posts an empty token_ttl.
			if n := strings.Count(page, " checked"); n != 1 {
				t.Fatalf("submitted %q: %d radios checked, want exactly 1: %.900s",
					tc.submitted, n, page)
			}
		})
	}
}

// TestDeviceTokenLifetimeResolvesOnlyPublishedKeys pins the redemption-side
// judgement. An empty key is a record written mid-rollout by a pod predating
// Record.TokenTTL; the rest cannot be produced by DeviceApprove at all, which is
// exactly why they must not be honoured — this endpoint consumes no cookie, so
// it can only grant a lifetime traceable to the allowlist.
//
// Storing the KEY is what makes most of these unrepresentable rather than merely
// rejected: there is no way to write "45 days" into the record at all.
func TestDeviceTokenLifetimeResolvesOnlyPublishedKeys(t *testing.T) {
	def, _ := parseDeviceTokenTTL("")
	cases := []struct {
		name   string
		stored string
		want   time.Duration
	}{
		{"no choice on record", "", def.TTL},
		{"off-list key", "45d", def.TTL},
		{"longer than any option", "365d", def.TTL},
		{"not a duration at all", "garbage", def.TTL},
		{"a raw duration string, not a key", "604800000000000", def.TTL},
		{"an allowlist key", "7d", 7 * 24 * time.Hour},
		{"the default's own key", deviceTokenTTLDefaultValue, def.TTL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deviceTokenLifetime(tc.stored); got != tc.want {
				t.Fatalf("deviceTokenLifetime(%q) = %v, want %v", tc.stored, got, tc.want)
			}
		})
	}
}

// TestDeviceTokenTTLOptionsAreSelfConsistent: the allowlist is hand-edited, and
// its Value and TTL encode the same fact twice. A row like {Value: "1d", TTL:
// 90d} would show "1 day" on the page and mint 90 days, and nothing else would
// notice — the page renders Value, the PAT uses TTL. Pinning agreement here
// fails at the edit rather than in production.
func TestDeviceTokenTTLOptionsAreSelfConsistent(t *testing.T) {
	for _, opt := range deviceTokenTTLOptions {
		d, err := parseDuration(opt.Value)
		if err != nil {
			t.Fatalf("option %q is not a parseable duration: %v", opt.Value, err)
		}
		if d != opt.TTL {
			t.Fatalf("option %q says %v but carries TTL %v", opt.Value, d, opt.TTL)
		}
		for _, text := range deviceTextByLang {
			if label := opt.label(text); !strings.HasPrefix(label, strings.TrimSuffix(opt.Value, "d")) {
				t.Fatalf("option %q renders as %q, which does not name the same number",
					opt.Value, label)
			}
		}
	}
	// The preselected radio must resolve, or the page renders nothing checked.
	if _, ok := parseDeviceTokenTTL(deviceTokenTTLDefaultValue); !ok {
		t.Fatalf("default %q is not in the allowlist", deviceTokenTTLDefaultValue)
	}
}

func renderDevicePage(t *testing.T, app *fiber.App) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/auth/device", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	m := csrfTokenRe.FindSubmatch(raw)
	if len(m) < 2 {
		t.Fatalf("no csrf_token in the rendered page: %.400s", raw)
	}
	return resp, string(m[1])
}

// TestDevicePageSecurityHeaders: this repo has NO global security-header
// middleware (view_shell.go says each handler sets its own), so a missing
// header here means the page is simply naked. frame-ancestors must be 'none',
// not 'self': a clickjacked confirmation page reduces the social-engineering
// cost of the whole flow to zero — no pretext needed at all.
func TestDevicePageSecurityHeaders(t *testing.T) {
	app, _ := newTestApp(t)
	resp, _ := renderDevicePage(t, app)

	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP = %q, want frame-ancestors 'none'", csp)
	}
	if !strings.Contains(csp, "script-src 'none'") {
		t.Fatalf("CSP = %q, want script-src 'none'", csp)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff (setCommonSecurityHeaders not called)")
	}
	// Must be same-origin, not the global no-referrer — see the override in
	// renderDeviceConfirm (device.go). approveForm's Origin header is
	// simulated (see its doc comment), so this is the only test that would
	// catch a regression here.
	if rp := resp.Header.Get("Referrer-Policy"); rp != "same-origin" {
		t.Fatalf("Referrer-Policy = %q, want same-origin", rp)
	}
	if !strings.Contains(resp.Header.Get("Strict-Transport-Security"), "max-age=") {
		t.Fatal("missing HSTS (setCommonSecurityHeaders not called)")
	}
}

// TestApproveSucceedsWithoutXRequestedWith is THE anti-regression test for the
// blocking conflict: the global CSRF middleware hard-requires X-Requested-With,
// which a plain <form> cannot send. If approve is ever put back under the
// middleware, this goes 403 for every real user while every other test stays
// green.
func TestApproveSucceedsWithoutXRequestedWith(t *testing.T) {
	app, deps := newTestApp(t)
	iss, err := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{NameHint: "laptop"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, token := renderDevicePage(t, app)

	status, body := approveForm(t, app, deps.Cfg.Server.BaseURL, token, iss.UserCode, nil)
	if status != 200 {
		t.Fatalf("status = %d body = %s, want 200", status, body)
	}
	rec, err := deps.DeviceCodes.Consume(t.Context(), iss.DeviceCode)
	if err != nil {
		t.Fatalf("Consume after approve: %v", err)
	}
	if rec.AuthzID != testAuthzID {
		t.Fatalf("approved authz_id = %q, want %q", rec.AuthzID, testAuthzID)
	}
	if rec.ApproverIP == "" {
		t.Fatal("approver_ip not recorded — the phishing forensics trail needs it")
	}
}

// TestApproveRejectsCrossSiteSignals: since approve is exempt from the CSRF
// middleware, the cross-site judgement MUST come from Sec-Fetch-Site / Origin
// here. Note what is NOT a rejection criterion: a missing X-Requested-With.
func TestApproveRejectsCrossSiteSignals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"cross-site fetch metadata", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{"missing fetch metadata (fail-closed)", func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") }},
		{"origin not in allowlist", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }},
		// "null" is what a no-referrer page's form POST sends (see device.go);
		// pinned as a rejection so a future 403 here isn't "fixed" by allowlisting it.
		{"origin null is never allowlisted", func(r *http.Request) { r.Header.Set("Origin", "null") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, deps := newTestApp(t)
			iss, _ := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{})
			_, token := renderDevicePage(t, app)
			status, body := approveForm(t, app, deps.Cfg.Server.BaseURL, token, iss.UserCode, tc.mutate)
			if status != 403 {
				t.Fatalf("status = %d body = %s, want 403", status, body)
			}
		})
	}
}

func TestApproveRejectsBadOrReplayedCSRFToken(t *testing.T) {
	app, deps := newTestApp(t)
	iss, _ := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{})
	_, token := renderDevicePage(t, app)

	if status, _ := approveForm(t, app, deps.Cfg.Server.BaseURL, "not-the-token", iss.UserCode, nil); status != 403 {
		t.Fatalf("wrong csrf_token status = %d, want 403", status)
	}
	if status, _ := approveForm(t, app, deps.Cfg.Server.BaseURL, token, iss.UserCode, nil); status != 200 {
		t.Fatalf("valid csrf_token status = %d, want 200", status)
	}
	// One-shot: the very same token must not work twice.
	if status, _ := approveForm(t, app, deps.Cfg.Server.BaseURL, token, iss.UserCode, nil); status != 403 {
		t.Fatalf("replayed csrf_token status = %d, want 403", status)
	}
}

func TestApproveIsOneShotPerDeviceCode(t *testing.T) {
	app, deps := newTestApp(t)
	iss, _ := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{})

	_, tok1 := renderDevicePage(t, app)
	if status, _ := approveForm(t, app, deps.Cfg.Server.BaseURL, tok1, iss.UserCode, nil); status != 200 {
		t.Fatalf("first approve status = %d, want 200", status)
	}
	_, tok2 := renderDevicePage(t, app)
	status, body := approveForm(t, app, deps.Cfg.Server.BaseURL, tok2, iss.UserCode, nil)
	if status != 409 {
		t.Fatalf("second approve status = %d body = %s, want 409", status, body)
	}
}

func TestApproveUnknownUserCodeIs400(t *testing.T) {
	app, deps := newTestApp(t)
	_, token := renderDevicePage(t, app)
	status, _ := approveForm(t, app, deps.Cfg.Server.BaseURL, token, "ZZZZZZZZ", nil)
	if status != 400 {
		t.Fatalf("status = %d, want 400", status)
	}
}

func postDeviceToken(t *testing.T, app *fiber.App, deviceCode string) (int, []byte) {
	t.Helper()
	return doJSON(t, app, "POST", "/auth/device/token", `{"device_code":"`+deviceCode+`"}`)
}

func TestDeviceTokenPendingThenApproved(t *testing.T) {
	// newTestAppRedis already hands back the
	// miniredis handle — device-flow tests need it to advance the clock past
	// the poll spacing and the TTL. No production-side test hook required.
	app, deps, advance := newTestAppClock(t)
	iss, _ := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{NameHint: "laptop"})

	status, body := postDeviceToken(t, app, iss.DeviceCode)
	if status != 200 {
		t.Fatalf("status = %d body = %s", status, body)
	}
	var out struct {
		Status    string     `json:"status"`
		Token     string     `json:"token"`
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	_ = json.Unmarshal(body, &out)
	if out.Status != "pending" || out.Token != "" {
		t.Fatalf("first poll = %+v, want pending with no token", out)
	}

	if err := deps.DeviceCodes.Approve(t.Context(), iss.DeviceCode, devicecode.Approval{
		AuthzID: testAuthzID, ApproverIP: "192.0.2.7", TokenTTL: "30d",
	}); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	// Respect the poll spacing the previous request just armed.
	advance(devicecode.PollInterval + time.Second)

	_, body = postDeviceToken(t, app, iss.DeviceCode)
	_ = json.Unmarshal(body, &out)
	if out.Status != "approved" {
		t.Fatalf("after approve = %s", body)
	}
	if !strings.HasPrefix(out.Token, "pl_") {
		t.Fatalf("token = %q, want a pl_ PAT", out.Token)
	}
	if out.Name != "CLI on laptop" {
		t.Fatalf("name = %q, want %q", out.Name, "CLI on laptop")
	}
	// The lifetime comes from the APPROVER's stored choice (30d above), never
	// from parseTokenExpiry's 365-day cap fallback and never from this request.
	if out.ExpiresAt == nil {
		t.Fatal("expires_at is null; the device PAT must not be permanent")
	}
	if d := time.Until(*out.ExpiresAt); d > 31*24*time.Hour || d < 29*24*time.Hour {
		t.Fatalf("expires_at is %v away, want ~30d (the approved choice)", d)
	}
}

func TestDeviceTokenIsOneShot(t *testing.T) {
	app, deps, advance := newTestAppClock(t)
	iss, _ := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{})
	_ = deps.DeviceCodes.Approve(t.Context(), iss.DeviceCode, devicecode.Approval{
		AuthzID: testAuthzID, ApproverIP: "192.0.2.7", TokenTTL: "180d",
	})

	_, body := postDeviceToken(t, app, iss.DeviceCode)
	if !strings.Contains(string(body), `"approved"`) {
		t.Fatalf("first redemption = %s", body)
	}
	advance(devicecode.PollInterval + time.Second)
	_, body = postDeviceToken(t, app, iss.DeviceCode)
	if !strings.Contains(string(body), `"expired"`) {
		t.Fatalf("second redemption = %s, want expired (one-shot)", body)
	}
}

func TestDeviceTokenSlowDownOnFastPolling(t *testing.T) {
	app, deps := newTestApp(t)
	iss, _ := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{})

	if _, body := postDeviceToken(t, app, iss.DeviceCode); !strings.Contains(string(body), `"pending"`) {
		t.Fatalf("first poll = %s", body)
	}
	_, body := postDeviceToken(t, app, iss.DeviceCode)
	if !strings.Contains(string(body), `"slow_down"`) {
		t.Fatalf("immediate second poll = %s, want slow_down", body)
	}
}

func TestDeviceTokenExpiredAfterTTL(t *testing.T) {
	app, deps, advance := newTestAppClock(t)
	iss, _ := deps.DeviceCodes.Create(t.Context(), devicecode.NewFlow{})
	advance(devicecode.TTL + time.Second)

	status, body := postDeviceToken(t, app, iss.DeviceCode)
	if status != 200 || !strings.Contains(string(body), `"expired"`) {
		t.Fatalf("status = %d body = %s, want 200 + expired", status, body)
	}
}

// TestDeviceTokenUnknownCodeIsExpired: an unknown device_code must be
// indistinguishable from an expired one — a distinct "no such code" answer
// would turn this endpoint into an oracle for guessing.
func TestDeviceTokenUnknownCodeIsExpired(t *testing.T) {
	app, _ := newTestApp(t)
	_, body := postDeviceToken(t, app, "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ")
	if !strings.Contains(string(body), `"expired"`) {
		t.Fatalf("unknown device_code = %s, want expired", body)
	}
}
