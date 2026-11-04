package payengine

import (
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// D-5：美元地区的注资金额不能低于预期金额（两者都是美分），否则每张单都会在付款时被拒。
func TestSavePlanTableUSDFundCoversExpected(t *testing.T) {
	dbtest.Open(t)
	mk := func(fund int64) error {
		tbl := DefaultPlanTable()
		tbl[1].Regions["US"] = RegionConf{Enabled: true, Currency: "USD", ExpectMinor: 2000, FloorMinor: 1500, CapMinor: 2500, FundCents: fund}
		return SavePlanTable(tbl)
	}
	if err := mk(1500); err == nil {
		t.Fatal("USD 注资 1500 分低于预期 2000 分，应拒绝保存")
	}
	if err := mk(2000); err != nil {
		t.Fatalf("注资等于预期应可保存: %v", err)
	}
	if err := mk(2500); err != nil {
		t.Fatalf("注资高于预期应可保存: %v", err)
	}
}
