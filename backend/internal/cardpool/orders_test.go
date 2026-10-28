package cardpool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

const (
	pathApply      = "/v2/card"
	pathApplyQuery = "/v1/card/apply-order"
	pathRecharge   = "/v1/card/recharge-transaction"
	pathWithdraw   = "/v1/card/withdraw-transaction"
	pathTransferQ  = "/v1/card/transfer-order"
	pathStatus     = "/v1/card/status"
)

func mustGetOrder(t *testing.T, no string) *Order {
	t.Helper()
	o, err := getOrder(context.Background(), no)
	if err != nil {
		t.Fatalf("getOrder(%s): %v", no, err)
	}
	return o
}

func mustGetCard(t *testing.T, id string) *Card {
	t.Helper()
	c, err := getCard(context.Background(), id)
	if err != nil {
		t.Fatalf("getCard(%s): %v", id, err)
	}
	return c
}

func countOrders(t *testing.T) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM mw_card_orders`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func i64(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}

func testApplyParams(clock *testClock) applyParams {
	return applyParams{
		HolderID: "h1", ProductID: "p1", Currency: "USD", NamePrefix: "POOL",
		Expiry: expiryMMyy(clock.Now(), 24), By: "admin",
	}
}

func TestBackoffAndExpiryHelpers(t *testing.T) {
	want := []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second, 120 * time.Second, 300 * time.Second, 300 * time.Second, 300 * time.Second}
	for i, w := range want {
		if got := backoff(i); got != w {
			t.Fatalf("backoff(%d) = %v, want %v", i, got, w)
		}
	}
	if got := expiryMMyy(time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC), 24); got != "1028" {
		t.Fatalf("expiryMMyy = %q", got)
	}
	// 北京时间已是次月，但按 UTC 计算
	if got := expiryMMyy(time.Date(2026, 1, 31, 20, 0, 0, 0, time.UTC), 2); got != "0326" {
		t.Fatalf("expiryMMyy = %q", got)
	}
}

func TestApplyOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		s, mw, clock, _ := newTestService(t)
		mw.On("POST", pathApply, mwtest.OK(`{"out_order_no":"x","transaction_id":"T1"}`))
		o, err := s.submitApply(ctx, testApplyParams(clock))
		if err != nil {
			t.Fatal(err)
		}
		now := clock.Now().Unix()
		got := mustGetOrder(t, o.OutOrderNo)
		if got.State != StateProcessing || got.PlatformTxnID != "T1" || got.Attempts != 1 ||
			i64(got.FirstSentAt) != now || i64(got.NextCheckAt) != now+10 || got.EverUnknown {
			t.Fatalf("%+v", got)
		}
		if got.Kind != KindApply || got.ProductID != "p1" || got.CardHolderID != "h1" || got.Currency != "USD" || got.RequestedBy != "admin" {
			t.Fatalf("%+v", got)
		}
		if o.State != StateProcessing {
			t.Fatalf("返回的单据应为最新状态: %+v", o)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		s, mw, clock, _ := newTestService(t)
		mw.On("POST", pathApply, mwtest.Rejected(400, "PARAM"))
		o, err := s.submitApply(ctx, testApplyParams(clock))
		if err != nil {
			t.Fatal(err)
		}
		got := mustGetOrder(t, o.OutOrderNo)
		if got.State != StateFailed || got.EverUnknown || got.LastError == "" || got.FinishedAt == nil ||
			i64(got.FirstSentAt) != clock.Now().Unix() || got.Attempts != 1 {
			t.Fatalf("%+v", got)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		s, mw, clock, _ := newTestService(t)
		mw.On("POST", pathApply, mwtest.Unknown())
		o, err := s.submitApply(ctx, testApplyParams(clock))
		if err != nil {
			t.Fatal(err)
		}
		now := clock.Now().Unix()
		got := mustGetOrder(t, o.OutOrderNo)
		if got.State != StateUnknown || !got.EverUnknown || i64(got.NextCheckAt) != now+10 ||
			i64(got.FirstSentAt) != now || got.Attempts != 1 || got.FinishedAt != nil {
			t.Fatalf("%+v", got)
		}
	})

	t.Run("not_sent", func(t *testing.T) {
		s, mw, clock, _ := newTestService(t)
		s.opts.Client = func() (*marswallex.Client, error) { return mw.NotSentClient(), nil }
		o, err := s.submitApply(ctx, testApplyParams(clock))
		if err != nil {
			t.Fatal(err)
		}
		got := mustGetOrder(t, o.OutOrderNo)
		if got.State != StateNotSent || got.FirstSentAt != nil || got.Attempts != 0 || got.FinishedAt == nil || got.EverUnknown {
			t.Fatalf("%+v", got)
		}
	})
}

func TestSubmitClientErrorLeavesNoRow(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	cfgErr := errors.New("未配置")
	s.opts.Client = func() (*marswallex.Client, error) { return nil, cfgErr }
	ctx := context.Background()
	if _, err := s.submitApply(ctx, testApplyParams(clock)); !errors.Is(err, cfgErr) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.submitTransfer(ctx, KindRecharge, "c1", 100, "USD", "admin"); !errors.Is(err, cfgErr) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.submitStatus(ctx, "c1", "REVOKED", "admin"); !errors.Is(err, cfgErr) {
		t.Fatalf("got %v", err)
	}
	if n := countOrders(t); n != 0 {
		t.Fatalf("配置错误时不应留下单据, got %d", n)
	}
}

// canceledAfterInsertCtx 模拟取消恰好落在「写入 pending 之后、发送之前」：Done 为 nil，写库照常进行；Err 已是 Canceled。
type canceledAfterInsertCtx struct{ context.Context }

func (canceledAfterInsertCtx) Done() <-chan struct{} { return nil }
func (canceledAfterInsertCtx) Err() error            { return context.Canceled }

// 写入 pending 后发现 ctx 已取消：请求确定没有发出，单据记为 not_sent（终态），返回 ctx 的错误。
func TestSubmitCanceledBeforeSendIsNotSent(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	insertCard(t, Card{CardID: "c2", ProductID: "p1"})
	mw.On("POST", pathApply, mwtest.OK(`{"transaction_id":"T1"}`))
	mw.On("POST", pathRecharge, mwtest.OK(`{"transaction_id":"T2","status":"APPLY"}`))
	mw.On("PUT", pathStatus, mwtest.OK(`{}`))
	ctx := canceledAfterInsertCtx{context.Background()}

	_, errA := s.submitApply(ctx, testApplyParams(clock))
	_, errT := s.submitTransfer(ctx, KindRecharge, "c1", 100, "USD", "admin")
	_, errS := s.submitStatus(ctx, "c2", "REVOKED", "admin")
	for name, err := range map[string]error{"开卡": errA, "转入": errT, "改状态": errS} {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s: 应返回 ctx 错误, got %v", name, err)
		}
	}
	for _, p := range [][2]string{{"POST", pathApply}, {"POST", pathRecharge}, {"PUT", pathStatus}} {
		if n := len(mw.Requests(p[0], p[1])); n != 0 {
			t.Fatalf("%s %s 不应发出, got %d", p[0], p[1], n)
		}
	}
	orders, _, err := s.ListOrders(context.Background(), OrderFilter{})
	if err != nil || len(orders) != 3 {
		t.Fatalf("应留下 3 张单据: %d %v", len(orders), err)
	}
	for _, o := range orders {
		if o.State != StateNotSent || o.FinishedAt == nil || o.FirstSentAt != nil || o.EverUnknown {
			t.Fatalf("应为 not_sent: %+v", o)
		}
	}
}

func TestApplyRequestBody(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	mw.On("POST", pathApply, mwtest.OK(`{"out_order_no":"x","transaction_id":"T1"}`))
	o, err := s.submitApply(context.Background(), testApplyParams(clock))
	if err != nil {
		t.Fatal(err)
	}
	reqs := mw.Requests("POST", pathApply)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests", len(reqs))
	}
	var body map[string]any
	if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"out_order_no":   o.OutOrderNo,
		"product_id":     "p1",
		"card_holder_id": "h1",
		"currency":       "USD",
		"card_name":      "POOL000001",
		"expiry_date":    "1028",
		"amount":         "0.00",
	}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("%s = %v, want %q（body %s）", k, body[k], v, reqs[0].Body)
		}
	}
	if _, ok := body["fee_deduct_type"]; ok {
		t.Fatalf("不应传 fee_deduct_type: %s", reqs[0].Body)
	}
	if _, ok := body["account_no"]; ok {
		t.Fatalf("不应传 account_no: %s", reqs[0].Body)
	}
}

func TestTransferStatusMapping(t *testing.T) {
	cases := []struct {
		status, want string
		everUnknown  bool
	}{
		{"APPLY", StateProcessing, false},
		{"SUCCESS", StateSuccess, false},
		{"FAIL", StateFailed, false},
		{"WEIRD", StateUnknown, true},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			s, mw, clock, _ := newTestService(t)
			insertCard(t, Card{CardID: "c1", ProductID: "p1"})
			mw.On("POST", pathRecharge, mwtest.OK(`{"out_order_no":"x","transaction_id":"T1","card_id":"c1","status":"`+tc.status+`"}`))
			o, err := s.submitTransfer(context.Background(), KindRecharge, "c1", 250, "USD", "lease:tok")
			if err != nil {
				t.Fatal(err)
			}
			now := clock.Now().Unix()
			got := mustGetOrder(t, o.OutOrderNo)
			if got.State != tc.want || got.EverUnknown != tc.everUnknown || got.Attempts != 1 || i64(got.FirstSentAt) != now {
				t.Fatalf("%+v", got)
			}
			if got.Kind != KindRecharge || got.CardID != "c1" || got.AmountCents != 250 || got.Currency != "USD" || got.RequestedBy != "lease:tok" {
				t.Fatalf("%+v", got)
			}
			if IsTerminal(tc.want) {
				if got.FinishedAt == nil {
					t.Fatalf("终态应写 finished_at: %+v", got)
				}
				// 转入到达终态：卡的 next_action_at = now
				if c := mustGetCard(t, "c1"); i64(c.NextActionAt) != now {
					t.Fatalf("next_action_at = %v", c.NextActionAt)
				}
			} else if i64(got.NextCheckAt) != now+10 {
				t.Fatalf("next_check_at = %v", got.NextCheckAt)
			}
			var body map[string]any
			if err := json.Unmarshal(mw.Requests("POST", pathRecharge)[0].Body, &body); err != nil {
				t.Fatal(err)
			}
			if body["amount"] != "2.50" || body["currency"] != "USD" || body["card_id"] != "c1" || body["out_order_no"] != o.OutOrderNo {
				t.Fatalf("body %v", body)
			}
		})
	}
}

func TestTransferBusyCard(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	insertCard(t, Card{CardID: "c2", ProductID: "p1"})
	insertCard(t, Card{CardID: "c3", ProductID: "p1"})
	insertOrder(t, Order{Kind: KindRecharge, CardID: "c1", AmountCents: 100, Currency: "USD", State: StateProcessing})
	insertOrder(t, Order{Kind: KindWithdraw, CardID: "c2", AmountCents: 100, Currency: "USD", State: StateNeedsReview})
	insertOrder(t, Order{Kind: KindWithdraw, CardID: "c3", AmountCents: 100, Currency: "USD", State: StateSuccess})

	if _, err := s.submitTransfer(ctx, KindWithdraw, "c1", 100, "USD", "admin"); !errors.Is(err, ErrCardBusy) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.submitTransfer(ctx, KindRecharge, "c2", 100, "USD", "admin"); !errors.Is(err, ErrCardBusy) {
		t.Fatalf("needs_review 也应拒绝, got %v", err)
	}
	if n := len(mw.Requests("POST", pathWithdraw)) + len(mw.Requests("POST", pathRecharge)); n != 0 {
		t.Fatalf("卡忙时不应发请求, got %d", n)
	}
	if n := countOrders(t); n != 3 {
		t.Fatalf("卡忙时不应写入单据, got %d", n)
	}
	// 已终结的单据不占用
	mw.On("POST", pathWithdraw, mwtest.OK(`{"status":"APPLY"}`))
	o, err := s.submitTransfer(ctx, KindWithdraw, "c3", 100, "USD", "admin")
	if err != nil || o.State != StateProcessing {
		t.Fatalf("%+v %v", o, err)
	}
}

func TestTransferRejectsNonPositiveAmount(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	for _, amt := range []int64{0, -1} {
		if _, err := s.submitTransfer(ctx, KindRecharge, "c1", amt, "USD", "admin"); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("amount %d: got %v", amt, err)
		}
		if _, err := s.submitTransfer(ctx, KindWithdraw, "c1", amt, "USD", "admin"); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("amount %d: got %v", amt, err)
		}
	}
	if _, err := s.submitTransfer(ctx, KindStatus, "c1", 100, "USD", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("kind 非转入转出: got %v", err)
	}
	if n := countOrders(t); n != 0 {
		t.Fatalf("got %d orders", n)
	}
	if n := len(mw.Requests("POST", pathRecharge)) + len(mw.Requests("POST", pathWithdraw)); n != 0 {
		t.Fatalf("got %d requests", n)
	}
}

func TestFinalizeNeverOverwritesTerminal(t *testing.T) {
	s, _, clock, log := newTestService(t)
	ctx := context.Background()
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	o := insertOrder(t, Order{Kind: KindRecharge, CardID: "c1", AmountCents: 100, Currency: "USD", State: StateProcessing})

	got, err := s.finalize(ctx, o.OutOrderNo, queryResult{State: StateSuccess, TxnID: "T1"}, "webhook")
	if err != nil || got.State != StateSuccess || got.PlatformTxnID != "T1" || got.FinishedAt == nil {
		t.Fatalf("%+v %v", got, err)
	}
	if c := mustGetCard(t, "c1"); i64(c.NextActionAt) != clock.Now().Unix() {
		t.Fatalf("next_action_at = %v", c.NextActionAt)
	}
	// 清掉副作用，验证重复定论不会再次执行
	if _, err := db.DB.Exec(`UPDATE mw_cards SET next_action_at = NULL WHERE card_id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)

	got, err = s.finalize(ctx, o.OutOrderNo, queryResult{State: StateFailed, Reason: "x"}, "reconcile")
	if err != nil || got.State != StateSuccess {
		t.Fatalf("%+v %v", got, err)
	}
	if log.Count() != 1 {
		t.Fatalf("终态冲突应告警 1 次, got %v", log.Texts())
	}
	if _, err := s.finalize(ctx, o.OutOrderNo, queryResult{State: StateProcessing}, "webhook"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.finalize(ctx, o.OutOrderNo, queryResult{State: StateSuccess}, "webhook"); err != nil {
		t.Fatal(err)
	}
	got = mustGetOrder(t, o.OutOrderNo)
	if got.State != StateSuccess || got.LastError != "" {
		t.Fatalf("%+v", got)
	}
	if log.Count() != 1 {
		t.Fatalf("processing 与重复 success 不应告警, got %v", log.Texts())
	}
	if c := mustGetCard(t, "c1"); c.NextActionAt != nil {
		t.Fatalf("重复定论不应再执行副作用: %v", *c.NextActionAt)
	}
}

func TestFinalizeTransitions(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	ctx := context.Background()
	sent := clock.Now().Unix() - 3600
	// needs_review → processing：恢复自动查单，保留 first_sent_at
	o := insertOrder(t, Order{Kind: KindWithdraw, CardID: "c1", AmountCents: 100, Currency: "USD", State: StateNeedsReview,
		EverUnknown: true, Attempts: 9, FirstSentAt: &sent})
	got, err := s.finalize(ctx, o.OutOrderNo, queryResult{State: StateProcessing}, "recheck")
	if err != nil || got.State != StateProcessing || i64(got.FirstSentAt) != sent || got.NextCheckAt == nil || !got.EverUnknown {
		t.Fatalf("%+v %v", got, err)
	}
	// 失败原因写入 last_error；卡不存在不算错误
	got, err = s.finalize(ctx, o.OutOrderNo, queryResult{State: StateFailed, Reason: "余额不足"}, "webhook")
	if err != nil || got.State != StateFailed || got.LastError != "余额不足" || got.FinishedAt == nil {
		t.Fatalf("%+v %v", got, err)
	}
	// 没有定论：不改动
	o2 := insertOrder(t, Order{Kind: KindRecharge, CardID: "c2", State: StateUnknown})
	got, err = s.finalize(ctx, o2.OutOrderNo, queryResult{}, "reconcile")
	if err != nil || got.State != StateUnknown {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.finalize(ctx, "nope", queryResult{State: StateSuccess}, "webhook"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestWebhookBeforeSendResult(t *testing.T) {
	s, mw, _, log := newTestService(t)
	ctx := context.Background()
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	rep := mwtest.OK(`{"out_order_no":"x","transaction_id":"T1","card_id":"c1","status":"APPLY"}`)
	rep.Hang = 300 * time.Millisecond
	mw.On("POST", pathRecharge, rep)

	done := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			var no string
			err := db.DB.QueryRow(`SELECT out_order_no FROM mw_card_orders WHERE card_id = 'c1' AND kind = 'recharge'`).Scan(&no)
			if err == nil {
				_, err = s.finalize(ctx, no, queryResult{State: StateSuccess, TxnID: "T1"}, "webhook")
				done <- err
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		done <- errors.New("单据一直没有出现")
	}()

	o, err := s.submitTransfer(ctx, KindRecharge, "c1", 100, "USD", "admin")
	if werr := <-done; werr != nil {
		t.Fatal(werr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if o.State != StateSuccess {
		t.Fatalf("返回的单据应保持 success: %+v", o)
	}
	if got := mustGetOrder(t, o.OutOrderNo); got.State != StateSuccess {
		t.Fatalf("%+v", got)
	}
	if log.Count() != 0 {
		t.Fatalf("APPLY 不是终态，不应告警: %v", log.Texts())
	}
}

func TestApplySuccessCreatesReleasingCard(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	ctx := context.Background()
	o := insertOrder(t, Order{Kind: KindApply, CardHolderID: "h1", ProductID: "p1", Currency: "USD", State: StateProcessing})
	got, err := s.finalize(ctx, o.OutOrderNo, queryResult{State: StateSuccess, CardID: "c9", TxnID: "T9"}, "webhook")
	if err != nil || got.State != StateSuccess || got.CardID != "c9" || got.PlatformTxnID != "T9" {
		t.Fatalf("%+v %v", got, err)
	}
	c := mustGetCard(t, "c9")
	if c.PoolState != PoolReleasing || c.Source != "opened" || c.ProductID != "p1" || c.CardHolderID != "h1" ||
		c.Currency != "USD" || i64(c.NextActionAt) != clock.Now().Unix() {
		t.Fatalf("%+v", c)
	}
	// 卡已存在时 INSERT OR IGNORE 不报错、不改动
	o2 := insertOrder(t, Order{Kind: KindApply, CardHolderID: "h2", ProductID: "p2", Currency: "EUR", State: StateUnknown})
	if _, err := s.finalize(ctx, o2.OutOrderNo, queryResult{State: StateSuccess, CardID: "c9"}, "reconcile"); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "c9"); c.ProductID != "p1" {
		t.Fatalf("%+v", c)
	}
}

func TestApplySuccessWithoutCardIDStaysProcessing(t *testing.T) {
	s, _, _, _ := newTestService(t)
	o := insertOrder(t, Order{Kind: KindApply, CardHolderID: "h1", ProductID: "p1", Currency: "USD", State: StateUnknown})
	got, err := s.finalize(context.Background(), o.OutOrderNo, queryResult{State: StateSuccess}, "webhook")
	if err != nil || got.State != StateProcessing || got.FinishedAt != nil {
		t.Fatalf("%+v %v", got, err)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM mw_cards`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("不应建卡: %d %v", n, err)
	}
}

func TestStatusSuccessSideEffects(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	ctx := context.Background()
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	insertCard(t, Card{CardID: "c2", ProductID: "p1", PoolState: PoolUsed})
	insertCard(t, Card{CardID: "c3", ProductID: "p1"})
	mw.On("PUT", pathStatus, mwtest.OK(`{"card_id":"c","card_status":"X"}`))
	now := clock.Now().Unix()

	o, err := s.submitStatus(ctx, "c1", "CANCELED", "admin")
	if err != nil || o.State != StateSuccess || o.TargetStatus != "CANCELED" || o.Kind != KindStatus || o.FinishedAt == nil {
		t.Fatalf("%+v %v", o, err)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolRetired || c.PlatformStatus != "CANCELED" {
		t.Fatalf("%+v", c)
	}

	if _, err := s.submitStatus(ctx, "c2", "REVOKED", "admin"); err != nil {
		t.Fatal(err)
	}
	c := mustGetCard(t, "c2")
	if c.PlatformStatus != "REVOKED" || i64(c.FrozenAt) != now || i64(c.CancelAfter) != now+7*86400 || c.PoolState != PoolUsed {
		t.Fatalf("%+v", c)
	}

	// 管理员冻结可用的卡：停用（原因「管理员冻结」），不记冻结时间
	if _, err := s.submitStatus(ctx, "c3", "REVOKED", "admin"); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "c3"); c.PlatformStatus != "REVOKED" || c.FrozenAt != nil || c.CancelAfter != nil ||
		c.PoolState != PoolDisabled || c.DisabledReason != "管理员冻结" {
		t.Fatalf("%+v", c)
	}
	// 解冻成功不自动启用，由管理员「启用」
	if _, err := s.submitStatus(ctx, "c3", "ACTIVE", "admin"); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "c3"); c.PlatformStatus != "ACTIVE" || c.PoolState != PoolDisabled || c.DisabledReason != "管理员冻结" {
		t.Fatalf("%+v", c)
	}

	// 请求体：不传 account_no
	for _, r := range mw.Requests("PUT", pathStatus) {
		var body map[string]any
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["account_no"]; ok {
			t.Fatalf("不应传 account_no: %s", r.Body)
		}
	}
	if _, err := s.submitStatus(ctx, "c3", "FROZEN", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("got %v", err)
	}
}
