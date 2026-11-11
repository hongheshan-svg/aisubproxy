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

// 冻结、注资等单据类操作：平台结果不明时照常返回单据，审计里带上单号和单据状态，看得出结果不明。
func TestOrderActionAuditCarriesOrderState(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	cpInsertCard(t, c, "c1", cardpool.PoolAvailable, "")
	mw.On("PUT", "/v1/card/status", mwtest.Unknown())
	if w := cpDo(AdminCardPoolFreezeCard, "POST", "/c/:id", "/c/c1", ``); w.Code != 200 {
		t.Fatalf("冻结: %d %s", w.Code, w.Body.String())
	}
	a := cpAudit(t, c)
	if !strings.Contains(a, "cardpool.freeze_card card_id=c1 order=") || !strings.Contains(a, "state=unknown") {
		t.Fatalf("冻结审计应带单号和状态: %s", a)
	}

	cpInsertCard(t, c, "c2", cardpool.PoolAvailable, "")
	if w := cpDo(AdminCardPoolLeaseCard, "POST", "/c/:id", "/c/c2", ``); w.Code != 200 {
		t.Fatalf("租用: %d %s", w.Code, w.Body.String())
	}
	mw.On("POST", "/v1/card/recharge-transaction", mwtest.Unknown())
	if w := cpDo(AdminCardPoolFundCard, "POST", "/c/:id", "/c/c2", `{"amount":"1.00"}`); w.Code != 200 {
		t.Fatalf("注资: %d %s", w.Code, w.Body.String())
	}
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.fund_card card_id=c2") || !strings.Contains(a, "amount_cents=100 state=unknown") {
		t.Fatalf("注资审计应带状态: %s", a)
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
