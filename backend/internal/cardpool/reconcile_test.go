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

func TestReconcileClientErrorDoesNothing(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	o := insertOrder(t, Order{Kind: KindRecharge, CardID: "c1", AmountCents: 100, Currency: "USD"})
	clock.Advance(time.Hour)
	cfgErr := errors.New("未配置")
	s.opts.Client = func() (*marswallex.Client, error) { return nil, cfgErr }
	if err := s.reconcileOrdersOnce(context.Background()); !errors.Is(err, cfgErr) {
		t.Fatalf("got %v", err)
	}
	if got := mustGetOrder(t, o.OutOrderNo); got.State != StatePending {
		t.Fatalf("%+v", got)
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
