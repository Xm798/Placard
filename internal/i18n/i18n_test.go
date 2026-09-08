package i18n

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   Lang
	}{
		{"absent header", "", EN},
		{"plain english", "en", EN},
		{"english with region", "en-US,en;q=0.9", EN},
		{"plain chinese", "zh-CN", ZhCN},
		{"chinese without region", "zh", ZhCN},
		// One Chinese bundle serves every Chinese reader: Simplified beats
		// English for someone who asked for Traditional.
		{"traditional chinese", "zh-TW,zh;q=0.9", ZhCN},
		{"case is insignificant", "ZH-Hans", ZhCN},
		{"quality order wins over document order", "en;q=0.4,zh-CN;q=0.9", ZhCN},
		{"ties go to the earlier tag", "en,zh-CN", EN},
		{"unspoken languages fall back", "ko,ja;q=0.8", EN},
		// A language explicitly refused must not be picked just because it is
		// the only one this server recognises in the header.
		{"q=0 is a refusal", "zh-CN;q=0,ko", EN},
		{"wildcard asks for nothing in particular", "*", EN},
		{"malformed q keeps the tag", "zh-CN;q=high", ZhCN},
		{"whitespace and empty entries", " , zh-CN ; q=0.8 ", ZhCN},
		{"garbage", ";;;", EN},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.header); got != tc.want {
				t.Fatalf("Match(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}
