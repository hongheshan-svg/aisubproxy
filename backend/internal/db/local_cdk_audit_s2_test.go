package db_test

import (
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// D-4：取到卡但注资之前就放弃（卡没有姓名、币种不对、读卡信息失败）不占「注资后失败」名额；
// 三次这样的失败之后码仍可用。真正注资后失败的仍然计次，三次用完转人工。
func TestS2PreFundCardFailureNotCounted(t *testing.T) {
	failed := func(code, card string) db.LocalCDKOrder {
		return db.LocalCDKOrder{State: "failed", Failure: "not_submitted", ErrorCode: code, CardID: card}
	}
	pre := []db.LocalCDKOrder{failed("card_info_failed", "c1"), failed("card_info_failed", "c1"), failed("card_currency_unsupported", "c2"), failed("card_info_failed", "c1")}
	v := db.DeriveLocalCDK(db.LocalCDK{Manual: db.LocalCDKManualActive}, pre)
	if v.Status != db.LocalCDKUnused || v.FundedFailures != 0 || v.PrechargeFailures != 4 {
		t.Fatalf("注资前放弃不该占名额: %+v", v)
	}
	// 注资后失败照旧计次
	funded := []db.LocalCDKOrder{failed("fund_failed", "c1"), failed("fill_failed", "c1"), failed("service_restart", "c1")}
	v = db.DeriveLocalCDK(db.LocalCDK{Manual: db.LocalCDKManualActive}, append(pre, funded...))
	if v.FundedFailures != 3 || v.Status == db.LocalCDKUnused {
		t.Fatalf("注资后失败要占满名额: %+v", v)
	}
}
