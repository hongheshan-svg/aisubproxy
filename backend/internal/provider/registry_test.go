package provider_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/provider/providertest"
)

func mustPanic(t *testing.T, what string, p provider.Provider) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s 应 panic", what)
		}
	}()
	provider.Register(p)
}

// 注册校验：重名、前缀重叠、两家兜底、名称不合法、类型和接口不符，都要让启动失败。
func TestRegisterRejectsBadProviders(t *testing.T) {
	providertest.RegisterBoth(t)
	card := providertest.NewTestCard()
	up := providertest.NewTestUp()

	dup := card
	mustPanic(t, "重名", dup)
	dupUp := up
	dupUp.N = provider.ZovoCard
	mustPanic(t, "和内置重名", dupUp)

	for _, tc := range []struct{ what, code, tok string }{
		{"码前缀和 MW- 相同", "MW-", "abc_"},
		{"码前缀是别家的前缀", "MW-X", "abc_"},
		{"码前缀以别家为前缀", "T", "abc_"},
		{"令牌前缀和 tct_ 相同", "AB-", "tct_"},
		{"令牌前缀是别家的前缀", "AB-", "mwt"},
		{"令牌前缀和码前缀重叠", "AB-", "ab-x"},
		{"自己的两个前缀重叠", "AB-", "ab-"},
		{"码前缀为空", "", "abc_"},
		{"令牌前缀为空", "AB-", ""},
		{"码前缀小写", "ab-", "abc_"},
		{"令牌前缀大写", "AB-", "ABC_"},
	} {
		c := providertest.Card{N: "other_card", Display: "Other", Code: tc.code, Token: tc.tok, Prods: []string{"gpt"}}
		mustPanic(t, tc.what, c)
	}

	fb := providertest.Up{N: "second_fallback", Display: "Second", Prods: []string{"gpt"}, IsFallback: true}
	mustPanic(t, "两家兜底", fb)

	for _, bad := range []provider.Name{"Bad", "bad-name", "", "坏"} {
		mustPanic(t, "名称 "+string(bad), providertest.Up{N: bad, Display: "X", Prods: []string{"gpt"}})
	}
	mustPanic(t, "未知产品线", providertest.Up{N: "unknown_prod", Display: "X", Prods: []string{"tiktok"}})
	mustPanic(t, "没有显示名", providertest.Up{N: "no_display", Prods: []string{"gpt"}})
	mustPanic(t, "nil", nil)

	// 合法的第三家能注册，且失败的注册没有留下半截
	provider.Register(providertest.Card{N: "third_card", Display: "Third", Code: "TH-", Token: "tht_", Prods: []string{"gpt"}, Plans: []string{"plus"}})
	names := provider.Names()
	want := []provider.Name{provider.ZovoCard, provider.MarsWallex, providertest.TestCard, providertest.TestUp, "third_card"}
	if len(names) != len(want) {
		t.Fatalf("%v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("按注册顺序: %v", names)
		}
	}
}

// 测试注册的假服务商在测试结束后恢复，不影响别的测试。
func TestRegisterRestored(t *testing.T) {
	t.Run("注册", func(t *testing.T) { providertest.RegisterBoth(t) })
	if _, ok := provider.Get(providertest.TestCard); ok {
		t.Fatal("应已恢复")
	}
	if len(provider.Names()) != 2 {
		t.Fatal(provider.Names())
	}
}

// 码与令牌归属：供卡型前缀优先，再看非兜底上游认领，最后兜底 ZovoCard。
func TestForCodeAndToken(t *testing.T) {
	providertest.RegisterBoth(t)
	for code, want := range map[string]provider.Name{
		"TC-AAAAA-BBBBB": providertest.TestCard,
		" tc-aaaaa ":     providertest.TestCard,
		"MW-AAAAA":       provider.MarsWallex,
		"TU-12345":       providertest.TestUp,
		" tu-12345 ":     providertest.TestUp,
		"ZC-AAAA":        provider.ZovoCard,
		"TCX-AAAA":       provider.ZovoCard,
		"":               provider.ZovoCard,
	} {
		p, err := provider.ForCode(code)
		if err != nil || p.Name() != want {
			t.Errorf("%q 应归 %s: %v %v", code, want, p, err)
		}
	}
	for tok, want := range map[string]provider.Name{
		"tct_abc":  providertest.TestCard,
		"TCT_ABC":  providertest.TestCard,
		"mwt_abc":  provider.MarsWallex,
		"tut_abc":  providertest.TestUp,
		"zc-tok-1": provider.ZovoCard,
	} {
		p, err := provider.ForToken(tok)
		if err != nil || p.Name() != want {
			t.Errorf("%q 应归 %s: %v %v", tok, want, p, err)
		}
	}
	if cs, ok := provider.CardSupplyForCode("TU-1"); ok {
		t.Fatalf("上游码不是供卡型: %v", cs)
	}
	if cs, ok := provider.CardSupplyForToken("tct_x"); !ok || cs.Name() != providertest.TestCard {
		t.Fatal(cs, ok)
	}
}

// 没有兜底上游时，认不出的码返回错误，绝不随便交给某一家。
func TestForCodeWithoutFallback(t *testing.T) {
	t.Cleanup(provider.TestOnlySnapshot())
	// 只留供卡型和非兜底上游：先清空再注册
	provider.TestOnlyReset()
	provider.Register(providertest.NewTestCard())
	provider.Register(providertest.NewTestUp())
	if _, err := provider.ForCode("ZC-AAAA"); !errors.Is(err, provider.ErrUnrecognized) {
		t.Fatal(err)
	}
	if _, err := provider.ForToken("zc-tok"); !errors.Is(err, provider.ErrUnrecognized) {
		t.Fatal(err)
	}
	if p, err := provider.ForCode("TU-1"); err != nil || p.Name() != providertest.TestUp {
		t.Fatal(p, err)
	}
}

func TestConfigResolution(t *testing.T) {
	dbtest.Open(t)
	providertest.RegisterBoth(t)
	set := func(k, v string) {
		t.Helper()
		if err := db.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}

	// 什么都没保存过：出厂配置是 GPT 用 MarsWallex，它不做的 X、Grok、Claude 单独选兜底的 ZovoCard
	cfg, err := provider.LoadConfig()
	if err != nil || cfg.Default != provider.MarsWallex || len(cfg.Warnings) != 0 {
		t.Fatalf("%+v %v", cfg, err)
	}
	if by := cfg.ByProduct; len(by) != 3 || by["x"] != provider.ZovoCard || by["grok"] != provider.ZovoCard || by["claude"] != provider.ZovoCard {
		t.Fatalf("%v", by)
	}
	if p, err := provider.ResolveIssue("gpt", "plus"); err != nil || p.Name() != provider.MarsWallex {
		t.Fatal(p, err)
	}
	if p, err := provider.ResolveIssue("grok", "premium_monthly"); err != nil || p.Name() != provider.ZovoCard {
		t.Fatal(p, err)
	}

	// 只有旧设置项（没有产品配置）的库能正常工作；出厂的按产品配置不再补上
	set(provider.SettingDefault, "marswallex")
	cfg, err = provider.LoadConfig()
	if err != nil || cfg.Default != provider.MarsWallex || len(cfg.ByProduct) != 0 || len(cfg.Warnings) != 0 {
		t.Fatalf("%+v %v", cfg, err)
	}
	if p, err := provider.ResolveIssue("gpt", "plus"); err != nil || p.Name() != provider.MarsWallex {
		t.Fatal(p, err)
	}

	// 产品配置优先，没配的产品用全局默认
	set(provider.SettingDefault, "zovocard")
	set(provider.SettingByProduct, `{"gpt":"testcard","x":"testup","grok":""}`)
	if p, err := provider.ResolveIssue("GPT", "plus"); err != nil || p.Name() != providertest.TestCard {
		t.Fatal(p, err)
	}
	if p, err := provider.ResolveIssue("x", "premium_monthly"); err != nil || p.Name() != providertest.TestUp {
		t.Fatal(p, err)
	}
	if p, err := provider.ResolveIssue("grok", "premium_monthly"); err != nil || p.Name() != provider.ZovoCard {
		t.Fatal(p, err)
	}
	cfg, _ = provider.LoadConfig()
	eff := cfg.Effective()
	if eff["gpt"] != providertest.TestCard || eff["x"] != providertest.TestUp || eff["grok"] != provider.ZovoCard || eff["claude"] != provider.ZovoCard {
		t.Fatalf("%v", eff)
	}

	// 不支持的档位返回 ErrUnsupported，绝不换成别家
	_, err = provider.ResolveIssue("gpt", "pro_5x")
	var ue *provider.UnsupportedError
	if !errors.Is(err, provider.ErrUnsupported) || !errors.As(err, &ue) || ue.Provider.Name() != providertest.TestCard {
		t.Fatal(err)
	}
	if msg := err.Error(); msg != "TestCard 不支持 ChatGPT pro_5x，请在发码时手动选择服务商，或到「卡服务商」页调整" {
		t.Fatal(msg)
	}
	tc, _ := provider.Get(providertest.TestCard)
	if err := provider.CheckSupports(tc, "gpt", "pro_5x"); !errors.Is(err, provider.ErrUnsupported) || strings.Contains(err.Error(), "卡服务商") {
		t.Fatal(err)
	}

	// 库里存的服务商名已经没有注册：全局默认按兜底，产品配置按跟随全局默认，都给 warning
	set(provider.SettingDefault, "gone_provider")
	set(provider.SettingByProduct, `{"gpt":"gone_too","x":"marswallex","tiktok":"zovocard"}`)
	cfg, err = provider.LoadConfig()
	if err != nil || cfg.Default != provider.ZovoCard || len(cfg.ByProduct) != 0 {
		t.Fatalf("%+v %v", cfg, err)
	}
	joined := strings.Join(cfg.Warnings, "\n")
	for _, want := range []string{"gone_provider", "gone_too", "MarsWallex 不做这条产品线", "tiktok"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warning 缺 %s: %v", want, cfg.Warnings)
		}
	}
	if p, err := provider.ResolveIssue("gpt", "plus"); err != nil || p.Name() != provider.ZovoCard {
		t.Fatal(p, err)
	}
	// 解析不了的 JSON 按全部跟随默认，并提示
	set(provider.SettingByProduct, `{not json`)
	if cfg, err := provider.LoadConfig(); err != nil || len(cfg.ByProduct) != 0 || !strings.Contains(strings.Join(cfg.Warnings, ""), "无法解析") {
		t.Fatalf("%+v %v", cfg, err)
	}

	// 读库出错时报错，不能当成任何一家继续发码
	if _, err := db.DB.Exec(`DROP TABLE site_settings`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.LoadConfig(); err == nil {
		t.Fatal("读库出错应报错")
	}
	if p, err := provider.ResolveIssue("gpt", "plus"); err == nil {
		t.Fatalf("读库出错不能选出服务商: %v", p)
	}
	if _, err := provider.Selected(context.Background(), provider.MarsWallex); err == nil {
		t.Fatal("读库出错应报错")
	}
}

func TestValidateAndSaveConfig(t *testing.T) {
	dbtest.Open(t)
	providertest.RegisterBoth(t)
	for _, tc := range []struct {
		def  string
		by   map[string]string
		want string
	}{
		{"", nil, "全局默认"},
		{"gone", nil, "未注册"},
		{"zovocard", map[string]string{"tiktok": "zovocard"}, "未知产品"},
		{"zovocard", map[string]string{"gpt": "gone"}, "未注册"},
		{"zovocard", map[string]string{"x": "marswallex"}, "MarsWallex 不做 X Premium"},
		{"zovocard", map[string]string{"grok": "testup"}, "TestUp 不做 Grok"},
	} {
		_, _, err := provider.ValidateConfig(tc.def, tc.by)
		if !errors.Is(err, provider.ErrInvalidConfig) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: %v", tc, err)
		}
	}
	def, by, err := provider.ValidateConfig(" ZovoCard ", map[string]string{"GPT": "TestCard", "x": "", "claude": "zovocard"})
	if err != nil || def != provider.ZovoCard || len(by) != 2 || by["gpt"] != providertest.TestCard || by["claude"] != provider.ZovoCard {
		t.Fatalf("%v %v %v", def, by, err)
	}
	if err := provider.SaveConfig(def, by); err != nil {
		t.Fatal(err)
	}
	raw, _ := db.GetSetting(provider.SettingByProduct)
	if raw != `{"claude":"zovocard","gpt":"testcard"}` {
		t.Fatal(raw)
	}
	cfg, err := provider.LoadConfig()
	if err != nil || cfg.For("gpt") != providertest.TestCard || cfg.For("x") != provider.ZovoCard {
		t.Fatalf("%+v %v", cfg, err)
	}
}

// Selected：全局默认是它、任一产品配置是它，或它还有没结清的本地码。
func TestSelected(t *testing.T) {
	dbtest.Open(t)
	providertest.RegisterBoth(t)
	ctx := context.Background()
	check := func(n provider.Name, want bool) {
		t.Helper()
		got, err := provider.Selected(ctx, n)
		if err != nil || got != want {
			t.Fatalf("%s: %v %v，期望 %v", n, got, err, want)
		}
	}
	check(provider.MarsWallex, true) // 没设置过：出厂配置 GPT 用 MarsWallex
	check(provider.ZovoCard, true)   // X、Grok、Claude 用 ZovoCard
	check(providertest.TestCard, false)
	if err := provider.SaveConfig(provider.ZovoCard, map[string]provider.Name{"gpt": providertest.TestCard}); err != nil {
		t.Fatal(err)
	}
	check(providertest.TestCard, true)
	check(provider.MarsWallex, false)
	if ok, err := provider.MarsWallexSelected(ctx); err != nil || ok {
		t.Fatal(ok, err)
	}
	if err := provider.SaveConfig(provider.MarsWallex, nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := provider.MarsWallexSelected(ctx); err != nil || !ok {
		t.Fatal(ok, err)
	}
	check(providertest.TestCard, false)
}

// 能力矩阵列出全部已注册服务商，support 取自各自的 Supports。
func TestCapabilitiesIncludeRegistered(t *testing.T) {
	providertest.RegisterBoth(t)
	for _, r := range provider.Capabilities() {
		if len(r.Support) != 4 {
			t.Fatalf("%+v", r)
		}
		switch r.Product + "/" + r.Plan {
		case "gpt/plus":
			if !r.Support[providertest.TestCard] || !r.Support[providertest.TestUp] || r.Notes[providertest.TestCard] != "测试提示" {
				t.Fatalf("%+v", r)
			}
		case "gpt/pro_5x":
			if r.Support[providertest.TestCard] || !r.Support[providertest.TestUp] {
				t.Fatalf("%+v", r)
			}
		case "x/*":
			if r.Support[providertest.TestCard] || !r.Support[providertest.TestUp] {
				t.Fatalf("%+v", r)
			}
		case "grok/*":
			if r.Support[providertest.TestUp] {
				t.Fatalf("%+v", r)
			}
		}
	}
	tc, _ := provider.Get(providertest.TestCard)
	tu, _ := provider.Get(providertest.TestUp)
	if provider.CodeList(tc) != "local" || provider.CodeList(tu) != "" {
		t.Fatal(provider.CodeList(tc), provider.CodeList(tu))
	}
}
