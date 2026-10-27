package cardpool

import (
	"errors"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func TestDefaultPolicyValid(t *testing.T) {
	p := DefaultPolicy()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.PlanProducts == nil || p.ProductTargets == nil || p.HolderIDs == nil {
		t.Fatal("map/slice 不应为 nil")
	}
	if p.OpenAmountCents != 0 || p.ValidityMonths != 24 || p.CardNamePrefix != "POOL" || p.MaxUsesPerCard != 1 ||
		p.LeaseMinutes != 30 || p.CancelAfterDays != 7 || p.HealthWindowHours != 24 ||
		p.ProductFailThreshold != 3 || p.CardFailThreshold != 2 || !p.AutoSuspend {
		t.Fatalf("默认值不符: %+v", p)
	}
}

func TestPolicyValidateRejects(t *testing.T) {
	cases := map[string]func(*Policy){
		"前缀过长":   func(p *Policy) { p.CardNamePrefix = "TOO-LONG-PREFIX" },
		"有效期1":   func(p *Policy) { p.ValidityMonths = 1 },
		"有效期48":  func(p *Policy) { p.ValidityMonths = 48 },
		"使用次数0":  func(p *Policy) { p.MaxUsesPerCard = 0 },
		"开卡金额负":  func(p *Policy) { p.OpenAmountCents = -1 },
		"注销天数负":  func(p *Policy) { p.CancelAfterDays = -1 },
		"目标数501": func(p *Policy) { p.ProductTargets["p1"] = ProductTarget{Target: 501} },
		"上限501":  func(p *Policy) { p.ProductTargets["p1"] = ProductTarget{DailyLimit: 501} },
		"租约4":    func(p *Policy) { p.LeaseMinutes = 4 },
		"窗口721":  func(p *Policy) { p.HealthWindowHours = 721 },
		"阈值0":    func(p *Policy) { p.ProductFailThreshold = 0 },
		"卡阈值101": func(p *Policy) { p.CardFailThreshold = 101 },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			p := DefaultPolicy()
			mut(&p)
			if err := p.Validate(); !errors.Is(err, ErrBadRequest) {
				t.Fatalf("应返回 ErrBadRequest, got %v", err)
			}
		})
	}
}

func TestLoadPolicyFillsDefaults(t *testing.T) {
	dbtest.Open(t)
	p, err := LoadPolicy()
	if err != nil || p.LeaseMinutes != 30 {
		t.Fatalf("未设置应为默认: %+v %v", p, err)
	}
	if err := db.SetSetting(PolicySettingKey, `{"max_uses_per_card":3}`); err != nil {
		t.Fatal(err)
	}
	p, err = LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if p.MaxUsesPerCard != 3 || p.LeaseMinutes != 30 || !p.AutoSuspend {
		t.Fatalf("got %+v", p)
	}
}

func TestSavePolicyRejectsInvalidAndKeepsOld(t *testing.T) {
	dbtest.Open(t)
	p := DefaultPolicy()
	p.MaxUsesPerCard = 5
	if err := SavePolicy(p); err != nil {
		t.Fatal(err)
	}
	bad := DefaultPolicy()
	bad.LeaseMinutes = 1
	if err := SavePolicy(bad); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("got %v", err)
	}
	got, err := LoadPolicy()
	if err != nil || got.MaxUsesPerCard != 5 || got.LeaseMinutes != 30 {
		t.Fatalf("旧策略应保留: %+v %v", got, err)
	}
}
