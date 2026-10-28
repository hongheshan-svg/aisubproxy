package cardpool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

func detailPath(id string) string { return "/v1/card/detail/" + id }

func detailJSON(id, avail, frozen, status string) string {
	return `{"card_id":"` + id + `","card_no":"4111********1234","card_currency":"USD","expiry_date":"1028","status":"` + status +
		`","available_balance":"` + avail + `","freeze_balance":"` + frozen + `"}`
}

func mustAdvance(t *testing.T, s *Service) {
	t.Helper()
	if err := s.advanceCardsOnce(context.Background()); err != nil {
		t.Fatalf("advanceCardsOnce: %v", err)
	}
}

// lastOrder 返回该卡最新的一张单据。
func lastOrder(t *testing.T, s *Service, cardID string) *Order {
	t.Helper()
	orders, _, err := s.ListOrders(context.Background(), OrderFilter{CardID: cardID, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) == 0 {
		t.Fatalf("卡 %s 没有单据", cardID)
	}
	return &orders[0]
}

func reqBody(t *testing.T, r mwtest.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("请求体 %s: %v", r.Body, err)
	}
	return m
}

func ptr(v int64) *int64 { return &v }

func TestReleasingSweepsThenAvailable(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing, NextActionAt: ptr(clock.Now().Unix())})
	mw.On("GET", detailPath("c1"),
		mwtest.OK(detailJSON("c1", "2.00", "0.00", "ACTIVE")),
		mwtest.OK(detailJSON("c1", "0.00", "0.00", "ACTIVE")))
	mw.On("POST", pathWithdraw, mwtest.OK(`{"status":"APPLY","transaction_id":"w1"}`))

	mustAdvance(t, s)
	reqs := mw.Requests("POST", pathWithdraw)
	if len(reqs) != 1 {
		t.Fatalf("应提交一条转出单: %d", len(reqs))
	}
	if b := reqBody(t, reqs[0]); b["amount"] != "2.00" || b["currency"] != "USD" || b["card_id"] != "c1" {
		t.Fatalf("转出请求: %v", b)
	}
	o := lastOrder(t, s, "c1")
	if o.Kind != KindWithdraw || o.AmountCents != 200 || o.RequestedBy != "recycle" || o.State != StateProcessing {
		t.Fatalf("%+v", o)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolReleasing || c.NextActionAt != nil {
		t.Fatalf("转出在途：仍为 releasing，next_action_at 为空: %+v", c)
	}

	// 单据在途：跳过，不查卡
	clock.Advance(5 * time.Second)
	mustAdvance(t, s)
	if n := len(mw.Requests("GET", detailPath("c1"))); n != 1 {
		t.Fatalf("在途时不应刷新: %d", n)
	}

	if _, err := s.finalize(ctx, o.OutOrderNo, queryResult{State: StateSuccess}, "test"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Second)
	mustAdvance(t, s)
	c := mustGetCard(t, "c1")
	if c.PoolState != PoolAvailable || c.NextActionAt != nil || c.AvailableCents != 0 {
		t.Fatalf("清零后应回到 available: %+v", c)
	}
	if n := len(mw.Requests("POST", pathWithdraw)); n != 1 {
		t.Fatalf("withdraw %d", n)
	}
	// available 的卡不再推进
	clock.Advance(time.Hour)
	mustAdvance(t, s)
	if n := len(mw.Requests("GET", detailPath("c1"))); n != 2 {
		t.Fatalf("available 的卡不应再刷新: %d", n)
	}
}

func TestReleasingWaitsOnFrozenBalance(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing})
	mw.On("GET", detailPath("c1"),
		mwtest.OK(detailJSON("c1", "0.00", "1.00", "ACTIVE")),
		mwtest.OK(detailJSON("c1", "0.00", "0.00", "ACTIVE")))

	t0 := clock.Now().Unix()
	mustAdvance(t, s)
	c := mustGetCard(t, "c1")
	if c.PoolState != PoolReleasing || i64(c.NextActionAt) != t0+600 || c.FrozenCents != 100 {
		t.Fatalf("有冻结余额应 10 分钟后再查: %+v", c)
	}
	if n := len(mw.Requests("POST", pathWithdraw)); n != 0 {
		t.Fatalf("可用余额为 0 不应转出: %d", n)
	}

	clock.Advance(599 * time.Second)
	mustAdvance(t, s)
	if n := len(mw.Requests("GET", detailPath("c1"))); n != 1 {
		t.Fatalf("未到时间不应刷新: %d", n)
	}
	clock.Advance(time.Second)
	mustAdvance(t, s)
	if c := mustGetCard(t, "c1"); c.PoolState != PoolAvailable {
		t.Fatalf("冻结余额清零后应回 available: %+v", c)
	}
}

func TestUsedFlowSweepFreezeCancel(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolUsed, UseCount: 1, NextActionAt: ptr(clock.Now().Unix())})
	mw.On("GET", detailPath("c1"),
		mwtest.OK(detailJSON("c1", "2.00", "0.50", "ACTIVE")),
		mwtest.OK(detailJSON("c1", "0.00", "0.50", "ACTIVE")),
		mwtest.OK(detailJSON("c1", "0.00", "0.50", "REVOKED")))
	mw.On("POST", pathWithdraw, mwtest.OK(`{"status":"APPLY"}`))
	mw.On("PUT", pathStatus, mwtest.OK(`{"card_id":"c1","status":"PROCESSING"}`))

	// 1. 转出
	mustAdvance(t, s)
	o := lastOrder(t, s, "c1")
	if o.Kind != KindWithdraw || o.AmountCents != 200 || o.RequestedBy != "recycle" {
		t.Fatalf("%+v", o)
	}
	if _, err := s.finalize(ctx, o.OutOrderNo, queryResult{State: StateSuccess}, "test"); err != nil {
		t.Fatal(err)
	}

	// 2. 可用余额 0（冻结余额不用等）→ 冻结
	clock.Advance(5 * time.Second)
	tf := clock.Now().Unix()
	mustAdvance(t, s)
	puts := mw.Requests("PUT", pathStatus)
	if len(puts) != 1 || reqBody(t, puts[0])["card_status"] != "REVOKED" {
		t.Fatalf("应提交冻结: %d", len(puts))
	}
	o = lastOrder(t, s, "c1")
	if o.Kind != KindStatus || o.TargetStatus != "REVOKED" || o.State != StateSuccess || o.RequestedBy != "recycle" {
		t.Fatalf("%+v", o)
	}
	c := mustGetCard(t, "c1")
	if c.PoolState != PoolUsed || c.PlatformStatus != "REVOKED" || i64(c.FrozenAt) != tf || i64(c.CancelAfter) != tf+7*86400 {
		t.Fatalf("冻结后: %+v", c)
	}

	// 3. 时间未到：只把 next_action_at 设到 cancel_after，不发请求
	clock.Advance(5 * time.Second)
	mustAdvance(t, s)
	if c := mustGetCard(t, "c1"); i64(c.NextActionAt) != tf+7*86400 {
		t.Fatalf("应等到 cancel_after: %+v", c)
	}
	setUnix(clock, tf+7*86400-1)
	mustAdvance(t, s)
	if n := len(mw.Requests("PUT", pathStatus)); n != 1 {
		t.Fatalf("未到 cancel_after 不应注销: %d", n)
	}
	if n := len(mw.Requests("GET", detailPath("c1"))); n != 2 {
		t.Fatalf("等待注销期间不刷新: %d", n)
	}

	// 4. 到时间：注销 → retired
	setUnix(clock, tf+7*86400)
	mustAdvance(t, s)
	puts = mw.Requests("PUT", pathStatus)
	if len(puts) != 2 || reqBody(t, puts[1])["card_status"] != "CANCELED" {
		t.Fatalf("应提交注销: %d", len(puts))
	}
	if _, has := reqBody(t, puts[1])["account_no"]; has {
		t.Fatal("注销不应传 account_no")
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolRetired || c.PlatformStatus != "CANCELED" {
		t.Fatalf("注销后应 retired: %+v", c)
	}
	if n := len(mw.Requests("GET", detailPath("c1"))); n != 3 {
		t.Fatalf("注销前应刷新一次: %d", n)
	}
	clock.Advance(time.Hour)
	mustAdvance(t, s)
	if n := len(mw.Requests("PUT", pathStatus)); n != 2 {
		t.Fatalf("retired 不再推进: %d", n)
	}
}

func TestCancelAfterZeroDays(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	savePolicy(t, func(p *Policy) { p.CancelAfterDays = 0 })
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolUsed})
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "0.00", "0.00", "ACTIVE")))
	mw.On("PUT", pathStatus, mwtest.OK(`{"card_id":"c1","status":"PROCESSING"}`))

	mustAdvance(t, s)
	c := mustGetCard(t, "c1")
	if c.PlatformStatus != "REVOKED" || i64(c.FrozenAt) != clock.Now().Unix() || i64(c.CancelAfter) != clock.Now().Unix() {
		t.Fatalf("冻结: %+v", c)
	}
	clock.Advance(5 * time.Second)
	mustAdvance(t, s)
	puts := mw.Requests("PUT", pathStatus)
	if len(puts) != 2 || reqBody(t, puts[1])["card_status"] != "CANCELED" {
		t.Fatalf("冻结后的下一轮就注销: %d", len(puts))
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolRetired {
		t.Fatalf("%+v", c)
	}

	// 平台上已是 REVOKED：不再提交冻结，直接写入 frozen_at，下一轮注销
	insertCard(t, Card{CardID: "c2", ProductID: "p1", PoolState: PoolUsed})
	mw.On("GET", detailPath("c2"), mwtest.OK(detailJSON("c2", "0.00", "0.00", "REVOKED")))
	mustAdvance(t, s)
	if n := len(mw.Requests("PUT", pathStatus)); n != 2 {
		t.Fatalf("已冻结的卡不应再提交冻结: %d", n)
	}
	if c := mustGetCard(t, "c2"); i64(c.FrozenAt) != clock.Now().Unix() || i64(c.CancelAfter) != clock.Now().Unix() {
		t.Fatalf("应直接写入 frozen_at: %+v", c)
	}
	clock.Advance(5 * time.Second)
	mustAdvance(t, s)
	puts = mw.Requests("PUT", pathStatus)
	if len(puts) != 3 || reqBody(t, puts[2])["card_id"] != "c2" || reqBody(t, puts[2])["card_status"] != "CANCELED" {
		t.Fatalf("c2 应注销: %d", len(puts))
	}
	if c := mustGetCard(t, "c2"); c.PoolState != PoolRetired {
		t.Fatalf("%+v", c)
	}
}

func TestRecycleFailureThrottlesAndAlerts(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, al := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing})
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "2.00", "0.00", "ACTIVE")))
	mw.On("POST", pathWithdraw, mwtest.Rejected(400, "BAD_REQUEST"), mwtest.OK(`{"status":"APPLY"}`))

	t0 := clock.Now().Unix()
	mustAdvance(t, s)
	if o := lastOrder(t, s, "c1"); o.State != StateFailed {
		t.Fatalf("转出被拒应为 failed: %+v", o)
	}
	if al.Count() != 1 || !strings.Contains(al.Texts()[0], "c1") {
		t.Fatalf("应告警 1 次: %v", al.Texts())
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolReleasing || i64(c.NextActionAt) != t0+600 {
		t.Fatalf("应 10 分钟后再试: %+v", c)
	}
	for clock.Now().Unix() < t0+595 {
		clock.Advance(5 * time.Second)
		mustAdvance(t, s)
	}
	if n := len(mw.Requests("POST", pathWithdraw)); n != 1 {
		t.Fatalf("10 分钟内不应再发转出: %d", n)
	}
	if n := len(mw.Requests("GET", detailPath("c1"))); n != 1 {
		t.Fatalf("10 分钟内不应再刷新: %d", n)
	}
	if al.Count() != 1 {
		t.Fatalf("只告警 1 次: %v", al.Texts())
	}
	setUnix(clock, t0+600)
	mustAdvance(t, s)
	if n := len(mw.Requests("POST", pathWithdraw)); n != 2 {
		t.Fatalf("10 分钟后应重试: %d", n)
	}
	if o := lastOrder(t, s, "c1"); o.State != StateProcessing {
		t.Fatalf("%+v", o)
	}

	// 单据稍后才定论为失败：卡被推到期后，按最近一笔同类单据限流
	insertCard(t, Card{CardID: "c2", ProductID: "p1", PoolState: PoolReleasing})
	mw.On("GET", detailPath("c2"), mwtest.OK(detailJSON("c2", "3.00", "0.00", "ACTIVE")))
	mustAdvance(t, s)
	o2 := lastOrder(t, s, "c2")
	if o2.State != StateProcessing || o2.AmountCents != 300 {
		t.Fatalf("%+v", o2)
	}
	clock.Advance(time.Minute)
	t1 := clock.Now().Unix()
	if _, err := s.finalize(ctx, o2.OutOrderNo, queryResult{State: StateFailed, Reason: "FAIL"}, "test"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Second)
	mustAdvance(t, s)
	if n := len(mw.Requests("POST", pathWithdraw)); n != 3 {
		t.Fatalf("失败后 10 分钟内不应重发: %d", n)
	}
	if c := mustGetCard(t, "c2"); i64(c.NextActionAt) != t1+600 {
		t.Fatalf("next_action_at 应为定论时间 + 600: %+v", c)
	}
	if al.Count() != 2 || !strings.Contains(al.Texts()[1], "c2") {
		t.Fatalf("c2 应告警: %v", al.Texts())
	}
	setUnix(clock, t1+600)
	mustAdvance(t, s)
	if n := len(mw.Requests("POST", pathWithdraw)); n != 4 {
		t.Fatalf("10 分钟后应重试: %d", n)
	}
}

func TestRecycleSkipsWhenOrderInFlightOrNeedsReview(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, _ := newTestService(t)
	now := clock.Now().Unix()
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing, NextActionAt: ptr(now)})
	insertCard(t, Card{CardID: "c2", ProductID: "p1", PoolState: PoolUsed, NextActionAt: ptr(now)})
	insertCard(t, Card{CardID: "c3", ProductID: "p1", PoolState: PoolUsed, FrozenAt: ptr(now - 86400), CancelAfter: ptr(now - 10)})
	o1 := insertOrder(t, Order{Kind: KindWithdraw, CardID: "c1", AmountCents: 100, Currency: "USD", State: StateProcessing, NextCheckAt: ptr(now + 3600)})
	insertOrder(t, Order{Kind: KindStatus, CardID: "c2", TargetStatus: "REVOKED", State: StateNeedsReview})
	insertOrder(t, Order{Kind: KindRecharge, CardID: "c3", AmountCents: 100, Currency: "USD", State: StateUnknown, NextCheckAt: ptr(now + 3600)})

	before := map[string]*Card{}
	for _, id := range []string{"c1", "c2", "c3"} {
		before[id] = mustGetCard(t, id)
	}
	for i := 0; i < 3; i++ {
		clock.Advance(5 * time.Second)
		mustAdvance(t, s) // 未编排任何接口：发出请求即测试失败
	}
	for id, b := range before {
		if c := mustGetCard(t, id); c.PoolState != b.PoolState || i64(c.NextActionAt) != i64(b.NextActionAt) {
			t.Fatalf("%s 有非终态单据时应跳过: %+v", id, c)
		}
	}

	// 单据定论后从这一步继续
	if _, err := s.finalize(ctx, o1.OutOrderNo, queryResult{State: StateSuccess}, "test"); err != nil {
		t.Fatal(err)
	}
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "0.00", "0.00", "ACTIVE")))
	mustAdvance(t, s)
	if c := mustGetCard(t, "c1"); c.PoolState != PoolAvailable {
		t.Fatalf("定论后应继续推进: %+v", c)
	}
}

func TestExternalFreezeOnReleasing(t *testing.T) {
	s, mw, _, al := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing})
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "1.00", "0.00", "REVOKED")))

	mustAdvance(t, s)
	c := mustGetCard(t, "c1")
	if c.PoolState != PoolDisabled || c.DisabledReason != "平台冻结" || c.PlatformStatus != "REVOKED" {
		t.Fatalf("平台冻结应停用: %+v", c)
	}
	if n := len(mw.Requests("POST", pathWithdraw)); n != 0 {
		t.Fatalf("不应转出: %d", n)
	}
	if al.Count() != 1 || !strings.Contains(al.Texts()[0], "c1") {
		t.Fatalf("应告警: %v", al.Texts())
	}

	// 平台已注销：releasing、used 都转 retired
	insertCard(t, Card{CardID: "c2", ProductID: "p1", PoolState: PoolReleasing})
	insertCard(t, Card{CardID: "c3", ProductID: "p1", PoolState: PoolUsed})
	mw.On("GET", detailPath("c2"), mwtest.OK(detailJSON("c2", "0.00", "0.00", "CANCELED")))
	mw.On("GET", detailPath("c3"), mwtest.OK(detailJSON("c3", "0.00", "0.00", "CANCELED")))
	mustAdvance(t, s)
	for _, id := range []string{"c2", "c3"} {
		if c := mustGetCard(t, id); c.PoolState != PoolRetired {
			t.Fatalf("%s: %+v", id, c)
		}
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolDisabled {
		t.Fatalf("disabled 的卡不再推进: %+v", c)
	}
}

func TestRecycleRefreshErrorRetriesInOneMinute(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing})
	mw.On("GET", detailPath("c1"), mwtest.Unknown(), mwtest.OK(detailJSON("c1", "0.00", "0.00", "ACTIVE")))

	t0 := clock.Now().Unix()
	_ = s.advanceCardsOnce(context.Background())
	if c := mustGetCard(t, "c1"); c.PoolState != PoolReleasing || i64(c.NextActionAt) != t0+60 {
		t.Fatalf("刷新失败应 1 分钟后再试: %+v", c)
	}
	clock.Advance(59 * time.Second)
	mustAdvance(t, s)
	if n := len(mw.Requests("GET", detailPath("c1"))); n != 1 {
		t.Fatalf("1 分钟内不应再刷新: %d", n)
	}
	clock.Advance(time.Second)
	mustAdvance(t, s)
	if c := mustGetCard(t, "c1"); c.PoolState != PoolAvailable {
		t.Fatalf("%+v", c)
	}
}

// 平台上已是 REVOKED 的 used 卡：不再尝试转出（冻结的卡可能不让转出），直接记冻结，等注销把余额退回主钱包。
func TestUsedRevokedOnPlatformSkipsSweep(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolUsed, UseCount: 1})
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "3.00", "0.00", "REVOKED")))

	now := clock.Now().Unix()
	mustAdvance(t, s)
	if n := len(mw.Requests("POST", pathWithdraw)); n != 0 {
		t.Fatalf("已冻结的卡不应转出: %d", n)
	}
	if n := countOrders(t); n != 0 {
		t.Fatalf("不应建单: %d", n)
	}
	c := mustGetCard(t, "c1")
	if c.PoolState != PoolUsed || c.AvailableCents != 300 || i64(c.FrozenAt) != now ||
		i64(c.CancelAfter) != now+7*86400 || i64(c.NextActionAt) != now+7*86400 {
		t.Fatalf("应直接记冻结: %+v", c)
	}
}

// 已冻结、到了 cancel_after 的卡：注销前先刷新；平台已注销就直接退役，刷新出错 1 分钟后再试。
func TestFrozenCardRefreshesBeforeCancel(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, _ := newTestService(t)
	now := clock.Now().Unix()
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolUsed, PlatformStatus: "REVOKED",
		FrozenAt: ptr(now - 86400), CancelAfter: ptr(now)})
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "0.00", "0.00", "CANCELED")))

	mustAdvance(t, s)
	if c := mustGetCard(t, "c1"); c.PoolState != PoolRetired || c.PlatformStatus != "CANCELED" {
		t.Fatalf("平台已注销应直接退役: %+v", c)
	}
	if n := len(mw.Requests("PUT", pathStatus)); n != 0 || countOrders(t) != 0 {
		t.Fatalf("不应提交注销: PUT %d, orders %d", n, countOrders(t))
	}

	insertCard(t, Card{CardID: "c2", ProductID: "p1", PoolState: PoolUsed, PlatformStatus: "REVOKED",
		FrozenAt: ptr(now - 86400), CancelAfter: ptr(now)})
	mw.On("GET", detailPath("c2"), mwtest.Unknown(), mwtest.OK(detailJSON("c2", "0.00", "0.00", "REVOKED")))
	mw.On("PUT", pathStatus, mwtest.OK(`{"card_id":"c2","status":"PROCESSING"}`))
	_ = s.advanceCardsOnce(ctx)
	if c := mustGetCard(t, "c2"); c.PoolState != PoolUsed || i64(c.NextActionAt) != now+60 {
		t.Fatalf("刷新出错应 1 分钟后再试: %+v", c)
	}
	if n := len(mw.Requests("PUT", pathStatus)); n != 0 {
		t.Fatalf("刷新出错不应提交注销: %d", n)
	}
	clock.Advance(59 * time.Second)
	mustAdvance(t, s)
	if n := len(mw.Requests("GET", detailPath("c2"))); n != 1 {
		t.Fatalf("1 分钟内不应再刷新: %d", n)
	}
	clock.Advance(time.Second)
	mustAdvance(t, s)
	puts := mw.Requests("PUT", pathStatus)
	if len(puts) != 1 || reqBody(t, puts[0])["card_id"] != "c2" || reqBody(t, puts[0])["card_status"] != "CANCELED" {
		t.Fatalf("应提交注销: %d", len(puts))
	}
	if c := mustGetCard(t, "c2"); c.PoolState != PoolRetired {
		t.Fatalf("%+v", c)
	}
}

// 已冻结、到了 cancel_after、但上一张注销单刚失败而被限流的卡：next_action_at 未到之前不再选中，不发任何请求。
func TestFrozenCardThrottledNotReprocessed(t *testing.T) {
	s, mw, clock, al := newTestService(t)
	now := clock.Now().Unix()
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolUsed, PlatformStatus: "REVOKED",
		FrozenAt: ptr(now - 86400), CancelAfter: ptr(now - 3600), NextActionAt: ptr(now + 300)})
	insertOrder(t, Order{Kind: KindStatus, CardID: "c1", TargetStatus: "CANCELED", State: StateFailed,
		LastError: "rejected", FinishedAt: ptr(now - 300)})

	// 未编排任何接口：被选中就会发请求而导致测试失败
	for clock.Now().Unix() < now+295 {
		clock.Advance(5 * time.Second)
		mustAdvance(t, s)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolUsed || i64(c.NextActionAt) != now+300 || c.UpdatedAt != now {
		t.Fatalf("限流期间不应处理: %+v", c)
	}
	if al.Count() != 0 {
		t.Fatalf("限流期间不应被重新选中: %v", al.Texts())
	}

	// 到点：刷新后重新提交注销
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "0.00", "0.00", "REVOKED")))
	mw.On("PUT", pathStatus, mwtest.Rejected(400, "BAD"))
	setUnix(clock, now+300)
	mustAdvance(t, s)
	if n := len(mw.Requests("PUT", pathStatus)); n != 1 {
		t.Fatalf("到点应重试注销: %d", n)
	}
	o := lastOrder(t, s, "c1")
	if o.State != StateFailed || o.TargetStatus != "CANCELED" {
		t.Fatalf("%+v", o)
	}
	c := mustGetCard(t, "c1")
	if c.PoolState != PoolUsed || i64(c.NextActionAt) != i64(o.FinishedAt)+600 {
		t.Fatalf("再次失败应限流 10 分钟: %+v", c)
	}
	if al.Count() != 1 || !strings.Contains(al.Texts()[0], "c1") {
		t.Fatalf("应告警: %v", al.Texts())
	}
	// 再次限流期间：不刷新、不重发
	for clock.Now().Unix() < now+895 {
		clock.Advance(5 * time.Second)
		mustAdvance(t, s)
	}
	if n := len(mw.Requests("GET", detailPath("c1"))); n != 1 {
		t.Fatalf("限流期间不应刷新: %d", n)
	}
	if n := len(mw.Requests("PUT", pathStatus)); n != 1 {
		t.Fatalf("限流期间不应重发: %d", n)
	}
}

// 维护循环：有效期不足一个月的可用卡转 used（退役原因「即将过期」），之后按 used 流程清零、冻结、注销；
// 有效期无法识别的可用卡停用（原因「有效期无法识别」），不自动注销。各告警一次（每卡每天）。
func TestRetireExpiringAndBadExpiryAvailableCards(t *testing.T) {
	s, _, clock, log := newTestService(t)
	ctx := context.Background()
	insertCard(t, Card{CardID: "ok", ProductID: "p1", Expiry: "1126"})       // 2026-12-01 失效，还剩一个月以上
	insertCard(t, Card{CardID: "expiring", ProductID: "p1", Expiry: "1026"}) // 2026-11-01 失效
	insertCard(t, Card{CardID: "expired", ProductID: "p1", Expiry: "0926"})
	insertCard(t, Card{CardID: "malformed", ProductID: "p1", Expiry: "13/26"})
	insertCard(t, Card{CardID: "empty", ProductID: "p1", Expiry: " "})
	insertCard(t, Card{CardID: "leased", ProductID: "p1", Expiry: "1026", PoolState: PoolLeased, LeaseToken: "t"})
	for i := 0; i < 2; i++ {
		if err := s.retireExpiringOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	now := clock.Now().Unix()
	for _, id := range []string{"expiring", "expired"} {
		c := mustGetCard(t, id)
		if c.PoolState != PoolUsed || c.RetireReason != "即将过期" || i64(c.NextActionAt) != now {
			t.Fatalf("%s: %+v", id, c)
		}
		if alertsContaining(log, "卡 "+id+" ") != 1 {
			t.Fatalf("%s 应告警一次: %v", id, log.Texts())
		}
	}
	for _, id := range []string{"malformed", "empty"} {
		c := mustGetCard(t, id)
		if c.PoolState != PoolDisabled || c.DisabledReason != "有效期无法识别" || c.RetireReason != "" {
			t.Fatalf("%s: %+v", id, c)
		}
		if alertsContaining(log, "卡 "+id+" ") != 1 {
			t.Fatalf("%s 应告警一次: %v", id, log.Texts())
		}
	}
	if c := mustGetCard(t, "ok"); c.PoolState != PoolAvailable {
		t.Fatalf("%+v", c)
	}
	if c := mustGetCard(t, "leased"); c.PoolState != PoolLeased {
		t.Fatalf("租用中的卡不动: %+v", c)
	}
	if log.Count() != 4 {
		t.Fatalf("告警: %v", log.Texts())
	}
}
