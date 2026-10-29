package cardpool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

const (
	pathConversion      = "/v1/conversion"
	pathWalletTransfer  = "/v1/transfer"
	pathWalletTransferQ = "/v1/transfer/order"
)

func convQueryPath(no string) string { return pathConversion + "/" + no }

// acctJSON 生成一条平台账户；balances 依次为「币种, 可用余额」。
func acctJSON(no, typ, status string, balances ...string) string {
	var items []string
	for i := 0; i+1 < len(balances); i += 2 {
		items = append(items, fmt.Sprintf(`{"currency":%q,"available_balance":%q,"freeze_balance":"0.00"}`, balances[i], balances[i+1]))
	}
	return fmt.Sprintf(`{"account_no":%q,"account_name":%q,"account_status":%q,"account_type":%q,"balance_list":[%s]}`,
		no, no, status, typ, strings.Join(items, ","))
}

// acctPage 生成只有一页的账户分页响应。
func acctPage(items ...string) mwtest.Reply {
	return mwtest.Reply{Body: fmt.Sprintf(`{"count":"%d","data":[%s]}`, len(items), strings.Join(items, ","))}
}

// mockFundsAccounts 编排默认账户：主钱包 M1（USDT 100.00、USD 50.00），子账户 S1（启用，USD 20.00、HKD 9.00）。
func mockFundsAccounts(mw *mwtest.Server) {
	mw.On("GET", pathAccounts, acctPage(
		acctJSON("M1", "MAIN", "ACTIVE", "USDT", "100.00", "USD", "50.00"),
		acctJSON("S1", "SUB_ACCOUNT", "ACTIVE", "USD", "20.00", "HKD", "9.00")))
}

func validConv() ConversionParams {
	return ConversionParams{SellCurrency: "USDT", BuyCurrency: "USD", SellCents: 1250, By: "admin"}
}

func validXfer() WalletTransferParams {
	return WalletTransferParams{PayerAccountNo: "M1", PayeeAccountNo: "S1", Currency: "USD", AmountCents: 1000, Remark: "备用金", By: "admin"}
}

// requestData 把请求体解析为字符串字段。
func requestData(t *testing.T, r mwtest.Request) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("请求体 %s: %v", r.Body, err)
	}
	return m
}

func detailMap(t *testing.T, o *Order) map[string]string {
	t.Helper()
	m := map[string]string{}
	if o.Detail == "" {
		return m
	}
	if err := json.Unmarshal([]byte(o.Detail), &m); err != nil {
		t.Fatalf("detail %q: %v", o.Detail, err)
	}
	return m
}

func mustNoFundsRequests(t *testing.T, mw *mwtest.Server) {
	t.Helper()
	if n := len(mw.Requests("POST", pathConversion)); n != 0 {
		t.Fatalf("不应发出兑换请求，收到 %d", n)
	}
	if n := len(mw.Requests("POST", pathWalletTransfer)); n != 0 {
		t.Fatalf("不应发出转账请求，收到 %d", n)
	}
	if n := countOrders(t); n != 0 {
		t.Fatalf("不应建单，单据数 %d", n)
	}
}

func TestConversionValidation(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	mockFundsAccounts(mw)
	cases := []struct {
		name string
		p    ConversionParams
	}{
		{"金额为 0", ConversionParams{SellCurrency: "USDT", BuyCurrency: "USD", SellCents: 0}},
		{"金额为负", ConversionParams{SellCurrency: "USDT", BuyCurrency: "USD", SellCents: -100}},
		{"同币种", ConversionParams{SellCurrency: " usdt ", BuyCurrency: "USDT", SellCents: 100}},
		{"币种为空", ConversionParams{SellCurrency: " ", BuyCurrency: "USD", SellCents: 100}},
		{"币种过长", ConversionParams{SellCurrency: "USDT", BuyCurrency: "ABCDEFGHIJK", SellCents: 100}},
		{"卖出币种不在任何余额里", ConversionParams{SellCurrency: "EUR", BuyCurrency: "USD", SellCents: 100}},
		{"卖出币种只在子账户里有", ConversionParams{SellCurrency: "HKD", BuyCurrency: "USD", SellCents: 100}},
		{"余额不足", ConversionParams{SellCurrency: "USDT", BuyCurrency: "USD", SellCents: 10001}},
	}
	for _, c := range cases {
		c.p.By = "admin"
		if _, err := s.SubmitConversion(ctx, c.p); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("%s: err = %v，期望 ErrBadRequest", c.name, err)
		}
	}
	mustNoFundsRequests(t, mw)

	// 恰好等于可用余额可以提交；币种去空白转大写
	mw.On("POST", pathConversion, mwtest.OK(`{"transaction_id":"CV1"}`))
	o, err := s.SubmitConversion(ctx, ConversionParams{SellCurrency: " usdt ", BuyCurrency: "usd", SellCents: 10000, By: "admin"})
	if err != nil || o.State != StateProcessing || o.Currency != "USDT" || o.BuyCurrency != "USD" {
		t.Fatalf("%+v %v", o, err)
	}
}

func TestFundsRejectUnparsableBalance(t *testing.T) {
	ctx := context.Background()
	for name, bal := range map[string]string{"千分位": "1,000.00", "空串": ""} {
		t.Run(name, func(t *testing.T) {
			s, mw, _, _ := newTestService(t)
			mw.On("GET", pathAccounts, acctPage(
				acctJSON("M1", "MAIN", "ACTIVE", "USDT", bal),
				acctJSON("S1", "SUB_ACCOUNT", "ACTIVE")))
			_, err := s.SubmitConversion(ctx, ConversionParams{SellCurrency: "USDT", BuyCurrency: "USD", SellCents: 100, By: "admin"})
			if !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), "余额格式无法识别") {
				t.Fatalf("兑换: err = %v", err)
			}
			_, err = s.SubmitWalletTransfer(ctx, WalletTransferParams{PayerAccountNo: "M1", PayeeAccountNo: "S1",
				Currency: "USDT", AmountCents: 100, By: "admin"})
			if !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), "余额格式无法识别") {
				t.Fatalf("转账: err = %v", err)
			}
			mustNoFundsRequests(t, mw)
		})
	}
}

func TestFundsSyncFailureRejects(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	// 本地已有账户，但提交前的同步失败：照样拒绝
	mockFundsAccounts(mw)
	if err := s.SyncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	mw.On("GET", pathAccounts, mwtest.Unknown())
	_, err := s.SubmitConversion(ctx, validConv())
	if err == nil || errors.Is(err, ErrBadRequest) || marswallex.OutcomeOf(err) != marswallex.OutcomeUnknown {
		t.Fatalf("兑换: err = %v，期望同步错误", err)
	}
	_, err = s.SubmitWalletTransfer(ctx, validXfer())
	if err == nil || errors.Is(err, ErrBadRequest) || marswallex.OutcomeOf(err) != marswallex.OutcomeUnknown {
		t.Fatalf("转账: err = %v，期望同步错误", err)
	}
	mustNoFundsRequests(t, mw)
}

func TestConversionSendOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		s, mw, clock, _ := newTestService(t)
		mockFundsAccounts(mw)
		mw.On("POST", pathConversion, mwtest.OK(`{"transaction_id":"CV1"}`))
		o, err := s.SubmitConversion(ctx, ConversionParams{SellCurrency: "usdt", BuyCurrency: " usd ", SellCents: 1250, By: "admin"})
		if err != nil {
			t.Fatal(err)
		}
		now := clock.Now().Unix()
		got := mustGetOrder(t, o.OutOrderNo)
		if got.State != StateProcessing || got.PlatformTxnID != "CV1" || got.Attempts != 1 || i64(got.FirstSentAt) != now ||
			i64(got.NextCheckAt) != now+10 || got.EverUnknown || got.FinishedAt != nil {
			t.Fatalf("%+v", got)
		}
		if got.Kind != KindConversion || !strings.HasPrefix(got.OutOrderNo, "MC") || got.AmountCents != 1250 ||
			got.Currency != "USDT" || got.BuyCurrency != "USD" || got.RequestedBy != "admin" {
			t.Fatalf("%+v", got)
		}
		if o.State != StateProcessing {
			t.Fatalf("返回的单据应为最新状态: %+v", o)
		}
		reqs := mw.Requests("POST", pathConversion)
		if len(reqs) != 1 {
			t.Fatalf("请求数 = %d", len(reqs))
		}
		body := requestData(t, reqs[0])
		if body["out_order_no"] != o.OutOrderNo || body["sell_currency"] != "USDT" || body["sell_amount"] != "12.50" ||
			body["buy_currency"] != "USD" || len(body) != 4 {
			t.Fatalf("请求体 = %s", reqs[0].Body)
		}
		if s.accountsDirty.Load() {
			t.Fatal("processing 不是终态，不应标脏")
		}
	})

	t.Run("rejected", func(t *testing.T) {
		s, mw, clock, _ := newTestService(t)
		mockFundsAccounts(mw)
		mw.On("POST", pathConversion, mwtest.Rejected(400, "PARAM"))
		o, err := s.SubmitConversion(ctx, validConv())
		if err != nil {
			t.Fatal(err)
		}
		got := mustGetOrder(t, o.OutOrderNo)
		if got.State != StateFailed || got.EverUnknown || got.LastError == "" || got.FinishedAt == nil ||
			i64(got.FirstSentAt) != clock.Now().Unix() || got.Attempts != 1 {
			t.Fatalf("%+v", got)
		}
		if !s.accountsDirty.Load() {
			t.Fatal("进入终态后应标脏")
		}
	})

	t.Run("unknown", func(t *testing.T) {
		s, mw, clock, _ := newTestService(t)
		mockFundsAccounts(mw)
		mw.On("POST", pathConversion, mwtest.Unknown())
		o, err := s.SubmitConversion(ctx, validConv())
		if err != nil {
			t.Fatal(err)
		}
		now := clock.Now().Unix()
		got := mustGetOrder(t, o.OutOrderNo)
		if got.State != StateUnknown || !got.EverUnknown || i64(got.NextCheckAt) != now+10 ||
			i64(got.FirstSentAt) != now || got.Attempts != 1 || got.FinishedAt != nil {
			t.Fatalf("%+v", got)
		}
		if s.accountsDirty.Load() {
			t.Fatal("unknown 不是终态，不应标脏")
		}
	})

	t.Run("not_sent", func(t *testing.T) {
		s, mw, _, _ := newTestService(t)
		mockFundsAccounts(mw)
		// 同步账户用正常客户端，之后的下单请求发不出去
		s.opts.Client = func() (*marswallex.Client, error) {
			if len(mw.Requests("GET", pathAccounts)) == 0 {
				return mw.Client(), nil
			}
			return mw.NotSentClient(), nil
		}
		o, err := s.SubmitConversion(ctx, validConv())
		if err != nil {
			t.Fatal(err)
		}
		got := mustGetOrder(t, o.OutOrderNo)
		if got.State != StateNotSent || got.EverUnknown || got.Attempts != 0 || got.FirstSentAt != nil ||
			got.FinishedAt == nil || got.LastError == "" {
			t.Fatalf("%+v", got)
		}
	})
}

func TestWalletTransferValidation(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	m1 := acctJSON("M1", "MAIN", "ACTIVE", "USD", "50.00")
	m2 := acctJSON("M2", "MAIN", "ACTIVE", "USD", "10.00")
	s1 := acctJSON("S1", "SUB_ACCOUNT", "ACTIVE", "USD", "20.00")
	s2 := acctJSON("S2", "SUB_ACCOUNT", "ACTIVE", "USD", "20.00")
	s3 := acctJSON("S3", "SUB_ACCOUNT", "DISABLED", "USD", "5.00")
	s4 := acctJSON("S4", "SUB_ACCOUNT", "ACTIVE", "USD", "30.00")
	mw.On("GET", pathAccounts, acctPage(m1, m2, s1, s2, s3, s4))
	if err := s.SyncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	// 提交前的同步里 S2 不见了：标记 gone
	mw.On("GET", pathAccounts, acctPage(m1, m2, s1, s3, s4))

	x := func(payer, payee, cur string, cents int64, remark string) WalletTransferParams {
		return WalletTransferParams{PayerAccountNo: payer, PayeeAccountNo: payee, Currency: cur, AmountCents: cents, Remark: remark, By: "admin"}
	}
	cases := []struct {
		name string
		p    WalletTransferParams
	}{
		{"金额为 0", x("M1", "S1", "USD", 0, "")},
		{"币种为空", x("M1", "S1", " ", 100, "")},
		{"付款方为空", x("", "S1", "USD", 100, "")},
		{"备注 201 字", x("M1", "S1", "USD", 100, strings.Repeat("字", 201))},
		{"两个 MAIN", x("M1", "M2", "USD", 100, "")},
		{"两个 SUB", x("S1", "S4", "USD", 100, "")},
		{"同一账户", x("S1", "S1", "USD", 100, "")},
		{"同一主钱包", x(" M1", "M1 ", "USD", 100, "")},
		{"收款方不在本地", x("M1", "S9", "USD", 100, "")},
		{"付款方不在本地", x("S9", "M1", "USD", 100, "")},
		{"子账户 gone", x("M1", "S2", "USD", 100, "")},
		{"子账户 gone（付款方）", x("S2", "M1", "USD", 100, "")},
		{"收款子账户 DISABLED", x("M1", "S3", "USD", 100, "")},
		{"付款子账户 DISABLED", x("S3", "M1", "USD", 100, "")},
		{"主钱包余额不足", x("M1", "S1", "USD", 5001, "")},
		{"子账户余额不足", x("S1", "M1", "USD", 2001, "")},
		{"付款方没有该币种", x("M1", "S1", "EUR", 100, "")},
	}
	for _, c := range cases {
		if _, err := s.SubmitWalletTransfer(ctx, c.p); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("%s: err = %v，期望 ErrBadRequest", c.name, err)
		}
	}
	mustNoFundsRequests(t, mw)
	if !accountRow(t, "S2").Gone {
		t.Fatal("S2 应已标记 gone")
	}

	// 边界：备注恰好 200 字、金额恰好等于可用余额
	mw.On("POST", pathWalletTransfer, mwtest.OK(`{"order_no":"TO1","status":"PROCESSING"}`))
	remark := strings.Repeat("字", 200)
	o, err := s.SubmitWalletTransfer(ctx, x("M1", "S1", "usd", 5000, remark))
	if err != nil || o.State != StateProcessing || o.Note != remark || o.Currency != "USD" {
		t.Fatalf("%+v %v", o, err)
	}
}

func TestWalletTransferSendOutcomes(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ status, want string }{
		{"PROCESSING", StateProcessing},
		{"SUCCESS", StateSuccess},
		{"FAIL", StateFailed},
		{"WEIRD", StateUnknown},
	} {
		t.Run(c.status, func(t *testing.T) {
			s, mw, clock, _ := newTestService(t)
			mockFundsAccounts(mw)
			mw.On("POST", pathWalletTransfer, mwtest.OK(`{"out_order_no":"x","order_no":"TO1","status":"`+c.status+`"}`))
			o, err := s.SubmitWalletTransfer(ctx, WalletTransferParams{PayerAccountNo: " S1 ", PayeeAccountNo: "M1",
				Currency: "usd", AmountCents: 1999, Remark: " 回收 ", By: "admin"})
			if err != nil {
				t.Fatal(err)
			}
			got := mustGetOrder(t, o.OutOrderNo)
			if got.State != c.want || o.State != c.want {
				t.Fatalf("state = %s / %s，期望 %s", got.State, o.State, c.want)
			}
			if got.Kind != KindWalletTransfer || !strings.HasPrefix(got.OutOrderNo, "MT") || got.PayerAccountNo != "S1" ||
				got.PayeeAccountNo != "M1" || got.Currency != "USD" || got.AmountCents != 1999 || got.Note != "回收" ||
				got.RequestedBy != "admin" || got.Attempts != 1 || i64(got.FirstSentAt) != clock.Now().Unix() {
				t.Fatalf("%+v", got)
			}
			if detailMap(t, got)["order_no"] != "TO1" {
				t.Fatalf("detail = %q，应含 order_no", got.Detail)
			}
			switch c.want {
			case StateFailed:
				if got.LastError == "" || got.FinishedAt == nil {
					t.Fatalf("%+v", got)
				}
			case StateUnknown:
				if !got.EverUnknown || !strings.Contains(got.LastError, "WEIRD") || got.FinishedAt != nil {
					t.Fatalf("%+v", got)
				}
			}
			reqs := mw.Requests("POST", pathWalletTransfer)
			if len(reqs) != 1 {
				t.Fatalf("请求数 = %d", len(reqs))
			}
			body := requestData(t, reqs[0])
			if body["out_order_no"] != o.OutOrderNo || body["payer_account_no"] != "S1" || body["payee_account_no"] != "M1" ||
				body["amount"] != "19.99" || body["currency"] != "USD" || body["remark"] != "回收" {
				t.Fatalf("请求体 = %s", reqs[0].Body)
			}
			if s.accountsDirty.Load() != IsTerminal(c.want) {
				t.Fatalf("标脏 = %v，期望 %v", s.accountsDirty.Load(), IsTerminal(c.want))
			}
		})
	}

	t.Run("500", func(t *testing.T) {
		s, mw, _, _ := newTestService(t)
		mockFundsAccounts(mw)
		mw.On("POST", pathWalletTransfer, mwtest.Unknown())
		o, err := s.SubmitWalletTransfer(ctx, validXfer())
		if err != nil || o.State != StateUnknown || !o.EverUnknown || o.Detail != "" {
			t.Fatalf("%+v %v", o, err)
		}
	})
}

func TestWalletInflightUnique(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	mockFundsAccounts(mw)
	mw.On("POST", pathConversion, mwtest.Unknown())
	c1, err := s.SubmitConversion(ctx, validConv())
	if err != nil || c1.State != StateUnknown {
		t.Fatalf("%+v %v", c1, err)
	}
	if _, err := s.SubmitConversion(ctx, validConv()); !errors.Is(err, ErrInflight) {
		t.Fatalf("err = %v，期望 ErrInflight", err)
	}
	if n := len(mw.Requests("POST", pathConversion)); n != 1 {
		t.Fatalf("兑换请求数 = %d，期望 1", n)
	}
	if n := countOrders(t); n != 1 {
		t.Fatalf("单据数 = %d，期望 1", n)
	}

	// 钱包转账不受在途兑换影响
	mw.On("POST", pathWalletTransfer, mwtest.Unknown())
	x1, err := s.SubmitWalletTransfer(ctx, validXfer())
	if err != nil || x1.State != StateUnknown {
		t.Fatalf("%+v %v", x1, err)
	}
	if _, err := s.SubmitWalletTransfer(ctx, validXfer()); !errors.Is(err, ErrInflight) {
		t.Fatalf("err = %v，期望 ErrInflight", err)
	}
	if n := len(mw.Requests("POST", pathWalletTransfer)); n != 1 {
		t.Fatalf("转账请求数 = %d，期望 1", n)
	}

	// needs_review 也算在途；定论后才能再兑换
	if _, err := db.DB.Exec(`UPDATE mw_card_orders SET state = ? WHERE out_order_no = ?`, StateNeedsReview, c1.OutOrderNo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitConversion(ctx, validConv()); !errors.Is(err, ErrInflight) {
		t.Fatalf("needs_review: err = %v，期望 ErrInflight", err)
	}
	if _, err := s.ConfirmFailed(ctx, c1.OutOrderNo, "后台核对无此单", "admin"); err != nil {
		t.Fatal(err)
	}
	c2, err := s.SubmitConversion(ctx, validConv())
	if err != nil || c2.State != StateUnknown {
		t.Fatalf("%+v %v", c2, err)
	}
	if n := len(mw.Requests("POST", pathConversion)); n != 2 {
		t.Fatalf("兑换请求数 = %d，期望 2", n)
	}
}

func TestWalletOrdersReconcile(t *testing.T) {
	s, mw, clock, alerts := newTestService(t)
	ctx := context.Background()
	mockFundsAccounts(mw)
	mw.On("POST", pathConversion, mwtest.Unknown())
	mw.On("POST", pathWalletTransfer, mwtest.OK(`{"order_no":"TO1","status":"PROCESSING"}`))
	conv, err := s.SubmitConversion(ctx, validConv())
	if err != nil || conv.State != StateUnknown {
		t.Fatalf("%+v %v", conv, err)
	}
	xfer, err := s.SubmitWalletTransfer(ctx, validXfer())
	if err != nil || xfer.State != StateProcessing {
		t.Fatalf("%+v %v", xfer, err)
	}
	mw.On("GET", convQueryPath(conv.OutOrderNo), mwtest.OK(`{"out_order_no":"`+conv.OutOrderNo+`","transaction_id":"CV1",
		"status":"SUCCESS","sell_currency":"USDT","sell_amount":"12.50","buy_currency":"USD","buy_amount":"12.40",
		"rate":"0.992","fee_amount":"","base_currency":"USDT"}`))
	mw.On("GET", pathWalletTransferQ, mwtest.OK(`{"out_order_no":"`+xfer.OutOrderNo+`","status":"FAIL","fail_reason":"账户余额不足"}`))

	clock.Advance(10 * time.Second)
	mustReconcile(t, s)

	got := mustGetOrder(t, conv.OutOrderNo)
	if got.State != StateSuccess || got.PlatformTxnID != "CV1" || got.FinishedAt == nil {
		t.Fatalf("兑换单: %+v", got)
	}
	d := detailMap(t, got)
	if d["buy_amount"] != "12.40" || d["rate"] != "0.992" || d["buy_currency"] != "USD" || d["base_currency"] != "USDT" {
		t.Fatalf("detail = %q", got.Detail)
	}
	if _, ok := d["fee_amount"]; ok {
		t.Fatalf("空字段不应写入 detail: %q", got.Detail)
	}
	got = mustGetOrder(t, xfer.OutOrderNo)
	if got.State != StateFailed || got.LastError != "账户余额不足" || got.FinishedAt == nil {
		t.Fatalf("转账单: %+v", got)
	}
	if detailMap(t, got)["order_no"] != "TO1" {
		t.Fatalf("查单结果没有 order_no 时应保留原 detail: %q", got.Detail)
	}
	reqs := mw.Requests("GET", pathWalletTransferQ)
	if len(reqs) != 1 {
		t.Fatalf("转账查单次数 = %d", len(reqs))
	}
	if q, _ := url.ParseQuery(reqs[0].RawQuery); q.Get("out_order_no") != xfer.OutOrderNo || q.Get("order_no") != "" {
		t.Fatalf("查单参数 %q", reqs[0].RawQuery)
	}
	if !s.accountsDirty.Load() {
		t.Fatal("定论后应标脏")
	}

	// 查单出错、查不到、状态不认识都保持 unknown；满 30 分钟转人工
	c2, err := s.SubmitConversion(ctx, validConv())
	if err != nil || c2.State != StateUnknown {
		t.Fatalf("%+v %v", c2, err)
	}
	mw.On("GET", convQueryPath(c2.OutOrderNo), mwtest.Rejected(404, "NOT_FOUND"), mwtest.Unknown(), mwtest.OK(`{"status":"WEIRD"}`))
	t0 := clock.Now().Unix()
	for clock.Now().Unix() < t0+1790 {
		clock.Advance(10 * time.Second)
		mustReconcile(t, s)
		if got := mustGetOrder(t, c2.OutOrderNo); got.State != StateUnknown {
			t.Fatalf("t0+%d 时应仍为 unknown: %+v", clock.Now().Unix()-t0, got)
		}
	}
	if n := len(mw.Requests("GET", convQueryPath(c2.OutOrderNo))); n < 3 {
		t.Fatalf("期间应持续查单，只查了 %d 次", n)
	}
	if alerts.Count() != 0 {
		t.Fatalf("30 分钟前不应告警: %v", alerts.Texts())
	}
	clock.Advance(10 * time.Second)
	mustReconcile(t, s)
	if got := mustGetOrder(t, c2.OutOrderNo); got.State != StateNeedsReview {
		t.Fatalf("满 30 分钟应转 needs_review: %+v", got)
	}
	if alerts.Count() != 1 || !strings.Contains(alerts.Texts()[0], c2.OutOrderNo) || !strings.Contains(alerts.Texts()[0], "兑换单") {
		t.Fatalf("告警 = %v", alerts.Texts())
	}

	// 后台「再查一次」沿用现有接口
	mw.On("GET", convQueryPath(c2.OutOrderNo), mwtest.OK(`{"transaction_id":"CV2","status":"SUCCESS","buy_amount":"12.30"}`))
	got, err = s.Recheck(ctx, c2.OutOrderNo)
	if err != nil || got.State != StateSuccess || detailMap(t, got)["buy_amount"] != "12.30" {
		t.Fatalf("%+v %v", got, err)
	}
	if n := len(mw.Requests("POST", pathConversion)); n != 2 {
		t.Fatalf("兑换请求数 = %d，期望 2（从不重发）", n)
	}
}

func TestWalletOrdersNeverResent(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	ctx := context.Background()
	mockFundsAccounts(mw)
	mw.On("POST", pathConversion, mwtest.Unknown())
	mw.On("POST", pathWalletTransfer, mwtest.Unknown())
	conv, err := s.SubmitConversion(ctx, validConv())
	if err != nil || conv.State != StateUnknown {
		t.Fatalf("%+v %v", conv, err)
	}
	xfer, err := s.SubmitWalletTransfer(ctx, validXfer())
	if err != nil || xfer.State != StateUnknown {
		t.Fatalf("%+v %v", xfer, err)
	}
	mw.On("GET", convQueryPath(conv.OutOrderNo), mwtest.Unknown())
	mw.On("GET", pathWalletTransferQ, mwtest.Rejected(404, "NOT_FOUND"))
	for i := 0; i < 50; i++ {
		clock.Advance(time.Minute)
		mustReconcile(t, s)
	}
	if n := len(mw.Requests("POST", pathConversion)); n != 1 {
		t.Fatalf("POST %s 只能收到 1 次, got %d", pathConversion, n)
	}
	if n := len(mw.Requests("POST", pathWalletTransfer)); n != 1 {
		t.Fatalf("POST %s 只能收到 1 次, got %d", pathWalletTransfer, n)
	}
	if len(mw.Requests("GET", convQueryPath(conv.OutOrderNo))) == 0 || len(mw.Requests("GET", pathWalletTransferQ)) == 0 {
		t.Fatal("应有查单")
	}
}

func TestConversionWebhookBeforeResponse(t *testing.T) {
	s, mw, _, alerts := newTestService(t)
	ctx := context.Background()
	mockFundsAccounts(mw)
	rep := mwtest.OK(`{"transaction_id":"CV1"}`)
	rep.Hang = 300 * time.Millisecond
	mw.On("POST", pathConversion, rep)

	type result struct {
		o   *Order
		err error
	}
	done := make(chan result, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	t.Cleanup(wg.Wait) // 测试提前失败时也先等提交返回，再关库
	go func() {
		defer wg.Done()
		o, err := s.SubmitConversion(ctx, validConv())
		done <- result{o, err}
	}()

	// 等兑换请求已经发出、还没有响应
	deadline := time.Now().Add(2 * time.Second)
	for len(mw.Requests("POST", pathConversion)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("兑换请求一直没有发出")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var no string
	if err := db.DB.QueryRow(`SELECT out_order_no FROM mw_card_orders WHERE kind = ?`, KindConversion).Scan(&no); err != nil {
		t.Fatal(err)
	}
	id := enqueueEvent(t, marswallex.TopicConversionResult,
		`{"out_order_no":"`+no+`","transaction_id":"CV1","status":"SUCCESS","buy_amount":"12.40","buy_currency":"USD"}`)
	mustProcessEvents(t, s)
	mustBeProcessed(t, id, StateSuccess)
	if got := mustGetOrder(t, no); got.State != StateSuccess {
		t.Fatalf("推送应先定论: %+v", got)
	}

	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.o.State != StateSuccess {
		t.Fatalf("返回的单据应保持 success: %+v", r.o)
	}
	got := mustGetOrder(t, no)
	if got.State != StateSuccess || detailMap(t, got)["buy_amount"] != "12.40" {
		t.Fatalf("随后的 200 不应改回 processing: %+v", got)
	}
	if alerts.Count() != 0 {
		t.Fatalf("不应告警: %v", alerts.Texts())
	}
}

func TestWalletWebhooks(t *testing.T) {
	s, _, _, alerts := newTestService(t)
	conv := insertOrder(t, Order{Kind: KindConversion, AmountCents: 1250, Currency: "USDT", BuyCurrency: "USD",
		State: StateProcessing, PlatformTxnID: "CV1"})
	xfer := insertOrder(t, Order{Kind: KindWalletTransfer, AmountCents: 1000, Currency: "USD", PayerAccountNo: "M1",
		PayeeAccountNo: "S1", State: StateUnknown, Detail: `{"order_no":"TO1"}`})

	// 非本系统单据；兑换单号出现在转账推送里也不算；处理中不标脏
	e1 := enqueueEvent(t, marswallex.TopicConversionResult, `{"out_order_no":"MC000000000000000000ff","status":"SUCCESS"}`)
	e2 := enqueueEvent(t, marswallex.TopicTransferResult, `{"out_order_no":"MT000000000000000000ff","status":"SUCCESS"}`)
	e3 := enqueueEvent(t, marswallex.TopicTransferResult, `{"out_order_no":"`+conv.OutOrderNo+`","status":"SUCCESS"}`)
	e4 := enqueueEvent(t, marswallex.TopicTransferResult, `{"out_order_no":"`+xfer.OutOrderNo+`","order_no":"TO1","status":"PROCESSING"}`)
	if n := mustProcessEvents(t, s); n != 4 {
		t.Fatalf("处理了 %d 条，期望 4", n)
	}
	for _, id := range []int64{e1, e2, e3} {
		mustBeProcessed(t, id, "非本系统单据")
	}
	mustBeProcessed(t, e4, StateProcessing)
	if o := mustGetOrder(t, conv.OutOrderNo); o.State != StateProcessing {
		t.Fatalf("兑换单不应被转账推送改动: %+v", o)
	}
	if o := mustGetOrder(t, xfer.OutOrderNo); o.State != StateProcessing {
		t.Fatalf("转账单: %+v", o)
	}
	if s.accountsDirty.Load() {
		t.Fatal("没有进入终态，不应标脏")
	}

	e5 := enqueueEvent(t, marswallex.TopicConversionResult, `{"out_order_no":"`+conv.OutOrderNo+`","transaction_id":"CV1",
		"status":"FAILED","fail_reason":"汇率波动","buy_currency":"USD","rate":"0.99"}`)
	e6 := enqueueEvent(t, marswallex.TopicTransferResult, `{"out_order_no":"`+xfer.OutOrderNo+`","status":"FAIL"}`)
	mustProcessEvents(t, s)
	mustBeProcessed(t, e5, StateFailed)
	mustBeProcessed(t, e6, StateFailed)
	o := mustGetOrder(t, conv.OutOrderNo)
	if o.State != StateFailed || o.LastError != "汇率波动" || o.FinishedAt == nil || detailMap(t, o)["rate"] != "0.99" {
		t.Fatalf("兑换单: %+v", o)
	}
	o = mustGetOrder(t, xfer.OutOrderNo)
	if o.State != StateFailed || o.LastError != "平台返回失败" || detailMap(t, o)["order_no"] != "TO1" {
		t.Fatalf("转账单: %+v", o)
	}
	if !s.accountsDirty.Load() {
		t.Fatal("进入终态后应标脏")
	}

	// 没有 out_order_no 时按 transaction_id 找兑换单；状态不认识只写备注
	conv2 := insertOrder(t, Order{Kind: KindConversion, AmountCents: 500, Currency: "USDT", BuyCurrency: "USD",
		State: StateProcessing, PlatformTxnID: "CV2"})
	xfer2 := insertOrder(t, Order{Kind: KindWalletTransfer, AmountCents: 100, Currency: "USD", State: StateUnknown})
	e7 := enqueueEvent(t, marswallex.TopicConversionResult, `{"transaction_id":"CV2","status":"SUCCESS","buy_amount":"4.95"}`)
	e8 := enqueueEvent(t, marswallex.TopicTransferResult, `{"out_order_no":"`+xfer2.OutOrderNo+`","status":"WEIRD"}`)
	mustProcessEvents(t, s)
	mustBeProcessed(t, e7, StateSuccess)
	mustBeProcessed(t, e8, "无法识别")
	if o := mustGetOrder(t, conv2.OutOrderNo); o.State != StateSuccess || detailMap(t, o)["buy_amount"] != "4.95" {
		t.Fatalf("兑换单 2: %+v", o)
	}
	if o := mustGetOrder(t, xfer2.OutOrderNo); o.State != StateUnknown {
		t.Fatalf("转账单 2: %+v", o)
	}
	if alerts.Count() != 0 {
		t.Fatalf("不应告警: %v", alerts.Texts())
	}
}
