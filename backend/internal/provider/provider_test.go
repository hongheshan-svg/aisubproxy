package provider

import (
	"regexp"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func TestDefaultProviderLoadSave(t *testing.T) {
	dbtest.Open(t)
	if n, err := LoadDefault(); err != nil || n != ZovoCard {
		t.Fatalf("初始值应是 zovocard: %v %v", n, err)
	}
	if err := SaveDefault(MarsWallex); err != nil {
		t.Fatal(err)
	}
	if n, err := LoadDefault(); err != nil || n != MarsWallex {
		t.Fatal(n, err)
	}
	// 库里的非法值按 ZovoCard，不能变成别的
	if err := db.SetSetting(SettingDefault, "bogus"); err != nil {
		t.Fatal(err)
	}
	if n, err := LoadDefault(); err != nil || n != ZovoCard {
		t.Fatal(n, err)
	}
	if err := SaveDefault(Name("bogus")); err == nil {
		t.Fatal("非法值不能保存")
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Name{"zovocard": ZovoCard, " MarsWallex ": MarsWallex} {
		if n, err := Parse(in); err != nil || n != want {
			t.Errorf("%q: %v %v", in, n, err)
		}
	}
	for _, in := range []string{"", "mars", "zovo"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("%q 应被拒绝", in)
		}
	}
}

func TestSupports(t *testing.T) {
	cases := []struct {
		n             Name
		product, plan string
		want          bool
	}{
		{MarsWallex, "gpt", "plus", true},
		{MarsWallex, "GPT", "pro_20x", true},
		{MarsWallex, "gpt", "credit250", false},
		{MarsWallex, "gpt", "pro_20x_renew", false},
		{MarsWallex, "x", "premium_monthly", false},
		{MarsWallex, "grok", "plus", false}, // 键同名也不行
		{MarsWallex, "claude", "pro", false},
		{MarsWallex, "", "plus", false},
		{ZovoCard, "gpt", "credit250", true},
		{ZovoCard, "x", "premium_monthly", true},
		{ZovoCard, "grok", "plus_monthly", true},
		{ZovoCard, "claude", "pro", true},
		{ZovoCard, "bogus", "plus", false},
		{Name("bogus"), "gpt", "plus", false},
	}
	for _, tc := range cases {
		if got := Supports(tc.n, tc.product, tc.plan); got != tc.want {
			t.Errorf("Supports(%s,%s,%s)=%v", tc.n, tc.product, tc.plan, got)
		}
	}
}

func TestCapabilitiesFromSupports(t *testing.T) {
	rows := Capabilities()
	byKey := map[string]Capability{}
	for _, r := range rows {
		byKey[r.Product+"/"+r.Plan] = r
	}
	if r := byKey["gpt/plus"]; !r.Support[MarsWallex] || !r.Support[ZovoCard] {
		t.Fatalf("%+v", r)
	}
	if r := byKey["gpt/pro_20x"]; r.Notes[MarsWallex] != "仅限无订阅账号" || r.Notes[ZovoCard] != "" {
		t.Fatalf("pro_20x 要标注: %+v", r)
	}
	for _, k := range []string{"gpt/credit*", "x/*", "grok/*", "claude/*"} {
		if r := byKey[k]; r.Support[MarsWallex] || !r.Support[ZovoCard] {
			t.Fatalf("%s: %+v", k, r)
		}
	}
}

func TestResolveOnlyLooksAtTheCode(t *testing.T) {
	for _, code := range []string{"MW-AAAAA-BBBBB", "mw-aaaaa", "  MW-AAAAA  ", "\xe2\x80\x8bMW-AAAAA\xef\xbb\xbf", "\xe3\x80\x80mw-a"} {
		if ResolveCode(code) != MarsWallex {
			t.Errorf("%q 应归 MarsWallex", code)
		}
	}
	for _, code := range []string{"ZC-AAAA", "SXC-AAAA", "", "MWX-AAA", "M-W-AAA", "XMW-AAA"} {
		if ResolveCode(code) != ZovoCard {
			t.Errorf("%q 应归 ZovoCard", code)
		}
	}
	for _, tok := range []string{"mwt_abc", "MWT_ABC", "  mwt_abc "} {
		if ResolveToken(tok) != MarsWallex {
			t.Errorf("%q 应归 MarsWallex", tok)
		}
	}
	for _, tok := range []string{"tok-1", "", "mw_abc"} {
		if ResolveToken(tok) != ZovoCard {
			t.Errorf("%q 应归 ZovoCard", tok)
		}
	}
}

func TestGenerateLocalCode(t *testing.T) {
	re := regexp.MustCompile(`^MW-[A-HJ-NP-Z2-9]{5}(-[A-HJ-NP-Z2-9]{5}){4}$`)
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		c, err := GenerateLocalCode()
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(c.Code) {
			t.Fatalf("格式不对: %s", c.Code)
		}
		if seen[c.Code] {
			t.Fatalf("重复: %s", c.Code)
		}
		seen[c.Code] = true
		if !strings.HasPrefix(c.Code, c.Prefix) || len(c.Prefix) != 8 {
			t.Fatalf("前缀: %s", c.Prefix)
		}
		if c.CodeHash != db.HashCDKCode(strings.ToLower(c.Code)) {
			t.Fatal("哈希要和大小写无关")
		}
		if ResolveToken(c.Token) != MarsWallex || HashToken(c.Token) != c.TokenHash {
			t.Fatalf("令牌: %+v", c)
		}
		if TokenForCode(" "+strings.ToLower(c.Code)+" ") != c.Token {
			t.Fatal("同一张码的令牌要稳定")
		}
		if strings.Contains(c.Token, c.Code[3:8]) || c.Token == c.CodeHash || c.TokenHash == c.CodeHash {
			t.Fatal("令牌不能带码的内容，也不能和入库哈希相同")
		}
	}
}

func TestReadinessCollectsProblems(t *testing.T) {
	r := NewReadiness(MarsWallex)
	r.Pass("a", "好")
	if !r.OK || len(r.Problems) != 0 {
		t.Fatalf("%+v", r)
	}
	r.Fail("b", "坏")
	if r.OK || len(r.Problems) != 1 || r.Problems[0] != "坏" || len(r.Checks) != 2 || r.Checks[1].OK {
		t.Fatalf("%+v", r)
	}
}
