package payengine

import (
	"errors"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// 内置价格表的期望值（任务 11 说明里的验收表）：预期 / 最低 / 上限，单位是最小单位。
var builtinWant = map[string]map[string][3]int64{
	"go":          {"US": {800, 800, 880}, "PH": {30000, 26250, 30600}},
	"plus":        {"US": {2000, 2000, 2200}, "PH": {99900, 87412, 112200}},
	"pro_5x":      {"US": {10000, 10000, 11000}, "PH": {649000, 567875, 661980}},
	"pro_20x":     {"US": {20000, 20000, 22000}, "PH": {999000, 874125, 1018980}},
	"pro_50x":     {"US": {50000, 50000, 55000}, "PH": {3249000, 2842875, 3313980}},
	"credit250":   {"US": {1000, 1000, 1100}, "PH": {56500, 49437, 63280}},
	"credit25000": {"US": {100000, 100000, 110000}, "PH": {5650000, 4943750, 6328000}},
	// 其余点数档按同一规则线性计算
	"credit500":  {"US": {2000, 2000, 2200}, "PH": {113000, 98875, 126560}},
	"credit1000": {"US": {4000, 4000, 4400}, "PH": {226000, 197750, 253120}},
	"credit2500": {"US": {10000, 10000, 11000}, "PH": {565000, 494375, 632800}},
	"credit5000": {"US": {20000, 20000, 22000}, "PH": {1130000, 988750, 1265600}},
}

func TestBuiltinRegionPrice(t *testing.T) {
	cur := map[string]string{"US": "USD", "PH": "PHP"}
	for key, regions := range builtinWant {
		for r, w := range regions {
			rc, ok := BuiltinRegionPrice(key, r)
			if !ok {
				t.Fatalf("%s/%s 应有内置价", key, r)
			}
			if rc.Currency != cur[r] || rc.ExpectMinor != w[0] || rc.FloorMinor != w[1] || rc.CapMinor != w[2] {
				t.Fatalf("%s/%s 内置价错误: %+v 期望 %v", key, r, rc, w)
			}
			if rc.Enabled || rc.FundCents != 0 {
				t.Fatalf("%s/%s 内置价不应启用或带注资: %+v", key, r, rc)
			}
		}
	}
	for _, key := range PlanKeys {
		if _, ok := builtinWant[key]; !ok {
			t.Fatalf("验收表漏了 %s", key)
		}
		for _, r := range []string{"JP", "CL", "EG", "XX", ""} {
			if _, ok := BuiltinRegionPrice(key, r); ok {
				t.Fatalf("%s/%s 不应有内置价", key, r)
			}
		}
	}
	for _, key := range []string{"", "bogus", "credit0", "credit"} {
		if _, ok := BuiltinRegionPrice(key, "US"); ok {
			t.Fatalf("未知套餐 %q 不应有内置价", key)
		}
		if _, ok := BuiltinRegionPrice(key, "PH"); ok {
			t.Fatalf("未知套餐 %q 不应有内置价", key)
		}
	}
}

func TestDefaultPlanTableUsesBuiltin(t *testing.T) {
	for _, p := range DefaultPlanTable() {
		for _, r := range Regions {
			rc := p.Regions[r]
			b, ok := BuiltinRegionPrice(p.Key, r)
			if !ok {
				if rc.ExpectMinor != 0 || rc.FloorMinor != 0 || rc.CapMinor != 0 || rc.Currency != defaultCurrency[r] {
					t.Fatalf("%s/%s 没有内置价，出厂只应有币种: %+v", p.Key, r, rc)
				}
				continue
			}
			if rc != b || rc.Enabled || rc.FundCents != 0 {
				t.Fatalf("%s/%s 出厂值应等于内置价: %+v 内置 %+v", p.Key, r, rc, b)
			}
		}
	}
}

func rawRegion(key, region, body string) string {
	return `[{"key":"` + key + `","regions":{"` + region + `":` + body + `}}]`
}

func TestLoadPlanTableFillsZeroAmounts(t *testing.T) {
	dbtest.Open(t)
	raw := `[` +
		`{"key":"plus","regions":{"PH":{"enabled":true,"currency":"PHP","expect_minor":0,"floor_minor":0,"cap_minor":0,"fund_cents":2500},"US":{"enabled":true,"currency":"USD","expect_minor":0,"cap_minor":1500,"floor_minor":0}}},` +
		`{"key":"pro_5x","regions":{"US":{"enabled":false,"currency":"USD","expect_minor":10000,"cap_minor":10500,"floor_minor":0}}},` +
		`{"key":"credit250","regions":{"PH":{"currency":"USD"}}}` +
		`]`
	if err := db.SetSetting("pay_plan_table", raw); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadPlanTable()
	if err != nil {
		t.Fatal(err)
	}
	get := func(key, region string) RegionConf {
		for _, p := range tbl {
			if p.Key == key {
				return p.Regions[region]
			}
		}
		t.Fatalf("没有套餐 %s", key)
		return RegionConf{}
	}
	// 全是 0：补成内置值，启用开关和注资保留
	if rc := get("plus", "PH"); rc != (RegionConf{Enabled: true, Currency: "PHP", ExpectMinor: 99900, FloorMinor: 87412, CapMinor: 112200, FundCents: 2500}) {
		t.Fatalf("plus/PH: %+v", rc)
	}
	// 管理员填了预期和上限：只补最低金额
	if rc := get("pro_5x", "US"); rc != (RegionConf{Currency: "USD", ExpectMinor: 10000, FloorMinor: 10000, CapMinor: 10500}) {
		t.Fatalf("pro_5x/US: %+v", rc)
	}
	// 预期补 2000 会大于管理员的上限 1500，不补；最低降到上限
	if rc := get("plus", "US"); rc != (RegionConf{Enabled: true, Currency: "USD", ExpectMinor: 0, FloorMinor: 1500, CapMinor: 1500}) {
		t.Fatalf("plus/US: %+v", rc)
	}
	// 币种被管理员改了：不补
	if rc := get("credit250", "PH"); rc.Currency != "USD" || rc.ExpectMinor != 0 || rc.FloorMinor != 0 || rc.CapMinor != 0 {
		t.Fatalf("credit250/PH 币种不同不应补: %+v", rc)
	}
	// 补完的配置必须能原样保存
	if err := SavePlanTable(tbl); err != nil {
		t.Fatalf("补完的配置应能保存: %v", err)
	}
	// 其他地区不受影响
	if rc := get("plus", "JP"); rc.FloorMinor != 0 || rc.CapMinor != 0 {
		t.Fatalf("plus/JP 不应有内置价: %+v", rc)
	}
}

// 管理员已填非 0 的最低金额或预期金额时，补出来的值不能让配置通不过保存校验。
func TestLoadPlanTableFillKeepsSaveable(t *testing.T) {
	dbtest.Open(t)
	cases := []struct{ name, body string }{
		{"最低金额高于内置预期", `{"currency":"USD","floor_minor":2500}`},
		{"预期金额高于内置上限", `{"currency":"USD","expect_minor":9000}`},
		{"只填上限且低于内置最低", `{"currency":"USD","cap_minor":500}`},
		{"币种为空", `{"currency":""}`},
	}
	for _, c := range cases {
		if err := db.SetSetting("pay_plan_table", rawRegion("plus", "US", c.body)); err != nil {
			t.Fatal(err)
		}
		tbl, err := LoadPlanTable()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range tbl {
			if p.Key == "plus" && c.name == "最低金额高于内置预期" {
				if rc := p.Regions["US"]; rc.FloorMinor != 2500 {
					t.Fatalf("%s: 管理员的最低金额不能动: %+v", c.name, rc)
				}
			}
		}
		if c.name == "币种为空" {
			continue // 空币种本来就过不了保存，这里只确认不 panic
		}
		if err := SavePlanTable(tbl); err != nil {
			t.Fatalf("%s: 补完应能保存: %v", c.name, err)
		}
	}
}

func TestResolvePlanWorksWithBuiltin(t *testing.T) {
	dbtest.Open(t)
	raw := `[{"key":"plus","label":"Plus","chatgpt_plan_name":"chatgptplusplan","expect_plan_type":"plus","regions":{"PH":{"enabled":true,"currency":"PHP","fund_cents":2500}}}]`
	if err := db.SetSetting("pay_plan_table", raw); err != nil {
		t.Fatal(err)
	}
	_, rc, err := ResolvePlan("plus", "PH", false)
	if err != nil {
		t.Fatalf("US/PH 不填金额也应能下单: %v", err)
	}
	if rc.FloorMinor != 87412 || rc.CapMinor != 112200 || rc.ExpectMinor != 99900 {
		t.Fatalf("应为内置值: %+v", rc)
	}
	// JP 没有内置价，仍要手填
	raw = `[{"key":"plus","label":"Plus","chatgpt_plan_name":"chatgptplusplan","expect_plan_type":"plus","regions":{"JP":{"enabled":true,"currency":"JPY","fund_cents":2500}}}]`
	if err := db.SetSetting("pay_plan_table", raw); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolvePlan("plus", "JP", false); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatalf("JP 未填最低金额应拒绝: %v", err)
	}
}
