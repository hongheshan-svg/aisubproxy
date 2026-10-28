package cardpool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

func setTargets(t *testing.T, target, limit int, holders ...string) {
	t.Helper()
	savePolicy(t, func(p *Policy) {
		p.ProductTargets = map[string]ProductTarget{"p1": {Target: target, DailyLimit: limit}}
		p.HolderIDs = holders
	})
}

func TestReplenishFillsGap(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	setTargets(t, 3, 10, "h1")
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	insertOrder(t, Order{Kind: KindApply, ProductID: "p1", State: StateProcessing})
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("POST", pathApply, mwtest.OK(`{"transaction_id":"T1"}`))
	if err := s.replenishOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(mw.Requests("POST", pathApply)); n != 1 {
		t.Fatalf("POST count=%d", n)
	}
}

func TestReplenishDailyLimitBeijing(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	setTargets(t, 5, 2, "h1")
	clock.Set(time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC))
	for i := 0; i < 2; i++ {
		insertOrder(t, Order{Kind: KindApply, ProductID: "p1", State: StateSuccess})
	}
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("POST", pathApply, mwtest.OK(`{"transaction_id":"T1"}`))
	ctx := context.Background()
	if err := s.replenishOnce(ctx); err != nil {
		t.Fatal(err)
	}
	clock.Set(time.Date(2026, 10, 28, 15, 59, 0, 0, time.UTC))
	if err := s.replenishOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(mw.Requests("POST", pathApply)); n != 0 {
		t.Fatalf("应不开卡, got %d", n)
	}
	clock.Set(time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC))
	if err := s.replenishOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(mw.Requests("POST", pathApply)); n == 0 {
		t.Fatal("北京次日应开卡")
	}
}

func TestReplenishFailedCountsAndStops(t *testing.T) {
	s, mw, _, log := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	setTargets(t, 3, 10, "h1")
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("POST", pathApply, mwtest.Rejected(400, "PRODUCT_OFFLINE"))
	ctx := context.Background()
	if err := s.replenishOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(mw.Requests("POST", pathApply)); n != 1 {
		t.Fatalf("requests=%d", n)
	}
	if n := countTodayApply(t, s); n != 1 {
		t.Fatalf("today=%d", n)
	}
	if log.Count() != 1 {
		t.Fatalf("alerts=%d", log.Count())
	}
	if err := s.replenishOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countTodayApply(t, s); n != 2 {
		t.Fatalf("today=%d", n)
	}
	if log.Count() != 1 {
		t.Fatalf("同日应只告警一次, got %d", log.Count())
	}
}

func countTodayApply(t *testing.T, s *Service) int {
	t.Helper()
	n, err := s.todayApplyCount(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestReplenishHolderRoundRobinApprovedOnly(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	insertHolder(t, "h2", "REJECTED")
	insertHolder(t, "h3", "APPROVED")
	setTargets(t, 3, 10, "h1", "h2", "h3")
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("POST", pathApply, mwtest.OK(`{"transaction_id":"T1"}`))
	if err := s.replenishOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := mw.Requests("POST", pathApply)
	if len(reqs) != 3 {
		t.Fatalf("reqs=%d", len(reqs))
	}
	want := []string{"h1", "h3", "h1"}
	for i, r := range reqs {
		var m map[string]any
		if err := json.Unmarshal(r.Body, &m); err != nil {
			t.Fatal(err)
		}
		if m["card_holder_id"] != want[i] {
			t.Fatalf("#%d holder=%v want %s", i, m["card_holder_id"], want[i])
		}
	}
}

func TestReplenishNoHolderAlerts(t *testing.T) {
	s, mw, _, log := newTestService(t)
	insertHolder(t, "h2", "REJECTED")
	setTargets(t, 2, 10, "h2", "missing")
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("POST", pathApply, mwtest.OK(`{"transaction_id":"T1"}`))
	ctx := context.Background()
	_ = s.replenishOnce(ctx)
	_ = s.replenishOnce(ctx)
	if n := len(mw.Requests("POST", pathApply)); n != 0 {
		t.Fatalf("requests=%d", n)
	}
	if log.Count() != 1 {
		t.Fatalf("alerts=%d", log.Count())
	}
}

func TestReplenishProductMissingAlerts(t *testing.T) {
	s, mw, _, log := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	savePolicy(t, func(p *Policy) {
		p.ProductTargets = map[string]ProductTarget{"nope": {Target: 2, DailyLimit: 5}}
		p.HolderIDs = []string{"h1"}
	})
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	if err := s.replenishOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(mw.Requests("POST", pathApply)); n != 0 || log.Count() != 1 {
		t.Fatalf("requests=%d alerts=%d", n, log.Count())
	}
}

func TestReplenishOpenAmountUsesPolicy(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	setTargets(t, 1, 5, "h1")
	savePolicy(t, func(p *Policy) { p.OpenAmountCents = 100 })
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("POST", pathApply, mwtest.OK(`{"transaction_id":"T1"}`))
	if err := s.replenishOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := mw.Requests("POST", pathApply)
	if len(reqs) != 1 {
		t.Fatalf("reqs=%d", len(reqs))
	}
	var m map[string]any
	if err := json.Unmarshal(reqs[0].Body, &m); err != nil {
		t.Fatal(err)
	}
	if m["amount"] != "1.00" {
		t.Fatalf("amount=%v", m["amount"])
	}
}

func TestReplenishZeroDailyLimitDoesNothing(t *testing.T) {
	s, mw, _, log := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	setTargets(t, 2, 0, "h1")
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	ctx := context.Background()
	_ = s.replenishOnce(ctx)
	_ = s.replenishOnce(ctx)
	if n := len(mw.Requests("POST", pathApply)); n != 0 {
		t.Fatalf("requests=%d", n)
	}
	if log.Count() != 1 {
		t.Fatalf("alerts=%d", log.Count())
	}
}

// 缺口只算选卡能选中的可用卡（平台 ACTIVE、持卡人正常、有效期还剩一个月以上、未用满、没有退役原因），
// 加上清零中的卡和进行中的开卡单。
func TestReplenishGapCountsOnlySelectableCards(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	insertHolder(t, "h-sus", "SUSPENDED")
	setTargets(t, 5, 10, "h1")
	insertCard(t, Card{CardID: "ok", ProductID: "p1"})
	insertCard(t, Card{CardID: "releasing", ProductID: "p1", PoolState: PoolReleasing})
	insertOrder(t, Order{Kind: KindApply, ProductID: "p1", State: StateProcessing})
	// 以下可用卡选卡永远选不中，不计入
	insertCard(t, Card{CardID: "holder-suspended", ProductID: "p1", CardHolderID: "h-sus"})
	insertCard(t, Card{CardID: "revoked", ProductID: "p1", PlatformStatus: "REVOKED"})
	insertCard(t, Card{CardID: "used-up", ProductID: "p1", UseCount: 1})
	insertCard(t, Card{CardID: "retiring", ProductID: "p1", RetireReason: "健康度"})
	insertCard(t, Card{CardID: "expiring", ProductID: "p1", Expiry: "1026"})
	insertCard(t, Card{CardID: "malformed", ProductID: "p1", Expiry: "13/26"})
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("POST", pathApply, mwtest.OK(`{"transaction_id":"T1"}`))
	if err := s.replenishOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(mw.Requests("POST", pathApply)); n != 2 {
		t.Fatalf("缺口应为 5 - 2 张卡 - 1 张在途单 = 2, 实际开卡 %d", n)
	}
}

// 平台故障时结果未知的开卡单可能已经开出了卡（并扣费）：本轮停止该产品继续开卡并告警。
func TestReplenishStopsOnUnknown(t *testing.T) {
	s, mw, _, log := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	setTargets(t, 3, 10, "h1")
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("POST", pathApply, mwtest.Unknown())
	if err := s.replenishOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(mw.Requests("POST", pathApply)); n != 1 {
		t.Fatalf("结果未知后应停止, POST %d 次", n)
	}
	if alertsContaining(log, "p1") != 1 || alertsContaining(log, "unknown") != 1 {
		t.Fatalf("应告警一次: %v", log.Texts())
	}
}
