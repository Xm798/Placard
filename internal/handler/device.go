package handler

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/devicecode"
	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/idgen"
	"github.com/Xm798/placard/internal/middleware"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/userctx"
)

// DeviceCodeIPRateLimit is the per-IP per-minute cap main.go wires onto
// POST /auth/device/code (keyClass "devicecode"). A constant rather than a
// config key, and deliberately loose: a shared NAT puts many legitimate users
// behind one egress IP (the same reason the auth middleware gave up on
// limiting session misses). The real backstop is the store's global
// outstanding cap.
const DeviceCodeIPRateLimit = 60

// maxHostnameLen bounds the CLI-supplied hostname baked into a token name.
const maxHostnameLen = 64

// deviceTokenTTLOption is one selectable PAT lifetime on the confirmation page.
type deviceTokenTTLOption struct {
	// Value is the form value; it is matched EXACTLY, never parsed as a
	// duration, so no submitted string can express a TTL outside this list.
	Value string
	TTL   time.Duration
}

// label names the lifetime in the reader's language. Derived from Value rather
// than stored next to it, so the number the page shows and the number the token
// gets cannot drift apart.
func (o deviceTokenTTLOption) label(text deviceText) string {
	days := strings.TrimSuffix(o.Value, "d")
	if days == "1" {
		return fmt.Sprintf(text.DayLabelOne, days)
	}
	return fmt.Sprintf(text.DayLabel, days)
}

// deviceTokenTTLOptions is the allowlist of PAT lifetimes the approver may pick.
//
// A closed list, not a duration parser: this credential is redeemed through an
// UNAUTHENTICATED endpoint, so "how long does it live" must be answerable only
// from values the server itself published. It is the single resolution point for
// both directions: Value validates what the approver submits, and the same Value
// stored on the record is what deviceTokenLifetime resolves back at redemption.
//
// It deliberately does NOT consult Token.MaxTTLDays (365 days by default); the
// ceiling here is the stricter of the two, and a config-driven one would let an
// operator widen an unauthenticated endpoint's credential without touching this
// file.
var deviceTokenTTLOptions = []deviceTokenTTLOption{
	{Value: "1d", TTL: 24 * time.Hour},
	{Value: "7d", TTL: 7 * 24 * time.Hour},
	{Value: "30d", TTL: 30 * 24 * time.Hour},
	{Value: "90d", TTL: 90 * 24 * time.Hour},
	{Value: "180d", TTL: 180 * 24 * time.Hour},
}

// deviceTokenTTLDefaultValue is preselected on the page and is what an approval
// carrying no choice at all resolves to. 180d keeps the lifetime this flow
// granted before it became selectable, so an approver who ignores the control
// gets exactly the old behaviour.
const deviceTokenTTLDefaultValue = "180d"

// parseDeviceTokenTTL resolves a submitted form value against the allowlist.
//
// An empty value takes the default (a client that never sends the field keeps
// working). Anything else non-empty must match an option exactly — an
// unrecognized value is REJECTED rather than clamped to the default, because
// the only way to produce one is to bypass the rendered form, and silently
// granting a lifetime the requester did not see is worse than saying no.
func parseDeviceTokenTTL(raw string) (deviceTokenTTLOption, bool) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		raw = deviceTokenTTLDefaultValue
	}
	for _, opt := range deviceTokenTTLOptions {
		if opt.Value == raw {
			return opt, true
		}
	}
	return deviceTokenTTLOption{}, false
}

// DeviceCode handles POST /auth/device/code — unauthenticated (builtinSkips)
// and CSRF-exempt (csrfExactExempt), because at login time the CLI has no
// credential of any kind and consumes no cookie.
func (h *Handlers) DeviceCode(c *fiber.Ctx) error {
	if h.deps.DeviceCodes == nil {
		return apperr.Unavailable()
	}
	// Parse errors are ignored on purpose: the body is optional. No body, an
	// empty body, or a body without "hostname" all leave Hostname == "", which
	// sanitizeHostname maps to "unknown" — so a client predating the hostname
	// field keeps working.
	var req dto.DeviceCodeRequest
	_ = c.BodyParser(&req)

	iss, err := h.deps.DeviceCodes.Create(c.UserContext(), devicecode.NewFlow{
		NameHint:  sanitizeHostname(req.Hostname),
		CreatedIP: clientIPOf(c),
		CreatedUA: truncateUA(c.Get(fiber.HeaderUserAgent)),
	})
	if errors.Is(err, devicecode.ErrCapacity) {
		// Shedding load here protects the store the flows share with sessions:
		// an unbounded backlog there costs everyone their login.
		return apperr.Unavailable("device authorization is busy, retry shortly")
	}
	if err != nil {
		return apperr.Unavailable()
	}

	verificationURI := strings.TrimRight(h.deps.Cfg.Server.BaseURL, "/") + "/auth/device"
	return c.JSON(dto.DeviceCodeResponse{
		DeviceCode: iss.DeviceCode,
		UserCode:   iss.UserCode,
		// Bare form first (readable over the phone), then the complete form the
		// CLI opens. user_code is a 180s single-use code, not a secret worth
		// keeping out of a URL — see dto.DeviceCodeResponse's doc for the
		// trade this makes and what the confirmation page owes in return.
		VerificationURI:         verificationURI,
		VerificationURIComplete: verificationURI + "?user_code=" + url.QueryEscape(iss.UserCode),
		ExpiresIn:               devicecode.ExpiresIn,
		Interval:                devicecode.IntervalSecs,
	})
}

// sanitizeHostname strips a CLI-supplied hostname down to [A-Za-z0-9._-],
// caps it at maxHostnameLen and falls back to "unknown".
//
// Done SERVER-SIDE on purpose: client-side sanitization does not count. An
// unsanitized value lets an attacker name their stolen token
// "MacBook-Pro-<the victim's own machine name>" so the victim scrolling
// /settings reads it as their own,
// or stuff a huge string that wrecks the token list / gets silently truncated
// by the DB.
func sanitizeHostname(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
		if b.Len() >= maxHostnameLen {
			break
		}
	}
	out := b.String()
	if len(out) > maxHostnameLen {
		out = out[:maxHostnameLen]
	}
	if out == "" {
		return "unknown"
	}
	return out
}

// deviceTokenName is the PAT name minted by the device flow. The server builds
// it; the CLI never supplies a name.
func deviceTokenName(hostnameHint string) string {
	return "CLI on " + sanitizeHostname(hostnameHint)
}

// clientIPOf is middleware.ClientIP with fiber's own remote-addr fallback, so
// an audit row always carries something even without a trusted proxy hop.
func clientIPOf(c *fiber.Ctx) string {
	if ip := middleware.ClientIP(c); ip != "" {
		return ip
	}
	return c.IP()
}

// deviceConfirmCSP is the confirmation page's Content-Security-Policy.
//
// frame-ancestors is 'none', NOT the 'self' used by the viewer shell: this
// page has no legitimate reason to be framed by anything, and a clickjacked
// confirmation button removes every bit of social-engineering cost from the
// device flow — the victim never even sees a pretext.
//
// script-src 'none' keeps it a pure form page, which is also why its CSRF
// defence is a hidden synchronizer field rather than a custom header.
const deviceConfirmCSP = "default-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"script-src 'none'; frame-ancestors 'none'; base-uri 'none'"

// deviceConfirmView is the confirmation page's template data.
type deviceConfirmView struct {
	CSRFToken string
	// UserCode is the value echoed into the input — prefilled from the URL on a
	// fresh GET, or carried back from a rejected submission so it stays fixable
	// in place. It asserts only "well-formed enough to echo"; whether the server
	// actually has this code is Known. Keeping the two apart is what lets the
	// page show the code while still admitting it leads nowhere.
	UserCode string
	// Known reports that the store holds a live, still-unapproved flow for
	// UserCode — i.e. that confirming could actually succeed. It gates the
	// "compare this against your terminal" wording, which is worth showing only
	// when there is something on the other side to compare with.
	Known bool
	// Requester is filled by the same lookup that sets Known, from whichever
	// source supplied the code (see renderDeviceConfirm: ?user_code= on a GET,
	// the resubmitted form field on a re-render).
	// It is CORROBORATION FOR AN ALREADY-SUSPICIOUS USER, never a trust
	// signal: hostname, IP and User-Agent are all attacker-controlled.
	Requester *deviceRequesterView
	// Error, when set, is an inline message from a rejected submission.
	Error string
	// TTLOptions is the allowlist with each label already resolved for T's
	// language; the template marks SelectedTTL.
	TTLOptions []deviceTTLChoice
	// T is the page copy in the reader's language.
	T deviceText
	// SelectedTTL is the option Value to render checked. It is always a real
	// allowlist Value, never the raw submitted string — see selectedTTL.
	SelectedTTL string
}

// selectedTTL resolves a submitted token_ttl to the option Value to check.
//
// It returns the RESOLVED option's Value rather than echoing raw, because
// parseDeviceTokenTTL normalizes (trim + lower) and the template compares by
// equality: echoing raw would leave "30D" matching no option, rendering the
// control with nothing checked — and a control with no checked radio posts no
// token_ttl at all, silently taking the default the user did not pick.
func selectedTTL(raw string) string {
	opt, ok := parseDeviceTokenTTL(raw)
	if !ok {
		// Off-list: fall back to the default rather than leaving nothing checked.
		opt, _ = parseDeviceTokenTTL("")
	}
	return opt.Value
}

type deviceRequesterView struct {
	Hostname  string
	IP        string
	UserAgent string
}

// deviceTTLChoice is one lifetime radio, its label already in the reader's
// language.
type deviceTTLChoice struct {
	Value string
	Label string
}

func deviceTTLChoices(text deviceText) []deviceTTLChoice {
	choices := make([]deviceTTLChoice, 0, len(deviceTokenTTLOptions))
	for _, opt := range deviceTokenTTLOptions {
		choices = append(choices, deviceTTLChoice{Value: opt.Value, Label: opt.label(text)})
	}
	return choices
}

// deviceConfirmTmpl prefills the user_code input from the URL so the user
// confirms rather than transcribes. The visible value is what a suspicious user
// compares against their own terminal, and the wording carries the load the
// retyping step used to — it names the consequence and says when to close.
//
// The status wording branches on Known, NOT on UserCode: a well-formed code the
// store does not have still prefills, so keying the "compare against your
// terminal" line off the prefill alone would tell a user holding a dead code to
// verify a flow that no longer exists — while hiding the line that says what to
// do next.
//
// A rejected submission renders the inline error INSTEAD of a status line: that
// error already names both the cause and the next step, so a status line under
// it only makes the user read the same sentence twice. Known and Error cannot
// co-occur — both re-render paths are reached solely via ErrNotFound — so the
// Known branch needs no guard against Error beyond ordering.
//
// The last branch is the manual-entry page and is NOT dead code; the two cases
// that reach it are its removal criteria: an already-deployed older CLI that
// opens only the bare verification_uri (this ships as a standalone binary with
// no forced upgrade), and a malformed ?user_code= that IsUserCode rejects. Do
// not drop it just because the server now always emits
// verification_uri_complete.
//
// The lifetime control is a plain radiogroup with a preselected default, so the
// prose above it can no longer name one fixed number of days. Keep it that way:
// stating a lifetime the radios can contradict is how a security page starts
// lying about what the button grants.
var deviceConfirmTmpl = template.Must(template.New("device").Parse(`<!DOCTYPE html>
<html lang="{{.T.Lang}}">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{.T.Title}}</title>
<style>
:root{--bg:#fafaf8;--surface:#fff;--fg:#16150f;--fg-soft:#6b6960;--line:#e7e5dd;--ink:#16150f;--ink-fg:#fafaf8;--warn:#8a5a00;}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:var(--bg);color:var(--fg);font-family:system-ui,-apple-system,"Segoe UI","Noto Sans SC",sans-serif;}
.card{background:var(--surface);border:1px solid var(--line);border-radius:14px;padding:32px;width:min(480px,92vw);}
h1{font-size:20px;margin:0 0 8px;letter-spacing:-.02em;}
p{margin:0 0 16px;color:var(--fg-soft);font-size:14px;line-height:1.6;}
.warn{color:var(--warn);font-weight:600;}
label,fieldset.ttl legend{display:block;padding:0;font-size:13px;font-weight:600;margin-bottom:6px;}
input[type=text]{width:100%;padding:12px 14px;font-family:ui-monospace,Menlo,monospace;font-size:20px;letter-spacing:.28em;text-transform:uppercase;border:1px solid var(--line);border-radius:10px;background:var(--bg);color:var(--fg);}
button{margin-top:18px;width:100%;padding:12px;border:0;border-radius:10px;background:var(--ink);color:var(--ink-fg);font-size:15px;font-weight:600;cursor:pointer;}
.meta{margin-top:18px;padding:12px 14px;border:1px solid var(--line);border-radius:10px;font-size:12px;color:var(--fg-soft);}
.meta dt{font-weight:600;display:inline;}
.meta dd{display:inline;margin:0 0 0 4px;}
.meta div{margin-bottom:4px;word-break:break-all;}
.err{margin:0 0 16px;padding:10px 12px;border-radius:8px;background:#fdecea;color:#8a1c12;font-size:13px;}
code{font-family:ui-monospace,Menlo,monospace;font-size:.92em;}
/* Segmented radiogroup, CSS-only: this page is script-src 'none', so the
   checked state is painted with :checked + label and nothing else. */
fieldset.ttl{margin:18px 0 0;padding:0;border:0;}
.seg{display:flex;border:1px solid var(--line);border-radius:10px;overflow:hidden;}
.seg input{position:absolute;opacity:0;width:0;height:0;}
/* margin-bottom:0 resets the shared label rule above: under align-items:stretch
   an inherited 6px would shrink each segment and leave a strip of card white
   inside the rounded border, visible against the checked segment's dark fill. */
.seg label{flex:1;margin-bottom:0;padding:9px 4px;text-align:center;color:var(--fg-soft);background:var(--bg);cursor:pointer;border-left:1px solid var(--line);}
.seg label:first-of-type{border-left:0;}
.seg input:checked+label{background:var(--ink);color:var(--ink-fg);}
/* Keyboard users must still see focus: the input itself is visually hidden. */
.seg input:focus-visible+label{outline:2px solid var(--ink);outline-offset:-2px;}
</style>
</head>
<body>
<main class="card">
<h1>{{.T.Heading}}</h1>
{{if .Error}}<p class="err">{{.Error}}</p>
{{else if .Known}}<p class="warn">{{.T.CompareWarning}}</p>
{{else if .UserCode}}<p class="warn">{{.T.ExpiredWarning}}</p>
{{else}}<p class="warn">{{.T.ManualWarning}}</p>
{{end}}
<p>{{.T.Consequence}}</p>
<form method="POST" action="/auth/device/approve">
<input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
<label for="user_code">{{.T.CodeLabel}}</label>
<input id="user_code" name="user_code" type="text" autocomplete="off" spellcheck="false" maxlength="8" required value="{{.UserCode}}">
<fieldset class="ttl">
<legend>{{.T.TTLLegend}}</legend>
<div class="seg">
{{range .TTLOptions}}<input type="radio" id="ttl-{{.Value}}" name="token_ttl" value="{{.Value}}"{{if eq .Value $.SelectedTTL}} checked{{end}}><label for="ttl-{{.Value}}">{{.Label}}</label>
{{end}}</div>
</fieldset>
<button type="submit">{{.T.SubmitButton}}</button>
</form>
{{with .Requester}}
<div class="meta">
<div><dt>{{$.T.RequesterHost}}</dt><dd>{{.Hostname}}</dd></div>
<div><dt>{{$.T.RequesterIP}}</dt><dd>{{.IP}}</dd></div>
<div><dt>User-Agent</dt><dd>{{.UserAgent}}</dd></div>
</div>
{{end}}
</main>
</body>
</html>
`))

// deviceApprovedTmpl echoes the granted lifetime back. The approver picked it a
// click ago, so this is a receipt, not a reminder: it is the only place the
// decision is confirmed as recorded, and a mismatch here is the signal that the
// approval did not do what the page said it would.
var deviceApprovedTmpl = template.Must(template.New("approved").Parse(`<!DOCTYPE html>
<html lang="{{.Lang}}">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{.Title}}</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#fafaf8;color:#16150f;font-family:system-ui,-apple-system,"Segoe UI","Noto Sans SC",sans-serif;}
.card{background:#fff;border:1px solid #e7e5dd;border-radius:14px;padding:32px;width:min(420px,92vw);text-align:center;}
h1{font-size:20px;margin:0 0 8px;}
p{margin:0;color:#6b6960;font-size:14px;line-height:1.6;}
</style>
</head>
<body>
<main class="card">
<h1>{{.Heading}}</h1>
<p>{{.Body}}</p>
</main>
</body>
</html>
`))

// challengeKey identifies whose synchronizer token this is. The session id is
// the right key for a cookie-authenticated caller; dev_mock has no session
// cookie, so fall back to the authz id to keep local development usable.
func (h *Handlers) challengeKey(c *fiber.Ctx) string {
	if h.deps.SessionCookie != "" {
		if sid := c.Cookies(h.deps.SessionCookie); sid != "" {
			return sid
		}
	}
	return userctx.AuthzID(c)
}

// renderDeviceConfirm mints a fresh synchronizer token, stores it against the
// caller's session, and renders the confirmation page with the given status.
// Every render mints a NEW token because the previous one is single-use.
func (h *Handlers) renderDeviceConfirm(c *fiber.Ctx, status int, inlineErr string) error {
	key := h.challengeKey(c)
	if key == "" {
		return apperr.Unauthorized()
	}
	token := idgen.Generate(32)
	if err := h.deps.DeviceCodes.PutChallenge(c.UserContext(), key, token); err != nil {
		return apperr.Unavailable()
	}

	// FormValue for token_ttl too, so a rejected submission re-renders with the
	// lifetime the user had picked rather than snapping back to the default.
	text := deviceTextByLang[pageLang(c)]
	view := deviceConfirmView{
		CSRFToken:   token,
		Error:       inlineErr,
		TTLOptions:  deviceTTLChoices(text),
		SelectedTTL: selectedTTL(c.FormValue("token_ttl")),
		T:           text,
	}
	// FormValue covers both sources in one call: fasthttp's default resolver
	// peeks the query string first, then the POST body. So this reads
	// ?user_code= on a fresh GET and the rejected form field on a re-render —
	// a typo or an expired code lands back in the input instead of vanishing.
	//
	// Only well-formed codes are echoed: the value is attacker-controlled, and
	// while html/template escapes the attribute, reflecting arbitrary junk back
	// into the field just invites confusion about what is being approved.
	uc := normalizeUserCode(c.FormValue("user_code"))
	if devicecode.IsUserCode(uc) {
		view.UserCode = uc
		// The record lookup is separate from the echo: a well-formed but
		// unknown/expired code still prefills, it just is not Known — so the page
		// keeps the value visible without claiming there is a flow to compare
		// against. Setting Known off this lookup discloses nothing new; the
		// Requester block below already reveals the same existence bit.
		if _, rec, err := h.deps.DeviceCodes.ByUserCode(c.UserContext(), uc); err == nil {
			view.Known = true
			view.Requester = &deviceRequesterView{
				Hostname:  rec.NameHint,
				IP:        rec.CreatedIP,
				UserAgent: truncateString(rec.CreatedUA, 120),
			}
		}
	}

	var buf bytes.Buffer
	if err := deviceConfirmTmpl.Execute(&buf, view); err != nil {
		return apperr.Internal("could not render the device confirmation page")
	}
	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	setCommonSecurityHeaders(c)
	varyByLanguage(c)
	// Load-bearing: this page's CSP is script-src 'none', so it submits a plain
	// <form method="POST"> and the default no-referrer would make
	// DeviceApprove's Origin check unsatisfiable. See setFormPostReferrerPolicy.
	setFormPostReferrerPolicy(c)
	c.Set("Content-Security-Policy", deviceConfirmCSP)
	return c.Status(status).Send(buf.Bytes())
}

// DevicePage handles GET /auth/device — the confirmation page. It is behind
// the login gate purely by being absent from middleware.builtinSkips.
func (h *Handlers) DevicePage(c *fiber.Ctx) error {
	if h.deps.DeviceCodes == nil {
		return apperr.Unavailable()
	}
	return h.renderDeviceConfirm(c, fiber.StatusOK, "")
}

// DeviceApprove handles POST /auth/device/approve.
//
// This endpoint is in csrfExactExempt — NOT because it needs no CSRF defence
// (it consumes the session cookie, so it is exactly the kind of write CSRF
// exists for) but because the middleware is unusable for it: the middleware
// hard-requires X-Requested-With and this page is script-src 'none'. The three
// checks below ARE that defence. Deleting them leaves the endpoint naked.
func (h *Handlers) DeviceApprove(c *fiber.Ctx) error {
	if h.deps.DeviceCodes == nil {
		return apperr.Unavailable()
	}
	authzid := userctx.AuthzID(c)
	if authzid == "" {
		return apperr.Unauthorized()
	}

	// (1) Fetch metadata. A plain cross-site form POST is "cross-site"; a
	// missing header (ancient browser) is rejected too — fail-closed.
	if !strings.EqualFold(c.Get("Sec-Fetch-Site"), "same-origin") {
		return apperr.PermissionDenied()
	}
	// (2) Origin allowlist, reusing the CSRF middleware's own normalization.
	if !middleware.OriginAllowed(h.deps.Cfg.CSRF, middleware.RequestOrigin(c)) {
		return apperr.PermissionDenied()
	}
	// (3) Synchronizer token, single-use. Checked BEFORE the user_code lookup
	// so an unauthenticated cross-site probe can never learn whether a code
	// exists. A user typo therefore burns the token — renderDeviceConfirm
	// mints a fresh one on the re-render, so that is invisible to the user.
	key := h.challengeKey(c)
	if key == "" {
		return apperr.Unauthorized()
	}
	ok, err := h.deps.DeviceCodes.ConsumeChallenge(c.UserContext(), key, c.FormValue("csrf_token"))
	if err != nil {
		return apperr.Unavailable()
	}
	if !ok {
		return apperr.PermissionDenied()
	}

	// Resolved BEFORE the user_code lookup so a tampered lifetime is answered
	// without disclosing whether the code exists.
	ttl, ok := parseDeviceTokenTTL(c.FormValue("token_ttl"))
	if !ok {
		return h.renderDeviceConfirm(c, fiber.StatusBadRequest,
			deviceTextByLang[pageLang(c)].BadTTLError)
	}

	userCode := normalizeUserCode(c.FormValue("user_code"))
	deviceCode, _, err := h.deps.DeviceCodes.ByUserCode(c.UserContext(), userCode)
	if errors.Is(err, devicecode.ErrNotFound) {
		return h.renderDeviceConfirm(c, fiber.StatusBadRequest,
			deviceTextByLang[pageLang(c)].BadCodeError)
	}
	if err != nil {
		return apperr.Unavailable()
	}

	err = h.deps.DeviceCodes.Approve(c.UserContext(), deviceCode, devicecode.Approval{
		AuthzID:    authzid,
		ApproverIP: clientIPOf(c),
		// The KEY, not the duration: the record stays resolvable through this
		// same allowlist at redemption (see Record.TokenTTL).
		TokenTTL: ttl.Value,
	})
	if errors.Is(err, devicecode.ErrAlreadyApproved) {
		// One-shot, per the flow's hard constraints.
		return apperr.New("conflict", fiber.StatusConflict, "authorization code already used")
	}
	if errors.Is(err, devicecode.ErrNotFound) {
		return h.renderDeviceConfirm(c, fiber.StatusBadRequest,
			deviceTextByLang[pageLang(c)].BadCodeError)
	}
	if err != nil {
		return apperr.Unavailable()
	}

	// Audit entry #1 of 2. The second (token.create, with both IPs) is written
	// by the redemption endpoint, so "who approved" and "who collected" are
	// both on record and can be compared afterwards.
	//
	// device_code is a bearer credential and MUST NEVER be logged; the
	// short-lived user_code is what identifies the flow in audit.
	//
	// Details carries the chosen lifetime: "who approved" without "for how long"
	// leaves the audit trail unable to answer why a token outlived the incident
	// it was rotated for.
	h.auditBestEffort(c, &model.AuditLog{
		Action:       "token.device_approve",
		Actor:        authzid,
		ActorName:    displayName(c),
		ResourceType: "device",
		ResourceID:   userCode,
		Details:      `{"token_ttl":"` + ttl.Value + `"}`,
	})

	text := deviceTextByLang[pageLang(c)]
	var buf bytes.Buffer
	approved := struct{ Lang, Title, Heading, Body string }{
		Lang:    text.Lang,
		Title:   text.ApprovedTitle,
		Heading: text.ApprovedHeading,
		Body:    fmt.Sprintf(text.ApprovedBody, ttl.label(text)),
	}
	if err := deviceApprovedTmpl.Execute(&buf, approved); err != nil {
		return apperr.Internal("could not render the device approval page")
	}
	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	// No setFormPostReferrerPolicy here, deliberately: this page has no form and
	// no links, so it originates no further request and keeps the stricter default.
	setCommonSecurityHeaders(c)
	varyByLanguage(c)
	c.Set("Content-Security-Policy", deviceConfirmCSP)
	return c.Send(buf.Bytes())
}

// normalizeUserCode upper-cases and trims a human-typed code. Nothing else:
// the alphabet already excludes 0/O/1/I, so there is no ambiguity to repair.
func normalizeUserCode(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

// deviceTokenLifetime resolves a stored allowlist key to the TTL to mint with.
//
// The record carries a key, not a duration, so this is the same resolution the
// confirmation page did — a lifetime the server never published cannot be
// expressed in the record, let alone granted. An empty key (approved by a pod
// predating Record.TokenTTL, mid-rollout) and an unknown one (an allowlist entry
// removed while a flow was in flight) both take the default, which is what
// parseDeviceTokenTTL already does for "".
//
// This endpoint consumes no cookie, so it must not grant a lifetime it cannot
// trace to the allowlist — and the allowlist is compile-time literal, so no path
// here inherits parseTokenExpiry's MaxTTLDays fallback (365 days by default).
func deviceTokenLifetime(storedKey string) time.Duration {
	opt, ok := parseDeviceTokenTTL(storedKey)
	if !ok {
		opt, _ = parseDeviceTokenTTL("")
	}
	return opt.TTL
}

// DeviceToken handles POST /auth/device/token — the CLI's polling endpoint.
// Unauthenticated (builtinSkips) and CSRF-exempt: identity comes entirely from
// the device_code in the body, so no cookie is consumed.
func (h *Handlers) DeviceToken(c *fiber.Ctx) error {
	if h.deps.DeviceCodes == nil {
		return apperr.Unavailable()
	}
	var req dto.DeviceTokenRequest
	_ = c.BodyParser(&req)
	if req.DeviceCode == "" {
		return apperr.Validation("device_code is required")
	}

	ctx := c.UserContext()
	rec, tooSoon, err := h.deps.DeviceCodes.Poll(ctx, req.DeviceCode)
	if errors.Is(err, devicecode.ErrNotFound) {
		return c.JSON(dto.DeviceTokenResponse{Status: "expired"})
	}
	if err != nil {
		return apperr.Unavailable()
	}
	if tooSoon {
		return c.JSON(dto.DeviceTokenResponse{Status: "slow_down"})
	}
	if rec.Status != devicecode.StatusApproved {
		return c.JSON(dto.DeviceTokenResponse{Status: "pending"})
	}

	// One-shot: Consume atomically deletes the record, so two concurrent
	// redemptions can never both mint a PAT.
	rec, err = h.deps.DeviceCodes.Consume(ctx, req.DeviceCode)
	if errors.Is(err, devicecode.ErrNotFound) || errors.Is(err, devicecode.ErrNotApproved) {
		return c.JSON(dto.DeviceTokenResponse{Status: "expired"})
	}
	if err != nil {
		return apperr.Unavailable()
	}

	// The PAT is minted HERE, not at approval time: the device-flow store is
	// shared with sessions (and, on Redis, with rate limits and cron locks), so
	// any read access to it would otherwise equal disclosure of a long-lived
	// credential in plaintext.
	plaintext, tok, err := h.issueToken(
		ctx,
		rec.AuthzID,
		deviceTokenName(rec.NameHint),
		// The lifetime the APPROVER picked, read from the record — never from
		// this request's body, which carries no authentication.
		model.Timestamp(time.Now().Add(deviceTokenLifetime(rec.TokenTTL))),
		AuditMeta{
			Channel:    "device",
			ApproverIP: rec.ApproverIP,
			ExchangeIP: clientIPOf(c),
			UserAgent:  c.Get(fiber.HeaderUserAgent),
			RequestID:  middleware.RequestIDFromCtx(c),
		})
	if err != nil {
		return apperr.Internal("could not create token")
	}

	return c.JSON(dto.DeviceTokenResponse{
		Status:    "approved",
		Token:     plaintext,
		Name:      tok.Name,
		ExpiresAt: dto.NullableExpiry(tok.ExpiresAt),
	})
}
