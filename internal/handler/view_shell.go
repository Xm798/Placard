package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html"
	"strings"
	"text/template"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/authz"
	"github.com/Xm798/placard/internal/i18n"
	"github.com/Xm798/placard/internal/userctx"
)

// ViewShell handles GET /s/:id. It returns the viewer shell HTML on the primary
// origin. The shell's JS fetches /s/:id/meta, then sets the
// iframe src to the returned render_url — the same-origin proxy /s/:id/render.
//
// The shell is the only place the page is described to something that does not
// run JavaScript, so it renders the Open Graph tags a chat client scrapes when
// the link is pasted. That is also why it resolves the file server-side: a page
// the caller may not view answers 404 with the generic shell body and no tags
// at all, identically to an id that never existed, expired, or was deleted. The
// denial is not audited here — /s/:id/meta and /s/:id/render are the audited
// read points, and the browser reaches one of them on every real visit.
//
// Security:
//   - The iframe loads same-origin /s/:id/render (NEVER srcdoc). Isolation comes
//     from sandbox="allow-scripts" (an opaque origin) PLUS the CSP sandbox header
//     the render response sets — never from a cross-origin object-store URL.
//   - sandbox="allow-scripts" WITHOUT allow-same-origin; referrerpolicy=no-referrer.
//     NEVER add allow-same-origin, and NEVER switch the iframe to srcdoc — either
//     would let the user HTML run on the primary origin.
//   - External links are opened by the parent after confirmation; this does not
//     relax either the iframe sandbox or the render response CSP.
//   - Same-origin share-view paths (/s/<id>) are re-opened top-level in a new
//     tab, never navigated inside the sandboxed frame.
//   - Response headers add Strict-Transport-Security and a CSP with
//     frame-ancestors 'self' (anti-clickjacking) + base-uri 'none'.
//
// A page carrying a share code serves the unlock prompt (unlockShellTmpl)
// here instead, until the visitor holds a valid ticket cookie.
//
// The shell's own inline <style> and <script> are admitted via SHA-256 hashes
// (NOT 'unsafe-inline'): the blocks are fixed at build time so the hashes are
// stable, keeping the page self-contained with zero external subresources (F3)
// while still forbidding any injected inline execution.
//   - The version dropdown is owner-gated server-side: the shell merely probes
//     /api/files/:id/versions and stays silent on 401/404, so visitors get no
//     version UI and no existence signal. Switching versions swaps the iframe
//     src to the owner-only render?v=N and updates the title from the version
//     list already held in memory (no extra fetch, same textContent write as
//     the initial title).
var viewShells = renderShells("view", viewShellTmpl, viewShellTextByLang)

// renderedShell is one language's finished shell plus the CSP hashes of its own
// inline blocks. The hashes are per language because the copy lives inside
// those blocks: one document, one pair of hashes.
type renderedShell struct {
	html       string
	styleHash  string
	scriptHash string
}

// withOpenGraph splices og into the shell. ogPlaceholder sits outside the
// inline <style>/<script> blocks, so the hashes admit the result whatever it
// replaces the marker with.
func (s renderedShell) withOpenGraph(og string) string {
	return strings.Replace(s.html, ogPlaceholder, og, 1)
}

// renderShells renders one document per supported language at init and hashes
// each one's inline blocks. Any template fault is a panic here rather than a
// broken page later: these documents are fixed at build time.
func renderShells[T any](name, src string, texts map[i18n.Lang]T) map[i18n.Lang]renderedShell {
	// text/template, not html/template: the copy is spliced into JS string
	// literals and HTML text alike, and contextual escaping would mangle one of
	// them. Everything substituted here is a compile-time constant from
	// pagetext.go — no request data reaches these templates.
	tmpl := template.Must(template.New(name).Option("missingkey=error").Parse(src))
	out := make(map[i18n.Lang]renderedShell, len(texts))
	for lang, text := range texts {
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, text); err != nil {
			panic(name + " shell (" + string(lang) + "): " + err.Error())
		}
		doc := buf.String()
		out[lang] = renderedShell{
			html:       doc,
			styleHash:  inlineCSPHash(doc, "style"),
			scriptHash: inlineCSPHash(doc, "script"),
		}
	}
	return out
}

func inlineCSPHash(document, tag string) string {
	startMarker, endMarker := "<"+tag+">", "</"+tag+">"
	start := strings.LastIndex(document, startMarker)
	end := strings.LastIndex(document, endMarker)
	if start < 0 || end < start {
		panic("share page shell missing inline " + tag)
	}
	content := document[start+len(startMarker) : end]
	sum := sha256.Sum256([]byte(content))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func (h *Handlers) ViewShell(c *fiber.Ctx) error {
	id := c.Params("id")
	lang := pageLang(c)
	shell := viewShells[lang]

	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	setCommonSecurityHeaders(c)
	varyByLanguage(c)
	// The shell body depends on who is asking — an owner sees their private
	// page's Open Graph tags where everyone else gets the 404 body — and on
	// which language they asked for, so a shared cache must never hand one
	// visitor's copy to the next.
	c.Set("Cache-Control", "no-store")
	// The viewer shell's hashes are the default — it is both the served body
	// and the 404 body; only the unlock prompt below overrides them.
	setShellCSP(c, shell.styleHash, shell.scriptHash)

	file, err := h.deps.Files.GetActiveByNanoID(c.UserContext(), id)
	// A database outage is not "this page does not exist": the shell's status
	// is what a link-preview crawler caches for the URL, so a blip must not
	// teach it that every live share link is gone.
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.Unavailable()
	}
	authzid := userctx.AuthzID(c)
	if err != nil || !authz.View(file, authzid) {
		return c.Status(fiber.StatusNotFound).SendString(shell.withOpenGraph(""))
	}
	// A code-protected page serves the prompt instead of the viewer, at 200:
	// the URL is real and the visitor has something to do here, which is
	// exactly what a share code is for. Only the title goes into the Open
	// Graph block — the description is a sentence out of the page itself, and
	// pasting the link into a chat must not preview content the code gates.
	if h.shareCodeLocked(c, file, authzid) {
		unlock := unlockShells[lang]
		setShellCSP(c, unlock.styleHash, unlock.scriptHash)
		return c.SendString(unlock.withOpenGraph(
			openGraphTitleOnly(h.shareURL(id), file.Title)))
	}
	return c.SendString(shell.withOpenGraph(
		openGraphTags(h.shareURL(id), file.Title, file.Description)))
}

// setShellCSP sets the Content-Security-Policy shared by the two share-page
// shells, admitting each one's own inline style and script by hash. frame-src
// is 'self' for both: only the viewer shell has an iframe, and naming the
// directive on a page without one costs nothing. data: in img-src is what the
// inline SVG favicon both shells carry is loaded from; they use no other image.
func setShellCSP(c *fiber.Ctx, styleHash, scriptHash string) {
	c.Set("Content-Security-Policy", "default-src 'self'; "+
		"script-src "+scriptHash+"; "+
		"style-src "+styleHash+"; "+
		"img-src 'self' data:; "+
		"frame-ancestors 'self'; base-uri 'none'; "+
		"frame-src 'self'")
}

// openGraphTags renders the link-preview block a chat client reads. og:type and
// og:url describe the share page itself and are always emitted; og:title and
// og:description come from the page's own snapshots and are left out entirely
// when it has no title, rather than filled with a placeholder. A page with a
// title but no description repeats the title there, since a card with an empty
// description line reads as broken.
//
// Every value is attribute-escaped: this is the one place user content reaches
// the shell's own markup.
func openGraphTags(shareURL, title, description string) string {
	var b strings.Builder
	b.WriteString(`<meta property="og:type" content="website">` + "\n")
	b.WriteString(`<meta property="og:url" content="` + html.EscapeString(shareURL) + `">` + "\n")
	if title == "" {
		return b.String()
	}
	b.WriteString(`<meta property="og:title" content="` + html.EscapeString(title) + `">` + "\n")
	if description == "" {
		description = title
	}
	b.WriteString(`<meta property="og:description" content="` + html.EscapeString(description) + `">` + "\n")
	return b.String()
}

// openGraphTitleOnly is openGraphTags without the description, for a page whose
// content is behind a share code: the title is what the owner chose to put on
// the link, while the description is lifted out of the page body and would
// leak a sentence of gated content into every chat client that scrapes it.
func openGraphTitleOnly(shareURL, title string) string {
	var b strings.Builder
	b.WriteString(`<meta property="og:type" content="website">` + "\n")
	b.WriteString(`<meta property="og:url" content="` + html.EscapeString(shareURL) + `">` + "\n")
	if title != "" {
		b.WriteString(`<meta property="og:title" content="` + html.EscapeString(title) + `">` + "\n")
	}
	return b.String()
}

// ogPlaceholder marks where the Open Graph block is spliced into the shell.
const ogPlaceholder = "<!--og-->"

// setCommonSecurityHeaders sets the HSTS / Referrer-Policy / nosniff headers
// shared by every HTML response. The per-response Content-Security-Policy is
// set by each handler separately, since it is content-specific.
func setCommonSecurityHeaders(c *fiber.Ctx) {
	c.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	c.Set("Referrer-Policy", "no-referrer")
	c.Set("X-Content-Type-Options", "nosniff")
}

// setFormPostReferrerPolicy relaxes the no-referrer above to same-origin, and
// MUST be called by any page that submits a plain <form method="POST"> to this
// origin. Call it after setCommonSecurityHeaders, which sets the default.
//
// Under no-referrer a browser sends the literal string `Origin: null` on a form
// NAVIGATION POST and strips Referer entirely, so any handler validating
// Origin against an allowlist rejects every real submission. fetch() is NOT
// affected — it keeps a real Origin under CORS semantics — which is why only a
// script-less page hits this. same-origin still leaks nothing cross-origin.
//
// The alternative fix — accepting `Origin: null` in the handler — is wrong: it
// collapses a layered defence into a single check. See DeviceApprove.
func setFormPostReferrerPolicy(c *fiber.Ctx) {
	c.Set("Referrer-Policy", "same-origin")
}

// viewShellTmpl is the self-contained viewer shell, rendered once per language
// at init (see renderShells). Zero external subresources (F3): system font
// stack only. The state machine loads content via render_url (never srcdoc).
const viewShellTmpl = `<!DOCTYPE html>
<html lang="{{.Lang}}">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Placard</title>
<!--og-->
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'%3E%3Crect width='32' height='32' rx='7' fill='%2316150f'/%3E%3Ctext x='16' y='21' font-family='ui-monospace,monospace' font-size='13' font-weight='bold' fill='%23fafaf8' text-anchor='middle'%3E%26lt%3B/%26gt%3B%3C/text%3E%3C/svg%3E">
<style>
:root{--bg:#fafaf8;--surface:#fff;--fg:#16150f;--fg-soft:#6b6960;--line:#e7e5dd;--line-strong:#d4d2c8;--ink:#16150f;--ink-fg:#fafaf8;--hover:#f2f1eb;}
.dark{--bg:#0e0e0c;--surface:#181814;--fg:#f3f2ea;--fg-soft:#a3a195;--line:#262620;--line-strong:#34332b;--ink:#f3f2ea;--ink-fg:#16150f;--hover:#201f1a;}
*{box-sizing:border-box;}
html,body{height:100%;margin:0;}
body{font-family:system-ui,-apple-system,"Segoe UI","Noto Sans SC",sans-serif;background:var(--bg);color:var(--fg);display:flex;flex-direction:column;-webkit-font-smoothing:antialiased;}
.viewbar{flex-shrink:0;border-bottom:1px solid var(--line);background:var(--bg);}
.viewbar-inner{max-width:1200px;margin:0 auto;padding:0 20px;height:56px;display:flex;align-items:center;gap:16px;}
.brand{display:flex;align-items:center;gap:8px;flex-shrink:0;text-decoration:none;color:var(--fg);}
.brand-mark{width:26px;height:26px;border-radius:7px;background:var(--ink);color:var(--ink-fg);display:flex;align-items:center;justify-content:center;font-family:ui-monospace,Menlo,monospace;font-size:12px;}
.brand-name{font-weight:800;font-size:17px;letter-spacing:-.02em;}
.divider{width:1px;height:22px;background:var(--line-strong);flex-shrink:0;}
.doc-meta{flex:1;min-width:0;}
.doc-title{font-weight:500;font-size:14px;line-height:1.2;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;}
.stage{flex:1;min-height:0;position:relative;background:#fff;}
.frame-wrap{position:absolute;inset:0;display:flex;}
iframe{width:100%;height:100%;border:0;background:#fff;}
.hidden{display:none!important;}
.errstate{flex:1;min-height:0;display:flex;align-items:center;justify-content:center;padding:32px;}
.errstate-inner{text-align:center;max-width:420px;}
.err-mark{width:44px;height:44px;border-radius:12px;display:inline-flex;align-items:center;justify-content:center;font-family:ui-monospace,Menlo,monospace;font-size:18px;background:var(--ink);color:var(--ink-fg);margin-bottom:20px;}
.err-title{font-weight:800;font-size:26px;letter-spacing:-.02em;margin:0 0 8px;}
.err-sub{color:var(--fg-soft);font-size:14px;line-height:1.6;margin:0 0 24px;}
.btn{display:inline-flex;align-items:center;justify-content:center;height:32px;padding:0 12px;border-radius:8px;font:500 13px system-ui,-apple-system,"Segoe UI","Noto Sans SC",sans-serif;text-decoration:none;border:1px solid var(--line-strong);background:transparent;color:var(--fg);cursor:pointer;}
.btn:not(.btn-primary):hover{background:var(--hover);}
.btn-primary{border-color:var(--ink);background:var(--ink);color:var(--ink-fg);}
.ver-select{height:32px;padding:0 10px;border-radius:8px;border:1px solid var(--line-strong);background:var(--surface);color:var(--fg);font-size:13px;flex-shrink:0;font-family:inherit;}
.link-dialog-overlay{position:fixed;inset:0;z-index:10;display:flex;align-items:center;justify-content:center;padding:24px;background:rgba(0,0,0,.46);animation:dialog-fade .14s ease-out;}
.link-dialog{width:min(520px,100%);max-height:calc(100vh - 48px);overflow:auto;border:1px solid var(--line);border-radius:16px;background:var(--surface);color:var(--fg);padding:24px;box-shadow:0 20px 60px rgba(0,0,0,.24);animation:dialog-rise .14s ease-out;}
.link-dialog-title{margin:0 0 10px;font-size:20px;line-height:1.3;letter-spacing:-.01em;}
.link-dialog-copy{margin:0 0 14px;color:var(--fg-soft);font-size:14px;line-height:1.6;}
.link-dialog-url{max-height:112px;overflow:auto;margin:0 0 12px;padding:12px;border:1px solid var(--line);border-radius:10px;background:var(--bg);font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,monospace;word-break:break-all;}
.link-dialog-note{margin:0;color:var(--fg-soft);font-size:12px;line-height:1.5;}
.link-dialog-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:22px;}
@keyframes dialog-fade{from{opacity:0;}to{opacity:1;}}
@keyframes dialog-rise{from{opacity:0;transform:translateY(4px) scale(.99);}to{opacity:1;transform:none;}}
@media (prefers-reduced-motion:reduce){.link-dialog-overlay,.link-dialog{animation:none;}}
</style>
</head>
<body>
<div class="viewbar" id="viewbar">
  <div class="viewbar-inner">
    <a class="brand" href="/" aria-label="{{.BrandHome}}" title="Placard">
      <span class="brand-mark" aria-hidden="true">&lt;/&gt;</span>
      <span class="brand-name">Placard</span>
    </a>
    <div class="divider" aria-hidden="true"></div>
    <div class="doc-meta"><div class="doc-title" id="doc-title"></div></div>
    <select id="version-select" class="ver-select hidden" aria-label="{{.VersionSelect}}"></select>
  </div>
</div>
<div class="stage hidden" id="stage">
  <div class="frame-wrap">
    <iframe id="render" sandbox="allow-scripts" referrerpolicy="no-referrer" title="{{.FrameTitle}}"></iframe>
  </div>
</div>
<div class="errstate hidden" id="errstate" role="alert">
  <div class="errstate-inner">
    <span class="brand-mark err-mark" aria-hidden="true">&lt;/&gt;</span>
    <h1 class="err-title">{{.ErrorTitle}}</h1>
    <p class="err-sub">{{.ErrorBody}}</p>
    <a class="btn" href="/" aria-label="{{.HomeLink}}">{{.HomeLink}}</a>
  </div>
</div>
<div class="link-dialog-overlay hidden" id="link-dialog-overlay">
  <div class="link-dialog" role="dialog" aria-modal="true" aria-labelledby="link-dialog-title" aria-describedby="link-dialog-copy">
    <h2 class="link-dialog-title" id="link-dialog-title">{{.ExternalTitle}}</h2>
    <p class="link-dialog-copy" id="link-dialog-copy">{{.ExternalBody}}</p>
    <div class="link-dialog-url" id="link-dialog-url"></div>
    <p class="link-dialog-note">{{.SessionNote}}</p>
    <div class="link-dialog-actions">
      <button class="btn" id="link-dialog-cancel" type="button">{{.CancelButton}}</button>
      <button class="btn btn-primary" id="link-dialog-open" type="button">{{.OpenLinkButton}}</button>
    </div>
  </div>
</div>
<script>
(function(){
  var root=document.documentElement;
  var nano=location.pathname.split('/')[2]||'';
  var previewV=new URLSearchParams(location.search).get('v');
  var frame=document.getElementById('render');
  var dialogOverlay=document.getElementById('link-dialog-overlay');
  var dialogTitle=document.getElementById('link-dialog-title');
  var dialogCopy=document.getElementById('link-dialog-copy');
  var dialogURL=document.getElementById('link-dialog-url');
  var dialogOpen=document.getElementById('link-dialog-open');
  var dialogCancel=document.getElementById('link-dialog-cancel');
  var approvedOrigins=new Set();
  var pendingURL=null;
  var saved=localStorage.getItem('page-theme');
  if(saved?saved==='dark':window.matchMedia('(prefers-color-scheme: dark)').matches){root.classList.add('dark');}
  function showError(){document.getElementById('viewbar').style.display='none';document.getElementById('stage').classList.add('hidden');document.getElementById('errstate').classList.remove('hidden');}
  function setTitle(title){document.getElementById('doc-title').textContent=title||'';document.title=(title||'Placard')+' — Placard';}
  function showOk(meta){setTitle(meta.title);document.getElementById('errstate').classList.add('hidden');document.getElementById('stage').classList.remove('hidden');frame.src=meta.render_url;}
  function initVersions(){
    fetch('/api/files/'+encodeURIComponent(nano)+'/versions',{credentials:'same-origin',headers:{'X-Requested-With':'fetch'}})
      .then(function(r){if(!r.ok)throw 0;return r.json();})
      .then(function(d){
        if(!d||!d.versions||!d.versions.length){return;}
        var sel=document.getElementById('version-select');
        var serving=d.shared_version||d.latest_version;
        var current=previewV?parseInt(previewV,10):serving;
        var titleByVersion={};
        d.versions.forEach(function(v){
          titleByVersion[v.version]=v.title;
          var o=document.createElement('option');
          o.value=String(v.version);
          var label='v'+v.version;
          if(v.version===d.latest_version){label+='{{.LatestSuffix}}';}
          if(v.version===serving){label+='{{.SharedSuffix}}';}
          o.textContent=label;
          if(v.version===current){o.selected=true;}
          sel.appendChild(o);
        });
        sel.addEventListener('change',function(){
          setTitle(titleByVersion[sel.value]);
          frame.src='/s/'+encodeURIComponent(nano)+'/render?v='+sel.value;
        });
        sel.classList.remove('hidden');
      })
      .catch(function(){}); // 401/404 → visitor: no version UI, no probe feedback
  }
  function showLinkDialog(url){
    var sameOrigin=url.origin===location.origin;
    pendingURL=url;
    dialogTitle.textContent=sameOrigin?'{{.SharePageTitle}}':'{{.ExternalTitle}}';
    dialogCopy.textContent=sameOrigin?'{{.SharePageBody}}':'{{.ExternalBody}}';
    dialogURL.textContent=url.href;
    dialogOverlay.classList.remove('hidden');
    dialogOpen.focus();
  }
  function closeLinkDialog(){
    if(!pendingURL){return;}
    pendingURL=null;
    dialogOverlay.classList.add('hidden');
    frame.focus();
  }
  // Omitting noopener preserves null as the popup-blocked signal; the opener is
  // severed manually, and the shell's Referrer-Policy prevents referrer leakage.
  function openExternal(url){var w=window.open(url,'_blank');if(!w){return false;}try{w.opener=null;}catch(ignore){}return true;}
  function handleOpen(raw){
    var url;
    if(pendingURL){return;}
    try{url=new URL(raw);}catch(ignore){return;}
    if(url.protocol!=='http:'&&url.protocol!=='https:'){return;}
    if(url.origin===location.origin){if(!/^\/s\/[A-Za-z0-9_-]+$/.test(url.pathname)){return;}}
    if(approvedOrigins.has(url.origin)&&openExternal(url)){return;}
    showLinkDialog(url);
  }
  window.addEventListener('message',function(e){
    if(e.source!==frame.contentWindow||!e.data||e.data.type!=='` + linkRelayMessageType + `'||typeof e.data.href!=='string'||e.data.href.length>` + linkRelayMaxHrefChars + `){return;}
    handleOpen(e.data.href);
  });
  document.addEventListener('securitypolicyviolation',function(e){
    if(e.violatedDirective==='frame-src'&&e.blockedURI){handleOpen(e.blockedURI);}
  });
  dialogCancel.addEventListener('click',closeLinkDialog);
  dialogOpen.addEventListener('click',function(){
    if(!pendingURL){return;}
    var url=pendingURL;
    approvedOrigins.add(url.origin);
    openExternal(url);
    closeLinkDialog();
  });
  dialogOverlay.addEventListener('click',function(e){if(e.target===dialogOverlay){closeLinkDialog();}});
  dialogOverlay.addEventListener('keydown',function(e){
    if(e.key!=='Tab'||!pendingURL){return;}
    if(e.shiftKey&&document.activeElement===dialogCancel){e.preventDefault();dialogOpen.focus();}
    else if(!e.shiftKey&&document.activeElement===dialogOpen){e.preventDefault();dialogCancel.focus();}
  });
  document.addEventListener('keydown',function(e){if(e.key==='Escape'&&pendingURL){closeLinkDialog();}});
  fetch(location.pathname+'/meta'+(previewV?'?v='+encodeURIComponent(previewV):''),{credentials:'same-origin',headers:{'X-Requested-With':'fetch'}})
    // 403 is share_code_required: the unlock ticket lapsed between this page
    // being served and this fetch. Reloading re-serves the code prompt, which
    // is the one thing the visitor can act on.
    .then(function(r){if(r.status===403){location.reload();throw 0;}if(!r.ok)throw 0;return r.json();})
    .then(function(meta){if(meta&&meta.expired===false&&meta.render_url){showOk(meta);initVersions();}else{showError();}})
    .catch(function(){showError();});
})();
</script>
</body>
</html>`
