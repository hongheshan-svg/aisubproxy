package cardpool

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// 已知问题清单【涉及资金】：除了单日注资金额上限，再限每天最多注资多少笔（北京日），口径与金额上限一致。

func TestDailyFundCountCap(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, alerts := newTestService(t)
	mw.On("POST", pathRecharge, mwtest.OK(`{"transaction_id":"T1","status":"APPLY"}`))
	savePolicy(t, func(p *Policy) { p.DailyFundCountCap = 2 })
	fund := func(card string) error {
		_, err := s.submitTransfer(ctx, KindRecharge, card, 100, "USD", "alice")
		return err
	}
	clock.Set(time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC)) // 北京 10-29 00:00
	now := clock.Now().Unix()
	insertFundedOrder(t, KindRecharge, StateSuccess, 100, now-1, "old1") // 北京前一天，不算
	insertFundedOrder(t, KindRecharge, StateFailed, 100, now, "f1")      // 明确没动钱，不算
	insertFundedOrder(t, KindRecharge, StateNotSent, 100, now, "f2")     // 明确没动钱，不算
	insertFundedOrder(t, KindWithdraw, StateSuccess, 100, now, "w1")     // 转出，不算
	if err := fund("c1"); err != nil {
		t.Fatalf("第 1 笔应通过: %v", err)
	}
	insertFundedOrder(t, KindApply, StateUnknown, 100, now, "") // 结果不明的带金额开卡要算：钱可能已经转了
	if err := fund("c2"); !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), "2 笔") {
		t.Fatalf("今日已有 2 笔，第 3 笔应拒绝并写明笔数上限: %v", err)
	}
	if n := len(mw.Requests("POST", pathRecharge)); n != 1 {
		t.Fatalf("平台只应收到第 1 笔: %d", n)
	}
	if alerts.Count() != 1 || !strings.Contains(alerts.Texts()[0], "今日注资已达") || !strings.Contains(alerts.Texts()[0], "alice") {
		t.Fatalf("应告警并写明是笔数上限和操作方: %v", alerts.Texts())
	}
	left, err := s.RemainingDailyFundCount(ctx)
	if err != nil || left != 0 {
		t.Fatalf("今日剩余笔数应为 0: %d %v", left, err)
	}
	clock.Set(time.Date(2026, 10, 29, 16, 0, 0, 0, time.UTC)) // 北京 10-30 00:00
	if err := fund("c3"); err != nil {
		t.Fatalf("次日应重新计数: %v", err)
	}
	if left, err := s.RemainingDailyFundCount(ctx); err != nil || left != 1 {
		t.Fatalf("次日用了 1 笔，剩余应为 1: %d %v", left, err)
	}
}

// 不带金额的开卡单（默认策略开卡金额 0，自动补卡和手动开卡都是）不占单日注资笔数（C1-01）：
// 写单时的笔数判断和就绪检查的剩余笔数都只算带金额的。
func TestZeroAmountApplyDoesNotUseDailyFundCount(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, _ := newTestService(t)
	mw.On("POST", pathRecharge, mwtest.OK(`{"transaction_id":"T1","status":"APPLY"}`))
	savePolicy(t, func(p *Policy) { p.DailyFundCountCap = 2 })
	now := clock.Now().Unix()
	for i, st := range []string{StateSuccess, StateProcessing, StateUnknown, StateNeedsReview, StatePending} {
		insertFundedOrder(t, KindApply, st, 0, now, "z"+string(rune('0'+i))) // 0 元开卡：不算
	}
	if left, err := s.RemainingDailyFundCount(ctx); err != nil || left != 2 {
		t.Fatalf("0 元开卡不应占笔数，剩余应为 2: %d %v", left, err)
	}
	if _, err := s.submitTransfer(ctx, KindRecharge, "c1", 100, "USD", "alice"); err != nil {
		t.Fatalf("第 1 笔注资应通过: %v", err)
	}
	insertFundedOrder(t, KindApply, StateSuccess, 100, now, "") // 带金额的开卡照算
	if left, err := s.RemainingDailyFundCount(ctx); err != nil || left != 0 {
		t.Fatalf("1 笔注资 + 1 笔带金额开卡，剩余应为 0: %d %v", left, err)
	}
	if _, err := s.submitTransfer(ctx, KindRecharge, "c2", 100, "USD", "alice"); !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), "2 笔") {
		t.Fatalf("带金额的已满 2 笔，应按笔数上限拒绝: %v", err)
	}
}

// 金额没超、笔数超了，提示写笔数；笔数没超、金额超了，提示照旧写金额。
func TestDailyFundCapMessagesDistinguishCountAndAmount(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, _ := newTestService(t)
	mw.On("POST", pathRecharge, mwtest.OK(`{"transaction_id":"T1","status":"APPLY"}`))
	savePolicy(t, func(p *Policy) { p.DailyFundCountCap = 5; p.DailyFundCapCents = 1000 })
	insertFundedOrder(t, KindRecharge, StateSuccess, 1000, clock.Now().Unix(), "a1")
	_, err := s.submitTransfer(ctx, KindRecharge, "c1", 1, "USD", "alice")
	if err == nil || !strings.Contains(err.Error(), "今日注资累计") {
		t.Fatalf("金额超限应写金额: %v", err)
	}
}

func TestPolicyDailyFundCountCapDefaults(t *testing.T) {
	dbtest.Open(t)
	if p := DefaultPolicy(); p.DailyFundCountCap != DefaultDailyFundCountCap || DefaultDailyFundCountCap != 100 {
		t.Fatalf("默认应为 100 笔: %d", p.DailyFundCountCap)
	}
	for _, v := range []int64{0, -1} {
		p := DefaultPolicy()
		p.DailyFundCountCap = v
		if err := p.Validate(); !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), "daily_fund_count_cap") {
			t.Fatalf("%d 应被拒并写明字段: %v", v, err)
		}
	}
	// 旧版本存的策略没有这个字段：按默认值
	if err := db.SetSetting(PolicySettingKey, `{"max_uses_per_card":1}`); err != nil {
		t.Fatal(err)
	}
	if p, err := LoadPolicy(); err != nil || p.DailyFundCountCap != 100 {
		t.Fatalf("旧策略应补默认值: %d %v", p.DailyFundCountCap, err)
	}
	// 手改成 0 的存量数据：按默认值执行，绝不变成不限
	if err := db.SetSetting(PolicySettingKey, `{"max_uses_per_card":1,"daily_fund_count_cap":0}`); err != nil {
		t.Fatal(err)
	}
	if p, err := LoadPolicy(); err != nil || p.DailyFundCountCap != 100 {
		t.Fatalf("存量 0 应按默认值: %d %v", p.DailyFundCountCap, err)
	}
}
