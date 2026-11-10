package provider

import (
	"regexp"
	"strings"
)

const (
	// MaxIssueCount 与卡台 GPTDirectCDKBatchMax 对齐。
	MaxIssueCount = 200
)

// 2026-10-24 X 会员并入 XPASS:四档 plus/premium/super/ultra(替换旧 basic/premium/premium_plus)。
func IsXPremiumPlan(plan string) bool {
	plan = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(plan)), "x_")
	switch plan {
	case "plus_monthly", "plus_yearly", "premium_monthly", "premium_yearly",
		"super_monthly", "super_yearly", "ultra_monthly", "ultra_yearly":
		return true
	}
	return false
}

// Grok 档位：卡台注册表里是裸键（premium_monthly / plus_yearly …），ACC 定价表里带 grok_ 前缀。
//
// 2026-10-19 卡台改名对齐 xAI 现行四档 Plus / Premium / Super / Ultra：
// lite→plus($8)、monthly/yearly→premium($30)、plus→super($100)、heavy→ultra($200)。
// ★裸 premium_monthly 同时是 X Premium 的键★：没有产品信息时 X 优先（issueProduct 先判 X），
// Grok 发码一律带 grok_ 前缀（GrokIssuePlan）。旧键 lite/heavy/monthly/yearly 仍认，按新键处理。
// 带 grok_ 前缀时认全部四档；裸键不认 premium_*（归 X）。
// ★2026-10-24 Grok 专属半价档★：grok_<tier>_monthly_half(严格半价档,发码给顾客用)。
// 只在带 grok_ 前缀时认 _half 后缀;裸键不认(半价一律带前缀发码)。
var grokPlanRe = regexp.MustCompile(`^(?:grok_(?:(?:plus|premium|super|ultra|lite|heavy)_)?(?:monthly|yearly)(?:_half)?|(?:(?:plus|super|ultra|lite|heavy)_)?(?:monthly|yearly))$`)

func IsGrokPlan(plan string) bool {
	return grokPlanRe.MatchString(strings.ToLower(strings.TrimSpace(plan)))
}

// Claude 档位：卡台注册表里是裸键（pro / max_5x / max_20x），ACC 定价表里带 claude_ 前缀
// （claude_pro / claude_max_5x / claude_max_20x）。本站界面与码上统一用裸键，发码一律带
// claude_ 前缀（ClaudeIssuePlan），让卡台直接定位到 Claude，不靠「遍历各产品档位表」反查。
//
// Claude 直冲为恒价 USD（claudeProduct.UsesFlatFundingPrice），兑换一律按 US/USD 收单，
// 与 Grok 同构。三档裸键唯一，不与 GPT（plus/pro_20x…）、X、Grok 的键冲突。
var claudeBarePlans = map[string]bool{
	"pro":     true,
	"max_5x":  true,
	"max_20x": true,
}

// IsClaudePlan 识别 Claude 档位：带 claude_ 前缀，或裸键命中三档之一。
func IsClaudePlan(plan string) bool {
	p := strings.ToLower(strings.TrimSpace(plan))
	if strings.HasPrefix(p, "claude_") {
		return claudeBarePlans[strings.TrimPrefix(p, "claude_")]
	}
	return claudeBarePlans[p]
}
