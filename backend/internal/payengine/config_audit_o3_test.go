package payengine

import (
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// 审计 O3 组 money N-3：预期金额留空、走内置价时，注资金额也要按实际生效的预期金额校验。

func o3SaveUS(t *testing.T, key string, rc RegionConf) error {
	t.Helper()
	tbl := DefaultPlanTable()
	for i := range tbl {
		if tbl[i].Key == key {
			tbl[i].Regions["US"] = rc
			return SavePlanTable(tbl)
		}
	}
	t.Fatalf("没有套餐 %s", key)
	return nil
}

func TestO3SavePlanTableUSDFundChecksBuiltinExpected(t *testing.T) {
	dbtest.Open(t)
	b, ok := BuiltinRegionPrice("plus", "US")
	if !ok || b.ExpectMinor != 2000 {
		t.Fatalf("前提：plus/US 内置预期应为 2000 分，实际 %+v ok=%v", b, ok)
	}
	// 预期金额留空（读取时补内置 2000 分），注资 1500 分：付款时一定不够，应拒绝保存
	if err := o3SaveUS(t, "plus", RegionConf{Enabled: true, Currency: "USD", FundCents: 1500}); err == nil {
		t.Fatal("预期留空走内置 2000 分时，注资 1500 分应拒绝保存")
	}
	// 注资不低于内置预期：可以保存
	if err := o3SaveUS(t, "plus", RegionConf{Enabled: true, Currency: "USD", FundCents: 2000}); err != nil {
		t.Fatalf("注资等于内置预期应可保存: %v", err)
	}
	// 显式填了预期的，仍按填的值校验（原 D-5 行为不变）
	if err := o3SaveUS(t, "plus", RegionConf{Enabled: true, Currency: "USD", ExpectMinor: 1200, CapMinor: 2500, FundCents: 1500}); err != nil {
		t.Fatalf("显式预期 1200 分、注资 1500 分应可保存: %v", err)
	}
	// 上限填得比内置预期低时内置预期不补（fillBuiltin 的规则），这时没有生效的预期金额，不按内置价拦
	if err := o3SaveUS(t, "plus", RegionConf{Enabled: true, Currency: "USD", CapMinor: 1800, FundCents: 1500}); err != nil {
		t.Fatalf("内置预期补不上时不应按内置价拦: %v", err)
	}
	// 注资为 0（未配置）不在这里拦，由付款时的 ResolvePlan 拒绝
	if err := o3SaveUS(t, "plus", RegionConf{Currency: "USD"}); err != nil {
		t.Fatalf("注资为 0 不应在保存时拦: %v", err)
	}
}
