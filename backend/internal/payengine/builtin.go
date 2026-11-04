package payengine

// 美国、菲律宾的内置价格区间（任务 11，设计 2026-11-03 §5.10）。
// 这两个地区的价格是固定的，预期金额、最低金额、上限由程序内置，管理员不用填，也可以改。金额都是最小单位，整数运算。
// 价格来源（2026-11-04）：
//   - US 网页价，不含销售税：Go $8、Plus $20、Pro 5x $100、Pro 20x $200、Pro 50x $500；Codex 点数每 250 点 $10，线性。
//   - PH 含 12% 增值税的标价（App Store 与 ZovoCard 网页数据）：Go ₱300、Plus ₱999（上线时网页价 ₱1,100）、Pro 5x ₱6,490、
//     Pro 20x ₱9,990、Pro 50x ₱32,490（免税价 ₱29,008.93）；Codex 点数每 250 点 ₱565。
//     PH 实际扣款含税还是免税取决于账单地址，所以区间同时覆盖两种。
//
// 区间规则（整数运算，向下取整；上限一律向上取整，见 capOf）：
//   - US：预期 = 最低 = 标价，上限 = 标价 × 110%。
//   - PH 订阅：预期 = 最新标价；最低 = 已知标价最低值 × 7/8（÷1.12 再 ×0.98）；上限 = 已知标价最高值 × 102%。
//   - PH 点数：预期 = ₱565 × 数量 / 250；最低 = 预期 × 7/8；上限 = 预期 × 112%。
const (
	builtinUSCapPercent = 110 // US 上限 = 标价 × 110%
	phCapPercent        = 102 // PH 订阅上限 = 高标价 × 102%
	creditPHCapPercent  = 112 // PH 点数上限 = 预期 × 112%

	creditUSPer250Minor = 1000  // US 点数每 250 点 $10
	creditPHPer250Minor = 56500 // PH 点数每 250 点 ₱565

	phFloorNum, phFloorDen = 7, 8 // PH 最低 = 低标价 × 7/8
)

// phSubPrice 是 PH 订阅的已知标价（比索分）：latest 作预期，low、high 决定区间。
type phSubPrice struct{ latest, low, high int64 }

var builtinUSSub = map[string]int64{"go": 800, "plus": 2000, "pro_5x": 10000, "pro_20x": 20000, "pro_50x": 50000}

var builtinPHSub = map[string]phSubPrice{
	"go":      {30000, 30000, 30000},
	"plus":    {99900, 99900, 110000},
	"pro_5x":  {649000, 649000, 649000},
	"pro_20x": {999000, 999000, 999000},
	"pro_50x": {3249000, 3249000, 3249000},
}

// BuiltinRegionPrice 返回套餐在地区的内置价格（币种、预期、最低、上限）；Enabled 为 false、FundCents 为 0。
// 只有 US、PH 有内置价；其他地区、未知套餐，或订阅套餐在内置价格表里没有这一项，都返回 false。
func BuiltinRegionPrice(planKey, region string) (RegionConf, bool) {
	q := CreditQuantity(planKey)
	isSub := contains(SubscriptionPlanKeys, planKey)
	if !isSub && q <= 0 {
		return RegionConf{}, false
	}
	switch region {
	case "US":
		list := creditUSPer250Minor * int64(q) / 250
		if isSub {
			var ok bool
			if list, ok = builtinUSSub[planKey]; !ok {
				return RegionConf{}, false
			}
		}
		return RegionConf{Currency: "USD", ExpectMinor: list, FloorMinor: list, CapMinor: capOf(list, builtinUSCapPercent)}, true
	case "PH":
		if !isSub {
			expect := creditPHPer250Minor * int64(q) / 250
			return RegionConf{Currency: "PHP", ExpectMinor: expect, FloorMinor: expect * phFloorNum / phFloorDen, CapMinor: capOf(expect, creditPHCapPercent)}, true
		}
		p, ok := builtinPHSub[planKey]
		if !ok {
			return RegionConf{}, false
		}
		return RegionConf{
			Currency: "PHP", ExpectMinor: p.latest,
			FloorMinor: p.low * phFloorNum / phFloorDen,
			CapMinor:   capOf(p.high, phCapPercent),
		}, true
	}
	return RegionConf{}, false
}

// fillBuiltin 把已存配置里为 0 的预期、最低、上限用内置值补上，非 0 的值（管理员改过的）一律不动。
// 币种与内置币种不同（且不为空）时不补，因为金额单位不同。补完的结果仍能通过 SavePlanTable 的校验：
//   - 内置上限放不下管理员的非 0 预期或最低时，上限不补；
//   - 内置预期会大于非 0 上限、或小于非 0 最低时，预期不补，留 0；
//   - 最低只按上限拉低：补出来的最低高于非 0 的上限时，降到上限。
//   - 管理员的非 0 预期低于内置最低时，最低不补、留 0，真实付款由 ResolvePlan 以「未配置最低金额」拒绝。
//     预期多半是填错了（比如 pro_5x 填成 Plus 的 $20）：按它拉低最低，会让便宜档的页面过得了下限。
func fillBuiltin(planKey, region string, rc RegionConf) RegionConf {
	b, ok := BuiltinRegionPrice(planKey, region)
	if !ok || (rc.Currency != "" && rc.Currency != b.Currency) {
		return rc
	}
	if rc.CapMinor == 0 && rc.ExpectMinor <= b.CapMinor && rc.FloorMinor <= b.CapMinor {
		rc.CapMinor = b.CapMinor
	}
	if rc.ExpectMinor == 0 && (rc.CapMinor == 0 || b.ExpectMinor <= rc.CapMinor) && b.ExpectMinor >= rc.FloorMinor {
		rc.ExpectMinor = b.ExpectMinor
	}
	if rc.FloorMinor == 0 && (rc.ExpectMinor == 0 || rc.ExpectMinor >= b.FloorMinor) {
		// 预期为 0 或不低于内置最低：最低 ≤ 预期自然成立，只需不高于上限
		rc.FloorMinor = b.FloorMinor
		if rc.CapMinor > 0 && rc.FloorMinor > rc.CapMinor {
			rc.FloorMinor = rc.CapMinor
		}
	}
	return rc
}

// BuiltinPrices 返回全部套餐的内置价格（套餐键 → 地区 → 价格），只含有内置价的地区，供管理页「恢复内置价格」使用。
func BuiltinPrices() map[string]map[string]RegionConf {
	out := make(map[string]map[string]RegionConf, len(PlanKeys))
	for _, key := range PlanKeys {
		regions := map[string]RegionConf{}
		for _, r := range Regions {
			if b, ok := BuiltinRegionPrice(key, r); ok {
				regions[r] = b
			}
		}
		out[key] = regions
	}
	return out
}

// capOf 是上限 = x × pct%，向上取整（设计 §5.10：金额向下取整，上限一律向上取整）。
func capOf(x, pct int64) int64 { return (x*pct + 99) / 100 }
