package handler

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
)

// 已知问题清单【涉及敏感数据】：服务商原文清洗的漏网——卡号紧挨字母或下划线、URL 编码的邮箱、中文顶级域的邮箱、
// 被 + / 切成短段的 base64；以及售后查询出错日志只遮 20 位以上的令牌，短码、短令牌会原样进日志。

func TestSanitizeProviderNoteGaps(t *testing.T) {
	for _, tc := range []struct {
		in    string
		leaks []string
	}{
		{"卡号x4111111111111111被拒", []string{"4111111111111111"}},
		{"id_4111111111111111 bad", []string{"4111111111111111"}},
		{"pan4111 1111 1111 1111 bad", []string{"4111 1111 1111 1111"}},
		{"account bob%40example.com failed", []string{"bob", "example.com"}},
		{"邮箱 张三@例子.中国 已绑定", []string{"张三", "例子"}},
		{"token abcDEF123+ghiJKL456/mnoPQR789== bad", []string{"abcDEF123", "ghiJKL456", "mnoPQR789"}},
	} {
		got := sanitizeProviderNote(tc.in)
		for _, leak := range tc.leaks {
			if strings.Contains(got, leak) {
				t.Errorf("%q 清洗后漏出 %q: %q", tc.in, leak, got)
			}
		}
		if !strings.Contains(got, "***") {
			t.Errorf("%q 应有替换: %q", tc.in, got)
		}
	}
	// 原有不该动的照旧
	for _, keep := range []string{"2026-11-04 失败", "手机 13812345678 已绑定", "卡台超时，请稍后再试", "HTTP 502 Bad Gateway"} {
		if got := sanitizeProviderNote(keep); got != keep {
			t.Errorf("%q 不应改动: %q", keep, got)
		}
	}
}

func captureStdLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) })
	return &buf
}

// 上游错误里带这次查的卡密本身、或短于 20 位的令牌时，日志里不能出现原文。服务商名和错误类型留着便于排查。
func TestFactsForLogHidesCodeAndShortTokens(t *testing.T) {
	const code = "TU-AB12-CD34"
	useFakeUp(t, fakeUp{errs: map[string]error{
		code: errors.New("upstream rejected code tu-ab12-cd34 (TUAB12CD34) with token tk9x8y7z6w: HTTP 502"),
	}})
	buf := captureStdLog(t)
	if _, err := factsFor(context.Background(), code, ""); err == nil {
		t.Fatal("应返回上游错误")
	}
	out := buf.String()
	for _, leak := range []string{"AB12", "ab12", "CD34", "cd34", "TUAB12CD34", "tk9x8y7z6w"} {
		if strings.Contains(out, leak) {
			t.Fatalf("日志不应带卡密或短令牌 %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, "HTTP 502") || !strings.Contains(out, "upstream rejected") {
		t.Fatalf("其余错误文字应保留: %s", out)
	}
}
