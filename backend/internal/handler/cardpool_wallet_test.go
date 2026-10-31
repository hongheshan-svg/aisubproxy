package handler

import (
	"encoding/json"
	"fmt"
	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

const (
	cpPathAccounts   = "/v1/accounts/page"
	cpPathConversion = "/v1/conversion"
	cpPathSerials    = "/v1/accounts/serials"
	cpPathAuthTxns   = "/v1/card/transactions/authorization"
	cpPathSettleTxns = "/v1/card/transactions/settlement"
)

// cpTopPage 生成顶层 {count, data} 分页响应（账户、流水、交易查询均为此格式）。
func cpTopPage(items ...string) mwtest.Reply {
	return mwtest.Reply{Body: fmt.Sprintf(`{"count":"%d","data":[%s]}`, len(items), strings.Join(items, ","))}
}

// cpMockAccounts 编排账户同步：主钱包 M1（USDT 100.00）。
func cpMockAccounts(mw *mwtest.Server) {
	mw.On("GET", cpPathAccounts, cpTopPage(
		`{"account_no":"M1","account_name":"main","account_status":"ACTIVE","account_type":"MAIN",`+
			`"balance_list":[{"currency":"USDT","available_balance":"100.00","freeze_balance":"0.00"}]}`))
}

func TestWalletRoutesRequireService(t *testing.T) {
	mwSetup(t)
	SetCardPool(nil)
	cases := []struct {
		name   string
		h      gin.HandlerFunc
		method string
	}{
		{"accounts", AdminCardPoolAccounts, "GET"},
		{"create account", AdminCardPoolCreateAccount, "POST"},
		{"account status", AdminCardPoolAccountStatus, "POST"},
		{"receiving addresses", AdminCardPoolReceivingAddresses, "GET"},
		{"serials", AdminCardPoolSerials, "GET"},
		{"conversion", AdminCardPoolConversion, "POST"},
		{"wallet transfer", AdminCardPoolWalletTransfer, "POST"},
		{"upload photo", AdminCardPoolUploadPhoto, "POST"},
		{"create cardholder", AdminCardPoolCreateCardholder, "POST"},
		{"cardholder profile", AdminCardPoolCardholderProfile, "GET"},
		{"update cardholder", AdminCardPoolUpdateCardholder, "PUT"},
		{"cardholder status", AdminCardPoolCardholderStatus, "POST"},
		{"submissions", AdminCardPoolSubmissions, "GET"},
		{"abandon submission", AdminCardPoolAbandonSubmission, "POST"},
		{"backfill status", AdminCardPoolBackfillStatus, "GET"},
		{"backfill", AdminCardPoolBackfill, "POST"},
		{"card transfers", AdminCardPoolCardTransfers, "GET"},
	}
	for _, tc := range cases {
		w := cpDo(tc.h, tc.method, "/x", "/x", `{}`)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body.String())
		}
	}
}

func TestConversionParsesAmountAndAudits(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	for _, amt := range []string{"1.005", "0", "abc", "", "-1.00", "1234567890123"} {
		w := cpDo(AdminCardPoolConversion, "POST", "/x", "/x",
			`{"sell_currency":"USDT","sell_amount":"`+amt+`","buy_currency":"USD"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("amount %q: %d %s", amt, w.Code, w.Body.String())
		}
	}
	// 金额必须是字符串
	if w := cpDo(AdminCardPoolConversion, "POST", "/x", "/x", `{"sell_currency":"USDT","sell_amount":12.5,"buy_currency":"USD"}`); w.Code != 400 {
		t.Errorf("numeric amount: %d %s", w.Code, w.Body.String())
	}
	if n := mwCount(t, c, `SELECT COUNT(*) FROM mw_card_orders`); n != 0 {
		t.Fatalf("orders=%d", n)
	}
	if n := len(mw.Requests("POST", cpPathConversion)); n != 0 {
		t.Fatalf("conversion requests=%d", n)
	}

	cpMockAccounts(mw)
	mw.On("POST", cpPathConversion, mwtest.OK(`{"transaction_id":"CV1"}`))
	w := cpDo(AdminCardPoolConversion, "POST", "/x", "/x", `{"sell_currency":"USDT","sell_amount":"12.50","buy_currency":"USD"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("conversion %d %s", w.Code, w.Body.String())
	}
	m := mwJSON(t, w)
	no, _ := m["out_order_no"].(string)
	if !strings.HasPrefix(no, "MC") || m["kind"] != "conversion" || m["amount_cents"] != float64(1250) || m["state"] != "processing" {
		t.Fatalf("body %v", m)
	}
	if n := mwCount(t, c, `SELECT COUNT(*) FROM mw_card_orders WHERE kind='conversion' AND amount_cents=1250 AND currency='USDT' AND buy_currency='USD'`); n != 1 {
		t.Errorf("orders with 1250 cents = %d", n)
	}
	reqs := mw.Requests("POST", cpPathConversion)
	if len(reqs) != 1 {
		t.Fatalf("conversion requests=%d", len(reqs))
	}
	var sent map[string]string
	if err := json.Unmarshal(reqs[0].Body, &sent); err != nil || sent["sell_amount"] != "12.50" {
		t.Errorf("sent %s %v", reqs[0].Body, err)
	}
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.wallet_conversion out_order_no="+no+" sell=12.50USDT buy=USD") {
		t.Errorf("audit %q", a)
	}
}

func TestInflightMapsTo409(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	cpMockAccounts(mw)
	mw.On("POST", cpPathConversion, mwtest.Unknown())
	body := `{"sell_currency":"USDT","sell_amount":"1.00","buy_currency":"USD"}`
	first := cpDo(AdminCardPoolConversion, "POST", "/x", "/x", body)
	if first.Code != http.StatusOK || mwJSON(t, first)["state"] != "unknown" {
		t.Fatalf("first %d %s", first.Code, first.Body.String())
	}
	second := cpDo(AdminCardPoolConversion, "POST", "/x", "/x", body)
	if second.Code != http.StatusConflict {
		t.Fatalf("second %d %s", second.Code, second.Body.String())
	}
	if e, _ := mwJSON(t, second)["error"].(string); !strings.Contains(e, "在途") {
		t.Errorf("error %q", e)
	}
	if n := len(mw.Requests("POST", cpPathConversion)); n != 1 {
		t.Errorf("conversion requests=%d", n)
	}
	if n := mwCount(t, c, `SELECT COUNT(*) FROM mw_card_orders`); n != 1 {
		t.Errorf("orders=%d", n)
	}
}

func TestAccountsRefreshFailure(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	// 不带 refresh 只读本地，不调平台
	w := cpDo(AdminCardPoolAccounts, "GET", "/x", "/x", ``)
	if w.Code != 200 {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
	if items, _ := mwJSON(t, w)["items"].([]any); items == nil || len(items) != 0 {
		t.Errorf("items %s", w.Body.String())
	}
	if n := len(mw.Requests("GET", cpPathAccounts)); n != 0 {
		t.Fatalf("requests=%d", n)
	}

	cpMockAccounts(mw)
	w = cpDo(AdminCardPoolAccounts, "GET", "/x", "/x?refresh=1", ``)
	if w.Code != 200 {
		t.Fatalf("refresh %d %s", w.Code, w.Body.String())
	}
	items, _ := mwJSON(t, w)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["account_no"] != "M1" {
		t.Fatalf("items %s", w.Body.String())
	}

	// 同步失败时返回错误，而不是旧数据
	mw.On("GET", cpPathAccounts, mwtest.Unknown())
	w = cpDo(AdminCardPoolAccounts, "GET", "/x", "/x?refresh=1", ``)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("refresh failure %d %s", w.Code, w.Body.String())
	}
	if m := mwJSON(t, w); m["outcome"] != "unknown" || m["items"] != nil {
		t.Errorf("body %v", m)
	}
	if n := mwCount(t, c, `SELECT COUNT(*) FROM mw_accounts WHERE account_no='M1' AND gone=0`); n != 1 {
		t.Errorf("local accounts changed: %d", n)
	}
}

func TestSerialsParams(t *testing.T) {
	_, mw, _ := cpEnv(t, false)
	for _, q := range []string{
		"account_no=M1", // 缺 currency
		"currency=USD",  // 缺 account_no
		"account_no=M1&currency=USD&start_time=abc",
		"account_no=M1&currency=USD&end_time=1.5",
		"account_no=M1&currency=USD&start_time=1700000000&end_time=1700000000",                           // 结束不晚于开始
		"account_no=M1&currency=USD&start_time=1700000000&end_time=" + strconv.Itoa(1700000000+91*86400), // 超过 90 天
	} {
		if w := cpDo(AdminCardPoolSerials, "GET", "/x", "/x?"+q, ``); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", q, w.Code, w.Body.String())
		}
	}
	if n := len(mw.Requests("GET", cpPathSerials)); n != 0 {
		t.Fatalf("requests=%d", n)
	}

	mw.On("GET", cpPathSerials, mwtest.Reply{Body: `{"count":"41","data":[{"serial_id":"S1","currency":"USD","amount":"1.00","flag_dc":"C","transaction_type":"X","create_time":1700000000}]}`})
	w := cpDo(AdminCardPoolSerials, "GET", "/x", "/x?account_no=M1&currency=USD&start_time=1700000000&end_time=1700086400&page=3&page_size=20", ``)
	if w.Code != 200 {
		t.Fatalf("serials %d %s", w.Code, w.Body.String())
	}
	m := mwJSON(t, w)
	items, _ := m["items"].([]any)
	if len(items) != 1 || m["total"] != float64(41) || items[0].(map[string]any)["serial_id"] != "S1" {
		t.Fatalf("body %v", m)
	}
	reqs := mw.Requests("GET", cpPathSerials)
	q, _ := url.ParseQuery(reqs[0].RawQuery)
	if q.Get("account_no") != "M1" || q.Get("currency") != "USD" || q.Get("start_time") != "1700000000" ||
		q.Get("end_time") != "1700086400" || q.Get("page_no") != "3" || q.Get("page_size") != "20" {
		t.Errorf("query %v", q)
	}

	// 不传时间：默认最近 7 天
	before := time.Now().Unix()
	if w := cpDo(AdminCardPoolSerials, "GET", "/x", "/x?account_no=M1&currency=USD", ``); w.Code != 200 {
		t.Fatalf("default window %d %s", w.Code, w.Body.String())
	}
	reqs = mw.Requests("GET", cpPathSerials)
	q, _ = url.ParseQuery(reqs[len(reqs)-1].RawQuery)
	start, _ := strconv.ParseInt(q.Get("start_time"), 10, 64)
	end, _ := strconv.ParseInt(q.Get("end_time"), 10, 64)
	if end < before || end > time.Now().Unix() || end-start != 7*86400 {
		t.Errorf("default window start=%d end=%d", start, end)
	}
}

func TestBackfillEndpoint(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	for _, body := range []string{`{"days":2}`, `{}`, `{"days":"1"}`} {
		if w := cpDo(AdminCardPoolBackfill, "POST", "/x", "/x", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if a := cpAudit(t, c); strings.Contains(a, "cardpool.backfill") {
		t.Errorf("rejected request audited: %q", a)
	}
	if w := cpDo(AdminCardPoolBackfillStatus, "GET", "/x", "/x", ``); w.Code != 200 || mwJSON(t, w)["last_run_at"] != nil {
		t.Fatalf("status before %d %s", w.Code, w.Body.String())
	}

	mw.On("GET", cpPathAuthTxns, cpTopPage())
	mw.On("GET", cpPathSettleTxns, cpTopPage())
	w := cpDo(AdminCardPoolBackfill, "POST", "/x", "/x", `{"days":1}`)
	if w.Code != 200 {
		t.Fatalf("backfill %d %s", w.Code, w.Body.String())
	}
	m := mwJSON(t, w)
	if m["last_run_at"] == nil || m["cursor"] == nil || m["last_error"] != "" || m["last_added"] != float64(0) {
		t.Fatalf("state %v", m)
	}
	if n := len(mw.Requests("GET", cpPathAuthTxns)); n != 1 {
		t.Errorf("auth requests=%d", n)
	}
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.backfill days=1") {
		t.Errorf("audit %q", a)
	}
	st := cpDo(AdminCardPoolBackfillStatus, "GET", "/x", "/x", ``)
	if st.Code != 200 || mwJSON(t, st)["last_run_at"] != m["last_run_at"] {
		t.Errorf("status after %d %s", st.Code, st.Body.String())
	}
}

func TestWalletAccountAndTransferEndpoints(t *testing.T) {
	c, mw, _ := cpEnv(t, false)

	// 新建子账户
	if w := cpDo(AdminCardPoolCreateAccount, "POST", "/x", "/x", `{"account_name":"a b"}`); w.Code != 400 {
		t.Errorf("bad name: %d %s", w.Code, w.Body.String())
	}
	mw.On("POST", "/v1/accounts", mwtest.OK(`{"account_no":"S1","account_name":"Ops","out_account_no":"ACX","status":"ACTIVE","create_time":1700000000}`))
	w := cpDo(AdminCardPoolCreateAccount, "POST", "/x", "/x", `{"account_name":"Ops"}`)
	if w.Code != 200 || mwJSON(t, w)["account_no"] != "S1" {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	// 结果未知：原样返回平台错误
	mw.On("POST", "/v1/accounts", mwtest.Unknown())
	if w := cpDo(AdminCardPoolCreateAccount, "POST", "/x", "/x", `{"account_name":"Ops2"}`); w.Code != 502 || mwJSON(t, w)["outcome"] != "unknown" {
		t.Errorf("create unknown %d %s", w.Code, w.Body.String())
	}

	// 启停子账户
	mw.On("GET", cpPathAccounts, cpTopPage(
		`{"account_no":"M1","account_name":"main","account_status":"ACTIVE","account_type":"MAIN","balance_list":[{"currency":"USD","available_balance":"50.00","freeze_balance":"0.00"}]}`,
		`{"account_no":"S1","account_name":"Ops","account_status":"ACTIVE","account_type":"SUB_ACCOUNT","balance_list":[]}`))
	mw.On("PUT", "/v1/accounts/S1/status", mwtest.OK(`{"account_no":"S1"}`))
	w = cpDo(AdminCardPoolAccountStatus, "POST", "/a/:no/status", "/a/S1/status", `{"status":"DISABLED","remark":"暂停使用"}`)
	if w.Code != 200 || mwJSON(t, w)["status"] != "DISABLED" {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	if w := cpDo(AdminCardPoolAccountStatus, "POST", "/a/:no/status", "/a/M1/status", `{"status":"DISABLED","remark":"暂停使用"}`); w.Code != 400 {
		t.Errorf("main status %d %s", w.Code, w.Body.String())
	}

	// 钱包转账：金额校验与审计
	for _, amt := range []string{"1.005", "0", "abc", ""} {
		if w := cpDo(AdminCardPoolWalletTransfer, "POST", "/x", "/x",
			`{"payer_account_no":"M1","payee_account_no":"S1","currency":"USD","amount":"`+amt+`"}`); w.Code != 400 {
			t.Errorf("amount %q: %d %s", amt, w.Code, w.Body.String())
		}
	}
	mw.On("GET", cpPathAccounts, cpTopPage(
		`{"account_no":"M1","account_name":"main","account_status":"ACTIVE","account_type":"MAIN","balance_list":[{"currency":"USD","available_balance":"50.00","freeze_balance":"0.00"}]}`,
		`{"account_no":"S1","account_name":"Ops","account_status":"ACTIVE","account_type":"SUB_ACCOUNT","balance_list":[]}`))
	mw.On("POST", "/v1/transfer", mwtest.OK(`{"out_order_no":"x","order_no":"P1","status":"PROCESSING"}`))
	w = cpDo(AdminCardPoolWalletTransfer, "POST", "/x", "/x",
		`{"payer_account_no":"M1","payee_account_no":"S1","currency":"USD","amount":"10","remark":"备用金"}`)
	if w.Code != 200 {
		t.Fatalf("transfer %d %s", w.Code, w.Body.String())
	}
	no, _ := mwJSON(t, w)["out_order_no"].(string)
	if !strings.HasPrefix(no, "MT") {
		t.Fatalf("transfer body %s", w.Body.String())
	}
	if n := mwCount(t, c, `SELECT COUNT(*) FROM mw_card_orders WHERE kind='wallet_transfer' AND amount_cents=1000`); n != 1 {
		t.Errorf("orders with 1000 cents = %d", n)
	}

	// 收款地址
	mw.On("GET", "/v1/receiving/receiving-address", mwtest.OK(`[{"currencies":["USDT"],"address":"TADDR","chain_type":"TRC20"}]`))
	w = cpDo(AdminCardPoolReceivingAddresses, "GET", "/x", "/x", ``)
	if items, _ := mwJSON(t, w)["items"].([]any); w.Code != 200 || len(items) != 1 {
		t.Errorf("addresses %d %s", w.Code, w.Body.String())
	}

	// 卡转入转出明细
	cpInsertCard(t, c, "c1", "available", "")
	mw.On("GET", "/v1/card/balance-transactions", cpTopPage(
		`{"out_order_no":"EXT1","transaction_id":"T1","card_id":"c1","currency":"USD","amount":"1.00","type":"IN","status":"SUCCESS","create_time":1700000000}`))
	w = cpDo(AdminCardPoolCardTransfers, "GET", "/c/:id/bt", "/c/c1/bt", ``)
	items, _ := mwJSON(t, w)["items"].([]any)
	if w.Code != 200 || len(items) != 1 || items[0].(map[string]any)["origin"] != "external" {
		t.Errorf("card transfers %d %s", w.Code, w.Body.String())
	}
	if w := cpDo(AdminCardPoolCardTransfers, "GET", "/c/:id/bt", "/c/nope/bt", ``); w.Code != 404 {
		t.Errorf("missing card %d %s", w.Code, w.Body.String())
	}

	a := cpAudit(t, c)
	for _, want := range []string{
		"cardpool.wallet_create_account account_no=S1 name=Ops",
		"cardpool.wallet_account_status account_no=S1 status=DISABLED remark=暂停使用",
		"cardpool.wallet_transfer out_order_no=" + no + " payer=M1 payee=S1 amount=10.00 currency=USD",
	} {
		if !strings.Contains(a, want) {
			t.Errorf("audit missing %q: %q", want, a)
		}
	}
}

// 零账户属于发送前失败：映射为 502 且不带 outcome，前端据此显示「未提交」而不是「结果未知」。
func TestNoAccountsMapsTo502WithoutOutcome(t *testing.T) {
	status, body := poolErrorStatus(fmt.Errorf("同步账户: %w", cardpool.ErrNoAccounts))
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d，期望 502", status)
	}
	if _, ok := body["outcome"]; ok {
		t.Fatalf("body 不应带 outcome: %v", body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "未返回任何账户") {
		t.Fatalf("error = %q", msg)
	}
}
