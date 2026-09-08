package htmlmeta

import (
	"strings"
	"testing"
)

func TestTitle(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"plain", `<html><head><title>Q3 报表</title></head></html>`, "Q3 报表"},
		{"uppercase tag", `<TITLE>Hello</TITLE>`, "Hello"},
		{"with attributes", `<title lang="zh">带属性</title>`, "带属性"},
		{"multiline", "<title>\n  换行\n</title>", "换行"},
		{"closing tag with space", `<title>x</title >`, "x"},
		{"html entities unescaped", `<title>A &amp; B &lt;c&gt;</title>`, "A & B <c>"},
		{"leading trailing space trimmed", `<title>   spaced   </title>`, "spaced"},
		{"no title", `<html><body>hi</body></html>`, ""},
		{"empty title", `<title></title>`, ""},
		{"first title wins", `<title>one</title><title>two</title>`, "one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Title([]byte(tt.body)); got != tt.want {
				t.Fatalf("Title() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDescription(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"plain", `<meta name="description" content="季度总结">`, "季度总结"},
		{"single quotes", `<meta name='description' content='单引号'>`, "单引号"},
		{"unquoted", `<meta name=description content=bare>`, "bare"},
		{"content before name", `<meta content="倒序" name="description">`, "倒序"},
		{"uppercase", `<META NAME="Description" CONTENT="大写">`, "大写"},
		{"self closing", `<meta name="description" content="闭合" />`, "闭合"},
		{"entities unescaped", `<meta name="description" content="A &amp; B">`, "A & B"},
		{"trimmed", `<meta name="description" content="   spaced   ">`, "spaced"},
		{"other meta skipped", `<meta charset="utf-8"><meta name="viewport" content="width=device-width">`, ""},
		{"og description is not a description", `<meta property="og:description" content="og">`, ""},
		{"empty content falls through to the next", `<meta name="description" content=""><meta name="description" content="第二个">`, "第二个"},
		{"first non-empty wins", `<meta name="description" content="one"><meta name="description" content="two">`, "one"},
		{"none", `<html><body>hi</body></html>`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Description([]byte(tt.body)); got != tt.want {
				t.Fatalf("Description() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTruncatesByRunes pins that the cap is 255 RUNES, not bytes — a byte-based
// cap would cut a 3-byte CJK rune in half and emit invalid UTF-8.
func TestTruncatesByRunes(t *testing.T) {
	long := strings.Repeat("中", 300)
	for _, tc := range []struct {
		name string
		got  string
	}{
		{"title", Title([]byte("<title>" + long + "</title>"))},
		{"description", Description([]byte(`<meta name="description" content="` + long + `">`))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if n := len([]rune(tc.got)); n != MaxRunes {
				t.Fatalf("rune len = %d, want %d", n, MaxRunes)
			}
			if tc.got != strings.Repeat("中", MaxRunes) {
				t.Fatal("truncation split a multi-byte rune")
			}
		})
	}
}

// TestDescriptionWithAngleBracketInValue: > inside a quoted attribute value is
// valid HTML and ordinary prose. Matching the tag up to the first > would drop
// the description entirely, whichever side of it the content attribute sits on.
func TestDescriptionWithAngleBracketInValue(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{"content last", `<meta name="description" content="Q3 > Q2">`, "Q3 > Q2"},
		{"content first", `<meta content="Q3 > Q2" name="description">`, "Q3 > Q2"},
		{"bracket in a sibling attribute", `<meta data-x="a>b" name="description" content="真描述">`, "真描述"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Description([]byte(tt.body)); got != tt.want {
				t.Fatalf("Description() = %q, want %q", got, tt.want)
			}
		})
	}
}
