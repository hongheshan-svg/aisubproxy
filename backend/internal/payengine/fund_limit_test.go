package payengine

import (
	"errors"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func TestSavePlanTableRejectsFundOverLimit(t *testing.T) {
	dbtest.Open(t)
	build := func(fund int64) []PlanConf {
		tbl := DefaultPlanTable()
		tbl[1].Regions["PH"] = RegionConf{Enabled: true, Currency: "PHP", CapMinor: 1234, FundCents: fund}
		return tbl
	}
	if err := SavePlanTable(build(cardpool.DefaultMaxFundCents + 1)); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("超单笔注资上限应拒绝: %v", err)
	}
	if err := SavePlanTable(build(cardpool.DefaultMaxFundCents)); err != nil {
		t.Fatalf("等于上限应通过: %v", err)
	}
	p := cardpool.DefaultPolicy()
	p.MaxFundCents = 100
	if err := cardpool.SavePolicy(p); err != nil {
		t.Fatal(err)
	}
	if err := SavePlanTable(build(101)); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("应按后台改过的上限校验: %v", err)
	}
	if err := db.SetSetting(cardpool.PolicySettingKey, `{broken`); err != nil {
		t.Fatal(err)
	}
	if err := SavePlanTable(build(1)); err == nil {
		t.Fatal("策略读不出来时应拒绝保存")
	}
}
