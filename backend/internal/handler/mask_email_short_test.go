package handler

import "testing"

// 已知问题清单【涉及敏感数据】：本地部分很短的邮箱等于没打码（ab@… 变成 ab***@…），没有 @ 的短串原样返回。
// 本地部分不超过 2 个字符只留 1 个；没有 @ 的短串也打码。3 个字符以上照旧留 2 个。
func TestMaskEmailForAdminShort(t *testing.T) {
	for in, want := range map[string]string{
		"ab@x.com":           "a***@x.com",
		"a@x.com":            "a***@x.com",
		"张三@x.com":           "张***@x.com",
		"abc@x.com":          "ab***@x.com",
		"alice@example.com":  "al***@example.com",
		"张三丰@x.com":          "张三***@x.com",
		"abc":                "a***",
		"ab":                 "a***",
		"a":                  "a***",
		"abcd":               "ab***",
		"abcdef":             "ab***",
		"":                   "",
		"unknown":            "unknown",
		"  ab@x.com  ":       "a***@x.com",
		"@x.com":             "***@x.com",
		"first.last@x.co.uk": "fi***@x.co.uk",
	} {
		if got := maskEmailForAdmin(in); got != want {
			t.Errorf("maskEmailForAdmin(%q) = %q，期望 %q", in, got, want)
		}
	}
}
