package cardpool

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

func mustReconcile(t *testing.T, s *Service) {
	t.Helper()
	if err := s.reconcileOrdersOnce(context.Background()); err != nil {
		t.Fatalf("reconcileOrdersOnce: %v", err)
	}
}

func setUnix(clock *testClock, sec int64) { clock.Set(time.Unix(sec, 0).UTC()) }

func TestStalePendingBecomesUnknown(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	insertCard(t, Card{CardID: "c2", ProductID: "p1"})
	o := insertOrder(t, Order{Kind: KindRecharge, CardID: "c1", AmountCents: 100, Currency: "USD"})
	clock.Advance(3 * time.Minute)
	fresh := insertOrder(t, Order{Kind: KindRecharge, CardID: "c2", AmountCents: 100, Currency: "USD"})
	mw.On("GET", pathTransferQ, mwtest.Rejected(404, "NOT_FOUND"))

	mustReconcile(t, s)

	now := clock.Now().Unix()
	got := mustGetOrder(t, o.OutOrderNo)
	if got.State != StateUnknown || !got.EverUnknown || i64(got.FirstSentAt) != o.CreatedAt ||
		got.Attempts != 1 || i64(got.NextCheckAt) != now+10 {
		t.Fatalf("%+v", got)
	}
	reqs := mw.Requests("GET", pathTransferQ)
	if len(reqs) != 1 {
		t.Fatalf("应查单一次, got %d", len(reqs))
	}
	q, _ := url.ParseQuery(reqs[0].RawQuery)
	if q.Get("out_order_no") != o.OutOrderNo {
		t.Fatalf("query %q", reqs[0].RawQuery)
	}
	if n := len(mw.Requests("POST", pathRecharge)); n != 0 {
		t.Fatalf("pending 转 unknown 后绝不能重发, got %d", n)
	}
	if got := mustGetOrder(t, fresh.OutOrderNo); got.State != StatePending {
		t.Fatalf("未满 2 分钟的 pending 不动: %+v", got)
	}
}

func TestReconcileBackoffSchedule(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	mw.On("POST", pathRecharge, mwtest.Unknown())
	mw.On("GET", pathTransferQ, mwtest.Rejected(404, "NOT_FOUND"))
	o, err := s.submitTransfer(context.Background(), KindRecharge, "c1", 100, "USD", "admin")
	if err != nil || o.State != StateUnknown {
		t.Fatalf("%+v %v", o, err)
	}
	t0 := clock.Now().Unix()
	if i64(o.NextCheckAt) != t0+10 {
		t.Fatalf("首次 next_check_at = %v", o.NextCheckAt)
	}
	next := t0 + 10
	for i, d := range []int64{30, 60, 120, 300, 300} {
		setUnix(clock, next-1)
		mustReconcile(t, s)
		if n := len(mw.Requests("GET", pathTransferQ)); n != i {
			t.Fatalf("第 %d 次：未到期不应查单, got %d", i+1, n)
		}
		setUnix(clock, next)
		mustReconcile(t, s)
		if n := len(mw.Requests("GET", pathTransferQ)); n != i+1 {
			t.Fatalf("第 %d 次：到期应查一次, got %d", i+1, n)
		}
		got := mustGetOrder(t, o.OutOrderNo)
		if got.State != StateUnknown || got.Attempts != i+2 || i64(got.NextCheckAt) != next+d {
			t.Fatalf("第 %d 次：%+v（want next %d）", i+1, got, next+d)
		}
		next += d
	}
	if n := len(mw.Requests("POST", pathRecharge)); n != 1 {
		t.Fatalf("资金单不得重发, got %d", n)
	}
}

// escalateScenario：转入结果未知、查单一直回 reply，30 分钟后应转 needs_review，之后不再查。
func escalateScenario(t *testing.T, reply mwtest.Reply) {
	s, mw, clock, log := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	mw.On("POST", pathRecharge, mwtest.Unknown())
	mw.On("GET", pathTransferQ, reply)
	o, err := s.submitTransfer(context.Background(), KindRecharge, "c1", 100, "USD", "admin")
	if err != nil || o.State != StateUnknown {
		t.Fatalf("%+v %v", o, err)
	}
	t0 := clock.Now().Unix()
	for clock.Now().Unix() < t0+1790 {
		clock.Advance(10 * time.Second)
		mustReconcile(t, s)
		if got := mustGetOrder(t, o.OutOrderNo); got.State != StateUnknown {
			t.Fatalf("t0+%d 时应仍为 unknown: %+v", clock.Now().Unix()-t0, got)
		}
	}
	if log.Count() != 0 {
		t.Fatalf("30 分钟前不应告警: %v", log.Texts())
	}
	queried := len(mw.Requests("GET", pathTransferQ))
	if queried == 0 {
		t.Fatal("期间应有查单")
	}
	clock.Advance(10 * time.Second) // t0+1800
	mustReconcile(t, s)
	got := mustGetOrder(t, o.OutOrderNo)
	if got.State != StateNeedsReview {
		t.Fatalf("满 1800s 应转 needs_review: %+v", got)
	}
	if log.Count() != 1 || !strings.Contains(log.Texts()[0], o.OutOrderNo) {
		t.Fatalf("应告警 1 条且含单号: %v", log.Texts())
	}
	queried = len(mw.Requests("GET", pathTransferQ))
	for i := 0; i < 60; i++ {
		clock.Advance(time.Minute)
		mustReconcile(t, s)
	}
	if n := len(mw.Requests("GET", pathTransferQ)); n != queried {
		t.Fatalf("needs_review 之后不应再查, %d → %d", queried, n)
	}
	if got := mustGetOrder(t, o.OutOrderNo); got.State != StateNeedsReview {
		t.Fatalf("%+v", got)
	}
	if log.Count() != 1 {
		t.Fatalf("不应重复告警: %v", log.Texts())
	}
	if n := len(mw.Requests("POST", pathRecharge)); n != 1 {
		t.Fatalf("资金单不得重发, got %d", n)
	}
}

func TestUnknownToNeedsReviewAfter30Min(t *testing.T) {
	escalateScenario(t, mwtest.Rejected(404, "NOT_FOUND"))
}

func TestQueryErrorAlsoEscalates(t *testing.T) {
	escalateScenario(t, mwtest.Unknown())
}

func TestProcessingAlertAfter24h(t *testing.T) {
	s, mw, clock, log := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	mw.On("POST", pathRecharge, mwtest.OK(`{"transaction_id":"T1","status":"APPLY"}`))
	mw.On("GET", pathTransferQ, mwtest.OK(`{"transaction_id":"T1","status":"APPLY"}`))
	o, err := s.submitTransfer(context.Background(), KindRecharge, "c1", 100, "USD", "admin")
	if err != nil || o.State != StateProcessing {
		t.Fatalf("%+v %v", o, err)
	}
	t0 := clock.Now().Unix()
	setUnix(clock, t0+86399)
	mustReconcile(t, s)
	if got := mustGetOrder(t, o.OutOrderNo); got.State != StateProcessing || got.AlertedStale || log.Count() != 0 {
		t.Fatalf("%+v %v", got, log.Texts())
	}
	setUnix(clock, t0+86400)
	mustReconcile(t, s)
	got := mustGetOrder(t, o.OutOrderNo)
	if got.State != StateProcessing || !got.AlertedStale || log.Count() != 1 || !strings.Contains(log.Texts()[0], o.OutOrderNo) {
		t.Fatalf("%+v %v", got, log.Texts())
	}
	queried := len(mw.Requests("GET", pathTransferQ))
	for i := 0; i < 60; i++ {
		clock.Advance(time.Minute)
		mustReconcile(t, s)
	}
	if log.Count() != 1 {
		t.Fatalf("不应重复告警: %v", log.Texts())
	}
	if n := len(mw.Requests("GET", pathTransferQ)); n <= queried {
		t.Fatalf("告警后应继续查单, %d → %d", queried, n)
	}
	if got := mustGetOrder(t, o.OutOrderNo); got.State != StateProcessing {
		t.Fatalf("%+v", got)
	}
}

func TestFundsOrdersNeverResent(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	ctx := context.Background()
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	insertCard(t, Card{CardID: "c2", ProductID: "p1"})
	mw.On("POST", pathApply, mwtest.Unknown())
	mw.On("POST", pathRecharge, mwtest.Unknown())
	mw.On("POST", pathWithdraw, mwtest.Unknown())
	mw.On("GET", pathApplyQuery, mwtest.Unknown())
	mw.On("GET", pathTransferQ, mwtest.Rejected(404, "NOT_FOUND"))

	a, err := s.submitApply(ctx, testApplyParams(clock))
	if err != nil || a.State != StateUnknown {
		t.Fatalf("%+v %v", a, err)
	}
	r, err := s.submitTransfer(ctx, KindRecharge, "c1", 100, "USD", "admin")
	if err != nil || r.State != StateUnknown {
		t.Fatalf("%+v %v", r, err)
	}
	w, err := s.submitTransfer(ctx, KindWithdraw, "c2", 100, "USD", "admin")
	if err != nil || w.State != StateUnknown {
		t.Fatalf("%+v %v", w, err)
	}
	for i := 0; i < 50; i++ {
		clock.Advance(time.Minute)
		mustReconcile(t, s)
	}
	for _, p := range []string{pathApply, pathRecharge, pathWithdraw} {
		if n := len(mw.Requests("POST", p)); n != 1 {
			t.Fatalf("POST %s 只能收到 1 次, got %d", p, n)
		}
	}
	if len(mw.Requests("GET", pathApplyQuery)) == 0 || len(mw.Requests("GET", pathTransferQ)) == 0 {
		t.Fatal("应有查单")
	}
}

func TestStatusOrderResendOnMismatch(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	mw.On("PUT", pathStatus, mwtest.Unknown(), mwtest.Unknown())
	o, err := s.submitStatus(context.Background(), "c1", "REVOKED", "admin")
	if err != nil || o.State != StateUnknown || !o.EverUnknown {
		t.Fatalf("%+v %v", o, err)
	}
	mw.On("GET", "/v1/card/detail/c1",
		mwtest.OK(`{"card_id":"c1","card_no":"","card_name":"POOL000001","status":"ACTIVE"}`),
		mwtest.OK(`{"card_id":"c1","card_no":"","card_name":"POOL000001","status":"REVOKED"}`))

	clock.Advance(10 * time.Second)
	mustReconcile(t, s)
	puts := mw.Requests("PUT", pathStatus)
	if len(puts) != 2 {
		t.Fatalf("状态不一致应重发一次, PUT 共 %d 次", len(puts))
	}
	var body map[string]any
	if err := json.Unmarshal(puts[1].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["card_id"] != "c1" || body["card_status"] != "REVOKED" {
		t.Fatalf("body %s", puts[1].Body)
	}
	if _, ok := body["account_no"]; ok {
		t.Fatalf("不应传 account_no: %s", puts[1].Body)
	}
	if got := mustGetOrder(t, o.OutOrderNo); got.State != StateUnknown || got.Attempts != 2 {
		t.Fatalf("%+v", got)
	}

	clock.Advance(30 * time.Second)
	mustReconcile(t, s)
	got := mustGetOrder(t, o.OutOrderNo)
	if got.State != StateSuccess || got.FinishedAt == nil {
		t.Fatalf("%+v", got)
	}
	if n := len(mw.Requests("PUT", pathStatus)); n != 2 {
		t.Fatalf("已是目标状态不应再发, PUT 共 %d 次", n)
	}
	if c := mustGetCard(t, "c1"); c.PlatformStatus != "REVOKED" {
		t.Fatalf("%+v", c)
	}
}

func TestRejectedAfterUnknownStaysUnknown(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	mw.On("PUT", pathStatus, mwtest.Unknown(), mwtest.Rejected(400, "BAD_STATUS"))
	o, err := s.submitStatus(context.Background(), "c1", "REVOKED", "admin")
	if err != nil || o.State != StateUnknown {
		t.Fatalf("%+v %v", o, err)
	}
	mw.On("GET", "/v1/card/detail/c1", mwtest.OK(`{"card_id":"c1","card_no":"","card_name":"x","status":"ACTIVE"}`))
	clock.Advance(10 * time.Second)
	mustReconcile(t, s)
	if n := len(mw.Requests("PUT", pathStatus)); n != 2 {
		t.Fatalf("PUT 共 %d 次", n)
	}
	got := mustGetOrder(t, o.OutOrderNo)
	if got.State != StateUnknown || !got.EverUnknown || !strings.Contains(got.LastError, "BAD_STATUS") {
		t.Fatalf("%+v", got)
	}
}

func TestStatusQueryErrorDoesNotResend(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	mw.On("PUT", pathStatus, mwtest.Unknown())
	o, err := s.submitStatus(context.Background(), "c1", "CANCELED", "admin")
	if err != nil || o.State != StateUnknown {
		t.Fatalf("%+v %v", o, err)
	}
	mw.On("GET", "/v1/card/detail/c1", mwtest.Unknown())
	clock.Advance(10 * time.Second)
	mustReconcile(t, s)
	if n := len(mw.Requests("PUT", pathStatus)); n != 1 {
		t.Fatalf("查卡出错时不应重发, PUT 共 %d 次", n)
	}
	if got := mustGetOrder(t, o.OutOrderNo); got.State != StateUnknown || got.Attempts != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestReconcileQueryMapping(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	due := clock.Now().Unix()
	a := insertOrder(t, Order{Kind: KindApply, CardHolderID: "h1", ProductID: "p1", Currency: "USD", State: StateUnknown,
		EverUnknown: true, Attempts: 1, FirstSentAt: &due, NextCheckAt: &due})
	mw.On("GET", pathApplyQuery, mwtest.OK(`{"card_id":"c7","transaction_id":"T7","status":"SUCCESS"}`))
	w := insertOrder(t, Order{Kind: KindWithdraw, CardID: "c1", AmountCents: 100, Currency: "USD", State: StateProcessing,
		Attempts: 1, FirstSentAt: &due, NextCheckAt: &due})
	mw.On("GET", pathTransferQ, mwtest.OK(`{"transaction_id":"T8","card_id":"c1","status":"FAIL"}`))

	mustReconcile(t, s)

	if got := mustGetOrder(t, a.OutOrderNo); got.State != StateSuccess || got.CardID != "c7" || got.PlatformTxnID != "T7" {
		t.Fatalf("%+v", got)
	}
	if c := mustGetCard(t, "c7"); c.PoolState != PoolReleasing {
		t.Fatalf("%+v", c)
	}
	if got := mustGetOrder(t, w.OutOrderNo); got.State != StateFailed || got.FinishedAt == nil {
		t.Fatalf("%+v", got)
	}

	// 开卡单查单 SUCCESS 但没有 card_id、或状态未知：都算没有定论
	a2 := insertOrder(t, Order{Kind: KindApply, ProductID: "p1", State: StateUnknown, EverUnknown: true, FirstSentAt: &due, NextCheckAt: &due})
	mw.On("GET", pathApplyQuery, mwtest.OK(`{"status":"SUCCESS"}`), mwtest.OK(`{"status":"WEIRD"}`))
	mustReconcile(t, s)
	if got := mustGetOrder(t, a2.OutOrderNo); got.State != StateUnknown || got.Attempts != 1 {
		t.Fatalf("%+v", got)
	}
	clock.Advance(10 * time.Second)
	mustReconcile(t, s)
	if got := mustGetOrder(t, a2.OutOrderNo); got.State != StateUnknown || got.Attempts != 2 {
		t.Fatalf("%+v", got)
	}
}

// 客户端不可用时只跳过查单：pending 照样转 unknown、unknown 满 30 分钟照样转人工并告警、processing 满 24 小时照样告警，
// 并且每个北京日告警一次「客户端不可用」。
func TestReconcileClientDownStillEscalates(t *testing.T) {
	s, mw, clock, log := newTestService(t)
	now := clock.Now().Unix()
	stale := insertOrder(t, Order{Kind: KindRecharge, CardID: "c1", AmountCents: 100, Currency: "USD", CreatedAt: now - 3*60})
	sent := now - unknownReviewSeconds - 60
	unk := insertOrder(t, Order{Kind: KindWithdraw, CardID: "c2", AmountCents: 100, Currency: "USD", State: StateUnknown,
		EverUnknown: true, FirstSentAt: &sent, NextCheckAt: &sent, CreatedAt: sent})
	day := now - processingAlertSeconds - 60
	proc := insertOrder(t, Order{Kind: KindApply, ProductID: "p1", State: StateProcessing, FirstSentAt: &day, NextCheckAt: &day, CreatedAt: day})
	insertCard(t, Card{CardID: "r1", ProductID: "p1", PoolState: PoolReleasing})
	insertCard(t, Card{CardID: "u1", ProductID: "p1", PoolState: PoolUsed})
	s.opts.Client = func() (*marswallex.Client, error) { return nil, errors.New("MarsWallex 未配置") }

	mustReconcile(t, s)

	if got := mustGetOrder(t, stale.OutOrderNo); got.State != StateUnknown || !got.EverUnknown {
		t.Fatalf("pending 超过 2 分钟应转 unknown: %+v", got)
	}
	if got := mustGetOrder(t, unk.OutOrderNo); got.State != StateNeedsReview {
		t.Fatalf("unknown 满 30 分钟应转 needs_review: %+v", got)
	}
	if got := mustGetOrder(t, proc.OutOrderNo); !got.AlertedStale {
		t.Fatalf("processing 满 24 小时应告警: %+v", got)
	}
	if n := alertsContaining(log, unk.OutOrderNo); n != 1 {
		t.Fatalf("转人工应告警: %v", log.Texts())
	}
	if n := alertsContaining(log, proc.OutOrderNo); n != 1 {
		t.Fatalf("processing 应告警: %v", log.Texts())
	}
	down := alertsContaining(log, "客户端不可用")
	if down != 1 {
		t.Fatalf("应告警客户端不可用一次: %v", log.Texts())
	}
	for _, txt := range log.Texts() {
		if strings.Contains(txt, "客户端不可用") &&
			(!strings.Contains(txt, "MarsWallex 未配置") || !strings.Contains(txt, "3 张未定论单据") || !strings.Contains(txt, "2 张待回收卡")) {
			t.Fatalf("告警内容: %s", txt)
		}
	}
	if n := len(mw.Requests("GET", pathTransferQ)) + len(mw.Requests("GET", pathApplyQuery)); n != 0 {
		t.Fatalf("客户端不可用时不应查单: %d", n)
	}

	// 同一北京日不重复告警；次日再告警
	clock.Advance(time.Minute)
	mustReconcile(t, s)
	if n := alertsContaining(log, "客户端不可用"); n != 1 {
		t.Fatalf("同日不应重复告警: %v", log.Texts())
	}
	clock.Advance(24 * time.Hour)
	mustReconcile(t, s)
	if n := alertsContaining(log, "客户端不可用"); n != 2 {
		t.Fatalf("次日应再告警: %v", log.Texts())
	}
}

// 客户端不可用、但没有待处理的单据和卡时不告警。
func TestReconcileClientDownNothingPendingNoAlert(t *testing.T) {
	s, _, _, log := newTestService(t)
	insertCard(t, Card{CardID: "a1", ProductID: "p1"})
	insertOrder(t, Order{Kind: KindRecharge, CardID: "a1", AmountCents: 100, Currency: "USD", State: StateSuccess})
	s.opts.Client = func() (*marswallex.Client, error) { return nil, errors.New("未配置") }
	mustReconcile(t, s)
	if log.Count() != 0 {
		t.Fatalf("不应告警: %v", log.Texts())
	}
}

// maintainRound 在客户端不可用时也要跑对账里的本地升级（两层都不能整轮跳过）。
func TestMaintainRoundClientDownStillEscalates(t *testing.T) {
	s, _, clock, log := newTestService(t)
	now := clock.Now().Unix()
	sent := now - unknownReviewSeconds
	unk := insertOrder(t, Order{Kind: KindRecharge, CardID: "c1", AmountCents: 100, Currency: "USD", State: StateUnknown,
		EverUnknown: true, FirstSentAt: &sent, NextCheckAt: &sent, CreatedAt: sent})
	stale := insertOrder(t, Order{Kind: KindWithdraw, CardID: "c2", AmountCents: 100, Currency: "USD", CreatedAt: now - 3*60})
	s.opts.Client = func() (*marswallex.Client, error) { return nil, errors.New("未配置") }

	s.maintainRound(context.Background(), &loopLog{now: clock.Now, last: map[string]time.Time{}})

	if got := mustGetOrder(t, unk.OutOrderNo); got.State != StateNeedsReview {
		t.Fatalf("unknown 满 30 分钟应转 needs_review: %+v", got)
	}
	if got := mustGetOrder(t, stale.OutOrderNo); got.State != StateUnknown {
		t.Fatalf("pending 超过 2 分钟应转 unknown: %+v", got)
	}
	if alertsContaining(log, unk.OutOrderNo) != 1 || alertsContaining(log, "客户端不可用") != 1 {
		t.Fatalf("告警: %v", log.Texts())
	}
}

// 目标 CANCELED、卡在注销中（INCANCELED）或状态为空、不认识：不定论、不重发，继续查；30 分钟后照常转人工。
func TestStatusOrderInProgressOrUnknownStatusNoResend(t *testing.T) {
	for _, tc := range []struct{ name, status string }{
		{"注销中", "INCANCELED"},
		{"空状态", ""},
		{"不认识的状态", "WHATEVER"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mw, clock, _ := newTestService(t)
			insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolUsed})
			mw.On("PUT", pathStatus, mwtest.Unknown())
			o, err := s.submitStatus(context.Background(), "c1", "CANCELED", "recycle")
			if err != nil || o.State != StateUnknown {
				t.Fatalf("%+v %v", o, err)
			}
			mw.On("GET", "/v1/card/detail/c1", mwtest.OK(`{"card_id":"c1","card_no":"","card_name":"x","status":"`+tc.status+`"}`))
			t0 := clock.Now().Unix()
			for clock.Now().Unix() < t0+unknownReviewSeconds-10 {
				clock.Advance(10 * time.Second)
				mustReconcile(t, s)
				if got := mustGetOrder(t, o.OutOrderNo); got.State != StateUnknown {
					t.Fatalf("t0+%d 应仍为 unknown: %+v", clock.Now().Unix()-t0, got)
				}
			}
			if n := len(mw.Requests("PUT", pathStatus)); n != 1 {
				t.Fatalf("不应重发, PUT 共 %d 次", n)
			}
			if n := len(mw.Requests("GET", "/v1/card/detail/c1")); n == 0 {
				t.Fatal("期间应查卡")
			}
			clock.Advance(10 * time.Second)
			mustReconcile(t, s)
			if got := mustGetOrder(t, o.OutOrderNo); got.State != StateNeedsReview {
				t.Fatalf("满 30 分钟应转人工: %+v", got)
			}
		})
	}
}

func TestRecheckAndConfirmFailed(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	ctx := context.Background()
	for _, id := range []string{"c1", "c2", "c3"} {
		insertCard(t, Card{CardID: id, ProductID: "p1"})
	}
	sent := clock.Now().Unix() - 3600
	o1 := insertOrder(t, Order{Kind: KindRecharge, CardID: "c1", AmountCents: 100, Currency: "USD", State: StateNeedsReview,
		EverUnknown: true, FirstSentAt: &sent})
	mw.On("GET", pathTransferQ, mwtest.OK(`{"transaction_id":"T9","card_id":"c1","status":"SUCCESS"}`))
	got, err := s.Recheck(ctx, o1.OutOrderNo)
	if err != nil || got.State != StateSuccess || got.PlatformTxnID != "T9" {
		t.Fatalf("%+v %v", got, err)
	}
	if c := mustGetCard(t, "c1"); i64(c.NextActionAt) != clock.Now().Unix() {
		t.Fatalf("%+v", c)
	}
	if _, err := s.Recheck(ctx, o1.OutOrderNo); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("终态不能再查, got %v", err)
	}

	o2 := insertOrder(t, Order{Kind: KindWithdraw, CardID: "c2", AmountCents: 100, Currency: "USD", State: StateNeedsReview,
		EverUnknown: true, FirstSentAt: &sent})
	if _, err := s.ConfirmFailed(ctx, o2.OutOrderNo, "  ", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("备注为空应拒绝, got %v", err)
	}
	if got := mustGetOrder(t, o2.OutOrderNo); got.State != StateNeedsReview {
		t.Fatalf("%+v", got)
	}
	clock.Advance(time.Minute)
	got, err = s.ConfirmFailed(ctx, o2.OutOrderNo, "后台核对无此单", "admin")
	if err != nil || got.State != StateFailed || got.Note != "后台核对无此单" || got.FinishedAt == nil {
		t.Fatalf("%+v %v", got, err)
	}
	if c := mustGetCard(t, "c2"); i64(c.NextActionAt) != clock.Now().Unix() {
		t.Fatalf("%+v", c)
	}

	o3 := insertOrder(t, Order{Kind: KindRecharge, CardID: "c3", AmountCents: 100, Currency: "USD", State: StateProcessing})
	if _, err := s.ConfirmFailed(ctx, o3.OutOrderNo, "x", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("processing 不能确认失败, got %v", err)
	}
	if got := mustGetOrder(t, o3.OutOrderNo); got.State != StateProcessing {
		t.Fatalf("%+v", got)
	}
	if _, err := s.ConfirmFailed(ctx, "nope", "x", "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestWaitOrder(t *testing.T) {
	s, _, _, _ := newTestService(t)
	o := insertOrder(t, Order{Kind: KindRecharge, CardID: "c1", AmountCents: 100, Currency: "USD", State: StateProcessing})
	done := make(chan error, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		_, err := s.finalize(context.Background(), o.OutOrderNo, queryResult{State: StateSuccess}, "webhook")
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := s.WaitOrder(ctx, o.OutOrderNo)
	if ferr := <-done; ferr != nil {
		t.Fatal(ferr)
	}
	if err != nil || got.State != StateSuccess {
		t.Fatalf("%+v %v", got, err)
	}

	nr := insertOrder(t, Order{Kind: KindRecharge, CardID: "c2", AmountCents: 100, Currency: "USD", State: StateNeedsReview})
	start := time.Now()
	got, err = s.WaitOrder(ctx, nr.OutOrderNo)
	if err != nil || got.State != StateNeedsReview || time.Since(start) > 200*time.Millisecond {
		t.Fatalf("needs_review 应立即返回: %+v %v %v", got, err, time.Since(start))
	}

	p := insertOrder(t, Order{Kind: KindRecharge, CardID: "c3", AmountCents: 100, Currency: "USD", State: StateProcessing})
	short, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	if _, err := s.WaitOrder(short, p.OutOrderNo); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.WaitOrder(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
