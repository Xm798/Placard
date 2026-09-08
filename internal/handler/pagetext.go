package handler

import (
	"bytes"
	"html/template"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/i18n"
	"github.com/Xm798/placard/internal/web"
)

// The copy for every page this server renders itself. The SPA carries its own
// bundles; these are the pages a browser sees before any of it loads — the
// share-page shells, the access-code prompt, the device authorization pages and
// the sign-out / sign-in-failed landings.
//
// Each bundle is a struct rather than a map so a missing string is a compile
// error: these pages are rendered from templates at init, where a key typo
// would otherwise surface as a hole in a page nobody looks at until a link
// preview is already broken.

// pageLang resolves the language for one request. Accept-Language is the only
// signal available: none of these pages runs the SPA, so none of them can read
// the reader's stored choice.
func pageLang(c *fiber.Ctx) i18n.Lang {
	return i18n.Match(c.Get(fiber.HeaderAcceptLanguage))
}

// varyByLanguage MUST be called on every response whose body pageLang decided.
// Without it a shared cache is free to hand one reader's language to the next,
// and these are exactly the pages a link-preview crawler and a corporate proxy
// see first.
func varyByLanguage(c *fiber.Ctx) {
	c.Vary(fiber.HeaderAcceptLanguage)
}

// viewShellText is the viewer shell's copy. Values reaching the inline script
// are written into single-quoted JS string literals, so none of them may
// contain an apostrophe or a backslash.
type viewShellText struct {
	Lang           string
	BrandHome      string
	VersionSelect  string
	FrameTitle     string
	ErrorTitle     string
	ErrorBody      string
	HomeLink       string
	LatestSuffix   string
	SharedSuffix   string
	ExternalTitle  string
	ExternalBody   string
	SharePageTitle string
	SharePageBody  string
	SessionNote    string
	CancelButton   string
	OpenLinkButton string
}

var viewShellTextByLang = map[i18n.Lang]viewShellText{
	i18n.EN: {
		Lang:           "en",
		BrandHome:      "Placard home",
		VersionSelect:  "Switch version",
		FrameTitle:     "User HTML content",
		ErrorTitle:     "This page is not available",
		ErrorBody:      "The link is invalid, has expired, or was deleted.",
		HomeLink:       "Go to the Placard home page",
		LatestSuffix:   " · latest",
		SharedSuffix:   " · shared",
		ExternalTitle:  "Open an external link",
		ExternalBody:   "You are about to leave Placard. This opens in a new tab:",
		SharePageTitle: "Open a share page",
		SharePageBody:  "Another Placard share page opens in a new tab:",
		SessionNote:    "This site will not be asked about again during this session",
		CancelButton:   "Cancel",
		OpenLinkButton: "Open link",
	},
	i18n.ZhCN: {
		Lang:           "zh-CN",
		BrandHome:      "返回 Placard 首页",
		VersionSelect:  "切换版本",
		FrameTitle:     "用户 HTML 内容",
		ErrorTitle:     "此页面不可用",
		ErrorBody:      "该链接无效、已过期或已被删除。",
		HomeLink:       "前往 Placard 首页",
		LatestSuffix:   " · 最新",
		SharedSuffix:   " · 分享中",
		ExternalTitle:  "打开外部链接",
		ExternalBody:   "即将离开 Placard，在新标签页访问：",
		SharePageTitle: "打开分享页",
		SharePageBody:  "将在新标签页打开另一个 Placard 分享页：",
		SessionNote:    "本次会话内不再询问此站点",
		CancelButton:   "取消",
		OpenLinkButton: "打开链接",
	},
}

// unlockShellText is the access-code prompt's copy. The three error lines are
// written into single-quoted JS string literals — no apostrophes, no
// backslashes.
type unlockShellText struct {
	Lang         string
	Title        string
	Heading      string
	Body         string
	InputLabel   string
	SubmitButton string
	HomeLink     string
	CookieError  string
	RateError    string
	WrongError   string
	NetworkError string
}

var unlockShellTextByLang = map[i18n.Lang]unlockShellText{
	i18n.EN: {
		Lang:         "en",
		Title:        "Access code required — Placard",
		Heading:      "Access code required",
		Body:         "The owner protected this page with a 6-digit access code. Enter it to read the page.",
		InputLabel:   "6-digit access code",
		SubmitButton: "View page",
		HomeLink:     "Go to the Placard home page",
		CookieError:  "The code was right, but the browser kept no credential. Allow cookies for this site and try again.",
		RateError:    "Too many attempts. Try again shortly.",
		WrongError:   "That access code is not correct.",
		NetworkError: "Network error. Please try again.",
	},
	i18n.ZhCN: {
		Lang:         "zh-CN",
		Title:        "需要访问码 — Placard",
		Heading:      "需要访问码",
		Body:         "该页面由所有者设置了 6 位访问码，请输入后查看。",
		InputLabel:   "6 位访问码",
		SubmitButton: "查看页面",
		HomeLink:     "前往 Placard 首页",
		CookieError:  "访问码正确，但浏览器没有保存凭据。请允许本站使用 Cookie 后重试。",
		RateError:    "尝试次数过多，请稍后再试。",
		WrongError:   "访问码不正确。",
		NetworkError: "网络错误，请重试。",
	},
}

// deviceText is the device authorization pages' copy. CompareWarning carries
// markup (the <code>placard login</code> the reader is told to look for), so it
// is template.HTML and must never hold anything a request supplied.
type deviceText struct {
	Lang            string
	Title           string
	Heading         string
	CompareWarning  template.HTML
	ExpiredWarning  string
	ManualWarning   string
	Consequence     string
	CodeLabel       string
	TTLLegend       string
	SubmitButton    string
	RequesterHost   string
	RequesterIP     string
	ApprovedTitle   string
	ApprovedHeading string
	// ApprovedBody names the granted lifetime, so it carries a %s for it.
	ApprovedBody string
	// DayLabel names a lifetime in days; DayLabelOne covers the singular.
	DayLabel    string
	DayLabelOne string
	// BadTTLError and BadCodeError are the inline errors a rejected approval
	// re-renders with.
	BadTTLError  string
	BadCodeError string
}

var deviceTextByLang = map[i18n.Lang]deviceText{
	i18n.EN: {
		Lang:    "en",
		Title:   "Authorize the Placard CLI",
		Heading: "Authorize the Placard CLI",
		CompareWarning: "Check that this code matches the one your terminal shows. " +
			"If you did not run <code>placard login</code>, close this page now.",
		ExpiredWarning: "That authorization code has expired. Request a new one.",
		ManualWarning:  "Enter the 8-character authorization code your terminal shows.",
		Consequence: "Approving issues the requester an access token that can act as you. " +
			"You can revoke it any time under Settings → API tokens. Pick the shortest lifetime that will do.",
		CodeLabel:       "Authorization code",
		TTLLegend:       "Token lifetime",
		SubmitButton:    "Approve",
		RequesterHost:   "Requesting host",
		RequesterIP:     "Requesting IP",
		ApprovedTitle:   "Authorized",
		ApprovedHeading: "Authorized",
		ApprovedBody: "An access token valid for %s has been issued. Go back to your terminal — " +
			"the Placard CLI finishes signing in within a few seconds. This page can be closed.",
		DayLabel:     "%s days",
		DayLabelOne:  "%s day",
		BadTTLError:  "Pick one of the token lifetimes offered on this page.",
		BadCodeError: "That authorization code is invalid or has expired (they last 180 seconds). Run placard login again in your terminal.",
	},
	i18n.ZhCN: {
		Lang:            "zh-CN",
		Title:           "授权 Placard CLI",
		Heading:         "授权 Placard CLI",
		CompareWarning:  "请核对授权码应与终端显示一致。如果你没有运行 <code>placard login</code>，请立即关闭本页。",
		ExpiredWarning:  "授权码已过期，请重新获取。",
		ManualWarning:   "输入终端显示的 8 位授权码。",
		Consequence:     "授权后将向发起方签发一个可代表你操作的访问令牌，可随时在「设置 - 访问令牌」中吊销。请按需选择有效期，越短越安全。",
		CodeLabel:       "授权码",
		TTLLegend:       "令牌有效期",
		SubmitButton:    "确认授权",
		RequesterHost:   "发起主机",
		RequesterIP:     "发起 IP",
		ApprovedTitle:   "授权成功",
		ApprovedHeading: "授权成功",
		ApprovedBody:    "已签发有效期 %s 的访问令牌。可以回到终端了，Placard CLI 会在几秒内自动完成登录。本页可以关闭。",
		DayLabel:        "%s 天",
		DayLabelOne:     "%s 天",
		BadTTLError:     "请选择页面上提供的令牌有效期。",
		BadCodeError:    "授权码无效或已过期（有效期 180 秒）。请回到终端重新运行 placard login。",
	},
}

// landingText is the copy shared by the two script-less landings a browser
// navigation ends on: the sign-out page and the sign-in-failed page.
type landingText struct {
	Lang    string
	Title   string
	Heading string
	Body    string
	Action  string
}

var loggedOutTextByLang = map[i18n.Lang]landingText{
	i18n.EN: {
		Lang:    "en",
		Title:   "Signed out — Placard",
		Heading: "You are signed out",
		Body:    "This session has ended. On a shared device, make sure the browser window is closed too.",
		Action:  "Sign in again",
	},
	i18n.ZhCN: {
		Lang:    "zh-CN",
		Title:   "已退出 — Placard",
		Heading: "你已退出登录",
		Body:    "本次会话已结束。如果这是共享设备，请一并关闭浏览器窗口。",
		Action:  "重新登录",
	},
}

var oidcErrorTextByLang = map[i18n.Lang]landingText{
	i18n.EN: {
		Lang:    "en",
		Title:   "Sign-in failed — Placard",
		Heading: "Sign-in could not be completed",
		Body: "The single sign-on attempt could not be verified, or it expired before it came back. " +
			"Nothing was changed on your account. Starting again from the sign-in page usually resolves it.",
		Action: "Back to sign in",
	},
	i18n.ZhCN: {
		Lang:    "zh-CN",
		Title:   "登录失败 — Placard",
		Heading: "登录未能完成",
		Body:    "本次单点登录未通过校验，或在返回前已过期。你的账号没有发生任何变化。回到登录页重新开始通常即可解决。",
		Action:  "返回登录页",
	},
}

// The two landings are rendered per language at init: they are fixed pages, and
// a template fault in one is a build-time panic rather than a blank page at the
// end of a sign-out.
var (
	loggedOutPages = renderLandings("logged_out", web.LoggedOutTemplate, loggedOutTextByLang)
	oidcErrorPages = renderLandings("oidc_error", web.OIDCErrorTemplate, oidcErrorTextByLang)
)

func renderLandings(name, src string, texts map[i18n.Lang]landingText) map[i18n.Lang][]byte {
	tmpl := template.Must(template.New(name).Option("missingkey=error").Parse(src))
	out := make(map[i18n.Lang][]byte, len(texts))
	for lang, text := range texts {
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, text); err != nil {
			panic(name + " page (" + string(lang) + "): " + err.Error())
		}
		out[lang] = buf.Bytes()
	}
	return out
}

// A language with no bundle would reach a reader as a blank page at the one
// moment they were following a link, so the gap is a startup panic instead:
// these maps are hand-edited, and adding a language to i18n.Supported without
// its copy is exactly the edit that produces one.
func init() {
	requireAllLanguages("view shell", viewShellTextByLang)
	requireAllLanguages("unlock shell", unlockShellTextByLang)
	requireAllLanguages("device pages", deviceTextByLang)
	requireAllLanguages("logged-out page", loggedOutTextByLang)
	requireAllLanguages("sign-in-failed page", oidcErrorTextByLang)
}

func requireAllLanguages[T any](name string, texts map[i18n.Lang]T) {
	for _, lang := range i18n.Supported {
		if _, ok := texts[lang]; !ok {
			panic(name + " has no copy for " + string(lang))
		}
	}
}
