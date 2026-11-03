package payengine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// creditAccount 是可以买点数的账号：个人 Plus、没有欠费、计费币种 PHP。
func creditAccount() chatgpt.Account {
	return chatgpt.Account{AccountID: "acc-1", PlanType: "plus", HasActiveSubscription: true,
		Structure: "personal", BillingCurrency: "PHP", IsDelinquent: boolPtr(false)}
}

// newCreditHarness 在付款夹具上启用 credit250/PH（默认上限 ₱632.80，注资 15 美元），账号默认可以买点数，支付页金额 ₱565.00、页面显示成功。
// 续费协程每 5 毫秒扫一次：点数单要证明它从来不去关续费。
func newCreditHarness(t *testing.T) *harness {
	t.Helper()
	h := newPayHarness(t, func(to *Timeouts) { to.RenewalTick = 5 * time.Millisecond })
	tbl, err := LoadPlanTable()
	if err != nil {
		t.Fatal(err)
	}
	for i := range tbl {
		if tbl[i].Key == "credit250" {
			rc := tbl[i].Regions["PH"]
			rc.Enabled, rc.FundCents = true, 1500
			tbl[i].Regions["PH"] = rc
		}
	}
	if err := SavePlanTable(tbl); err != nil {
		t.Fatal(err)
	}
	h.gpt.account = creditAccount()
	h.br.amount, h.br.amountText = 56500, "₱565.00"
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess})
	return h
}

func (h *harness) submitCredit(t *testing.T, dryRun, dryRunFill bool) *Order {
	t.Helper()
	o, err := h.svc.Submit(ctx, Request{Provider: testProvider, PlanKey: "credit250", Region: "PH", Session: testSession,
		Source: "admin_test", SourceRef: "admin", DryRun: dryRun, DryRunFill: dryRunFill})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// creditTxns 让卡池返回该卡的这些交易（时间取测试时钟）。
func (h *harness) creditTxns(list ...cardpool.Transaction) {
	at := h.clock.Now().Unix()
	for i := range list {
		list[i].CardID = testCardID
		list[i].OccurredAt = &at
		if list[i].Kind == "" {
			list[i].Kind = "AUTH"
		}
	}
	h.pool.listTxns = func(cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
		return append([]cardpool.Transaction(nil), list...), len(list), nil
	}
}

func approvedPHP(amount string) cardpool.Transaction {
	return cardpool.Transaction{TransactionID: "t1", Status: "approved", TxnAmount: amount, TxnCurrency: "PHP", BillingAmount: "10.10", BillingCurrency: "USD"}
}

// balances 让第 1 次读余额（付款前快照）返回 before，之后返回 after。
func (h *harness) balances(before, after string) {
	h.gpt.balanceFn = func(n int) (chatgpt.CreditBalanceInfo, error) {
		if n == 1 {
			return chatgpt.CreditBalanceInfo{Available: true, Balance: before}, nil
		}
		return chatgpt.CreditBalanceInfo{Available: true, Balance: after}, nil
	}
}

// 正常点数单：checking 通过、建点数结账会话、注资、填表并确认「保存付款信息」没勾、点一次支付、
// 授权金额与余额都对上后成功。确认阶段不看 plan_type，也从不关闭续费。
func TestCreditHappyPath(t *testing.T) {
	h := newCreditHarness(t)
	// 第 2 次起账号显示 free：确认阶段要是看 plan_type 就会定不了成功
	h.gpt.accountFn = func(n int) (chatgpt.Account, error) {
		if n == 1 {
			return creditAccount(), nil
		}
		return accountFree, nil
	}
	h.creditTxns(approvedPHP("-565.00"))
	h.balances("100", "350.5")
	h.br.savePayment = func() (string, error) { return "已取消", nil }
	h.start(t)
	o := h.submitCredit(t, false, false)
	g := waitDone(t, o.OrderNo)
	if g.State != StateSucceeded || g.Kind != KindCredit || g.CreditQuantity != 250 || g.ErrorCode != "" {
		t.Fatalf("应成功: %+v", g)
	}
	if g.CreditBalanceBefore != "100" || g.CardSaved != CardSavedNo || g.RenewalState != RenewalNone || g.SessionSealed != "" {
		t.Fatalf("快照、已存卡核对、续费与会话不符: %+v", g)
	}
	if args := h.gpt.lastArgs("CreateCreditCheckout"); len(args) != 2 || args[0] != "acc-1" || args[1] != 250 {
		t.Fatalf("点数结账参数: %v", args)
	}
	if n := h.gpt.count("CreateCheckout"); n != 0 {
		t.Fatalf("点数单不能建订阅结账: %d", n)
	}
	if n := h.gpt.count("Account"); n != 1 {
		t.Fatalf("确认阶段不看 plan_type，只在检查时读一次账号: %d", n)
	}
	if n := theBrowser(t, h).count("EnsureNoSavePayment"); n != 1 {
		t.Fatalf("应确认一次「保存付款信息」: %d", n)
	}
	assertSubmitCount(t, h, 1)
	assertReleases(t, h, cardpool.ResultSuccess)
	waitEvent(t, o.OrderNo, "点数余额增加了 250.50")
	waitEvent(t, o.OrderNo, "我们的卡没有被存进用户账号")
	// 续费协程照常在跑（5 毫秒一轮），点数单绝不触发关闭续费
	time.Sleep(60 * time.Millisecond)
	if n := h.gpt.count("CancelRenewal") + h.gpt.count("SubscriptionStatus"); n != 0 {
		t.Fatalf("点数单不能关闭续费: %d", n)
	}
	if g := mustOrder(t, o.OrderNo); g.RenewalState != RenewalNone {
		t.Fatalf("点数单不适用续费: %+v", g)
	}
	if n := len(h.alerts.Texts()); n != 0 {
		t.Fatalf("不应告警: %v", h.alerts.Texts())
	}
}

// checking 的每一条拒绝原因：都在建结账会话之前结束，不碰卡池（没取卡、没注资），不启动浏览器。
func TestCreditCheckingRejections(t *testing.T) {
	cases := []struct {
		name string
		acc  func(*chatgpt.Account)
		sub  func(int) (chatgpt.RenewalInfo, error)
		code string
	}{
		{"没有账号 ID", func(a *chatgpt.Account) { a.AccountID = "" }, nil, "credit_account_unknown"},
		{"没有生效订阅", func(a *chatgpt.Account) { a.HasActiveSubscription, a.PlanType = false, "free" }, nil, "credit_no_subscription"},
		{"套餐类型不是 Plus 或 Pro", func(a *chatgpt.Account) { a.PlanType = "team" }, nil, "credit_plan_ineligible"},
		{"Go 账号", func(a *chatgpt.Account) { a.PlanType = "go" }, nil, "credit_plan_ineligible"},
		{"不是个人账号", func(a *chatgpt.Account) { a.Structure = "workspace" }, nil, "credit_not_personal"},
		{"账号类型缺失", func(a *chatgpt.Account) { a.Structure = "" }, nil, "credit_not_personal"},
		{"欠费", func(a *chatgpt.Account) { a.IsDelinquent = boolPtr(true) }, nil, "credit_delinquent"},
		{"账号接口没有欠费状态、订阅接口显示欠费", func(a *chatgpt.Account) { a.IsDelinquent = nil },
			func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{IsDelinquent: boolPtr(true)}, nil }, "credit_delinquent"},
		{"两个接口都没有欠费状态", func(a *chatgpt.Account) { a.IsDelinquent = nil },
			func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{}, nil }, "credit_delinquency_unknown"},
		{"订阅接口出错", func(a *chatgpt.Account) { a.IsDelinquent = nil },
			func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{}, errors.New("boom") }, "credit_delinquency_unknown"},
		{"计费币种不一致", func(a *chatgpt.Account) { a.BillingCurrency = "USD" }, nil, "credit_currency_mismatch"},
		{"计费币种缺失按美元算", func(a *chatgpt.Account) { a.BillingCurrency = "" }, nil, "credit_currency_mismatch"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newCreditHarness(t)
			acc := creditAccount()
			c.acc(&acc)
			h.gpt.account = acc
			h.gpt.subStatusFn = c.sub
			h.start(t)
			o := waitDone(t, h.submitCredit(t, false, false).OrderNo)
			assertNotSubmitted(t, o, c.code)
			if n := h.pool.total(); n != 0 {
				t.Fatalf("checking 放弃不能碰卡池（含注资）: %d 次", n)
			}
			if n := len(h.br.browsers()); n != 0 {
				t.Fatalf("不应启动浏览器: %d", n)
			}
			if n := h.gpt.count("CreateCreditCheckout") + h.gpt.count("CreateCheckout"); n != 0 {
				t.Fatalf("不应建结账会话: %d", n)
			}
		})
	}
}

// 账号接口没有欠费字段时，订阅接口明确说没欠费就能继续（这里用试运行走到读完金额）。
func TestCreditDelinquencyFromSubscriptionStatus(t *testing.T) {
	h := newCreditHarness(t)
	acc := creditAccount()
	acc.IsDelinquent = nil
	h.gpt.account = acc
	h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{IsDelinquent: boolPtr(false)}, nil }
	h.start(t)
	o := waitDone(t, h.submitCredit(t, true, false).OrderNo)
	if o.State != StatePreviewed || h.gpt.count("SubscriptionStatus") != 1 {
		t.Fatalf("应走到试运行完成: %+v", o)
	}
}

// 点数结账返回自定义结账页或未知类型：放弃，不注资、不打开任何页面。
func TestCreditCustomCheckoutAborts(t *testing.T) {
	cases := map[string]chatgpt.CreditCheckoutResult{
		"自定义结账页": {Tag: chatgpt.CheckoutTagCustom, SessionID: "cs_x"},
		"未知类型":   {Tag: "embedded_checkout_session", SessionID: "cs_y", URL: "https://checkout.stripe.com/c/pay/cs_y"},
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			h := newCreditHarness(t)
			r := res
			h.gpt.creditCheckout = &r
			h.start(t)
			o := waitDone(t, h.submitCredit(t, false, false).OrderNo)
			assertNotSubmitted(t, o, "custom_checkout_unsupported")
			if n := h.pool.total(); n != 0 {
				t.Fatalf("不能碰卡池: %d", n)
			}
			if n := len(h.br.browsers()); n != 0 {
				t.Fatalf("不应启动浏览器: %d", n)
			}
		})
	}
}

// 点数单的支付链接同样过支付域名白名单。
func TestCreditUntrustedCheckoutURLAborts(t *testing.T) {
	h := newCreditHarness(t)
	h.gpt.creditCheckout = &chatgpt.CreditCheckoutResult{Tag: chatgpt.CheckoutTagHosted, SessionID: "cs_z", URL: "https://chatgpt.com/checkout/openai_llc/cs_z"}
	h.start(t)
	o := waitDone(t, h.submitCredit(t, false, false).OrderNo)
	assertNotSubmitted(t, o, "checkout_url_untrusted")
	if n := h.pool.total(); n != 0 {
		t.Fatalf("不能碰卡池: %d", n)
	}
}

// 「保存付款信息」确认不了：注资之后、点支付之前放弃，按未提交处理，卡照常回收（Release aborted），并计入熔断的填表失败。
func TestCreditSavePaymentUnconfirmedAborts(t *testing.T) {
	h := newCreditHarness(t)
	h.br.savePayment = func() (string, error) {
		return "无法确认", errors.New("取消不掉「保存付款信息」勾选")
	}
	h.start(t)
	o := waitDone(t, h.submitCredit(t, false, false).OrderNo)
	assertNotSubmitted(t, o, "save_payment_unconfirmed")
	if o.SubmittedAt != nil {
		t.Fatalf("不应记下提交时间: %+v", o)
	}
	if h.pool.count("Fund") != 1 {
		t.Fatal("这时已经注资")
	}
	assertReleases(t, h, cardpool.ResultAborted)
	assertSubmitCount(t, h, 0)
	st, err := computeBreaker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.FillFailureStreak != 1 {
		t.Fatalf("应计入填表失败: %+v", st)
	}
}

// 开通单不检查「保存付款信息」。
func TestSubscriptionSkipsSavePayment(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.accountFn = planFrom(2)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess})
	h.start(t)
	o := waitDone(t, h.submit(t, false).OrderNo)
	if o.State != StateSucceeded || theBrowser(t, h).count("EnsureNoSavePayment") != 0 {
		t.Fatalf("开通单不应调用: %+v", o)
	}
}

// 证据冲突转人工：成功授权的金额或币种对不上，不等期限，立即转。
func TestCreditAuthMismatchNeedsReview(t *testing.T) {
	cases := map[string]cardpool.Transaction{
		"金额超出容差": approvedPHP("-600.00"),
		"币种对不上":  {TransactionID: "t1", Status: "approved", TxnAmount: "10.10", TxnCurrency: "USD", BillingAmount: "10.10", BillingCurrency: "USD"},
		"金额读不出":  approvedPHP("abc"),
	}
	for name, txn := range cases {
		t.Run(name, func(t *testing.T) {
			h := newCreditHarness(t)
			h.creditTxns(txn)
			h.balances("100", "350")
			h.start(t)
			g := waitDone(t, h.submitCredit(t, false, false).OrderNo)
			if g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" {
				t.Fatalf("应转人工: %+v", g)
			}
			assertReleases(t, h, cardpool.ResultAborted)
			if len(h.alerts.Texts()) == 0 {
				t.Fatal("转人工应告警")
			}
		})
	}
}

// 容差内（1%）的金额差算相符；账单币种与页面币种一致时也能核对。
func TestTxnAmountMismatchTolerance(t *testing.T) {
	o := &Order{Currency: "PHP", AmountMinor: 56500, AmountText: "₱565.00"}
	for _, ok := range []cardpool.Transaction{
		approvedPHP("565.00"), approvedPHP("-565"), approvedPHP("570.65"), approvedPHP("559.35"),
		{TxnAmount: "9.99", TxnCurrency: "USD", BillingAmount: "565.00", BillingCurrency: "php"},
	} {
		if why := txnAmountMismatch(ok, o); why != "" {
			t.Errorf("%+v 应相符: %s", ok, why)
		}
	}
	for _, bad := range []cardpool.Transaction{approvedPHP("570.66"), approvedPHP("559.34"), approvedPHP("1,565.00"), approvedPHP("")} {
		if why := txnAmountMismatch(bad, o); why == "" {
			t.Errorf("%+v 应不符", bad)
		}
	}
	// 无小数的币种：容差至少 1 个最小单位
	jp := &Order{Currency: "JPY", AmountMinor: 50, AmountText: "¥50"}
	if why := txnAmountMismatch(cardpool.Transaction{TxnAmount: "51", TxnCurrency: "JPY"}, jp); why != "" {
		t.Errorf("JPY 差 1 应相符: %s", why)
	}
	if why := txnAmountMismatch(cardpool.Transaction{TxnAmount: "52", TxnCurrency: "JPY"}, jp); why == "" {
		t.Error("JPY 差 2 应不符")
	}
}

// 页面成功、授权相符，但余额没有增加：到期前等，到期转人工（证据冲突）。人工定论成功也不关续费。
func TestCreditBalanceNotIncreasedNeedsReview(t *testing.T) {
	h := newCreditHarness(t)
	h.creditTxns(approvedPHP("565.00"))
	h.balances("100", "100")
	h.start(t)
	o := h.submitCredit(t, false, false)
	waitState(t, o.OrderNo, StateVerifying)
	waitCalls(t, h, "CreditBalance", 3)
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying {
		t.Fatalf("期限内应继续等: %+v", g)
	}
	h.clock.Advance(h.timeouts.Verify + time.Second)
	g := waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" || !strings.Contains(g.ErrorMessage, "点数余额没有增加 250") {
		t.Fatalf("应转人工并说明余额没增加: %+v", g)
	}
	if err := h.svc.Resolve(ctx, o.OrderNo, StateSucceeded, "用户确认点数已到账", "alice"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if g := mustOrder(t, o.OrderNo); g.State != StateSucceeded || g.RenewalState != RenewalNone || h.gpt.count("CancelRenewal") != 0 {
		t.Fatalf("点数单人工定论成功也不关续费: %+v", g)
	}
}

// 支付页认不出结果（真实 Stripe 页成功后多半直接跳走）：授权相符、且余额按快照增加了点数，两条同时满足才自动定成功，
// 不用等期限；成功依据写进时间线，resolved_by 为空，计入点数码的发码门槛。
func TestCreditPageUnknownProvenSucceeds(t *testing.T) {
	h := newCreditHarness(t)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeUnknown})
	h.creditTxns(approvedPHP("565.00"))
	h.balances("0", "250")
	h.start(t)
	o := h.submitCredit(t, false, false)
	g := waitDone(t, o.OrderNo)
	if g.State != StateSucceeded || g.PageOutcome != string(paybrowser.OutcomeUnknown) || g.ResolvedBy != "" || g.ErrorCode != "" || g.Failure != "" {
		t.Fatalf("授权与余额都到位应自动成功: %+v", g)
	}
	if g.CardSaved != CardSavedNo || g.RenewalState != RenewalNone {
		t.Fatalf("成功后照常核对已存卡、不关续费: %+v", g)
	}
	waitEvent(t, o.OrderNo, "点数付款成功（页面未识别；授权 565.00 PHP 相符；余额 +250.00，快照 0）")
	assertReleases(t, h, cardpool.ResultSuccess)
	if ok, err := db.HasRealSucceededPayOrder(ctx, testProvider, db.PayKindCredit); err != nil || !ok {
		t.Fatalf("这样定的成功单应计入点数码门槛: %v %v", ok, err)
	}
	if len(h.alerts.Texts()) != 0 {
		t.Fatalf("不应告警: %v", h.alerts.Texts())
	}
}

// 支付页认不出结果时，授权与余额缺一条（或余额读不出、增加不足、没有快照）都不放宽：期限内一直等，到期转人工（verify_timeout），说明里列出证据。
func TestCreditPageUnknownWithoutBothNeedsReview(t *testing.T) {
	snapshotThenErr := func(n int) (chatgpt.CreditBalanceInfo, error) {
		if n == 1 {
			return chatgpt.CreditBalanceInfo{Available: true, Balance: "0"}, nil
		}
		return chatgpt.CreditBalanceInfo{}, errors.New("boom")
	}
	snapshotThenMissing := func(n int) (chatgpt.CreditBalanceInfo, error) {
		if n == 1 {
			return chatgpt.CreditBalanceInfo{Available: true, Balance: "0"}, nil
		}
		return chatgpt.CreditBalanceInfo{Available: false}, nil
	}
	noSnapshot := func(n int) (chatgpt.CreditBalanceInfo, error) {
		if n == 1 {
			return chatgpt.CreditBalanceInfo{Available: false}, nil
		}
		return chatgpt.CreditBalanceInfo{Available: true, Balance: "250"}, nil
	}
	cases := []struct {
		name    string
		txns    []cardpool.Transaction
		balance func(int) (chatgpt.CreditBalanceInfo, error)
		before  string // 期望的余额快照
		want    string // 说明里应有的证据
	}{
		{"只有授权、余额没变", []cardpool.Transaction{approvedPHP("565.00")}, func(n int) (chatgpt.CreditBalanceInfo, error) {
			return chatgpt.CreditBalanceInfo{Available: true, Balance: "100"}, nil
		}, "100", "点数余额没有增加 250"},
		{"只有余额增加、没有授权", nil, nil, "0", "该卡没有成功授权"},
		{"授权还在处理中、余额增加", []cardpool.Transaction{{TransactionID: "t1", Status: "pending", TxnAmount: "565.00", TxnCurrency: "PHP"}}, nil, "0", "该卡的授权还在处理中"},
		{"读余额出错", []cardpool.Transaction{approvedPHP("565.00")}, snapshotThenErr, "0", "点数余额查不到"},
		{"余额接口这次没返回余额", []cardpool.Transaction{approvedPHP("565.00")}, snapshotThenMissing, "0", "点数余额查不到"},
		{"余额增加不足", []cardpool.Transaction{approvedPHP("565.00")}, func(n int) (chatgpt.CreditBalanceInfo, error) {
			if n == 1 {
				return chatgpt.CreditBalanceInfo{Available: true, Balance: "0"}, nil
			}
			return chatgpt.CreditBalanceInfo{Available: true, Balance: "249.99"}, nil
		}, "0", "点数余额没有增加 250（变化 249.99）"},
		{"没有余额快照", []cardpool.Transaction{approvedPHP("565.00")}, noSnapshot, "", "没有余额快照"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newCreditHarness(t)
			h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeUnknown})
			h.creditTxns(c.txns...) // 没有交易时卡池返回空列表（不是查询出错）
			if c.balance != nil {
				h.gpt.balanceFn = c.balance
			} else {
				h.balances("0", "250")
			}
			h.start(t)
			o := h.submitCredit(t, false, false)
			waitState(t, o.OrderNo, StateVerifying)
			if c.before != "" {
				waitCalls(t, h, "CreditBalance", 3) // 快照 1 次 + 至少两轮确认
			} else {
				time.Sleep(30 * time.Millisecond)
			}
			if g := mustOrder(t, o.OrderNo); g.State != StateVerifying || g.CreditBalanceBefore != c.before {
				t.Fatalf("缺一条证据不能自动定成功，期限内应继续等: %+v", g)
			}
			h.clock.Advance(h.timeouts.Verify + time.Second)
			g := waitDone(t, o.OrderNo)
			if g.State != StateNeedsReview || g.ErrorCode != "verify_timeout" || g.ResolvedBy != "" {
				t.Fatalf("应到期转人工: %+v", g)
			}
			for _, want := range []string{"支付页没有显示成功", c.want} {
				if !strings.Contains(g.ErrorMessage, want) {
					t.Fatalf("说明里应有 %q: %s", want, g.ErrorMessage)
				}
			}
			if ok, err := db.HasRealSucceededPayOrder(ctx, testProvider, db.PayKindCredit); err != nil || ok {
				t.Fatalf("转人工的单不计入门槛: %v %v", ok, err)
			}
		})
	}
}

// 页面显示被拒但授权相符、余额也增加了：不走放宽规则，证据冲突转人工。
func TestCreditDeclinedWithProofNeedsReview(t *testing.T) {
	h := newCreditHarness(t)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeDeclined, text: "Your card was declined."})
	h.creditTxns(approvedPHP("565.00"))
	h.balances("0", "250")
	h.start(t)
	o := h.submitCredit(t, false, false)
	waitState(t, o.OrderNo, StateVerifying)
	waitCalls(t, h, "CreditBalance", 3)
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying {
		t.Fatalf("页面被拒不能自动定成功: %+v", g)
	}
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	g := waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" {
		t.Fatalf("应证据冲突转人工: %+v", g)
	}
}

// 放宽规则只对点数单生效：订阅开通单页面认不出、卡上有成功授权，账号没开通照旧到期转人工，也不去读点数余额。
func TestSubscriptionPageUnknownUnaffected(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeUnknown})
	h.creditTxns(cardpool.Transaction{TransactionID: "t1", Status: "approved", TxnAmount: "1120.00", TxnCurrency: "PHP"})
	h.balances("0", "250")
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	waitCalls(t, h, "Account", 3)
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying {
		t.Fatalf("订阅单账号没开通不能定成功: %+v", g)
	}
	h.clock.Advance(h.timeouts.Verify + time.Second)
	g := waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "verify_timeout" || g.Kind != KindSubscription {
		t.Fatalf("订阅单应照旧到期转人工: %+v", g)
	}
	if n := h.gpt.count("CreditBalance"); n != 0 {
		t.Fatalf("订阅单不读点数余额: %d", n)
	}
}

// 页面显示被拒：没有授权、余额没变，宽限期后定论失败（declined）；余额却增加了就是证据冲突。
func TestCreditDeclined(t *testing.T) {
	t.Run("证据干净定论失败", func(t *testing.T) {
		h := newCreditHarness(t)
		h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeDeclined, text: "Your card was declined."})
		h.balances("100", "100")
		h.start(t)
		o := h.submitCredit(t, false, false)
		waitState(t, o.OrderNo, StateVerifying)
		h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
		g := waitDone(t, o.OrderNo)
		if g.State != StateFailed || g.Failure != FailureDeclined || g.ErrorCode != "card_declined" {
			t.Fatalf("应 failed / declined: %+v", g)
		}
		assertReleases(t, h, cardpool.ResultFailed)
	})
	t.Run("余额增加了转人工", func(t *testing.T) {
		h := newCreditHarness(t)
		h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeDeclined, text: "Your card was declined."})
		h.balances("100", "350")
		h.start(t)
		o := h.submitCredit(t, false, false)
		waitState(t, o.OrderNo, StateVerifying)
		h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
		g := waitDone(t, o.OrderNo)
		if g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" {
			t.Fatalf("应转人工: %+v", g)
		}
	})
	t.Run("有成功授权转人工", func(t *testing.T) {
		h := newCreditHarness(t)
		h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeDeclined, text: "Your card was declined."})
		h.creditTxns(approvedPHP("565.00"))
		h.start(t)
		o := h.submitCredit(t, false, false)
		waitState(t, o.OrderNo, StateVerifying)
		h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
		g := waitDone(t, o.OrderNo)
		if g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" {
			t.Fatalf("应转人工: %+v", g)
		}
	})
}

// 余额接口不可用（没有快照）：页面成功加授权相符即可定成功；余额查询出错时等到期转人工。
func TestCreditBalanceSnapshot(t *testing.T) {
	t.Run("没有快照不核对余额", func(t *testing.T) {
		h := newCreditHarness(t)
		h.creditTxns(approvedPHP("565.00"))
		h.start(t)
		g := waitDone(t, h.submitCredit(t, false, false).OrderNo)
		if g.State != StateSucceeded || g.CreditBalanceBefore != "" {
			t.Fatalf("应成功: %+v", g)
		}
	})
	t.Run("有快照但余额查询出错", func(t *testing.T) {
		h := newCreditHarness(t)
		h.creditTxns(approvedPHP("565.00"))
		h.gpt.balanceFn = func(n int) (chatgpt.CreditBalanceInfo, error) {
			if n == 1 {
				return chatgpt.CreditBalanceInfo{Available: true, Balance: "5"}, nil
			}
			return chatgpt.CreditBalanceInfo{}, errors.New("boom")
		}
		h.start(t)
		o := h.submitCredit(t, false, false)
		waitState(t, o.OrderNo, StateVerifying)
		waitEvent(t, o.OrderNo, "查询点数余额失败")
		h.clock.Advance(h.timeouts.Verify + time.Second)
		g := waitDone(t, o.OrderNo)
		if g.State != StateNeedsReview || !strings.Contains(g.ErrorMessage, "点数余额查不到") {
			t.Fatalf("应转人工: %+v", g)
		}
	})
}

// 成功后重读已存卡：新出现了尾号相同的卡就标 saved 并告警；付款前就在的同尾号卡不算。
func TestCreditCardSavedCheck(t *testing.T) {
	ours := chatgpt.SavedCard{ID: "pm_new", Type: "card", Last4: "4242"}
	cases := []struct {
		name   string
		before []chatgpt.SavedCard
		after  []chatgpt.SavedCard
		want   string
		alert  bool
	}{
		{"新存了我们的卡", []chatgpt.SavedCard{}, []chatgpt.SavedCard{ours}, CardSavedYes, true},
		{"同尾号的卡付款前就在", []chatgpt.SavedCard{{ID: "pm_old", Last4: "4242"}}, []chatgpt.SavedCard{{ID: "pm_old", Last4: "4242"}}, CardSavedNo, false},
		{"新卡尾号不同", []chatgpt.SavedCard{}, []chatgpt.SavedCard{{ID: "pm_x", Last4: "1111"}}, CardSavedNo, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newCreditHarness(t)
			h.creditTxns(approvedPHP("565.00"))
			h.gpt.savedCardsFn = func(n int) ([]chatgpt.SavedCard, error) {
				if n == 1 {
					return c.before, nil
				}
				return c.after, nil
			}
			h.start(t)
			g := waitDone(t, h.submitCredit(t, false, false).OrderNo)
			if g.State != StateSucceeded || g.CardSaved != c.want {
				t.Fatalf("card_saved 应为 %s: %+v", c.want, g)
			}
			if got := len(h.alerts.Texts()) > 0; got != c.alert {
				t.Fatalf("告警 %v，期望 %v: %v", got, c.alert, h.alerts.Texts())
			}
			if c.alert && !strings.Contains(h.alerts.Texts()[0], "尾号 4242") {
				t.Fatalf("告警应写尾号: %v", h.alerts.Texts())
			}
		})
	}
	t.Run("成功后读不到已存卡", func(t *testing.T) {
		h := newCreditHarness(t)
		h.creditTxns(approvedPHP("565.00"))
		h.gpt.savedCardsFn = func(n int) ([]chatgpt.SavedCard, error) {
			if n == 1 {
				return nil, nil
			}
			return nil, errors.New("boom")
		}
		h.start(t)
		g := waitDone(t, h.submitCredit(t, false, false).OrderNo)
		if g.State != StateSucceeded || g.CardSaved != CardSavedUnknown {
			t.Fatalf("读不到已存卡不影响成功，标 unknown: %+v", g)
		}
	})
}

// 两种试运行都支持点数单：不碰卡池、不点支付；试运行到填表也要确认「保存付款信息」。
func TestCreditDryRuns(t *testing.T) {
	t.Run("读金额", func(t *testing.T) {
		h := newCreditHarness(t)
		h.start(t)
		g := waitDone(t, h.submitCredit(t, true, false).OrderNo)
		if g.State != StatePreviewed || g.AmountMinor != 56500 {
			t.Fatalf("应试运行完成: %+v", g)
		}
		if h.pool.total() != 0 || h.br.submitted() != 0 {
			t.Fatal("试运行不能碰卡池、不能点支付")
		}
	})
	t.Run("填表", func(t *testing.T) {
		h := newCreditHarness(t)
		h.start(t)
		g := waitDone(t, h.submitCredit(t, true, true).OrderNo)
		if g.State != StatePreviewed || theBrowser(t, h).count("EnsureNoSavePayment") != 1 {
			t.Fatalf("应试运行到填表完成并确认勾选框: %+v", g)
		}
		if h.pool.total() != 0 || h.br.submitted() != 0 {
			t.Fatal("试运行不能碰卡池、不能点支付")
		}
	})
	t.Run("填表时勾选框确认不了", func(t *testing.T) {
		h := newCreditHarness(t)
		h.br.savePayment = func() (string, error) { return "无法确认", errors.New("有 frame 读不出") }
		h.start(t)
		g := waitDone(t, h.submitCredit(t, true, true).OrderNo)
		assertNotSubmitted(t, g, "dry_run_fill_failed")
	})
	t.Run("金额超过上限的试运行照样读完", func(t *testing.T) {
		h := newCreditHarness(t)
		h.br.amount, h.br.amountText = 99999999, "₱999,999.99"
		h.start(t)
		if g := waitDone(t, h.submitCredit(t, true, false).OrderNo); g.State != StatePreviewed {
			t.Fatalf("试运行不用上限: %+v", g)
		}
	})
}

// 真实点数单超过「点数档 × 币种」的上限：读金额后放弃，不注资。
func TestCreditAmountOverCap(t *testing.T) {
	h := newCreditHarness(t)
	h.br.amount, h.br.amountText = 63281, "₱632.81"
	h.start(t)
	o := waitDone(t, h.submitCredit(t, false, false).OrderNo)
	assertNotSubmitted(t, o, "amount_over_cap")
	if h.pool.count("Fund") != 0 {
		t.Fatal("超上限不能注资")
	}
}

// 没有配置的地区不能下真实点数单。
func TestCreditRegionWithoutConfigUnavailable(t *testing.T) {
	h := newCreditHarness(t)
	_, err := h.svc.Submit(ctx, Request{Provider: testProvider, PlanKey: "credit250", Region: "US", Session: testSession, Source: "admin_test"})
	if !errors.Is(err, ErrPlanUnavailable) {
		t.Fatalf("US 没有配置应不可用: %v", err)
	}
}

func TestDecimalMinor(t *testing.T) {
	cases := []struct {
		in, cur string
		want    int64
		ok      bool
	}{
		{"565.00", "PHP", 56500, true}, {"-565", "PHP", 56500, true}, {"+1.005", "PHP", 101, true},
		{"1234", "JPY", 1234, true}, {"12.5", "JPY", 13, true}, {"", "PHP", 0, false}, {"1,000.00", "PHP", 0, false},
		{"1e3", "PHP", 0, false}, {"1/2", "PHP", 0, false}, {" 7.1 ", "USD", 710, true},
	}
	for _, c := range cases {
		got, ok := decimalMinor(c.in, c.cur)
		if ok != c.ok || got != c.want {
			t.Errorf("decimalMinor(%q, %s) = %d, %v；期望 %d, %v", c.in, c.cur, got, ok, c.want, c.ok)
		}
	}
}
