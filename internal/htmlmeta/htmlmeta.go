// Package htmlmeta extracts the document metadata Placard snapshots at publish
// time: a page's <title> text and its <meta name="description"> content. It is
// a leaf package depending only on regexp/strings/html so BOTH the server
// (publish path) and the CLI can use the exact same implementation.
//
// The CLI cannot call the server's copy: the original htmlTitleRE /
// extractHTMLTitle were unexported members of package handler, and cmd/placard
// is forbidden from importing internal/handler (it would drag in the embedded
// SPA, GORM, Redis and the S3 SDK). Duplicating the regexes in the CLI would
// require hand-syncing THREE things — the patterns, html.UnescapeString, and
// the 255-RUNE (not byte) cap — so this package is the single source of truth.
package htmlmeta

import (
	"html"
	"regexp"
	"strings"
)

// MaxRunes bounds every returned value. Runes, not bytes: a byte cap would
// slice a multi-byte CJK rune in half. It is also the width of the columns
// these snapshots are stored in.
const MaxRunes = 255

var (
	titleRE = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title\s*>`)
	// A quoted attribute value may legitimately contain > ("Q3 > Q2" in a
	// description is ordinary prose), so the tag cannot simply run to the first
	// > — quoted runs are consumed whole before the closing bracket is looked
	// for.
	metaRE = regexp.MustCompile(`(?is)<meta\b(?:"[^"]*"|'[^']*'|[^>"'])*>`)
	// attrRE splits one tag's attributes. RE2 has no lookahead, so a meta tag
	// cannot be matched on "carries name=description AND content=…" in a single
	// pattern — the tag is matched first, then its attributes are read here.
	attrRE = regexp.MustCompile(`(?is)([a-z0-9_:-]+)\s*=\s*("[^"]*"|'[^']*'|[^\s"'` + "`" + `<>]+)`)
)

// Title returns the first <title>'s text: HTML-unescaped, TrimSpace'd, and
// capped at MaxRunes. Returns "" when the body carries no <title>.
func Title(body []byte) string {
	m := titleRE.FindSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return clean(string(m[1]))
}

// Description returns the first non-empty <meta name="description"> content,
// cleaned the same way Title is. Attribute order and quoting style are free —
// the tag is matched first and its attributes read individually. Returns ""
// when the body declares no description.
//
// Like Title, this scans the raw bytes rather than parsing the document, so a
// tag inside an HTML comment counts as if it were live. The cost of that is a
// stale link-preview description on a page whose author commented one out,
// which is not worth an HTML parser in a package the CLI also links.
func Description(body []byte) string {
	for _, tag := range metaRE.FindAll(body, -1) {
		attrs := attributes(tag)
		if !strings.EqualFold(attrs["name"], "description") {
			continue
		}
		if d := clean(attrs["content"]); d != "" {
			return d
		}
	}
	return ""
}

// attributes reads one tag's attributes into a lowercase-keyed map, unquoting
// values but leaving entities alone — clean unescapes whichever one is used.
// A repeated attribute keeps its first occurrence, matching how a browser
// resolves the duplicate.
func attributes(tag []byte) map[string]string {
	attrs := map[string]string{}
	for _, m := range attrRE.FindAllSubmatch(tag, -1) {
		key := strings.ToLower(string(m[1]))
		if _, seen := attrs[key]; seen {
			continue
		}
		attrs[key] = unquote(string(m[2]))
	}
	return attrs
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// clean unescapes entities, trims surrounding whitespace and caps the result at
// MaxRunes.
func clean(raw string) string {
	v := strings.TrimSpace(html.UnescapeString(raw))
	if len([]rune(v)) > MaxRunes {
		v = string([]rune(v)[:MaxRunes])
	}
	return v
}
