// Package i18n resolves the language a server-rendered page is written in.
//
// The SPA carries its own translation bundles and remembers what the reader
// picked; this package covers the pages the browser gets before any JavaScript
// runs — the share-page shells, the access-code prompt, the device
// authorization pages and the sign-out / sign-in-failed landings. Those have
// only the request to go on, so they read Accept-Language.
package i18n

import (
	"strconv"
	"strings"
)

// Lang is a language this server can render a page in.
type Lang string

const (
	EN   Lang = "en"
	ZhCN Lang = "zh-CN"
)

// Default is what a reader whose languages this server does not speak gets.
const Default = EN

// Supported lists every language a page can be rendered in, Default first.
var Supported = []Lang{EN, ZhCN}

// Match picks the page language for an Accept-Language header.
//
// Tags are read in quality order and the first one this server speaks wins;
// ties go to the earlier tag, which is the order the browser wrote its own
// preferences in. An absent, malformed or entirely unspoken header yields
// Default — never a partial guess, since a reader who asked for Korean is no
// better served by Chinese than by English.
//
// Region and script subtags are not distinguished: there is one Chinese bundle,
// so zh, zh-CN, zh-TW and zh-Hant all resolve to it. That is deliberate — a
// reader asking for zh-TW would rather have Simplified Chinese than English.
func Match(header string) Lang {
	best := Default
	bestQ := 0.0
	for entry := range strings.SplitSeq(header, ",") {
		tag, q := parseLanguageRange(entry)
		if tag == "" || q <= 0 {
			continue
		}
		lang, ok := lookup(tag)
		if !ok || q <= bestQ {
			continue
		}
		best, bestQ = lang, q
	}
	return best
}

// lookup maps a language range to a supported language by its primary subtag.
// The "*" wildcard is not a preference for anything in particular, so it is
// left to Default rather than resolved to the first supported language.
func lookup(tag string) (Lang, bool) {
	primary, _, _ := strings.Cut(tag, "-")
	switch primary {
	case "zh":
		return ZhCN, true
	case "en":
		return EN, true
	}
	return "", false
}

// parseLanguageRange splits one Accept-Language entry into its lowercased tag
// and quality. A missing or unparsable q is 1, per RFC 9110: a client that
// names a language without weighting it wants it most.
func parseLanguageRange(entry string) (string, float64) {
	tag, params, hasParams := strings.Cut(entry, ";")
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" {
		return "", 0
	}
	if !hasParams {
		return tag, 1
	}
	for param := range strings.SplitSeq(params, ";") {
		name, value, ok := strings.Cut(param, "=")
		if !ok || strings.ToLower(strings.TrimSpace(name)) != "q" {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return tag, 1
		}
		return tag, q
	}
	return tag, 1
}
