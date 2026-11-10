package handler

import (
	"testing"
)

func TestIssueProductKeepsGPTDefault(t *testing.T) {
	cases := map[[2]string]string{
		{"", "plus"}: "gpt", {"", "pro_20x_renew"}: "gpt",
		// 2026-10-24 XPASS 改版后 X 档位为 plus/premium/super/ultra，basic_monthly 已退役，不再归 X
		{"", "basic_monthly"}: "gpt", {"", "plus_monthly"}: "x",
		{"", "heavy_yearly"}: "grok", {"gpt", "plus"}: "gpt", {"bogus", "plus"}: "gpt",
		// 裸 premium_* 同时是 X 和 Grok 的键：没有 product 时归 X；带 grok_ 前缀才是 Grok
		{"", "premium_monthly"}: "x", {"", "grok_premium_monthly"}: "grok",
		// super_monthly 同理：X 与 Grok 同名，没有 product 时归 X（X 优先判定）
		{"", "super_monthly"}:       "x",
		{"grok", "premium_monthly"}: "grok",
	}
	for in, want := range cases {
		if got := issueProduct(in[0], in[1]); got != want {
			t.Fatalf("issueProduct(%q,%q)=%s want %s", in[0], in[1], got, want)
		}
	}
}
