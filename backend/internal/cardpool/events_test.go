package cardpool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

var eventSeq atomic.Int64

// enqueueEvent 入库一条事件；data 原样拼入原文（对象或 JSON 字符串）。返回事件 id。
func enqueueEvent(t *testing.T, topic, data string) int64 {
	t.Helper()
	msgID := fmt.Sprintf("msg-%d", eventSeq.Add(1))
	payload := `{"topic":"` + topic + `","message_id":"` + msgID + `","data":` + data + `}`
	return enqueueRaw(t, msgID, topic, payload)
}

// enqueueRaw 按原文入库一条事件，收到时间（received_at）取测试时钟当前时刻，返回事件 id。
func enqueueRaw(t *testing.T, msgID, topic, payload string) int64 {
	t.Helper()
	ok, err := db.InsertMarsWallexWebhookEvent(msgID, topic, payload)
	if err != nil || !ok {
		t.Fatalf("入库事件 %s: ok=%v err=%v", msgID, ok, err)
	}
	var id int64
	if err := db.DB.QueryRow(`SELECT id FROM marswallex_webhook_events WHERE message_id = ?`, msgID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// 与 CURRENT_TIMESTAMP 的写法一致（UTC，精确到秒）
	if _, err := db.DB.Exec(`UPDATE marswallex_webhook_events SET received_at = ? WHERE id = ?`,
		testNow().UTC().Format("2006-01-02 15:04:05"), id); err != nil {
		t.Fatal(err)
	}
	return id
}

type eventState struct {
	processed       bool
	note, procErr   string
	payload         string
	attempts        int
	nextAttemptAt   *int64
	processedAtTime sql.NullTime
}

func getEvent(t *testing.T, id int64) eventState {
	t.Helper()
	var e eventState
	var pe sql.NullString
	if err := db.DB.QueryRow(`SELECT processed_at, process_note, process_error, payload, process_attempts, next_attempt_at
		FROM marswallex_webhook_events WHERE id = ?`, id).
		Scan(&e.processedAtTime, &e.note, &pe, &e.payload, &e.attempts, &e.nextAttemptAt); err != nil {
		t.Fatalf("读取事件 %d: %v", id, err)
	}
	e.processed = e.processedAtTime.Valid
	e.procErr = pe.String
	return e
}

func mustProcessEvents(t *testing.T, s *Service) int {
	t.Helper()
	n, err := s.processEventsOnce(context.Background())
	if err != nil {
		t.Fatalf("processEventsOnce: %v", err)
	}
	return n
}

// mustBeProcessed 检查事件已处理、没有 process_error，备注包含 noteWant。
func mustBeProcessed(t *testing.T, id int64, noteWant string) eventState {
	t.Helper()
	e := getEvent(t, id)
	if !e.processed || e.procErr != "" || !strings.Contains(e.note, noteWant) {
		t.Fatalf("事件 %d: processed=%v err=%q note=%q，期望备注含 %q", id, e.processed, e.procErr, e.note, noteWant)
	}
	return e
}

func mustGetTxn(t *testing.T, id string) Transaction {
	t.Helper()
	var x Transaction
	if err := x.scan(db.DB.QueryRow(`SELECT `+txnCols+` FROM mw_card_transactions WHERE transaction_id = ?`, id)); err != nil {
		t.Fatalf("读取交易 %s: %v", id, err)
	}
	return x
}

func countRows(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func alertsContaining(a *alertLog, sub string) int {
	n := 0
	for _, s := range a.Texts() {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

func TestApplyResultEventFinalizes(t *testing.T) {
	s, _, _, alerts := newTestService(t)
	o1 := insertOrder(t, Order{Kind: KindApply, CardHolderID: "h1", ProductID: "p1", Currency: "USD", State: StateProcessing, PlatformTxnID: "T1"})
	o2 := insertOrder(t, Order{Kind: KindApply, CardHolderID: "h1", ProductID: "p1", Currency: "USD", State: StateProcessing, PlatformTxnID: "T2"})
	o3 := insertOrder(t, Order{Kind: KindApply, CardHolderID: "h1", ProductID: "p1", Currency: "USD", State: StateUnknown, PlatformTxnID: "T3"})

	e1 := enqueueEvent(t, marswallex.TopicCardApplyResult,
		`{"out_order_no":"`+o1.OutOrderNo+`","card_id":"c1","transaction_id":"T1","status":"SUCCESS"}`)
	e2 := enqueueEvent(t, marswallex.TopicCardApplyResult,
		`{"out_order_no":"`+o2.OutOrderNo+`","transaction_id":"T2","status":"FAILED","failure_reason":"KYC 不通过"}`)
	// 没有 out_order_no：按平台交易号找单据
	e3 := enqueueEvent(t, marswallex.TopicCardApplyResult, `{"card_id":"c3","transaction_id":"T3","status":"SUCCESS"}`)
	e4 := enqueueEvent(t, marswallex.TopicCardApplyResult, `{"out_order_no":"MA000000000000000000ff","card_id":"c9","status":"SUCCESS"}`)

	if n := mustProcessEvents(t, s); n != 4 {
		t.Fatalf("处理了 %d 条，期望 4", n)
	}
	if o := mustGetOrder(t, o1.OutOrderNo); o.State != StateSuccess || o.CardID != "c1" {
		t.Fatalf("o1: %+v", o)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolReleasing || c.ProductID != "p1" || c.CardHolderID != "h1" {
		t.Fatalf("c1 应入池: %+v", c)
	}
	if o := mustGetOrder(t, o2.OutOrderNo); o.State != StateFailed || o.LastError != "KYC 不通过" {
		t.Fatalf("o2: %+v", o)
	}
	if o := mustGetOrder(t, o3.OutOrderNo); o.State != StateSuccess || o.CardID != "c3" {
		t.Fatalf("o3: %+v", o)
	}
	for _, id := range []int64{e1, e2, e3} {
		mustBeProcessed(t, id, "")
	}
	mustBeProcessed(t, e4, "无对应本地单据")
	if _, err := getCard(context.Background(), "c9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("没有单据的开卡结果不应入池: %v", err)
	}
	if alerts.Count() != 0 {
		t.Fatalf("不应告警: %v", alerts.Texts())
	}
}

func TestTransferResultEvent(t *testing.T) {
	s, mw, _, alerts := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolLeased})
	insertCard(t, Card{CardID: "c2", ProductID: "p1"})
	insertCard(t, Card{CardID: "c3", ProductID: "p1"})
	insertCard(t, Card{CardID: "c4", ProductID: "p1", PoolState: PoolReleasing})
	insertCard(t, Card{CardID: "c5", ProductID: "p1", PoolState: PoolReleasing})
	oOK := insertOrder(t, Order{Kind: KindRecharge, CardID: "c1", AmountCents: 500, Currency: "USD", State: StateProcessing})
	oApply := insertOrder(t, Order{Kind: KindWithdraw, CardID: "c4", AmountCents: 100, Currency: "USD", State: StatePending})
	oFail := insertOrder(t, Order{Kind: KindWithdraw, CardID: "c5", AmountCents: 100, Currency: "USD", State: StateUnknown})
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "5.00", "0.00", "ACTIVE")))
	mw.On("GET", detailPath("c2"), mwtest.OK(detailJSON("c2", "3.21", "0.00", "ACTIVE")))
	mw.On("GET", detailPath("c3"), mwtest.Rejected(404, "NOT_FOUND"))
	mw.On("GET", detailPath("c5"), mwtest.OK(detailJSON("c5", "1.00", "0.00", "ACTIVE")))

	transfer := func(no, card, status string) string {
		return `{"out_order_no":"` + no + `","transaction_id":"TX-` + card + `","card_id":"` + card + `","status":"` + status + `"}`
	}
	e1 := enqueueEvent(t, marswallex.TopicCardTransferResult, transfer(oOK.OutOrderNo, "c1", "SUCCESS"))
	e2 := enqueueEvent(t, marswallex.TopicCardTransferResult, transfer(oApply.OutOrderNo, "c4", "APPLY"))
	e3 := enqueueEvent(t, marswallex.TopicCardTransferResult, transfer(oFail.OutOrderNo, "c5", "FAIL"))
	e4 := enqueueEvent(t, marswallex.TopicCardTransferResult, transfer("MI000000000000000000aa", "c2", "SUCCESS"))
	e5 := enqueueEvent(t, marswallex.TopicCardTransferResult, transfer("", "c3", "SUCCESS"))
	e6 := enqueueEvent(t, marswallex.TopicCardTransferResult, transfer("", "cX", "SUCCESS"))

	if n := mustProcessEvents(t, s); n != 6 {
		t.Fatalf("处理了 %d 条，期望 6", n)
	}
	if o := mustGetOrder(t, oOK.OutOrderNo); o.State != StateSuccess || o.PlatformTxnID != "TX-c1" {
		t.Fatalf("转入单: %+v", o)
	}
	if c := mustGetCard(t, "c1"); c.AvailableCents != 500 {
		t.Fatalf("转入成功后应刷新余额: %+v", c)
	}
	if o := mustGetOrder(t, oApply.OutOrderNo); o.State != StateProcessing {
		t.Fatalf("APPLY 应为 processing: %+v", o)
	}
	if o := mustGetOrder(t, oFail.OutOrderNo); o.State != StateFailed {
		t.Fatalf("FAIL 应为 failed: %+v", o)
	}
	mustBeProcessed(t, e1, "")
	mustBeProcessed(t, e2, "")
	mustBeProcessed(t, e3, "")
	// 没有本地单据：卡在池中就刷新余额
	mustBeProcessed(t, e4, "余额已刷新")
	if c := mustGetCard(t, "c2"); c.AvailableCents != 321 || c.BalanceSyncedAt == nil {
		t.Fatalf("c2 余额应已刷新: %+v", c)
	}
	// 刷新失败只写备注，不重试
	mustBeProcessed(t, e5, "刷新余额失败")
	mustBeProcessed(t, e6, "无对应本地单据")
	if got := len(mw.Requests("GET", detailPath("c4"))); got != 0 {
		t.Fatalf("APPLY 不应刷新余额，请求了 %d 次", got)
	}
	if alerts.Count() != 0 {
		t.Fatalf("不应告警: %v", alerts.Texts())
	}
}

func TestStatusChangedOursVsExternal(t *testing.T) {
	s, _, clock, alerts := newTestService(t)
	now := clock.Now().Unix()
	recent, old := now-100, now-601

	// 我们发起的冻结，单据尚未定论 → 定论为成功
	insertCard(t, Card{CardID: "u1", ProductID: "p1", PoolState: PoolUsed})
	o1 := insertOrder(t, Order{Kind: KindStatus, CardID: "u1", TargetStatus: "REVOKED", State: StateUnknown})
	// 我们发起的冻结，600 秒内已成功 → 只记备注
	insertCard(t, Card{CardID: "u2", ProductID: "p1", PoolState: PoolUsed, PlatformStatus: "REVOKED", FrozenAt: &recent})
	o2 := insertOrder(t, Order{Kind: KindStatus, CardID: "u2", TargetStatus: "REVOKED", State: StateSuccess, FinishedAt: &recent})
	// 外部冻结
	insertCard(t, Card{CardID: "a1", ProductID: "p1"})
	insertCard(t, Card{CardID: "l1", ProductID: "p1", PoolState: PoolLeased, LeaseToken: "tok"})
	// 同卡的改状态单早已成功（超过 600 秒）→ 按外部变更处理
	insertCard(t, Card{CardID: "a2", ProductID: "p1"})
	insertOrder(t, Order{Kind: KindStatus, CardID: "a2", TargetStatus: "REVOKED", State: StateSuccess, FinishedAt: &old})
	// 外部注销
	insertCard(t, Card{CardID: "x1", ProductID: "p1"})
	insertCard(t, Card{CardID: "x2", ProductID: "p1", PoolState: PoolLeased, LeaseToken: "tok2"})
	// 外部恢复 ACTIVE → 只更新平台状态
	insertCard(t, Card{CardID: "d1", ProductID: "p1", PoolState: PoolDisabled, PlatformStatus: "REVOKED", DisabledReason: "平台冻结"})

	ev := func(card, status string) int64 {
		return enqueueEvent(t, marswallex.TopicCardStatusChanged, `{"card_id":"`+card+`","card_status":"`+status+`"}`)
	}
	eU1, eU2 := ev("u1", "REVOKED"), ev("u2", "REVOKED")
	eA1, eL1, eA2 := ev("a1", "REVOKED"), ev("l1", "REVOKED"), ev("a2", "REVOKED")
	eX1, eX2 := ev("x1", "CANCELED"), ev("x2", "CANCELED")
	eD1, eNone := ev("d1", "ACTIVE"), ev("nope", "REVOKED")

	if n := mustProcessEvents(t, s); n != 9 {
		t.Fatalf("处理了 %d 条，期望 9", n)
	}

	if o := mustGetOrder(t, o1.OutOrderNo); o.State != StateSuccess {
		t.Fatalf("我们发起的改状态单应定论为成功: %+v", o)
	}
	if c := mustGetCard(t, "u1"); c.PoolState != PoolUsed || c.PlatformStatus != "REVOKED" || c.FrozenAt == nil {
		t.Fatalf("u1: %+v", c)
	}
	mustBeProcessed(t, eU1, o1.OutOrderNo)
	if o := mustGetOrder(t, o2.OutOrderNo); o.State != StateSuccess || *o.FinishedAt != recent {
		t.Fatalf("o2 不应变化: %+v", o)
	}
	if c := mustGetCard(t, "u2"); c.PoolState != PoolUsed {
		t.Fatalf("u2: %+v", c)
	}
	mustBeProcessed(t, eU2, o2.OutOrderNo)

	if c := mustGetCard(t, "a1"); c.PoolState != PoolDisabled || c.DisabledReason != "平台冻结" || c.PlatformStatus != "REVOKED" {
		t.Fatalf("a1: %+v", c)
	}
	if c := mustGetCard(t, "l1"); c.PoolState != PoolLeased || c.LeaseToken != "tok" || c.PlatformStatus != "REVOKED" {
		t.Fatalf("l1 只告警，不改池状态: %+v", c)
	}
	if c := mustGetCard(t, "a2"); c.PoolState != PoolDisabled {
		t.Fatalf("a2: %+v", c)
	}
	if c := mustGetCard(t, "x1"); c.PoolState != PoolRetired || c.PlatformStatus != "CANCELED" {
		t.Fatalf("x1: %+v", c)
	}
	if c := mustGetCard(t, "x2"); c.PoolState != PoolRetired || c.PlatformStatus != "CANCELED" {
		t.Fatalf("x2: %+v", c)
	}
	if c := mustGetCard(t, "d1"); c.PoolState != PoolDisabled || c.PlatformStatus != "ACTIVE" {
		t.Fatalf("d1: %+v", c)
	}
	for _, id := range []int64{eA1, eL1, eA2, eX1, eX2, eD1} {
		mustBeProcessed(t, id, "")
	}
	mustBeProcessed(t, eNone, "卡不在池中")

	if alerts.Count() != 4 {
		t.Fatalf("期望 4 条告警（a1、l1、a2 冻结，x2 注销），实际: %v", alerts.Texts())
	}
	for _, id := range []string{"a1", "l1", "a2", "x2"} {
		if alertsContaining(alerts, "卡 "+id) != 1 {
			t.Fatalf("缺少卡 %s 的告警: %v", id, alerts.Texts())
		}
	}
	// 重复推送不重复告警、不改变结果
	ev("a1", "REVOKED")
	mustProcessEvents(t, s)
	if alerts.Count() != 4 {
		t.Fatalf("重复推送不应再告警: %v", alerts.Texts())
	}
}

func TestCardholderStatusChangeEvent(t *testing.T) {
	s, _, clock, alerts := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	insertHolder(t, "h2", "PENDING")
	ev := func(id, status, remark string) int64 {
		return enqueueEvent(t, marswallex.TopicCardholderStatusChange,
			`{"card_holder_id":"`+id+`","status":"`+status+`","remark":"`+remark+`","update_time":1793160000}`)
	}
	e1, e2, e3 := ev("h1", "DISABLED", "风控冻结"), ev("h2", "approved", ""), ev("h9", "REJECTED", "资料不符")
	clock.Advance(time.Minute)
	if n := mustProcessEvents(t, s); n != 3 {
		t.Fatalf("处理了 %d 条，期望 3", n)
	}
	h1, err := getCardholder(context.Background(), "h1")
	if err != nil {
		t.Fatal(err)
	}
	if h1.Status != "DISABLED" || h1.Remark != "风控冻结" || h1.SyncedAt == nil || *h1.SyncedAt != clock.Now().Unix() {
		t.Fatalf("h1: %+v", h1)
	}
	h2, err := getCardholder(context.Background(), "h2")
	if err != nil {
		t.Fatal(err)
	}
	if h2.Status != "APPROVED" {
		t.Fatalf("h2: %+v", h2)
	}
	if _, err := getCardholder(context.Background(), "h9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未导入的持卡人不应写入: %v", err)
	}
	mustBeProcessed(t, e1, "")
	mustBeProcessed(t, e2, "")
	mustBeProcessed(t, e3, "未导入")
	if alerts.Count() != 1 || alertsContaining(alerts, "h1") != 1 {
		t.Fatalf("应只对 h1 告警一次: %v", alerts.Texts())
	}
}

func authData(txnID, card, status, extra string) string {
	return `{"transaction_id":"` + txnID + `","transaction_type":"AUTH","card_id":"` + card + `","status":"` + status +
		`","transaction_amount":"1.00","transaction_currency":"USD","billing_amount":"1.00","billing_currency":"USD",` +
		`"merchant":{"merchant_name":"SHOP"},"transaction_time":1793160000` + extra + `}`
}

func TestAuthOnUnusedAvailableCardDisables(t *testing.T) {
	s, _, _, alerts := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	id := enqueueEvent(t, marswallex.TopicAuthEvent, authData("A1", "c1", "APPROVED", ""))
	mustProcessEvents(t, s)
	mustBeProcessed(t, id, "")

	x := mustGetTxn(t, "A1")
	if x.Kind != "AUTH" || x.CardID != "c1" || x.Status != "APPROVED" || x.MerchantName != "SHOP" ||
		x.TxnAmount != "1.00" || x.BillingCurrency != "USD" || x.OccurredAt == nil || *x.OccurredAt != 1793160000 {
		t.Fatalf("交易: %+v", x)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolDisabled || c.DisabledReason != "未交出卡号出现授权" {
		t.Fatalf("c1 应停用: %+v", c)
	}
	if alerts.Count() != 1 || alertsContaining(alerts, "c1") != 1 {
		t.Fatalf("应告警一次: %v", alerts.Texts())
	}
}

func TestAuthOnUsedCardOnlyRecords(t *testing.T) {
	s, _, _, alerts := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", UseCount: 1})
	insertCard(t, Card{CardID: "c2", ProductID: "p1", PoolState: PoolLeased, LeaseToken: "tok"})
	e1 := enqueueEvent(t, marswallex.TopicAuthEvent, authData("A1", "c1", "APPROVED", ""))
	e2 := enqueueEvent(t, marswallex.TopicAuthEvent, authData("A2", "c2", "APPROVED", ""))
	e3 := enqueueEvent(t, marswallex.TopicAuthEvent, authData("A3", "not-in-pool", "DECLINED", `,"decline_reason":"INSUFFICIENT"`))
	if n := mustProcessEvents(t, s); n != 3 {
		t.Fatalf("处理了 %d 条，期望 3", n)
	}
	for _, id := range []int64{e1, e2, e3} {
		mustBeProcessed(t, id, "")
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolAvailable {
		t.Fatalf("c1 不应变化: %+v", c)
	}
	if c := mustGetCard(t, "c2"); c.PoolState != PoolLeased {
		t.Fatalf("c2 不应变化: %+v", c)
	}
	mustGetTxn(t, "A1")
	mustGetTxn(t, "A2")
	if x := mustGetTxn(t, "A3"); x.DeclineReason != "INSUFFICIENT" || x.CardID != "not-in-pool" {
		t.Fatalf("卡不在池中也应记录: %+v", x)
	}
	if alerts.Count() != 0 {
		t.Fatalf("不应告警: %v", alerts.Texts())
	}
}

func TestTxnUpsertOverwritesPending(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	enqueueEvent(t, marswallex.TopicAuthEvent, authData("A1", "cz", "PENDING", ""))
	mustProcessEvents(t, s)
	if x := mustGetTxn(t, "A1"); x.Status != "PENDING" {
		t.Fatalf("%+v", x)
	}
	clock.Advance(time.Minute)
	enqueueEvent(t, marswallex.TopicAuthEvent, `{"transaction_id":"A1","transaction_type":"AUTH","card_id":"cz","status":"APPROVED",`+
		`"transaction_amount":"1.05","transaction_currency":"USD","billing_amount":"1.05","billing_currency":"USD",`+
		`"merchant":{"merchant_name":"SHOP2"},"transaction_time":1793160060}`)
	// 结算推送：merchant 缺省、没有发生时间
	enqueueEvent(t, marswallex.TopicSettleEvent, `{"transaction_id":"S1","transaction_type":"SETTLE","auth_transaction_id":"A1",`+
		`"card_id":"cz","status":"SUCCESS","transaction_amount":"1.05","transaction_currency":"USD","billing_amount":"1.05","billing_currency":"USD"}`)
	if n := mustProcessEvents(t, s); n != 2 {
		t.Fatalf("处理了 %d 条，期望 2", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_card_transactions WHERE transaction_id = 'A1'`); n != 1 {
		t.Fatalf("A1 应只有 1 行，实际 %d", n)
	}
	x := mustGetTxn(t, "A1")
	if x.Status != "APPROVED" || x.TxnAmount != "1.05" || x.MerchantName != "SHOP2" || *x.OccurredAt != 1793160060 ||
		x.UpdatedAt != clock.Now().Unix() {
		t.Fatalf("A1 应被覆盖: %+v", x)
	}
	st := mustGetTxn(t, "S1")
	if st.Kind != "SETTLE" || st.AuthTransactionID != "A1" || st.MerchantName != "" || st.OccurredAt != nil {
		t.Fatalf("S1: %+v", st)
	}
}

func TestUnknownTopicAndUnparseable(t *testing.T) {
	s, _, _, alerts := newTestService(t)
	e1 := enqueueEvent(t, "SOMETHING_NEW", `{}`)
	e2 := enqueueEvent(t, marswallex.TopicAccountStatusChanged, `{"account_no":"1","account_status":"ACTIVE"}`)
	e3 := enqueueEvent(t, marswallex.TopicConversionResult, `{}`)
	e4 := enqueueEvent(t, marswallex.TopicTransferResult, `{}`)
	e5 := enqueueRaw(t, "raw-sha", "", "not json at all")
	e6 := enqueueEvent(t, marswallex.TopicCardStatusChanged, `{"card_id":123}`)
	if n := mustProcessEvents(t, s); n != 6 {
		t.Fatalf("处理了 %d 条，期望 6", n)
	}
	mustBeProcessed(t, e1, "未知 topic")
	mustBeProcessed(t, e2, "本子项目不处理")
	mustBeProcessed(t, e3, "本子项目不处理")
	mustBeProcessed(t, e4, "本子项目不处理")
	if e := mustBeProcessed(t, e5, "无法解析"); e.payload != "not json at all" {
		t.Fatalf("原文不应改动: %q", e.payload)
	}
	if e := mustBeProcessed(t, e6, "数据无法解析"); e.attempts != 0 {
		t.Fatalf("数据无法解析不应计入重试: %+v", e)
	}
	if alerts.Count() != 1 || alertsContaining(alerts, fmt.Sprint(e6)) != 1 {
		t.Fatalf("数据无法解析应告警一次: %v", alerts.Texts())
	}
	if n := mustProcessEvents(t, s); n != 0 {
		t.Fatalf("已处理的事件不应再取出: %d", n)
	}
}

func TestEventRetryBackoffAndStop(t *testing.T) {
	s, _, clock, alerts := newTestService(t)
	var calls int
	s.eventHandler = func(_ context.Context, _ *queuedEvent, ev *marswallex.WebhookEvent) (string, error) {
		calls++
		if ev.Topic == marswallex.TopicCard3DSOTP {
			return "", errors.New("boom 123456") // 错误里带了验证码：写入前要抹掉
		}
		return "", errors.New("boom")
	}
	id := enqueueEvent(t, marswallex.TopicCardStatusChanged, `{"card_id":"c1","card_status":"ACTIVE"}`)
	otpID := enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c1", "123456"))
	want := []int64{10, 30, 60, 120, 300, 300, 300, 300, 300, 300}
	for i, d := range want {
		if n := mustProcessEvents(t, s); n != 2 {
			t.Fatalf("第 %d 次：处理了 %d 条", i+1, n)
		}
		e := getEvent(t, id)
		if e.processed || e.attempts != i+1 || e.procErr != "boom" || e.nextAttemptAt == nil ||
			*e.nextAttemptAt != clock.Now().Unix()+d {
			t.Fatalf("第 %d 次: %+v（期望 +%ds）", i+1, e, d)
		}
		eo := getEvent(t, otpID)
		if eo.processed || eo.attempts != i+1 || eo.procErr != "boom ***" {
			t.Fatalf("第 %d 次（验证码事件）: %+v", i+1, eo)
		}
		// 重试还要用到验证码：停止重试前不抹原文，停止时抹掉
		if last := i == len(want)-1; strings.Contains(eo.payload, "123456") == last {
			t.Fatalf("第 %d 次：原文 %s", i+1, eo.payload)
		}
		if n := mustProcessEvents(t, s); n != 0 {
			t.Fatalf("第 %d 次后未到重试时间不应再处理", i+1)
		}
		if i < len(want)-1 && alerts.Count() != 0 {
			t.Fatalf("未满 10 次不应告警: %v", alerts.Texts())
		}
		clock.Advance(time.Duration(d) * time.Second)
	}
	clock.Advance(time.Hour)
	if n := mustProcessEvents(t, s); n != 0 {
		t.Fatalf("满 10 次后不应再处理: %d", n)
	}
	if calls != 20 {
		t.Fatalf("处理函数被调用 %d 次，期望 20", calls)
	}
	if alerts.Count() != 2 || alertsContaining(alerts, fmt.Sprintf("#%d（", id)) != 1 || alertsContaining(alerts, fmt.Sprintf("#%d（", otpID)) != 1 {
		t.Fatalf("每条事件应告警一次: %v", alerts.Texts())
	}
	for _, text := range alerts.Texts() {
		if strings.Contains(text, "123456") {
			t.Fatalf("告警里出现验证码: %s", text)
		}
	}
}

func TestReprocessIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, alerts := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolLeased})
	o := insertOrder(t, Order{Kind: KindRecharge, CardID: "c1", AmountCents: 500, Currency: "USD", State: StateProcessing})
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "5.00", "0.00", "ACTIVE")))
	id := enqueueEvent(t, marswallex.TopicCardTransferResult,
		`{"out_order_no":"`+o.OutOrderNo+`","transaction_id":"TX1","card_id":"c1","status":"SUCCESS"}`)
	mustProcessEvents(t, s)
	first := mustGetOrder(t, o.OutOrderNo)
	if first.State != StateSuccess {
		t.Fatalf("%+v", first)
	}

	clock.Advance(time.Minute)
	if err := s.Reprocess(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.kickEvents:
	default:
		t.Fatal("Reprocess 应通知处理协程")
	}
	if e := getEvent(t, id); e.processed || e.attempts != 0 || e.note != "" || e.procErr != "" || e.nextAttemptAt != nil {
		t.Fatalf("重新处理前应清空: %+v", e)
	}
	if n := mustProcessEvents(t, s); n != 1 {
		t.Fatalf("处理了 %d 条", n)
	}
	mustBeProcessed(t, id, "")
	again := mustGetOrder(t, o.OutOrderNo)
	if again.State != StateSuccess || *again.FinishedAt != *first.FinishedAt {
		t.Fatalf("重复处理不应改动单据: %+v", again)
	}
	if alerts.Count() != 0 {
		t.Fatalf("不应有冲突告警: %v", alerts.Texts())
	}
	if err := s.Reprocess(ctx, 99999, "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的事件: %v", err)
	}
}
