package payengine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

const (
	pathRecharge      = "/v1/card/recharge-transaction"
	pathWithdraw      = "/v1/card/withdraw-transaction"
	pathTransferQuery = "/v1/card/transfer-order"
	pathSensitive     = "/v1/card/sensitive/c1"
)

// newRealPoolHarness 用真实 cardpool.Service + mwtest 替换假卡池：插入持卡人 JOHN DOE 与一张 available 的卡 c1，
// plus 的卡产品优先级指向它；mwtest 对转入、查单、取卡号、转出都返回成功。卡池后台协程不启动。
func newRealPoolHarness(t *testing.T) (*harness, *mwtest.Server) {
	t.Helper()
	h := newPayHarness(t, nil)
	mw := mwtest.New(t)
	pool := cardpool.New(cardpool.Options{
		Client: func() (*marswallex.Client, error) { return mw.Client(), nil },
		Now:    h.clock.Now,
		Alert:  h.alerts.add,
	})
	now := h.clock.Now().Unix()
	if _, err := db.DB.Exec(`INSERT INTO mw_cardholders (card_holder_id, first_name, last_name, status, created_at)
		VALUES ('h1', 'JOHN', 'DOE', 'APPROVED', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO mw_cards (card_id, card_holder_id, product_id, currency, last4, expiry, platform_status,
		source, pool_state, created_at, updated_at) VALUES ('c1', 'h1', 'p1', 'USD', '4242', '1230', 'ACTIVE', 'opened', ?, ?, ?)`,
		cardpool.PoolAvailable, now, now); err != nil {
		t.Fatal(err)
	}
	pol, err := cardpool.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	pol.PlanProducts["plus"] = []string{"p1"}
	if err := cardpool.SavePolicy(pol); err != nil {
		t.Fatal(err)
	}
	mw.On("POST", pathRecharge, mwtest.OK(`{"out_order_no":"x","transaction_id":"T1","card_id":"c1","status":"SUCCESS"}`))
	mw.On("GET", pathTransferQuery, mwtest.OK(`{"out_order_no":"x","transaction_id":"T1","card_id":"c1","status":"SUCCESS"}`))
	mw.On("POST", pathWithdraw, mwtest.OK(`{"out_order_no":"x","transaction_id":"T2","card_id":"c1","status":"SUCCESS"}`))
	mw.On("GET", pathSensitive, mwtest.OK(`{"card_id":"c1","card_no":"`+testCardNo+`","cvv2":"`+testCVV+`","expiry_date":"1230"}`))
	h.rebuild(pool)
	return h, mw
}

func cardPoolState(t *testing.T, id string) (state string, useCount int) {
	t.Helper()
	if err := db.DB.QueryRow(`SELECT pool_state, use_count FROM mw_cards WHERE card_id = ?`, id).Scan(&state, &useCount); err != nil {
		t.Fatal(err)
	}
	return state, useCount
}

func TestEngineWithRealCardPool(t *testing.T) {
	h, mw := newRealPoolHarness(t)
	h.gpt.accountFn = planFrom(2)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess})
	h.start(t)
	o := h.submit(t, false)
	g := waitDone(t, o.OrderNo)

	if g.State != StateSucceeded {
		t.Fatalf("应成功: %+v", g)
	}
	if g.CardID != "c1" || g.CardLast4 != "4242" || g.LeaseToken == "" || g.FundOrderNo == "" {
		t.Fatalf("单据字段不符: %+v", g)
	}
	bill := theBrowser(t, h).lastArgs("Fill")
	if c := bill[0].(paybrowser.Card); c.Number != testCardNo || c.ExpMonth != "12" || c.ExpYear != "30" || c.CVC != testCVV {
		t.Fatalf("填表的卡不符: %+v", c)
	}
	if b := bill[1].(paybrowser.Billing); b.Name != "JOHN DOE" {
		t.Fatalf("持卡人姓名不符: %+v", b)
	}
	assertSubmitCount(t, h, 1)

	reqs := mw.Requests("POST", pathRecharge)
	if len(reqs) != 1 {
		t.Fatalf("应转入 1 次: %d", len(reqs))
	}
	var body marswallex.CardTransferRequest
	if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.CardID != "c1" || body.Amount != "30.00" || body.Currency != "USD" || body.OutOrderNo != g.FundOrderNo {
		t.Fatalf("转入请求不符: %+v", body)
	}
	var cents int64
	var requestedBy string
	if err := db.DB.QueryRow(`SELECT amount_cents, requested_by FROM mw_card_orders WHERE out_order_no = ?`, g.FundOrderNo).Scan(&cents, &requestedBy); err != nil {
		t.Fatal(err)
	}
	if cents != g.FundCents || requestedBy != "lease:"+o.OrderNo {
		t.Fatalf("注资单不符: %d %s", cents, requestedBy)
	}
	if state, used := cardPoolState(t, "c1"); state == cardpool.PoolLeased || used != 1 {
		t.Fatalf("卡应已释放并记一次使用: %s %d", state, used)
	}
	var result, ref string
	if err := db.DB.QueryRow(`SELECT result, order_ref FROM mw_card_outcomes WHERE card_id = 'c1'`).Scan(&result, &ref); err != nil {
		t.Fatal(err)
	}
	if result != cardpool.ResultSuccess || ref != o.OrderNo {
		t.Fatalf("用卡结果不符: %s %s", result, ref)
	}
}

// 真实交易表里的成功授权（平台状态原样入库）也能挡住误判被拒。
func TestEngineRealPoolApprovedAuthNeedsReview(t *testing.T) {
	h, _ := newRealPoolHarness(t)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeDeclined, text: "Your card was declined."})
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	if _, err := db.DB.Exec(`INSERT INTO mw_card_transactions (transaction_id, kind, card_id, status, occurred_at, updated_at)
		VALUES ('A1', 'AUTH', 'c1', 'APPROVED', ?, ?)`, h.clock.Now().Unix()+1, h.clock.Now().Unix()+1); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	g := waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" {
		t.Fatalf("应转待人工: %+v", g)
	}
	list, err := h.svc.Evidence(ctx, o.OrderNo)
	if err != nil || len(list) != 1 || list[0].TransactionID != "A1" {
		t.Fatalf("应能查到这笔授权: %v %+v", err, list)
	}
	if state, _ := cardPoolState(t, "c1"); state == cardpool.PoolLeased {
		t.Fatalf("待人工时卡应已释放: %s", state)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM mw_card_outcomes`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("待人工不计入健康度: %d %v", n, err)
	}
}

// 租约在确认期间到期被卡池回收：真实的 ErrLeaseInvalid 不影响单据结果。
func TestEngineRealPoolLeaseGone(t *testing.T) {
	h, _ := newRealPoolHarness(t)
	h.gpt.accountFn = planFrom(3)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess})
	h.br.submit = func() error {
		_, err := db.DB.Exec(`UPDATE mw_cards SET pool_state = ?, lease_token = '' WHERE card_id = 'c1'`, cardpool.PoolReleasing)
		return err
	}
	h.start(t)
	o := h.submit(t, false)
	g := waitDone(t, o.OrderNo)
	if g.State != StateSucceeded {
		t.Fatalf("应成功: %+v", g)
	}
	waitEvent(t, o.OrderNo, "租约")
}
