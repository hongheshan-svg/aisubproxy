package payengine

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func TestDefaultPlanTable(t *testing.T) {
	tbl := DefaultPlanTable()
	wantKeys := []string{"go", "plus", "pro_5x", "pro_20x", "pro_50x",
		"credit250", "credit500", "credit1000", "credit2500", "credit5000", "credit25000"}
	if len(tbl) != len(wantKeys) {
		t.Fatalf("套餐数 %d", len(tbl))
	}
	cur := map[string]string{"PH": "PHP", "US": "USD", "JP": "JPY", "CL": "CLP", "EG": "EGP"}
	names := map[string]string{"go": "", "plus": "chatgptplusplan", "pro_5x": "chatgptprolite", "pro_20x": "chatgptpro", "pro_50x": "chatgptpromax"}
	expect := map[string]string{"go": "", "plus": "plus", "pro_5x": "", "pro_20x": "pro", "pro_50x": "promax"}
	for i, p := range tbl {
		if p.Key != wantKeys[i] {
			t.Fatalf("顺序错误: %s", p.Key)
		}
		if i >= len(SubscriptionPlanKeys) {
			continue // 点数档见 TestDefaultCreditPlans
		}
		if p.Kind != KindSubscription || p.CreditQuantity != 0 {
			t.Fatalf("%s 种类错误: %+v", p.Key, p)
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
	saveWith(t, "plus", "PH", RegionConf{Enabled: true, Currency: "PHP", FloorMinor: 100, FundCents: 100})
	_, _, err := ResolvePlan("plus", "PH", false)
	if !errors.Is(err, ErrPlanUnavailable) || !strings.Contains(err.Error(), "金额上限") {
		t.Fatalf("缺金额上限: %v", err)
	}
	saveWith(t, "plus", "PH", RegionConf{Enabled: true, Currency: "PHP", CapMinor: 5000, FloorMinor: 100, FundCents: 100})
	p, rc, err := ResolvePlan("plus", "PH", false)
	if err != nil || p.Key != "plus" || rc.CapMinor != 5000 {
		t.Fatalf("应可用: %v %+v %+v", err, p, rc)
	}
	saveWith(t, "go", "PH", RegionConf{Enabled: true, Currency: "PHP", CapMinor: 5000, FloorMinor: 100, FundCents: 100})
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
	if len(tbl) != len(PlanKeys) || tbl[1].Key != "plus" {
		t.Fatalf("应补齐 %d 个套餐: %+v", len(PlanKeys), tbl)
	}
	if c := tbl[len(SubscriptionPlanKeys)]; c.Key != "credit250" || c.Kind != KindCredit || c.Regions["PH"].CapMinor != 63280 {
		t.Fatalf("旧配置里没有点数档，应补默认: %+v", c)
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
	tbl[1].Regions["PH"] = RegionConf{Enabled: true, Currency: "PHP", CapMinor: 1234, FloorMinor: 1000, FundCents: 99}
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
	if err := SaveProxies(map[string]string{"PH": "http://u:p@h:8080"}); err == nil {
		t.Fatal("无主密钥应报错")
	}
	dbtest.SetMasterKey(t)
	if err := SaveProxies(map[string]string{"PH": "http://u:p@h:8080"}); err != nil {
		t.Fatal(err)
	}
	st, err := ProxyStatus()
	if err != nil || !st["PH"] || st["US"] || len(st) != 5 {
		t.Fatalf("状态错误: %v %v", err, st)
	}
	px, _ := LoadProxies()
	if px["PH"] != "http://u:p@h:8080" {
		t.Fatalf("原值错误: %v", px)
	}
	if err := SaveProxies(map[string]string{"PH": ""}); err != nil {
		t.Fatal(err)
	}
	if px, _ = LoadProxies(); px["PH"] == "" {
		t.Fatal("空串应保留原值")
	}
	raw, _ := db.GetSetting("pay_region_proxies")
	if raw == "" || strings.Contains(raw, "u:p@h") {
		t.Fatalf("库里应是密文: %q", raw)
	}
	err = SaveProxies(map[string]string{"PH": "ftp://secret:pw@x"})
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "pw") {
		t.Fatalf("协议非法应报错且不含原文: %v", err)
	}
	if err := SaveProxies(map[string]string{"PH": "socks5h://h:1080"}); err == nil {
		t.Fatal("socks5h 应报错")
	}
	err = SaveProxies(map[string]string{"US": "socks5://user:pw@h:1080"})
	if err == nil || !strings.Contains(err.Error(), "socks5 代理不支持账号密码，请改用 http 代理") ||
		strings.Contains(err.Error(), "user") || strings.Contains(err.Error(), "pw") {
		t.Fatalf("带账号密码的 socks5 应报错且不含原文: %v", err)
	}
	if err := SaveProxies(map[string]string{"US": "socks5://h:1080"}); err != nil {
		t.Fatalf("不带账号密码的 socks5 应可保存: %v", err)
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

// M-7：两名管理员同时保存不同地区的代理，后写的不能覆盖先写的，否则那个地区会无声地变成直连。
func TestSaveProxiesConcurrent(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	for round := 0; round < 20; round++ {
		if err := SaveProxies(map[string]string{"PH": "-", "US": "-", "JP": "-", "CL": "-", "EG": "-"}); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, len(Regions))
		for i, r := range Regions {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = SaveProxies(map[string]string{r: "http://proxy-" + strings.ToLower(r) + ".example:8080"})
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("第 %d 轮保存 %s 失败: %v", round, Regions[i], err)
			}
		}
		got, err := LoadProxies()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(Regions) {
			t.Fatalf("第 %d 轮并发保存后只剩 %d 个地区，其余被覆盖: %v", round, len(got), got)
		}
		for _, r := range Regions {
			if want := "http://proxy-" + strings.ToLower(r) + ".example:8080"; got[r] != want {
				t.Fatalf("第 %d 轮地区 %s = %q，期望 %q", round, r, got[r], want)
			}
		}
	}
}

// 点数六档：种类、点数取自键名；PH 默认预期金额 ₱565 × 数量 / 250、上限为预期 × 1.12，其他地区没有默认值；全部默认未启用、注资为 0。
func TestDefaultCreditPlans(t *testing.T) {
	want := map[string][3]int64{ // 点数、PH 预期金额、PH 上限（分）
		"credit250": {250, 56500, 63280}, "credit500": {500, 113000, 126560}, "credit1000": {1000, 226000, 253120},
		"credit2500": {2500, 565000, 632800}, "credit5000": {5000, 1130000, 1265600}, "credit25000": {25000, 5650000, 6328000},
	}
	n := 0
	for _, p := range DefaultPlanTable() {
		w, ok := want[p.Key]
		if !ok {
			continue
		}
		n++
		if p.Kind != KindCredit || int64(p.CreditQuantity) != w[0] || p.ChatGPTPlanName != "" || p.ExpectPlanType != "" {
			t.Fatalf("%s 默认值错误: %+v", p.Key, p)
		}
		if rc := p.Regions["PH"]; rc.Enabled || rc.Currency != "PHP" || rc.ExpectMinor != w[1] || rc.CapMinor != w[2] || rc.FundCents != 0 {
			t.Fatalf("%s/PH 默认值错误: %+v", p.Key, rc)
		}
		for _, r := range []string{"US", "JP", "CL", "EG"} {
			if rc := p.Regions[r]; rc.Enabled || rc.CapMinor != 0 || rc.ExpectMinor != 0 || rc.Currency != defaultCurrency[r] {
				t.Fatalf("%s/%s 不应有默认金额: %+v", p.Key, r, rc)
			}
		}
		if PlanKind(p.Key) != KindCredit || CreditQuantity(p.Key) != int(w[0]) {
			t.Fatalf("%s 种类或点数取值错误", p.Key)
		}
	}
	if n != len(CreditPlanKeys) {
		t.Fatalf("点数档 %d 个", n)
	}
	if CreditQuantity("plus") != 0 || CreditQuantity("credit") != 0 || CreditQuantity("credit999") != 0 || PlanKind("plus") != KindSubscription {
		t.Fatal("非点数档的取值错误")
	}
	if got := SupportedPlanKeys(); len(got) != 11 || got[0] != "go" || got[10] != "credit25000" {
		t.Fatalf("SupportedPlanKeys: %v", got)
	}
}

// 点数档不需要 ChatGPT 套餐名和期望套餐类型；真实付款照样要求启用、上限和注资。
func TestResolveCreditPlan(t *testing.T) {
	dbtest.Open(t)
	if _, _, err := ResolvePlan("credit250", "PH", true); err != nil {
		t.Fatalf("点数档试运行应可用: %v", err)
	}
	if _, _, err := ResolvePlan("credit250", "PH", false); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatalf("未启用应不可用: %v", err)
	}
	saveWith(t, "credit250", "PH", RegionConf{Enabled: true, Currency: "PHP", ExpectMinor: 56500, CapMinor: 63280, FloorMinor: 56500})
	if _, _, err := ResolvePlan("credit250", "PH", false); !errors.Is(err, ErrPlanUnavailable) || !strings.Contains(err.Error(), "注资") {
		t.Fatalf("缺注资应不可用: %v", err)
	}
	saveWith(t, "credit250", "PH", RegionConf{Enabled: true, Currency: "PHP", ExpectMinor: 56500, CapMinor: 63280, FloorMinor: 56500, FundCents: 1500})
	p, rc, err := ResolvePlan("credit250", "PH", false)
	if err != nil || p.Kind != KindCredit || p.CreditQuantity != 250 || rc.CapMinor != 63280 {
		t.Fatalf("应可用: %v %+v %+v", err, p, rc)
	}
}

// 保存时种类、点数以键为准，点数档清掉套餐名；上限不能低于预期金额。
func TestSavePlanTableCredit(t *testing.T) {
	dbtest.Open(t)
	tbl := DefaultPlanTable()
	i := len(SubscriptionPlanKeys)
	tbl[i].Kind, tbl[i].CreditQuantity, tbl[i].ChatGPTPlanName = KindSubscription, 99999, "chatgptpro"
	tbl[0].Kind = KindCredit
	if err := SavePlanTable(tbl); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPlanTable()
	if err != nil {
		t.Fatal(err)
	}
	if got[i].Kind != KindCredit || got[i].CreditQuantity != 250 || got[i].ChatGPTPlanName != "" || got[0].Kind != KindSubscription {
		t.Fatalf("应以键为准: %+v %+v", got[0], got[i])
	}
	bad := DefaultPlanTable()
	bad[i].Regions["PH"] = RegionConf{Currency: "PHP", ExpectMinor: 56500, CapMinor: 50000}
	if err := SavePlanTable(bad); !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), "预期金额") {
		t.Fatalf("上限低于预期金额应拒绝: %v", err)
	}
	bad = DefaultPlanTable()
	bad[i].Regions["PH"] = RegionConf{Currency: "PHP", ExpectMinor: -1}
	if err := SavePlanTable(bad); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("负的预期金额应拒绝: %v", err)
	}
}

func TestResolvePlanRequiresFloor(t *testing.T) {
	dbtest.Open(t)
	saveWith(t, "plus", "PH", RegionConf{Enabled: true, Currency: "PHP", CapMinor: 12000, FundCents: 3000})
	if _, _, err := ResolvePlan("plus", "PH", false); !errors.Is(err, ErrPlanUnavailable) || !strings.Contains(err.Error(), "未配置最低金额") {
		t.Fatalf("缺最低金额应不可用: %v", err)
	}
	if _, _, err := ResolvePlan("plus", "PH", true); err != nil {
		t.Fatalf("试运行不要求最低金额: %v", err)
	}
}

func TestSavePlanTableValidatesFloor(t *testing.T) {
	dbtest.Open(t)
	build := func(rc RegionConf) []PlanConf {
		tbl := DefaultPlanTable()
		tbl[1].Regions["PH"] = rc
		return tbl
	}
	base := RegionConf{Enabled: true, Currency: "PHP", FundCents: 3000}
	cases := []struct {
		name            string
		expect, cp, fl  int64
		wantErr, wantIn string
	}{
		{"负数", 0, 0, -1, "bad", ""},
		{"高于上限", 0, 12000, 12001, "bad", "最低金额"},
		{"高于预期金额", 10000, 12000, 10001, "bad", "最低金额"},
		{"等于预期金额", 10000, 12000, 10000, "", ""},
	}
	for _, c := range cases {
		rc := base
		rc.ExpectMinor, rc.CapMinor, rc.FloorMinor = c.expect, c.cp, c.fl
		err := SavePlanTable(build(rc))
		if c.wantErr == "" {
			if err != nil {
				t.Fatalf("%s 应通过: %v", c.name, err)
			}
			continue
		}
		if !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), c.wantIn) {
			t.Fatalf("%s 应拒绝（含 %q）: %v", c.name, c.wantIn, err)
		}
	}
}

func TestDefaultFloor(t *testing.T) {
	for _, p := range DefaultPlanTable() {
		for r, rc := range p.Regions {
			switch {
			case p.Kind == KindCredit && r == "PH":
				if rc.FloorMinor != rc.ExpectMinor || rc.FloorMinor <= 0 {
					t.Fatalf("%s/PH 出厂最低金额应等于预期金额: %+v", p.Key, rc)
				}
			case rc.FloorMinor != 0:
				t.Fatalf("%s/%s 出厂最低金额应为 0: %+v", p.Key, r, rc)
			}
		}
	}
}
