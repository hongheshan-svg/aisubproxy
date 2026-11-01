package payengine

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func TestDefaultPlanTable(t *testing.T) {
	tbl := DefaultPlanTable()
	wantKeys := []string{"go", "plus", "pro_5x", "pro_20x", "pro_50x"}
	if len(tbl) != 5 {
		t.Fatalf("套餐数 %d", len(tbl))
	}
	cur := map[string]string{"PH": "PHP", "US": "USD", "JP": "JPY", "CL": "CLP", "EG": "EGP"}
	names := map[string]string{"go": "", "plus": "chatgptplusplan", "pro_5x": "chatgptprolite", "pro_20x": "chatgptpro", "pro_50x": "chatgptpromax"}
	expect := map[string]string{"go": "", "plus": "plus", "pro_5x": "", "pro_20x": "pro", "pro_50x": "promax"}
	for i, p := range tbl {
		if p.Key != wantKeys[i] {
			t.Fatalf("顺序错误: %s", p.Key)
		}
		if p.ChatGPTPlanName != names[p.Key] || p.ExpectPlanType != expect[p.Key] {
			t.Fatalf("%s 套餐名/期望类型错误: %+v", p.Key, p)
		}
		if len(p.Regions) != 5 {
			t.Fatalf("%s 地区数 %d", p.Key, len(p.Regions))
		}
		for r, c := range cur {
			rc := p.Regions[r]
			if rc.Currency != c || rc.Enabled || rc.CapMinor != 0 || rc.FundCents != 0 {
				t.Fatalf("%s/%s 默认值错误: %+v", p.Key, r, rc)
			}
		}
	}
}

func saveWith(t *testing.T, key, region string, rc RegionConf) {
	t.Helper()
	tbl := DefaultPlanTable()
	for i := range tbl {
		if tbl[i].Key == key {
			tbl[i].Regions[region] = rc
		}
	}
	if err := SavePlanTable(tbl); err != nil {
		t.Fatal(err)
	}
}

func TestResolvePlanRules(t *testing.T) {
	dbtest.Open(t)
	if _, _, err := ResolvePlan("plus", "PH", false); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatalf("未启用应不可用: %v", err)
	}
	saveWith(t, "plus", "PH", RegionConf{Enabled: true, Currency: "PHP", FundCents: 100})
	_, _, err := ResolvePlan("plus", "PH", false)
	if !errors.Is(err, ErrPlanUnavailable) || !strings.Contains(err.Error(), "金额上限") {
		t.Fatalf("缺金额上限: %v", err)
	}
	saveWith(t, "plus", "PH", RegionConf{Enabled: true, Currency: "PHP", CapMinor: 5000, FundCents: 100})
	p, rc, err := ResolvePlan("plus", "PH", false)
	if err != nil || p.Key != "plus" || rc.CapMinor != 5000 {
		t.Fatalf("应可用: %v %+v %+v", err, p, rc)
	}
	saveWith(t, "go", "PH", RegionConf{Enabled: true, Currency: "PHP", CapMinor: 5000, FundCents: 100})
	if _, _, err := ResolvePlan("go", "PH", false); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatalf("go 缺期望类型应不可用: %v", err)
	}
	if _, _, err := ResolvePlan("go", "PH", true); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatalf("go 试运行缺套餐名应不可用: %v", err)
	}
	if _, _, err := ResolvePlan("pro_5x", "PH", false); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatalf("pro_5x 未启用真实付款应不可用: %v", err)
	}
	if _, _, err := ResolvePlan("pro_5x", "PH", true); err != nil {
		t.Fatalf("pro_5x 试运行应可用: %v", err)
	}
	if _, _, err := ResolvePlan("nope", "PH", true); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatalf("未知套餐: %v", err)
	}
	if _, _, err := ResolvePlan("plus", "XX", true); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatalf("未知地区: %v", err)
	}
}

func TestLoadPlanTableFillsMissing(t *testing.T) {
	dbtest.Open(t)
	raw := `[{"key":"plus","label":"X","chatgpt_plan_name":"chatgptplusplan","expect_plan_type":"plus","regions":{"PH":{"enabled":true,"currency":"PHP","cap_minor":900,"fund_cents":50}}},{"key":"bogus","regions":{}}]`
	if err := db.SetSetting("pay_plan_table", raw); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadPlanTable()
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl) != 5 || tbl[1].Key != "plus" {
		t.Fatalf("应补齐 5 个套餐: %+v", tbl)
	}
	if rc := tbl[1].Regions["PH"]; !rc.Enabled || rc.CapMinor != 900 || rc.FundCents != 50 {
		t.Fatalf("plus/PH 应保留: %+v", rc)
	}
	if rc := tbl[1].Regions["US"]; rc.Currency != "USD" || rc.Enabled {
		t.Fatalf("缺的地区应补默认: %+v", rc)
	}
	if len(tbl[0].Regions) != 5 {
		t.Fatal("go 应补齐地区")
	}
}

func TestSavePlanTableValidates(t *testing.T) {
	dbtest.Open(t)
	mut := func(f func([]PlanConf)) error {
		tbl := DefaultPlanTable()
		f(tbl)
		return SavePlanTable(tbl)
	}
	cases := map[string]func([]PlanConf){
		"未知套餐键": func(p []PlanConf) { p[0].Key = "zzz" },
		"未知地区":  func(p []PlanConf) { p[1].Regions["XX"] = RegionConf{Currency: "USD"} },
		"负金额":   func(p []PlanConf) { rc := p[1].Regions["PH"]; rc.CapMinor = -1; p[1].Regions["PH"] = rc },
		"负注资":   func(p []PlanConf) { rc := p[1].Regions["PH"]; rc.FundCents = -1; p[1].Regions["PH"] = rc },
		"币种非法":  func(p []PlanConf) { rc := p[1].Regions["PH"]; rc.Currency = "php"; p[1].Regions["PH"] = rc },
	}
	for name, f := range cases {
		if err := mut(f); err == nil {
			t.Fatalf("%s 应报错", name)
		}
	}
	tbl := DefaultPlanTable()
	tbl[1].Regions["PH"] = RegionConf{Enabled: true, Currency: "PHP", CapMinor: 1234, FundCents: 99}
	if err := SavePlanTable(tbl); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPlanTable()
	if err != nil || !reflect.DeepEqual(got, tbl) {
		t.Fatalf("读回不一致: %v\n%+v\n%+v", err, got, tbl)
	}
}

func TestConcurrencyRange(t *testing.T) {
	dbtest.Open(t)
	if LoadConcurrency() != 1 {
		t.Fatal("默认应为 1")
	}
	if SaveConcurrency(0) == nil || SaveConcurrency(4) == nil {
		t.Fatal("越界应报错")
	}
	if err := SaveConcurrency(2); err != nil || LoadConcurrency() != 2 {
		t.Fatalf("存 2 失败: %v", err)
	}
	_ = db.SetSetting("pay_concurrency", "99")
	if LoadConcurrency() != 1 {
		t.Fatal("非法值应读出 1")
	}
	_ = db.SetSetting("pay_concurrency", "abc")
	if LoadConcurrency() != 1 {
		t.Fatal("非数字应读出 1")
	}
}

func TestProxiesWriteOnly(t *testing.T) {
	dbtest.Open(t)
	if err := SaveProxies(map[string]string{"PH": "socks5://u:p@h:1080"}); err == nil {
		t.Fatal("无主密钥应报错")
	}
	dbtest.SetMasterKey(t)
	if err := SaveProxies(map[string]string{"PH": "socks5://u:p@h:1080"}); err != nil {
		t.Fatal(err)
	}
	st, err := ProxyStatus()
	if err != nil || !st["PH"] || st["US"] || len(st) != 5 {
		t.Fatalf("状态错误: %v %v", err, st)
	}
	px, _ := LoadProxies()
	if px["PH"] != "socks5://u:p@h:1080" {
		t.Fatalf("原值错误: %v", px)
	}
	if err := SaveProxies(map[string]string{"PH": ""}); err != nil {
		t.Fatal(err)
	}
	if px, _ = LoadProxies(); px["PH"] == "" {
		t.Fatal("空串应保留原值")
	}
	raw, _ := db.GetSetting("pay_region_proxies")
	if raw == "" || strings.Contains(raw, "socks5") {
		t.Fatalf("库里应是密文: %q", raw)
	}
	err = SaveProxies(map[string]string{"PH": "ftp://secret:pw@x"})
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "pw") {
		t.Fatalf("协议非法应报错且不含原文: %v", err)
	}
	if err := SaveProxies(map[string]string{"XX": "http://a:1"}); err == nil {
		t.Fatal("未知地区应报错")
	}
	if err := SaveProxies(map[string]string{"PH": "-"}); err != nil {
		t.Fatal(err)
	}
	if st, _ = ProxyStatus(); st["PH"] {
		t.Fatal("应已清除")
	}
}
