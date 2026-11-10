package cardplatform

import "strings"

// grokLegacyPlans 改名前的旧裸键 → 新裸键。★不含 plus_*★：它现在就是新键（$8 档）。
var grokLegacyPlans = map[string]string{
	"lite_monthly": "plus_monthly", "lite_yearly": "plus_yearly",
	"monthly": "premium_monthly", "yearly": "premium_yearly",
	"heavy_monthly": "ultra_monthly", "heavy_yearly": "ultra_yearly",
}

// GrokBarePlan 去掉 grok_ 前缀的裸档位（本站界面与码上都用裸键，与 X 一致），旧键换成新键。
func GrokBarePlan(plan string) string {
	p := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(plan)), "grok_")
	if n, ok := grokLegacyPlans[p]; ok {
		return n
	}
	return p
}

// GrokIssuePlan 发给卡台的发码档位：显式带 grok_ 前缀。
// 卡台也认裸键，但裸键要靠「遍历各产品档位表」反查产品，将来哪个产品也有 monthly 就会认错；
// 带前缀则直接定位到 Grok。卡台落库时会去掉前缀。
func GrokIssuePlan(plan string) string {
	return "grok_" + GrokBarePlan(plan)
}
