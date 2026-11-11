package handler

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// 卡池操作的审计口径（H2-08）：只要平台可能已生效（结果不明、或已发出但本地没记下）就记 *_failed 审计；
// 明确没生效的（参数错、卡不存在、平台明确拒绝）不记。备注进审计统一截到 200 字。

func TestSetCardLimitFailedAuditOnlyWhenMaybeApplied(t *testing.T) {
	const path = "/v1/card/limit"
	c, mw, _ := cpEnv(t, false)
	cpInsertCard(t, c, "c1", cardpool.PoolAvailable, "")

	if w := cpDo(AdminCardPoolSetCardLimit, "PUT", "/c/:id/limit", "/c/c1/limit", `{"daily_limit":"0"}`); w.Code != 400 {
		t.Fatalf("金额不合法应 400: %d", w.Code)
	}
	if w := cpDo(AdminCardPoolSetCardLimit, "PUT", "/c/:id/limit", "/c/nope/limit", `{"daily_limit":"10"}`); w.Code != 404 {
		t.Fatalf("未知卡应 404: %d", w.Code)
	}
	mw.On("PUT", path, mwtest.Rejected(400, "BAD"))
	if w := cpDo(AdminCardPoolSetCardLimit, "PUT", "/c/:id/limit", "/c/c1/limit", `{"daily_limit":"10"}`); w.Code != 502 {
		t.Fatalf("平台明确拒绝应 502: %d", w.Code)
	}
	if a := cpAudit(t, c); strings.Contains(a, "set_card_limit") {
		t.Fatalf("明确没生效的不应记审计: %s", a)
	}

	mw.On("PUT", path, mwtest.Unknown())
	cpDo(AdminCardPoolSetCardLimit, "PUT", "/c/:id/limit", "/c/c1/limit", `{"daily_limit":"10"}`)
	a := cpAudit(t, c)
	if !strings.Contains(a, "cardpool.set_card_limit_failed card_id=c1") || !strings.Contains(a, "结果不明") {
		t.Fatalf("结果不明应记失败审计并写明结果不明: %s", a)
	}
	if strings.Count(a, "set_card_limit_failed") != 1 {
		t.Fatalf("只应记一条失败审计: %s", a)
	}
}

// 冻结、注资等单据类操作：平台结果不明（单据 state=unknown）记 <动作>_failed，和 set_card_limit_failed 同一口径；
// 确定成功的照旧记成功名。审计都带单号和单据状态。
func TestOrderActionAuditCarriesOrderState(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	cpInsertCard(t, c, "c1", cardpool.PoolAvailable, "")
	mw.On("PUT", "/v1/card/status", mwtest.Unknown())
	if w := cpDo(AdminCardPoolFreezeCard, "POST", "/c/:id", "/c/c1", ``); w.Code != 200 {
		t.Fatalf("冻结: %d %s", w.Code, w.Body.String())
	}
	a := cpAudit(t, c)
	if !strings.Contains(a, "cardpool.freeze_card_failed card_id=c1 order=") || !strings.Contains(a, "state=unknown") {
		t.Fatalf("冻结结果不明应记失败审计，带单号和状态: %s", a)
	}
	if strings.Contains(a, "cardpool.freeze_card card_id=c1") {
		t.Fatalf("结果不明不应再记成功动作名: %s", a)
	}

	cpInsertCard(t, c, "c3", cardpool.PoolAvailable, "")
	mw.On("PUT", "/v1/card/status", mwtest.OK(`{}`))
	if w := cpDo(AdminCardPoolFreezeCard, "POST", "/c/:id", "/c/c3", ``); w.Code != 200 {
		t.Fatalf("冻结: %d %s", w.Code, w.Body.String())
	}
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.freeze_card card_id=c3 order=") || !strings.Contains(a, "state=success") ||
		strings.Contains(a, "freeze_card_failed card_id=c3") {
		t.Fatalf("确定成功的照旧记成功名: %s", a)
	}

	cpInsertCard(t, c, "c2", cardpool.PoolAvailable, "")
	if w := cpDo(AdminCardPoolLeaseCard, "POST", "/c/:id", "/c/c2", ``); w.Code != 200 {
		t.Fatalf("租用: %d %s", w.Code, w.Body.String())
	}
	mw.On("POST", "/v1/card/recharge-transaction", mwtest.Unknown())
	if w := cpDo(AdminCardPoolFundCard, "POST", "/c/:id", "/c/c2", `{"amount":"1.00"}`); w.Code != 200 {
		t.Fatalf("注资: %d %s", w.Code, w.Body.String())
	}
	a = cpAudit(t, c)
	if !strings.Contains(a, "cardpool.fund_card_failed card_id=c2 order=") || !strings.Contains(a, "amount_cents=100 state=unknown") {
		t.Fatalf("注资结果不明应记失败审计并带状态: %s", a)
	}
	if strings.Contains(a, "cardpool.fund_card card_id=c2") {
		t.Fatalf("注资结果不明不应再记成功动作名: %s", a)
	}
}

// 兑换、钱包转账结果不明：同样记 <动作>_failed 并带 state=unknown。
func TestWalletOrderAuditFailedWhenUnknown(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	mw.On("GET", cpPathAccounts, cpTopPage(
		`{"account_no":"M1","account_name":"main","account_status":"ACTIVE","account_type":"MAIN","balance_list":[{"currency":"USDT","available_balance":"100.00","freeze_balance":"0.00"},{"currency":"USD","available_balance":"50.00","freeze_balance":"0.00"}]}`,
		`{"account_no":"S1","account_name":"Ops","account_status":"ACTIVE","account_type":"SUB_ACCOUNT","balance_list":[]}`))
	mw.On("POST", cpPathConversion, mwtest.Unknown())
	if w := cpDo(AdminCardPoolConversion, "POST", "/x", "/x", `{"sell_currency":"USDT","sell_amount":"1.00","buy_currency":"USD"}`); w.Code != 200 {
		t.Fatalf("兑换: %d %s", w.Code, w.Body.String())
	}
	a := cpAudit(t, c)
	if !strings.Contains(a, "cardpool.wallet_conversion_failed out_order_no=MC") || !strings.Contains(a, "state=unknown") ||
		strings.Contains(a, "cardpool.wallet_conversion out_order_no=") {
		t.Fatalf("兑换结果不明应记失败审计: %s", a)
	}

	mw.On("POST", "/v1/transfer", mwtest.Unknown())
	if w := cpDo(AdminCardPoolWalletTransfer, "POST", "/x", "/x",
		`{"payer_account_no":"M1","payee_account_no":"S1","currency":"USD","amount":"10"}`); w.Code != 200 {
		t.Fatalf("转账: %d %s", w.Code, w.Body.String())
	}
	a = cpAudit(t, c)
	if !strings.Contains(a, "cardpool.wallet_transfer_failed out_order_no=MT") || !strings.Contains(a, "currency=USD state=unknown") ||
		strings.Contains(a, "cardpool.wallet_transfer out_order_no=") {
		t.Fatalf("转账结果不明应记失败审计: %s", a)
	}
}

// 批量开卡：返回的单据里有结果不明的，记 apply_cards_failed，带已建张数和结果不明的张数；全部确定的照旧记 apply_cards。
func TestApplyCardsAuditFailedWhenUnknown(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	cpInsertHolder(t, "h1", "APPROVED")
	mw.On("GET", "/v1/card/product", mwtest.OK(`[{"product_id":"p1","card_label":"VISA","card_country":"US","card_mode":"PREPAID","card_currency":"USD"}]`))

	mw.On("POST", "/v2/card", mwtest.OK(`{"transaction_id":"T1"}`))
	if w := cpDo(AdminCardPoolApplyCards, "POST", "/x", "/x", `{"product_id":"p1","card_holder_id":"h1","count":2}`); w.Code != 200 {
		t.Fatalf("开卡: %d %s", w.Code, w.Body.String())
	}
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.apply_cards product_id=p1 card_holder_id=h1 count=2 created=2") ||
		strings.Contains(a, "apply_cards_failed") {
		t.Fatalf("全部确定的记 apply_cards: %s", a)
	}

	mw.On("POST", "/v2/card", mwtest.Unknown())
	if w := cpDo(AdminCardPoolApplyCards, "POST", "/x", "/x", `{"product_id":"p1","card_holder_id":"h1","count":3}`); w.Code != 200 {
		t.Fatalf("开卡: %d %s", w.Code, w.Body.String())
	}
	a := cpAudit(t, c)
	if !strings.Contains(a, "cardpool.apply_cards_failed product_id=p1 card_holder_id=h1 count=3 created=1 unknown=1") {
		t.Fatalf("有结果不明的单据应记 apply_cards_failed: %s", a)
	}
	if strings.Count(a, "count=3") != 1 {
		t.Fatalf("一次开卡只记一条审计: %s", a)
	}
}

// 批量开卡最后一张已发出、本地没记下（ErrMaybeApplied）：也记 apply_cards_failed，结果不明的张数算上这一张。
func TestApplyCardsAuditMaybeApplied(t *testing.T) {
	maybe := fmt.Errorf("%w: disk I/O error", cardpool.ErrMaybeApplied)
	ok := cardpool.Order{State: cardpool.StateProcessing}
	unknown := cardpool.Order{State: cardpool.StateUnknown}
	cases := []struct {
		name         string
		orders       []cardpool.Order
		err          error
		action, tail string
	}{
		{"全部确定", []cardpool.Order{ok, ok}, nil, "apply_cards", ""},
		{"有结果不明", []cardpool.Order{ok, unknown}, nil, "apply_cards_failed", " unknown=1"},
		{"最后一张没记下", []cardpool.Order{ok}, maybe, "apply_cards_failed", " unknown=1 last_result=已发出，本地没记下结果"},
		{"第一张就没记下", nil, maybe, "apply_cards_failed", " unknown=1 last_result=已发出，本地没记下结果"},
		{"建了单后明确出错", []cardpool.Order{ok}, cardpool.ErrNoCard, "apply_cards", ""},
		{"一张没建、明确没生效", nil, fmt.Errorf("%w: x", cardpool.ErrBadRequest), "", ""},
	}
	for _, tc := range cases {
		action, tail := applyCardsAudit(tc.orders, tc.err)
		if action != tc.action || tail != tc.tail {
			t.Errorf("%s: 得到 %q %q，应为 %q %q", tc.name, action, tail, tc.action, tc.tail)
		}
	}
}

// 新建持卡人平台结果不明：记 holder_create_failed 并带 state=unknown；成功的记 holder_create 并带 state。
func TestCreateCardholderAuditUnknownAsFailed(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	mw.On("POST", cpPathCreateHolder, mwtest.Unknown())
	if w := cpDo(AdminCardPoolCreateCardholder, "POST", "/x", "/x", cpProfileJSON(t)); w.Code != 202 {
		t.Fatalf("结果不明: %d %s", w.Code, w.Body.String())
	}
	a := cpAudit(t, c)
	if !strings.Contains(a, "cardpool.holder_create_failed out_card_holder_id=CH") || !strings.Contains(a, "state=unknown") ||
		strings.Contains(a, "cardpool.holder_create out_card_holder_id=") {
		t.Fatalf("新建持卡人结果不明应记失败审计并带状态: %s", a)
	}

	mw.On("POST", cpPathCreateHolder, mwtest.OK(`{"card_holder_id":"h9"}`))
	if w := cpDo(AdminCardPoolCreateCardholder, "POST", "/x", "/x", cpProfileJSON(t)); w.Code != 200 {
		t.Fatalf("成功: %d %s", w.Code, w.Body.String())
	}
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.holder_create out_card_holder_id=CH") || !strings.Contains(a, "card_holder_id=h9 state=linked") {
		t.Fatalf("新建持卡人成功记 holder_create 并带状态: %s", a)
	}
}

// 子账户、持卡人等不走单据的平台操作：结果不明记失败审计（持卡人不带错误原文），明确被拒不记。
func TestWalletAndHolderFailedAudit(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	mw.On("POST", "/v1/accounts", mwtest.Rejected(400, "BAD"))
	cpDo(AdminCardPoolCreateAccount, "POST", "/x", "/x", `{"account_name":"Ops"}`)
	if a := cpAudit(t, c); strings.Contains(a, "wallet_create_account") {
		t.Fatalf("明确被拒不记: %s", a)
	}
	mw.On("POST", "/v1/accounts", mwtest.Unknown())
	cpDo(AdminCardPoolCreateAccount, "POST", "/x", "/x", `{"account_name":"Ops2"}`)
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.wallet_create_account_failed name=Ops2") {
		t.Fatalf("新建子账户结果不明应记失败审计: %s", a)
	}

	cpInsertHolder(t, "h1", "APPROVED")
	mw.On("PUT", "/v1/cardholder/status/h1", mwtest.Reply{Status: 500, Body: `{"code":"E","message":"secret-profile-text"}`})
	cpDo(AdminCardPoolCardholderStatus, "POST", "/h/:id/status", "/h/h1/status", `{"status":"DISABLED"}`)
	a := cpAudit(t, c)
	if !strings.Contains(a, "cardpool.holder_status_failed card_holder_id=h1 status=DISABLED") {
		t.Fatalf("持卡人状态结果不明应记失败审计: %s", a)
	}
	if strings.Contains(a, "secret-profile-text") {
		t.Fatalf("持卡人审计不应带平台错误原文: %s", a)
	}
}

// 已发出、本地没记下结果的错误（cardpool.ErrMaybeApplied）也算平台可能已生效。
func TestPoolMaybeApplied(t *testing.T) {
	if !poolMaybeApplied(fmt.Errorf("%w: disk I/O error", cardpool.ErrMaybeApplied)) {
		t.Fatal("ErrMaybeApplied 应算可能已生效")
	}
	if !poolMaybeApplied(fmt.Errorf("x: %w", cardpool.ErrHolderAccepted)) {
		t.Fatal("平台已受理应算可能已生效")
	}
	for _, err := range []error{cardpool.ErrNotFound, cardpool.ErrCardBusy, errors.New("db"), fmt.Errorf("%w: x", cardpool.ErrBadRequest)} {
		if poolMaybeApplied(err) {
			t.Fatalf("%v 不应算可能已生效", err)
		}
	}
	if code, body := poolErrorStatus(fmt.Errorf("%w: disk I/O error", cardpool.ErrMaybeApplied)); code != 500 ||
		body["error"] != cardpool.ErrMaybeApplied.Error() {
		t.Fatalf("应 500 并提示可能已生效、不带内部错误: %d %v", code, body)
	}
}

// 确认失败的备注进审计统一截到 200 字。
func TestConfirmFailedNoteTruncatedInAudit(t *testing.T) {
	c, _, _ := cpEnv(t, false)
	if _, err := c.Exec(`INSERT INTO mw_card_orders (out_order_no, kind, card_id, state, created_at, updated_at)
		VALUES ('MWR1', 'recharge', 'c1', 'needs_review', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	note := strings.Repeat("核", 300)
	if w := cpDo(AdminCardPoolConfirmFailed, "POST", "/o/:no", "/o/MWR1", `{"note":"`+note+`"}`); w.Code != 200 {
		t.Fatalf("确认失败: %d %s", w.Code, w.Body.String())
	}
	a := cpAudit(t, c)
	i := strings.Index(a, "note=")
	if i < 0 {
		t.Fatalf("审计缺少备注: %s", a)
	}
	got := strings.TrimSpace(a[i+len("note="):])
	if n := utf8.RuneCountInString(got); n != 200 {
		t.Fatalf("备注应截到 200 字，得到 %d", n)
	}
}

// 平台已建好子账户、本地读回失败：也算可能已生效，记失败审计并回固定提示（不带数据库错误）。
func TestCreateAccountReadBackFailureAudited(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	if _, err := c.Exec(`CREATE TRIGGER corrupt_acct AFTER INSERT ON mw_accounts BEGIN
		UPDATE mw_accounts SET created_at = 'bad' WHERE account_no = NEW.account_no; END`); err != nil {
		t.Fatal(err)
	}
	mw.On("POST", "/v1/accounts", mwtest.OK(`{"account_no":"S1","account_name":"Ops","out_account_no":"ACX","status":"ACTIVE"}`))
	w := cpDo(AdminCardPoolCreateAccount, "POST", "/x", "/x", `{"account_name":"Ops"}`)
	if w.Code != 500 || mwJSON(t, w)["error"] != cardpool.ErrMaybeApplied.Error() {
		t.Fatalf("应 500 并提示可能已生效: %d %s", w.Code, w.Body.String())
	}
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.wallet_create_account_failed name=Ops result=已发出，本地没记下结果") {
		t.Fatalf("应记失败审计: %s", a)
	}
}
