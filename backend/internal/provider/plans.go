package provider

import (
	"regexp"
	"strings"
)

const (
	// MaxIssueCount 是单次发码数量上限。
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

// Grok 档位键：裸键（plus_monthly / super_yearly …）或带 grok_ 前缀（grok_premium_monthly …）。
// 这里只用来按档位键推断产品线（handler 的 issueProduct），没有上游，也不改写发码时的档位键。
//
// 2026-10-19 改名对齐 xAI 现行四档 Plus / Premium / Super / Ultra：
// lite→plus($8)、monthly/yearly→premium($30)、plus→super($100)、heavy→ultra($200)。旧键 lite/heavy/monthly/yearly 仍认。
// ★裸键和 X 会员的键重名★（plus/premium/super/ultra_monthly|yearly）：没有产品信息时 X 优先（issueProduct 先判 X）。
// 带 grok_ 前缀时认全部四档；裸键不认 premium_*（归 X）。
// ★2026-10-24 Grok 专属半价档★：grok_<tier>_monthly_half，只在带 grok_ 前缀时认 _half 后缀，裸键不认。
var grokPlanRe = regexp.MustCompile(`^(?:grok_(?:(?:plus|premium|super|ultra|lite|heavy)_)?(?:monthly|yearly)(?:_half)?|(?:(?:plus|super|ultra|lite|heavy)_)?(?:monthly|yearly))$`)

func IsGrokPlan(plan string) bool {
	return grokPlanRe.MatchString(strings.ToLower(strings.TrimSpace(plan)))
}

// Claude 档位键：裸键（pro / max_5x / max_20x）或带 claude_ 前缀（claude_pro / claude_max_5x / claude_max_20x）。
// 这里只用来按档位键推断产品线（handler 的 issueProduct），没有上游，也不改写发码时的档位键。
// 三档裸键唯一，不与 GPT（plus/pro_20x…）、X、Grok 的键冲突。
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
