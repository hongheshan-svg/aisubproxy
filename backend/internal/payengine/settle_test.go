package payengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

const (
	testCardNo       = "4242424242424242"
	testCardNoSpaced = "4242 4242 4242 4242"
	testCardNoDashed = "4242-4242-4242-4242"
	testCVV          = "739"
	testLeaseToken   = "lease-tok-1"
	testCardID       = "card-1"
)

var (
	accountFree = chatgpt.Account{AccountID: "acc-1", PlanType: "free"}
	accountPlus = chatgpt.Account{AccountID: "acc-1", PlanType: "plus", HasActiveSubscription: true}
)

// testClock 是可前进、并发安全的测试时钟，注入 Options.Now。
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newPayHarness 在 newHarness 之上装好付款步骤的替身（默认全部成功）、可前进的时钟与缩短的超时；
// 默认账号始终是 free、支付页不给结果前须由测试设置 h.br.waitOutcome。
func newPayHarness(t *testing.T, tune func(*Timeouts)) *harness {
	t.Helper()
	h := newHarness(t)
	h.clock = &testClock{t: time.Now().UTC()}
	h.timeouts = DefaultTimeouts()
	h.timeouts.PageLoad = 5 * time.Second
	h.timeouts.Fill = 5 * time.Second
	h.timeouts.SubmitResult = 5 * time.Second
	h.timeouts.OTP = 5 * time.Second
	h.timeouts.Fund = 5 * time.Second
	h.timeouts.Verify = 10 * time.Minute
	h.timeouts.VerifyEvery = 0
	h.timeouts.DeclineGrace = 2 * time.Minute
	h.timeouts.VerifyTick = 5 * time.Millisecond
	if tune != nil {
		tune(&h.timeouts)
	}
	h.pool.acquire = func(r cardpool.AcquireRequest) (*cardpool.Lease, error) {
		return &cardpool.Lease{Token: testLeaseToken, CardID: testCardID, ProductID: "p1", Currency: "USD",
			PlanKey: r.PlanKey, OrderRef: r.OrderRef, AccountEmail: r.AccountEmail}, nil
	}
	h.pool.cardInfo = func(string) (cardpool.CardInfo, error) {
		return cardpool.CardInfo{Last4: "4242", HolderFirstName: "JOHN", HolderLastName: "DOE"}, nil
	}
	h.pool.fund = func(context.Context, *cardpool.Lease, int64) (*cardpool.Order, error) {
		return &cardpool.Order{OutOrderNo: "FUND-1", State: cardpool.StateProcessing}, nil
	}
	h.pool.waitOrder = func(_ context.Context, no string) (*cardpool.Order, error) {
		return &cardpool.Order{OutOrderNo: no, State: cardpool.StateSuccess}, nil
	}
	h.pool.sensitive = func(*cardpool.Lease) (*marswallex.CardSensitive, error) {
		return &marswallex.CardSensitive{CardID: testCardID, CardNo: testCardNo, CVV2: testCVV, ExpiryDate: "08/28"}, nil
	}
	h.pool.release = func(*cardpool.Lease, cardpool.Outcome) error { return nil }
	h.pool.listTxns = func(cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
		return []cardpool.Transaction{}, 0, nil
	}
	h.br.fill = func(paybrowser.Card, paybrowser.Billing) error { return nil }
	h.br.submit = func() error { return nil }
	h.br.enterOTP = func(string) error { return nil }
	h.gpt.account = accountFree
	h.rebuild(h.pool)
	return h
}

// rebuild 用给定卡池重建引擎，其余依赖取 harness 的替身。
func (h *harness) rebuild(pool Pool) {
	h.svc = New(Options{
		Pool: pool,
		ChatGPT: func(proxyURL string) (ChatGPT, error) {
			h.gpt.hit("New", proxyURL)
			return h.gpt, nil
		},
		Browser:  h.br.New,
		Now:      h.clock.Now,
		Alert:    h.alerts.add,
		ShotDir:  h.shotDir,
		Timeouts: &h.timeouts,
	})
}

type outcomeStep struct {
	out  paybrowser.Outcome
	text string
	err  error
}

// outcomes 让第 n 次 WaitOutcome 返回第 n 步，步数用完后重复最后一步。
func outcomes(steps ...outcomeStep) func(context.Context, int) (paybrowser.Outcome, string, error) {
	return func(_ context.Context, n int) (paybrowser.Outcome, string, error) {
		if n > len(steps) {
			n = len(steps)
		}
		s := steps[n-1]
		return s.out, s.text, s.err
	}
}

// planFrom 让第 n 次及之后的 Account 返回目标套餐，之前返回 free（第 1 次是步骤 2 的预检）。
func planFrom(from int) func(int) (chatgpt.Account, error) {
	return func(n int) (chatgpt.Account, error) {
		if n >= from {
			return accountPlus, nil
		}
		return accountFree, nil
	}
}

// presetOrder 直接插入一张处于 state 的单据（带可解开的会话密文），再写入 set 中的列。
func presetOrder(t *testing.T, email, state string, set map[string]any) *Order {
	t.Helper()
	key, err := secretbox.LoadKey()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := secretbox.Seal(key, sessionAAD, sessionFor(email))
	if err != nil {
		t.Fatal(err)
	}
	o := &Order{Source: "admin_test", SourceRef: "admin", PlanKey: "plus", Region: "PH", Currency: "PHP",
		CapMinor: 120000, FundCents: 3000, AccountEmail: email, State: state, SessionSealed: sealed}
	if err := insertOrder(ctx, o); err != nil {
		t.Fatal(err)
	}
	if len(set) > 0 {
		if err := transition(ctx, o.OrderNo, state, state, "info", "预置", set); err != nil {
			t.Fatal(err)
		}
	}
	return mustOrder(t, o.OrderNo)
}

func mustOrder(t *testing.T, no string) *Order {
	t.Helper()
	g, err := getOrder(ctx, no)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// waitState 轮询到单据进入 state，最多 5 秒。
func waitState(t *testing.T, no, state string) *Order {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		g, err := getOrder(ctx, no)
		if err == nil && g.State == state {
			return g
		}
		if time.Now().After(deadline) {
			t.Fatalf("单据 %s 未进入 %s: %+v %v", no, state, g, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitEvent 轮询到时间线上出现含 sub 的事件，最多 5 秒。
func waitEvent(t *testing.T, no, sub string) Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ev, err := listEvents(ctx, no)
		if err == nil {
			for _, e := range ev {
				if strings.Contains(e.Message, sub) {
					return e
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("单据 %s 的时间线未出现 %q: %+v %v", no, sub, ev, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitAccountCalls 轮询到 Account 至少被调用 n 次，最多 5 秒。
func waitAccountCalls(t *testing.T, h *harness, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.gpt.count("Account") < n {
		if time.Now().After(deadline) {
			t.Fatalf("Account 未达到 %d 次: %d", n, h.gpt.count("Account"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertReleases(t *testing.T, h *harness, want ...string) []releaseCall {
	t.Helper()
	got := h.pool.released()
	if len(got) != len(want) {
		t.Fatalf("Release 应调用 %d 次: %+v", len(want), got)
	}
	for i := range want {
		if got[i].Outcome.Result != want[i] {
			t.Fatalf("第 %d 次 Release 结果应为 %s: %+v", i+1, want[i], got[i])
		}
	}
	return got
}

func assertSubmitCount(t *testing.T, h *harness, want int) {
	t.Helper()
	if n := h.br.submitted(); n != want {
		t.Fatalf("Submit 应调用 %d 次，实际 %d 次", want, n)
	}
}

func theBrowser(t *testing.T, h *harness) *fakeBrowser {
	t.Helper()
	bs := h.br.browsers()
	if len(bs) != 1 {
		t.Fatalf("应启动 1 个浏览器: %d", len(bs))
	}
	return bs[0]
}

func TestHappyPathSucceeded(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.accountFn = planFrom(3)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess, text: "Thanks"})
	h.start(t)
	before := h.clock.Now().Unix()
	o := h.submit(t, false)
	g := waitDone(t, o.OrderNo)

	if g.State != StateSucceeded || g.Failure != "" || g.ErrorCode != "" {
		t.Fatalf("应成功: %+v", g)
	}
	if h.gpt.count("Account") != 3 {
		t.Fatalf("Account 应调用 3 次（预检 + 2 次确认）: %d", h.gpt.count("Account"))
	}
	if g.SessionSealed != "" || g.FinishedAt == nil {
		t.Fatalf("终态应清空会话并记结束时间: %+v", g)
	}
	if g.SubmittedAt == nil || *g.SubmittedAt != before {
		t.Fatalf("submitted_at 应取 Options.Now: %v, 期望 %d", g.SubmittedAt, before)
	}
	if g.VerifyUntil == nil || *g.VerifyUntil != before+int64(h.timeouts.Verify/time.Second) {
		t.Fatalf("verify_until 应为 now + Verify: %v", g.VerifyUntil)
	}
	if g.PageOutcome != "success" || g.CardID != testCardID || g.CardLast4 != "4242" ||
		g.LeaseToken != testLeaseToken || g.FundOrderNo != "FUND-1" {
		t.Fatalf("单据字段不符: %+v", g)
	}
	if !contains([]string{"DE", "OR", "NH", "MT", "AK"}, g.BillingState) {
		t.Fatalf("账单州应为免税州: %q", g.BillingState)
	}

	if r := h.pool.lastArgs("Acquire")[0].(cardpool.AcquireRequest); r.PlanKey != "plus" || r.OrderRef != o.OrderNo || r.AccountEmail != "buyer@example.com" {
		t.Fatalf("Acquire 参数不符: %+v", r)
	}
	if h.pool.count("Fund") != 1 || h.pool.lastArgs("Fund")[1] != int64(3000) {
		t.Fatalf("应注资 1 次 3000 分: %d %v", h.pool.count("Fund"), h.pool.lastArgs("Fund"))
	}
	if a := h.pool.lastArgs("WaitOrder"); a[0] != "FUND-1" {
		t.Fatalf("WaitOrder 单号不符: %v", a)
	}
	rel := assertReleases(t, h, cardpool.ResultSuccess)
	if l := rel[0].Lease; l.Token != testLeaseToken || l.CardID != testCardID || l.PlanKey != "plus" ||
		l.OrderRef != o.OrderNo || l.AccountEmail != "buyer@example.com" {
		t.Fatalf("释放用的租约不符: %+v", l)
	}

	b := theBrowser(t, h)
	assertSubmitCount(t, h, 1)
	if b.count("Close") != 1 {
		t.Fatalf("浏览器应关闭 1 次: %d", b.count("Close"))
	}
	fill := b.lastArgs("Fill")
	card, bill := fill[0].(paybrowser.Card), fill[1].(paybrowser.Billing)
	if card != (paybrowser.Card{Number: testCardNo, ExpMonth: "08", ExpYear: "28", CVC: testCVV}) {
		t.Fatalf("填表的卡不符: %+v", card)
	}
	if bill.Name != "JOHN DOE" || bill.State != g.BillingState || bill.Country != "US" {
		t.Fatalf("账单信息不符: %+v", bill)
	}
	// 填卡之后不得再整页截图
	if b.count("Shot") != 1 {
		t.Fatalf("没有 3DS 时只应在填卡前截 1 张图: %d", b.count("Shot"))
	}

	ev := eventStates(t, o.OrderNo)
	if !containsInOrder(ev, StateFunding, StateFilling, StateSubmitted, StateVerifying, StateSucceeded) {
		t.Fatalf("事件顺序不符: %+v", ev)
	}
}

func Test3DSFlow(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.accountFn = planFrom(2)
	h.pool.waitOTP = func(context.Context, string, time.Time) (string, error) { return "123456", nil }
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeChallenge}, outcomeStep{out: paybrowser.OutcomeSuccess, text: "ok"})
	h.start(t)
	o := h.submit(t, false)
	g := waitDone(t, o.OrderNo)

	if g.State != StateSucceeded || g.PageOutcome != "success" {
		t.Fatalf("应成功: %+v", g)
	}
	a := h.pool.lastArgs("WaitOTP")
	if a[0] != testCardID || !a[1].(time.Time).Equal(time.Unix(*g.SubmittedAt, 0)) {
		t.Fatalf("WaitOTP 的 since 应为 submitted_at: %v，submitted_at=%d", a, *g.SubmittedAt)
	}
	b := theBrowser(t, h)
	if otp := b.lastArgs("EnterOTP"); len(otp) != 1 || otp[0] != "123456" {
		t.Fatalf("EnterOTP 参数不符: %v", otp)
	}
	if s := b.lastArgs("Shot"); s[1] != paybrowser.ShotChallenge || s[0] != "02-challenge" {
		t.Fatalf("应只截 3DS 验证框: %v", s)
	}
	assertSubmitCount(t, h, 1)
	ev := eventStates(t, o.OrderNo)
	if !containsInOrder(ev, StateSubmitted, StateRequires3DS, StateVerifying, StateSucceeded) {
		t.Fatalf("事件顺序不符: %+v", ev)
	}
	for _, e := range ev {
		if strings.Contains(e.Message, "123456") {
			t.Fatalf("时间线不应含验证码: %+v", e)
		}
	}
}

func TestSecondChallengeNotAnswered(t *testing.T) {
	h := newPayHarness(t, nil)
	h.pool.waitOTP = func(context.Context, string, time.Time) (string, error) { return "123456", nil }
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeChallenge})
	h.start(t)
	o := h.submit(t, false)
	g := waitState(t, o.OrderNo, StateVerifying)
	if g.PageOutcome != "unknown" {
		t.Fatalf("再次出现验证框应按结果不明确认: %+v", g)
	}
	if h.pool.count("WaitOTP") != 1 || theBrowser(t, h).count("EnterOTP") != 1 {
		t.Fatalf("验证码只应等待并填写 1 次: %d", h.pool.count("WaitOTP"))
	}
	assertSubmitCount(t, h, 1)
}

func TestOTPTimeoutNeedsReview(t *testing.T) {
	h := newPayHarness(t, func(to *Timeouts) { to.OTP = 20 * time.Millisecond })
	h.pool.waitOTP = func(c context.Context, _ string, _ time.Time) (string, error) {
		<-c.Done()
		return "", c.Err()
	}
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeChallenge})
	h.start(t)
	o := h.submit(t, false)
	g := waitState(t, o.OrderNo, StateVerifying)
	if g.PageOutcome != "unknown" || g.FinishedAt != nil {
		t.Fatalf("验证码超时应转确认: %+v", g)
	}
	if b := theBrowser(t, h); b.count("Close") != 1 || b.count("EnterOTP") != 0 {
		t.Fatalf("应关闭浏览器且未填验证码: close=%d enter=%d", b.count("Close"), b.count("EnterOTP"))
	}
	waitAccountCalls(t, h, 2)
	if got := mustOrder(t, o.OrderNo); got.State != StateVerifying || len(h.alerts.Texts()) != 0 || len(h.pool.released()) != 0 {
		t.Fatalf("Verify 时长内不应定论: %+v", got)
	}

	h.clock.Advance(h.timeouts.Verify + time.Second)
	g = waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "verify_timeout" || g.SessionSealed != "" {
		t.Fatalf("应转待人工: %+v", g)
	}
	if a := h.alerts.Texts(); len(a) != 1 || !strings.Contains(a[0], o.OrderNo) || !strings.Contains(a[0], "待人工") {
		t.Fatalf("应告警 1 次: %v", a)
	}
	assertReleases(t, h, cardpool.ResultAborted)
	assertSubmitCount(t, h, 1)
}

func declinedHarness(t *testing.T) *harness {
	t.Helper()
	h := newPayHarness(t, nil)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeDeclined, text: "Your card was declined."})
	return h
}

func TestDeclinedAfterGrace(t *testing.T) {
	h := declinedHarness(t)
	h.start(t)
	start := h.clock.Now().Unix()
	o := h.submit(t, false)
	g := waitState(t, o.OrderNo, StateVerifying)
	if g.PageOutcome != "declined" || g.VerifyUntil == nil || *g.VerifyUntil != start+int64(h.timeouts.DeclineGrace/time.Second) {
		t.Fatalf("被拒后 verify_until 应为 now + DeclineGrace: %+v", g)
	}
	waitAccountCalls(t, h, 2)
	if got := mustOrder(t, o.OrderNo); got.State != StateVerifying || len(h.pool.released()) != 0 {
		t.Fatalf("宽限期内不应定论: %+v", got)
	}

	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	g = waitDone(t, o.OrderNo)
	if g.State != StateFailed || g.Failure != FailureDeclined || !strings.Contains(g.ErrorMessage, "declined") || g.SessionSealed != "" {
		t.Fatalf("应 failed / declined: %+v", g)
	}
	rel := assertReleases(t, h, cardpool.ResultFailed)
	if rel[0].Outcome.ErrorCode == "" {
		t.Fatal("Release(failed) 的 ErrorCode 不应为空")
	}
	assertSubmitCount(t, h, 1)
	if n := len(h.alerts.Texts()); n != 0 {
		t.Fatalf("明确被拒不告警: %d", n)
	}
}

func TestDeclinedButAccountOpenedSucceeds(t *testing.T) {
	h := declinedHarness(t)
	h.gpt.accountFn = planFrom(2)
	h.start(t)
	g := waitDone(t, h.submit(t, false).OrderNo)
	if g.State != StateSucceeded || g.ErrorMessage != "" {
		t.Fatalf("账号已开通应成功，并清掉页面提示: %+v", g)
	}
	assertReleases(t, h, cardpool.ResultSuccess)
}

// txnAt 造一条该卡的授权交易。
func txnAt(id, status string, at int64) cardpool.Transaction {
	return cardpool.Transaction{TransactionID: id, Kind: "AUTH", CardID: testCardID, Status: status, OccurredAt: &at, UpdatedAt: at}
}

// pagedTxns 模拟 ListTransactions 的分页；all 须已按时间倒序。
func pagedTxns(all []cardpool.Transaction) func(cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
	return func(f cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
		size := f.PageSize
		if size < 1 {
			size = 20
		}
		if size > 100 {
			size = 100
		}
		page := f.Page
		if page < 1 {
			page = 1
		}
		from := (page - 1) * size
		if from >= len(all) {
			return []cardpool.Transaction{}, len(all), nil
		}
		to := from + size
		if to > len(all) {
			to = len(all)
		}
		return all[from:to], len(all), nil
	}
}

func TestDeclinedButApprovedAuthNeedsReview(t *testing.T) {
	cases := map[string]func(at int64) cardpool.Transaction{
		"提交之后的成功授权": func(at int64) cardpool.Transaction { return txnAt("A1", "approved", at+10) },
		// 平台状态原样入库，大小写不能决定定论
		"状态大写": func(at int64) cardpool.Transaction { return txnAt("A1", "APPROVED", at+10) },
		// 时间只精确到秒：与提交同一秒的授权也可能是这次付款
		"与提交同一秒": func(at int64) cardpool.Transaction { return txnAt("A1", "approved", at) },
		"没有发生时间": func(at int64) cardpool.Transaction {
			return cardpool.Transaction{TransactionID: "A1", Kind: "AUTH", CardID: testCardID, Status: "approved", UpdatedAt: at + 10}
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			h := declinedHarness(t)
			h.pool.listTxns = pagedTxns([]cardpool.Transaction{mk(h.clock.Now().Unix())})
			h.start(t)
			o := h.submit(t, false)
			waitState(t, o.OrderNo, StateVerifying)
			h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
			g := waitDone(t, o.OrderNo)
			if g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" || g.Failure != "" {
				t.Fatalf("证据冲突应转待人工: %+v", g)
			}
			assertReleases(t, h, cardpool.ResultAborted)
			if a := h.alerts.Texts(); len(a) != 1 {
				t.Fatalf("应告警 1 次: %v", a)
			}
			if a := h.pool.lastArgs("ListTransactions"); a[0].(cardpool.TxnFilter).CardID != testCardID {
				t.Fatalf("应按卡查交易: %v", a)
			}
		})
	}
}

func TestApprovedAuthBeforeSubmitIgnored(t *testing.T) {
	h := declinedHarness(t)
	at := h.clock.Now().Unix()
	h.pool.listTxns = pagedTxns([]cardpool.Transaction{
		{TransactionID: "R1", Kind: "AUTH_REVERSAL", CardID: testCardID, Status: "approved", UpdatedAt: at + 10, OccurredAt: ptr(at + 10)},
		txnAt("F1", "failed", at+5),
		txnAt("A1", "approved", at-301), // 刚好在 5 分钟容差之外
		txnAt("A0", "approved", at-3600),
	})
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	g := waitDone(t, o.OrderNo)
	if g.State != StateFailed || g.Failure != FailureDeclined {
		t.Fatalf("提交之前的授权不算: %+v", g)
	}
	assertReleases(t, h, cardpool.ResultFailed)
}

func ptr(v int64) *int64 { return &v }

// R18：transaction_time 是平台时钟，比本机早一点的授权也可能就是这次付款，不能定成被拒。
func TestApprovedAuthWithinSkewNeedsReview(t *testing.T) {
	for name, before := range map[string]int64{"提交前 30 秒": 30, "恰好在容差边界": 300} {
		t.Run(name, func(t *testing.T) {
			h := declinedHarness(t)
			h.pool.listTxns = pagedTxns([]cardpool.Transaction{txnAt("A1", "approved", h.clock.Now().Unix()-before)})
			h.start(t)
			o := h.submit(t, false)
			waitState(t, o.OrderNo, StateVerifying)
			h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
			g := waitDone(t, o.OrderNo)
			if g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" || g.Failure != "" {
				t.Fatalf("容差内的成功授权应转待人工: %+v", g)
			}
			assertReleases(t, h, cardpool.ResultAborted)
		})
	}
}

// 第一页全是提交之后的其它交易，成功授权在第二页：只看第一页就会把已扣款的单子定成 declined。
func TestApprovedAuthOnLaterPageNeedsReview(t *testing.T) {
	h := declinedHarness(t)
	at := h.clock.Now().Unix()
	all := make([]cardpool.Transaction, 0, 130)
	for i := 0; i < 120; i++ {
		all = append(all, txnAt(fmt.Sprintf("F%d", i), "failed", at+1000-int64(i)))
	}
	all = append(all, txnAt("A1", "approved", at+5), txnAt("A0", "approved", at-100))
	h.pool.listTxns = pagedTxns(all)
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	g := waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" {
		t.Fatalf("第二页的成功授权也要算: %+v", g)
	}
}

// 翻页失败不等于「没有授权」：只记事件、等下次检查，恢复后再定论。
func TestTxnPageErrorDoesNotDecline(t *testing.T) {
	h := declinedHarness(t)
	at := h.clock.Now().Unix()
	all := make([]cardpool.Transaction, 0, 120)
	for i := 0; i < 110; i++ {
		all = append(all, txnAt(fmt.Sprintf("F%d", i), "failed", at+1000-int64(i)))
	}
	paged := pagedTxns(all)
	var broken atomic.Bool
	broken.Store(true)
	h.pool.listTxns = func(f cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
		if f.Page >= 2 && broken.Load() {
			return nil, 0, errors.New("读交易表失败")
		}
		return paged(f)
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	e := waitEvent(t, o.OrderNo, "读交易表失败")
	if e.Level != "warn" || e.State != StateVerifying {
		t.Fatalf("应记 warn 事件: %+v", e)
	}
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying || len(h.pool.released()) != 0 {
		t.Fatalf("翻页失败不应定论: %+v", g)
	}
	broken.Store(false)
	g := waitDone(t, o.OrderNo)
	if g.State != StateFailed || g.Failure != FailureDeclined {
		t.Fatalf("恢复后应定论被拒: %+v", g)
	}
}

// 账号查询失败时无法确认「账号未开通」，不能定成被拒。
func TestAccountErrorDoesNotDecline(t *testing.T) {
	h := declinedHarness(t)
	var broken atomic.Bool
	h.gpt.accountFn = func(n int) (chatgpt.Account, error) {
		if broken.Load() {
			return chatgpt.Account{}, errors.New("ChatGPT 返回 HTTP 502")
		}
		return accountFree, nil
	}
	broken.Store(false)
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	broken.Store(true)
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	waitEvent(t, o.OrderNo, "HTTP 502")
	time.Sleep(30 * time.Millisecond)
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying {
		t.Fatalf("账号查询失败不应定论: %+v", g)
	}
	broken.Store(false)
	if g := waitDone(t, o.OrderNo); g.State != StateFailed || g.Failure != FailureDeclined {
		t.Fatalf("恢复后应定论被拒: %+v", g)
	}
}

// 证据一直拿不到时不能无限等：到 verify_until + Verify 转人工，而不是定成被拒。
func TestEvidenceUnavailableEscalates(t *testing.T) {
	h := declinedHarness(t)
	h.pool.listTxns = func(cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
		return nil, 0, errors.New("读交易表失败")
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	waitEvent(t, o.OrderNo, "读交易表失败")
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying {
		t.Fatalf("上限之前应继续等: %+v", g)
	}
	h.clock.Advance(h.timeouts.Verify)
	g := waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "evidence_unavailable" {
		t.Fatalf("超过上限应转待人工: %+v", g)
	}
	assertReleases(t, h, cardpool.ResultAborted)
}

// 拿不到账号或卡的记录时同样不能定成被拒：到上限转人工。
func TestUnverifiableDeclinedEscalates(t *testing.T) {
	cases := map[string]func(*testing.T) *Order{
		"会话解不开": func(t *testing.T) *Order {
			o := presetOrder(t, "a@x.com", StateVerifying, map[string]any{"card_id": testCardID})
			if _, err := db.DB.Exec(`UPDATE pay_orders SET session_sealed = 'garbage' WHERE order_no = ?`, o.OrderNo); err != nil {
				t.Fatal(err)
			}
			return o
		},
		"单据没有记录卡": func(t *testing.T) *Order {
			return presetOrder(t, "a@x.com", StateVerifying, nil)
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			h := newPayHarness(t, nil)
			o := mk(t)
			at := h.clock.Now().Unix()
			if err := transition(ctx, o.OrderNo, StateVerifying, StateVerifying, "info", "预置", map[string]any{
				"submitted_at": at, "page_outcome": "declined", "verify_until": at - 1, "next_check_at": at,
			}); err != nil {
				t.Fatal(err)
			}
			h.start(t)
			waitEvent(t, o.OrderNo, "等下次检查")
			if g := mustOrder(t, o.OrderNo); g.State != StateVerifying {
				t.Fatalf("证据不全不应定论: %+v", g)
			}
			h.clock.Advance(h.timeouts.Verify)
			g := waitDone(t, o.OrderNo)
			if g.State != StateNeedsReview || g.ErrorCode != "evidence_unavailable" || g.Failure != "" {
				t.Fatalf("应转待人工: %+v", g)
			}
		})
	}
}

// 还在处理中的授权可能转成成功，不算「没有成功授权」。
func TestPendingAuthDoesNotDecline(t *testing.T) {
	h := declinedHarness(t)
	at := h.clock.Now().Unix()
	var txns atomic.Value
	txns.Store([]cardpool.Transaction{txnAt("A1", "pending", at+5)})
	h.pool.listTxns = func(f cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
		return pagedTxns(txns.Load().([]cardpool.Transaction))(f)
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	base := h.pool.count("ListTransactions")
	for deadline := time.Now().Add(5 * time.Second); h.pool.count("ListTransactions") < base+3; {
		if time.Now().After(deadline) {
			t.Fatal("宽限期后应继续检查交易")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying {
		t.Fatalf("授权处理中不应定论: %+v", g)
	}
	txns.Store([]cardpool.Transaction{txnAt("A1", "approved", at+5)})
	if g := waitDone(t, o.OrderNo); g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" {
		t.Fatalf("授权转成功后应转待人工: %+v", g)
	}
}

func TestBrowserErrorAfterSubmitVerifies(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.accountFn = planFrom(2)
	h.br.waitOutcome = outcomes(outcomeStep{err: errors.New("Chrome 崩溃了")})
	h.start(t)
	o := h.submit(t, false)
	g := waitDone(t, o.OrderNo)
	if g.State != StateSucceeded || g.Failure != "" {
		t.Fatalf("账号已开通应成功: %+v", g)
	}
	assertSubmitCount(t, h, 1)
	assertReleases(t, h, cardpool.ResultSuccess)
	ev := eventStates(t, o.OrderNo)
	if !containsInOrder(ev, StateSubmitted, StateVerifying, StateSucceeded) {
		t.Fatalf("事件顺序不符: %+v", ev)
	}
}

// R16：已转入 submitted 之后，无论哪一步出错都进 verifying，绝不写 failed / not_submitted，且不再点支付。
func TestErrorsAfterSubmitVerify(t *testing.T) {
	boom := errors.New("意外错误")
	otp := func(context.Context, string, time.Time) (string, error) { return "123456", nil }
	cases := map[string]func(*harness){
		"点支付报错":   func(h *harness) { h.br.submit = func() error { return boom } },
		"等页面结果报错": func(h *harness) { h.br.waitOutcome = outcomes(outcomeStep{err: boom}) },
		"等验证码报错": func(h *harness) {
			h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeChallenge})
			h.pool.waitOTP = func(context.Context, string, time.Time) (string, error) { return "", boom }
		},
		"填验证码报错": func(h *harness) {
			h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeChallenge})
			h.pool.waitOTP = otp
			h.br.enterOTP = func(string) error { return boom }
		},
		"填完验证码后等结果报错": func(h *harness) {
			h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeChallenge}, outcomeStep{err: boom})
			h.pool.waitOTP = otp
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			h := newPayHarness(t, nil)
			set(h)
			h.start(t)
			o := h.submit(t, false)
			g := waitState(t, o.OrderNo, StateVerifying)
			if g.Failure != "" || g.PageOutcome != "unknown" || g.SubmittedAt == nil || !strings.Contains(g.ErrorMessage, "意外错误") {
				t.Fatalf("应转确认且不记未提交: %+v", g)
			}
			waitAccountCalls(t, h, 2)
			if len(h.pool.released()) != 0 {
				t.Fatal("确认结束前不应释放租约")
			}
			if b := theBrowser(t, h); b.count("Close") != 1 {
				t.Fatalf("应关闭浏览器: %d", b.count("Close"))
			}
			h.clock.Advance(h.timeouts.Verify + time.Second)
			g = waitDone(t, o.OrderNo)
			if g.State != StateNeedsReview {
				t.Fatalf("查不到开通应转待人工: %+v", g)
			}
			assertSubmitCount(t, h, 1)
			assertReleases(t, h, cardpool.ResultAborted)
		})
	}
}

// R16：settle 本身的分流。stepFailure 与意外错误都进 verifying。
func TestSettleAfterSubmitNeverNotSubmitted(t *testing.T) {
	errs := map[string]error{
		"意外错误": errors.New("写库失败"),
		"放弃原因": abort("fill_failed", "不该出现的放弃"),
	}
	for _, state := range []string{StateSubmitted, StateRequires3DS} {
		for name, err := range errs {
			t.Run(state+"/"+name, func(t *testing.T) {
				h := newPayHarness(t, nil)
				at := h.clock.Now().Unix()
				o := presetOrder(t, "a@x.com", state, map[string]any{"submitted_at": at, "card_id": testCardID, "lease_token": testLeaseToken})
				h.svc.settle(ctx, &job{o: o}, err)
				g := mustOrder(t, o.OrderNo)
				if g.State != StateVerifying || g.Failure != "" || g.PageOutcome != "unknown" || g.FinishedAt != nil {
					t.Fatalf("应转 verifying: %+v", g)
				}
				if g.VerifyUntil == nil || *g.VerifyUntil != at+int64(h.timeouts.Verify/time.Second) || g.NextCheckAt == nil {
					t.Fatalf("应设确认时限: %+v", g)
				}
				if g.SessionSealed == "" {
					t.Fatal("确认阶段要用会话，不能清空")
				}
				if len(h.pool.released()) != 0 {
					t.Fatal("不应释放租约")
				}
			})
		}
	}
}

// R16：连转 verifying 也写不进去时只打日志，单据留给重启恢复。
func TestSettleAfterSubmitWriteFailureLeavesForRecovery(t *testing.T) {
	h := newPayHarness(t, nil)
	at := h.clock.Now().Unix()
	o := presetOrder(t, "a@x.com", StateRequires3DS, map[string]any{"submitted_at": at})
	stale := *o
	stale.State = StateSubmitted // 内存里的状态过期，写库的条件更新必然失败
	h.svc.settle(ctx, &job{o: &stale}, errors.New("boom"))
	g := mustOrder(t, o.OrderNo)
	if g.State != StateRequires3DS || g.Failure != "" || g.FinishedAt != nil {
		t.Fatalf("写库失败应保持原状: %+v", g)
	}
}

// 写入 submitted 失败时绝不能点支付，并放弃、释放租约。
func TestSubmittedWriteFailureBlocksSubmit(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.fill = func(paybrowser.Card, paybrowser.Billing) error {
		// 模拟状态写入失败：把单据改走，filling → submitted 的条件更新必然不生效
		_, err := db.DB.Exec(`UPDATE pay_orders SET state = ? WHERE account_email = ?`, StateFunding, "buyer@example.com")
		return err
	}
	h.start(t)
	o := h.submit(t, false)
	deadline := time.Now().Add(5 * time.Second)
	for len(h.pool.released()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("应释放租约")
		}
		time.Sleep(5 * time.Millisecond)
	}
	assertReleases(t, h, cardpool.ResultAborted)
	assertSubmitCount(t, h, 0)
	if g := mustOrder(t, o.OrderNo); g.SubmittedAt != nil {
		t.Fatalf("不应记提交时间: %+v", g)
	}
}

// 写入 submitted 必须发生在点击之前。
func TestSubmittedAtWrittenBeforeClick(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.accountFn = planFrom(2)
	var seen atomic.Pointer[Order]
	h.br.submit = func() error {
		g, err := getOrder(ctx, mustNo(t, h))
		if err != nil {
			return err
		}
		seen.Store(g)
		return nil
	}
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess})
	h.start(t)
	waitDone(t, h.submit(t, false).OrderNo)
	g := seen.Load()
	if g == nil || g.State != StateSubmitted || g.SubmittedAt == nil || g.BillingState == "" {
		t.Fatalf("点击时单据应已是 submitted 且有 submitted_at: %+v", g)
	}
}

// mustNo 返回库里唯一一张单据的单号。
func mustNo(t *testing.T, h *harness) string {
	t.Helper()
	list, _, err := h.svc.List(ctx, "", 1, 1)
	if err != nil || len(list) != 1 {
		t.Fatalf("应有 1 张单据: %v %+v", err, list)
	}
	return list[0].OrderNo
}

func TestFundFailureReleasesAborted(t *testing.T) {
	h := newPayHarness(t, nil)
	h.pool.waitOrder = func(_ context.Context, no string) (*cardpool.Order, error) {
		return &cardpool.Order{OutOrderNo: no, State: cardpool.StateFailed, LastError: "余额不足"}, nil
	}
	h.start(t)
	g := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, g, "fund_failed")
	assertReleases(t, h, cardpool.ResultAborted)
	if h.pool.count("Sensitive") != 0 || h.pool.count("Fund") != 1 {
		t.Fatalf("注资失败不应取卡号，也不应重发: sensitive=%d fund=%d", h.pool.count("Sensitive"), h.pool.count("Fund"))
	}
	if g.CardID != testCardID || g.LeaseToken != testLeaseToken || g.FundOrderNo != "FUND-1" {
		t.Fatalf("应记下卡与注资单: %+v", g)
	}
	assertSubmitCount(t, h, 0)
	if b := theBrowser(t, h); b.count("Close") != 1 {
		t.Fatalf("应关闭浏览器: %d", b.count("Close"))
	}
}

func TestFundTimeoutReleasesAborted(t *testing.T) {
	h := newPayHarness(t, func(to *Timeouts) { to.Fund = 20 * time.Millisecond })
	h.pool.waitOrder = func(c context.Context, _ string) (*cardpool.Order, error) {
		<-c.Done()
		return nil, c.Err()
	}
	h.start(t)
	g := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, g, "fund_timeout")
	assertReleases(t, h, cardpool.ResultAborted)
	if h.pool.count("Fund") != 1 || h.pool.count("WaitOrder") != 1 || h.pool.count("Sensitive") != 0 {
		t.Fatalf("结果不明不重试: fund=%d wait=%d sensitive=%d", h.pool.count("Fund"), h.pool.count("WaitOrder"), h.pool.count("Sensitive"))
	}
}

func TestFundCallErrorNotRetried(t *testing.T) {
	h := newPayHarness(t, nil)
	h.pool.fund = func(context.Context, *cardpool.Lease, int64) (*cardpool.Order, error) {
		return nil, errors.New("写单据失败")
	}
	h.start(t)
	g := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, g, "fund_failed")
	assertReleases(t, h, cardpool.ResultAborted)
	if h.pool.count("Fund") != 1 || h.pool.count("WaitOrder") != 0 {
		t.Fatalf("Fund 报错不重发、不查单: fund=%d wait=%d", h.pool.count("Fund"), h.pool.count("WaitOrder"))
	}
}

func TestFillFailureReleasesAborted(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.fill = func(paybrowser.Card, paybrowser.Billing) error { return errors.New("找不到卡号输入框") }
	h.start(t)
	g := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, g, "fill_failed")
	assertReleases(t, h, cardpool.ResultAborted)
	assertSubmitCount(t, h, 0)
	if g.SubmittedAt != nil || g.BillingState == "" {
		t.Fatalf("未提交且应记下账单州: %+v", g)
	}
}

// 控制者要求 4：Acquire 成功后、提交之前的任何失败都要释放，且不点支付。
func TestPreSubmitFailuresReleaseAborted(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name, code string
		set        func(*harness)
		fundCalls  int
	}{
		{"读卡信息失败", "card_info_failed", func(h *harness) {
			h.pool.cardInfo = func(string) (cardpool.CardInfo, error) { return cardpool.CardInfo{}, boom }
		}, 0},
		{"持卡人姓名为空", "card_info_failed", func(h *harness) {
			h.pool.cardInfo = func(string) (cardpool.CardInfo, error) { return cardpool.CardInfo{Last4: "4242"}, nil }
		}, 0},
		{"取卡号失败", "card_sensitive_failed", func(h *harness) {
			h.pool.sensitive = func(*cardpool.Lease) (*marswallex.CardSensitive, error) { return nil, boom }
		}, 1},
		{"有效期无法解析", "card_expiry_invalid", func(h *harness) {
			h.pool.sensitive = func(*cardpool.Lease) (*marswallex.CardSensitive, error) {
				return &marswallex.CardSensitive{CardNo: testCardNo, CVV2: testCVV, ExpiryDate: "13/99"}, nil
			}
		}, 1},
		{"租约已失效", "card_sensitive_failed", func(h *harness) {
			h.pool.sensitive = func(*cardpool.Lease) (*marswallex.CardSensitive, error) { return nil, cardpool.ErrLeaseInvalid }
			h.pool.release = func(*cardpool.Lease, cardpool.Outcome) error { return cardpool.ErrLeaseInvalid }
		}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newPayHarness(t, nil)
			c.set(h)
			h.start(t)
			g := waitDone(t, h.submit(t, false).OrderNo)
			assertNotSubmitted(t, g, c.code)
			assertReleases(t, h, cardpool.ResultAborted)
			assertSubmitCount(t, h, 0)
			if g.CardID != testCardID || g.LeaseToken != testLeaseToken {
				t.Fatalf("放弃时也应留下卡的记录: %+v", g)
			}
			if n := h.pool.count("Fund"); n != c.fundCalls {
				t.Fatalf("Fund 应调用 %d 次: %d", c.fundCalls, n)
			}
		})
	}
}

func TestNoCardFails(t *testing.T) {
	h := newPayHarness(t, nil)
	h.pool.acquire = func(cardpool.AcquireRequest) (*cardpool.Lease, error) {
		return nil, fmt.Errorf("%w: 无可用卡", cardpool.ErrNoCard)
	}
	h.start(t)
	g := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, g, "no_card")
	if len(h.pool.released()) != 0 || h.pool.count("Fund") != 0 {
		t.Fatal("没拿到卡不应释放或注资")
	}
	if b := theBrowser(t, h); b.count("Close") != 1 {
		t.Fatalf("应关闭浏览器: %d", b.count("Close"))
	}
}

func TestReleaseLeaseExpiredTolerated(t *testing.T) {
	invalid := func(*cardpool.Lease, cardpool.Outcome) error { return cardpool.ErrLeaseInvalid }
	countWarn := func(no string) int {
		n := 0
		for _, e := range eventStates(t, no) {
			if e.Level == "warn" && strings.Contains(e.Message, "租约") {
				n++
			}
		}
		return n
	}
	t.Run("确认成功后", func(t *testing.T) {
		h := newPayHarness(t, nil)
		h.pool.release = invalid
		h.gpt.accountFn = planFrom(2)
		h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess})
		h.start(t)
		o := h.submit(t, false)
		g := waitDone(t, o.OrderNo)
		if g.State != StateSucceeded {
			t.Fatalf("租约失效不影响单据结果: %+v", g)
		}
		assertReleases(t, h, cardpool.ResultSuccess)
		if countWarn(o.OrderNo) != 1 {
			t.Fatalf("应多一条 warn 事件: %+v", eventStates(t, o.OrderNo))
		}
	})
	t.Run("提交前放弃", func(t *testing.T) {
		h := newPayHarness(t, nil)
		h.pool.release = invalid
		h.br.fill = func(paybrowser.Card, paybrowser.Billing) error { return errors.New("填不上") }
		h.start(t)
		o := h.submit(t, false)
		g := waitDone(t, o.OrderNo)
		assertNotSubmitted(t, g, "fill_failed")
		assertReleases(t, h, cardpool.ResultAborted)
		if countWarn(o.OrderNo) != 1 {
			t.Fatalf("应多一条 warn 事件: %+v", eventStates(t, o.OrderNo))
		}
	})
}

// captureLog 把标准日志重定向到缓冲区；须在 h.start 之前调用，才能在工作协程退出后还原。
func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	old := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return buf
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// assertNoCardSecrets 检查单据、时间线、告警与日志都不含完整卡号与 CVV。单号里可能碰巧含这几位数字，先去掉。
func assertNoCardSecrets(t *testing.T, h *harness, g *Order, logs *lockedBuffer) {
	t.Helper()
	texts := []string{g.ErrorMessage, logs.String()}
	texts = append(texts, h.alerts.Texts()...)
	for _, e := range eventStates(t, g.OrderNo) {
		texts = append(texts, e.Message, e.Screenshot)
	}
	for _, txt := range texts {
		txt = strings.ReplaceAll(txt, g.OrderNo, "")
		for _, bad := range []string{testCardNo, testCardNoSpaced, testCardNoDashed, testCVV} {
			if strings.Contains(txt, bad) {
				t.Fatalf("文本不应含 %q: %q", bad, txt)
			}
		}
	}
}

// R17：错误文本不可控，夹带的卡号与 CVV 写入 error_message、事件、告警、日志前都要抹掉。
func TestCardSecretsScrubbed(t *testing.T) {
	dirty := fmt.Errorf("输入框内容 %s / %s / %s，cvc %s 校验失败", testCardNo, testCardNoSpaced, testCardNoDashed, testCVV)
	t.Run("填表报错", func(t *testing.T) {
		logs := captureLog(t)
		h := newPayHarness(t, nil)
		h.br.fill = func(paybrowser.Card, paybrowser.Billing) error { return dirty }
		h.start(t)
		g := waitDone(t, h.submit(t, false).OrderNo)
		assertNotSubmitted(t, g, "fill_failed")
		if !strings.Contains(g.ErrorMessage, "校验失败") {
			t.Fatalf("应保留其余说明: %q", g.ErrorMessage)
		}
		assertNoCardSecrets(t, h, g, logs)
	})
	t.Run("点支付报错", func(t *testing.T) {
		logs := captureLog(t)
		h := newPayHarness(t, nil)
		h.br.submit = func() error { return dirty }
		h.start(t)
		o := h.submit(t, false)
		waitState(t, o.OrderNo, StateVerifying)
		h.clock.Advance(h.timeouts.Verify + time.Second)
		g := waitDone(t, o.OrderNo)
		if g.State != StateNeedsReview || !strings.Contains(g.ErrorMessage, "校验失败") {
			t.Fatalf("应转待人工并保留说明: %+v", g)
		}
		if len(h.alerts.Texts()) != 1 {
			t.Fatalf("应告警 1 次: %v", h.alerts.Texts())
		}
		assertNoCardSecrets(t, h, g, logs)
	})
	t.Run("页面提示夹带", func(t *testing.T) {
		logs := captureLog(t)
		h := newPayHarness(t, nil)
		h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeDeclined, text: "declined for " + testCardNoSpaced + " cvc " + testCVV})
		h.start(t)
		o := h.submit(t, false)
		waitState(t, o.OrderNo, StateVerifying)
		h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
		g := waitDone(t, o.OrderNo)
		if g.State != StateFailed || !strings.Contains(g.ErrorMessage, "declined") {
			t.Fatalf("应定论被拒: %+v", g)
		}
		assertNoCardSecrets(t, h, g, logs)
		if rel := h.pool.released(); len(rel) != 1 || strings.Contains(rel[0].Outcome.ErrorCode, testCardNoSpaced) {
			t.Fatalf("Release 的原因也不应含卡号: %+v", rel)
		}
	})
}

func TestVerifyEveryRespected(t *testing.T) {
	h := newPayHarness(t, func(to *Timeouts) { to.VerifyEvery = 15 * time.Second })
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeUnknown})
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	waitAccountCalls(t, h, 2) // 预检 + 第一次确认
	time.Sleep(50 * time.Millisecond)
	if n := h.gpt.count("Account"); n != 2 {
		t.Fatalf("没到 next_check_at 不应再查: %d", n)
	}
	h.clock.Advance(15 * time.Second)
	waitAccountCalls(t, h, 3)
	if n := len(eventStates(t, o.OrderNo)); n > 20 {
		t.Fatalf("定时检查不应刷时间线: %d 条", n)
	}
}

func TestVerifyErrorsOnlyLogEvents(t *testing.T) {
	h := newPayHarness(t, nil)
	var open atomic.Bool
	h.gpt.accountFn = func(n int) (chatgpt.Account, error) {
		switch {
		case n == 1:
			return accountFree, nil
		case open.Load():
			return accountPlus, nil
		}
		return chatgpt.Account{}, fmt.Errorf("代理 %s 无响应，token %s", "http://u:pw@p", testAccessToken)
	}
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess})
	h.start(t)
	o := h.submit(t, false)
	e := waitEvent(t, o.OrderNo, "无响应")
	if e.Level != "warn" || strings.Contains(e.Message, testAccessToken) {
		t.Fatalf("应记脱敏后的 warn 事件: %+v", e)
	}
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying {
		t.Fatalf("查询失败不应定论: %+v", g)
	}
	open.Store(true)
	if g := waitDone(t, o.OrderNo); g.State != StateSucceeded {
		t.Fatalf("恢复后应成功: %+v", g)
	}
}

// 确认阶段用的是单据里的会话，且刷新后的 token 不写回；地区代理同样走配置。
func TestVerifyUsesSealedSessionAndProxy(t *testing.T) {
	h := newPayHarness(t, nil)
	const proxy = "http://u:pw@proxy.example:8080"
	if err := SaveProxies(map[string]string{"PH": proxy}); err != nil {
		t.Fatal(err)
	}
	h.gpt.accountFn = planFrom(1)
	o := presetOrder(t, "a@x.com", StateVerifying, map[string]any{
		"submitted_at": h.clock.Now().Unix(), "card_id": testCardID, "lease_token": testLeaseToken,
		"page_outcome": "unknown", "verify_until": h.clock.Now().Unix() + 600, "next_check_at": h.clock.Now().Unix(),
	})
	h.start(t)
	g := waitDone(t, o.OrderNo)
	if g.State != StateSucceeded {
		t.Fatalf("应成功: %+v", g)
	}
	if a := h.gpt.lastArgs("Refresh"); a[0] != testSessionToken {
		t.Fatalf("应解开会话并刷新: %v", a)
	}
	if a := h.gpt.lastArgs("Account"); a[0] != "at-refreshed" {
		t.Fatalf("应用刷新后的 token 查账号: %v", a)
	}
	if a := h.gpt.lastArgs("New"); a[0] != proxy {
		t.Fatalf("应走地区代理: %v", a)
	}
	rel := assertReleases(t, h, cardpool.ResultSuccess)
	if l := rel[0].Lease; l.Token != testLeaseToken || l.CardID != testCardID || l.OrderRef != o.OrderNo || l.PlanKey != "plus" || l.AccountEmail != "a@x.com" {
		t.Fatalf("应按单据字段重建租约: %+v", l)
	}
}

// 下单后管理员停用了该地区，确认阶段仍要能读到期望套餐类型。
func TestVerifySurvivesPlanDisabled(t *testing.T) {
	h := newPayHarness(t, nil)
	tbl := DefaultPlanTable()
	if err := SavePlanTable(tbl); err != nil {
		t.Fatal(err)
	}
	h.gpt.accountFn = planFrom(1)
	o := presetOrder(t, "a@x.com", StateVerifying, map[string]any{
		"submitted_at": h.clock.Now().Unix(), "card_id": testCardID, "lease_token": testLeaseToken,
		"page_outcome": "unknown", "verify_until": h.clock.Now().Unix() + 600, "next_check_at": h.clock.Now().Unix(),
	})
	h.start(t)
	if g := waitDone(t, o.OrderNo); g.State != StateSucceeded {
		t.Fatalf("应成功: %+v", g)
	}
}

// 确认阶段不占浏览器名额：第一张单确认中时，并发 1 下第二张单照常处理。
func TestVerifyingFreesBrowserSlot(t *testing.T) {
	h := newPayHarness(t, nil)
	if err := SaveConcurrency(1); err != nil {
		t.Fatal(err)
	}
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeUnknown})
	h.start(t)
	a := h.submitAs(t, sessionFor("a@x.com"), false)
	waitState(t, a.OrderNo, StateVerifying)
	b := h.submitAs(t, sessionFor("b@x.com"), true)
	if g := waitDone(t, b.OrderNo); g.State != StatePreviewed {
		t.Fatalf("第二张单应完成: %+v", g)
	}
	if g := mustOrder(t, a.OrderNo); g.State != StateVerifying {
		t.Fatalf("第一张单应仍在确认: %+v", g)
	}
}

func TestRecoveryOnStart(t *testing.T) {
	h := newPayHarness(t, nil)
	now := h.clock.Now().Unix()
	leaseCols := func(token, card string) map[string]any {
		return map[string]any{"lease_token": token, "card_id": card}
	}
	stale := []*Order{
		presetOrder(t, "checking@x.com", StateChecking, nil),
		presetOrder(t, "link@x.com", StateLinkCreated, nil),
		presetOrder(t, "opening@x.com", StateOpening, nil),
		presetOrder(t, "funding@x.com", StateFunding, leaseCols("tok-funding", "card-funding")),
		presetOrder(t, "filling@x.com", StateFilling, leaseCols("tok-filling", "card-filling")),
	}
	submitted := []*Order{
		presetOrder(t, "submitted@x.com", StateSubmitted, map[string]any{"submitted_at": now - 30, "card_id": "card-s", "lease_token": "tok-s", "page_outcome": ""}),
		presetOrder(t, "3ds@x.com", StateRequires3DS, map[string]any{"submitted_at": now - 30, "card_id": "card-3", "lease_token": "tok-3"}),
	}
	queued := h.submitAs(t, sessionFor("queued@x.com"), true)

	h.start(t)
	// Start 返回时恢复必须已同步完成
	for _, o := range stale {
		g := mustOrder(t, o.OrderNo)
		if g.State != StateFailed || g.Failure != FailureNotSubmitted || g.ErrorMessage != "服务重启，未提交" || g.SessionSealed != "" {
			t.Fatalf("%s 应 failed / not_submitted: %+v", o.State, g)
		}
	}
	rel := h.pool.released()
	if len(rel) != 2 {
		t.Fatalf("只有带租约的单才释放: %+v", rel)
	}
	byToken := map[string]releaseCall{}
	for _, r := range rel {
		byToken[r.Lease.Token] = r
	}
	for i, o := range stale[3:] {
		token := []string{"tok-funding", "tok-filling"}[i]
		r, ok := byToken[token]
		if !ok || r.Outcome.Result != cardpool.ResultAborted || r.Lease.CardID != o.CardID ||
			r.Lease.OrderRef != o.OrderNo || r.Lease.PlanKey != "plus" || r.Lease.AccountEmail != o.AccountEmail {
			t.Fatalf("应按单据字段重建租约并 Release(aborted): %+v", r)
		}
	}
	for _, o := range submitted {
		g := mustOrder(t, o.OrderNo)
		if g.State != StateVerifying || g.Failure != "" || g.PageOutcome != "unknown" || g.SessionSealed == "" ||
			g.VerifyUntil == nil || *g.VerifyUntil != now+int64(h.timeouts.Verify/time.Second) || g.NextCheckAt == nil {
			t.Fatalf("%s 应转 verifying: %+v", o.State, g)
		}
	}
	if g := waitDone(t, queued.OrderNo); g.State != StatePreviewed {
		t.Fatalf("queued 应照常处理: %+v", g)
	}
	assertSubmitCount(t, h, 0)
	if got := len(h.pool.released()); got != 2 {
		t.Fatalf("确认中的单不应释放租约: %d", got)
	}
}

// 恢复后的 verifying 走完确认，整个过程不碰浏览器。
func TestRecoveredOrderConcludes(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.accountFn = planFrom(2)
	o := presetOrder(t, "a@x.com", StateSubmitted, map[string]any{"submitted_at": h.clock.Now().Unix(), "card_id": testCardID, "lease_token": testLeaseToken})
	h.start(t)
	g := waitDone(t, o.OrderNo)
	if g.State != StateSucceeded {
		t.Fatalf("应成功: %+v", g)
	}
	assertReleases(t, h, cardpool.ResultSuccess)
	if len(h.br.browsers()) != 0 {
		t.Fatal("恢复路径不应启动浏览器，更不可能再点支付")
	}
}

// 恢复没做完时不能派发：否则新单进入 checking 后会和恢复互相踩。
func TestRecoveryFailureBlocksDispatch(t *testing.T) {
	h := newPayHarness(t, nil)
	stale := presetOrder(t, "stale@x.com", StateChecking, nil)
	queued := h.submitAs(t, sessionFor("queued@x.com"), true)
	if _, err := db.DB.Exec(`ALTER TABLE pay_order_events RENAME TO pay_order_events_hidden`); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	h.svc.Kick()
	time.Sleep(50 * time.Millisecond)
	if g := mustOrder(t, queued.OrderNo); g.State != StateQueued {
		t.Fatalf("恢复失败时不应派发: %+v", g)
	}
	if h.gpt.count("New") != 0 {
		t.Fatal("不应开始处理排队单")
	}
	if _, err := db.DB.Exec(`ALTER TABLE pay_order_events_hidden RENAME TO pay_order_events`); err != nil {
		t.Fatal(err)
	}
	h.svc.Kick()
	if g := waitDone(t, queued.OrderNo); g.State != StatePreviewed {
		t.Fatalf("恢复完成后应继续派发: %+v", g)
	}
	if g := mustOrder(t, stale.OrderNo); g.State != StateFailed || g.ErrorMessage != "服务重启，未提交" {
		t.Fatalf("遗留单应被恢复: %+v", g)
	}
}

// R19：恢复失败时派发被挡住，队列不能无声停摆：告警恰好一次，重试不重复；最终成功后打一条日志。
func TestRecoveryFailureAlertsOnce(t *testing.T) {
	logs := captureLog(t)
	h := newPayHarness(t, nil)
	stale := presetOrder(t, "stale@x.com", StateChecking, nil)
	if _, err := db.DB.Exec(`ALTER TABLE pay_order_events RENAME TO pay_order_events_hidden`); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	// Start 内同步做第一次恢复，返回时告警已发出
	a := h.alerts.Texts()
	if len(a) != 1 || !strings.Contains(a[0], "重启恢复失败") {
		t.Fatalf("首次失败应告警 1 次: %v", a)
	}
	// 每次 Kick 触发一轮重试，等日志确认重试真的发生过
	retries := func() int { return strings.Count(logs.String(), "重启恢复未完成") }
	for i := 0; i < 3; i++ {
		before := retries()
		h.svc.Kick()
		deadline := time.Now().Add(5 * time.Second)
		for retries() <= before {
			if time.Now().After(deadline) {
				t.Fatal("应继续重试恢复")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if a := h.alerts.Texts(); len(a) != 1 {
		t.Fatalf("重试不应重复告警: %v", a)
	}
	if strings.Contains(logs.String(), "重启恢复已完成") {
		t.Fatal("还没恢复成功，不应打完成日志")
	}

	if _, err := db.DB.Exec(`ALTER TABLE pay_order_events_hidden RENAME TO pay_order_events`); err != nil {
		t.Fatal(err)
	}
	h.svc.Kick()
	waitState(t, stale.OrderNo, StateFailed)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "重启恢复已完成") {
		if time.Now().After(deadline) {
			t.Fatal("恢复成功后应打一条日志")
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.svc.Kick()
	time.Sleep(30 * time.Millisecond)
	if n := strings.Count(logs.String(), "重启恢复已完成"); n != 1 {
		t.Fatalf("完成日志只打一条: %d", n)
	}
	if a := h.alerts.Texts(); len(a) != 1 {
		t.Fatalf("恢复成功不应再告警: %v", a)
	}
}

// 恢复一次就成功时不告警、不打完成日志。
func TestRecoverySuccessIsQuiet(t *testing.T) {
	logs := captureLog(t)
	h := newPayHarness(t, nil)
	stale := presetOrder(t, "stale@x.com", StateChecking, nil)
	h.start(t)
	if g := mustOrder(t, stale.OrderNo); g.State != StateFailed {
		t.Fatalf("应已恢复: %+v", g)
	}
	if len(h.alerts.Texts()) != 0 || strings.Contains(logs.String(), "重启恢复") {
		t.Fatalf("恢复顺利时不应告警或打日志: %v %s", h.alerts.Texts(), logs.String())
	}
}

// 停机取消导致的恢复中断不是故障，不告警。
func TestRecoveryCanceledDoesNotAlert(t *testing.T) {
	h := newPayHarness(t, nil)
	presetOrder(t, "stale@x.com", StateChecking, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	cancel()
	h.svc.Start(runCtx)
	h.svc.Wait()
	if a := h.alerts.Texts(); len(a) != 0 {
		t.Fatalf("停机不应告警: %v", a)
	}
}

// R19：恢复阶段没有单据上下文，告警与日志按配置的地区代理脱敏；读不出代理配置时不带原文。
func TestRecoveryAlertScrubbed(t *testing.T) {
	const proxy = "http://user:pw-secret@proxy.example:8080"
	dirty := errors.New("连接 " + proxy + " 失败，密码 pw-secret")
	assertClean := func(t *testing.T, h *harness, logs *lockedBuffer) {
		t.Helper()
		a := h.alerts.Texts()
		if len(a) != 1 || !strings.Contains(a[0], "重启恢复失败") {
			t.Fatalf("应告警 1 次: %v", a)
		}
		for _, txt := range []string{a[0], logs.String()} {
			if strings.Contains(txt, "pw-secret") || strings.Contains(txt, "user:") {
				t.Fatalf("文本不应含代理账号密码: %q", txt)
			}
		}
	}
	t.Run("按配置的代理脱敏", func(t *testing.T) {
		logs := captureLog(t)
		h := newPayHarness(t, nil)
		if err := SaveProxies(map[string]string{"PH": proxy}); err != nil {
			t.Fatal(err)
		}
		h.svc.reportRecoveryFailure(dirty)
		h.svc.reportRecoveryFailure(dirty)
		assertClean(t, h, logs)
		if a := h.alerts.Texts(); !strings.Contains(a[0], "失败") || !strings.Contains(a[0], "***") {
			t.Fatalf("应保留说明、只抹敏感部分: %q", a[0])
		}
	})
	t.Run("读不出代理配置时不带原文", func(t *testing.T) {
		logs := captureLog(t)
		h := newPayHarness(t, nil)
		if err := SaveProxies(map[string]string{"PH": proxy}); err != nil {
			t.Fatal(err)
		}
		t.Setenv("APP_MASTER_KEY", "")
		h.svc.reportRecoveryFailure(dirty)
		assertClean(t, h, logs)
	})
}

func TestLoopsStopOnCancel(t *testing.T) {
	h := newPayHarness(t, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	h.svc.Start(runCtx)
	cancel()
	done := make(chan struct{})
	go func() {
		h.svc.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 Wait 应返回：有协程泄漏")
	}
}

// 停机取消时不写终态：提交前的单留给重启恢复，提交后的单不被误记。
func TestShutdownLeavesOrderForRecovery(t *testing.T) {
	h := newPayHarness(t, nil)
	entered := make(chan struct{})
	h.br.waitOutcome = func(c context.Context, _ int) (paybrowser.Outcome, string, error) {
		close(entered)
		<-c.Done()
		return paybrowser.OutcomeUnknown, "", c.Err()
	}
	runCtx, cancel := context.WithCancel(context.Background())
	h.svc.Start(runCtx)
	o := h.submit(t, false)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("未进入 WaitOutcome")
	}
	cancel()
	h.svc.Wait()
	g := mustOrder(t, o.OrderNo)
	if g.State != StateSubmitted || g.Failure != "" {
		t.Fatalf("停机应保持 submitted: %+v", g)
	}
	if b := theBrowser(t, h); b.count("Close") != 1 {
		t.Fatalf("应关闭浏览器: %d", b.count("Close"))
	}
	// 重启：转 verifying，不再点支付
	h.rebuild(h.pool)
	h.start(t)
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying {
		t.Fatalf("重启后应转 verifying: %+v", g)
	}
	assertSubmitCount(t, h, 1)
}

func needsReviewOrder(t *testing.T, h *harness) *Order {
	t.Helper()
	o := presetOrder(t, "review@x.com", StateVerifying, map[string]any{"submitted_at": h.clock.Now().Unix(), "card_id": testCardID})
	if err := finish(ctx, o.OrderNo, StateVerifying, StateNeedsReview, "error", "转待人工", map[string]any{"error_code": "verify_timeout"}); err != nil {
		t.Fatal(err)
	}
	// 用哨兵值验证人工定论不会重写结束时间
	if _, err := db.DB.Exec(`UPDATE pay_orders SET finished_at = 12345 WHERE order_no = ?`, o.OrderNo); err != nil {
		t.Fatal(err)
	}
	return mustOrder(t, o.OrderNo)
}

func TestResolve(t *testing.T) {
	t.Run("标记成功", func(t *testing.T) {
		h := newPayHarness(t, nil)
		o := needsReviewOrder(t, h)
		if err := h.svc.Resolve(ctx, o.OrderNo, StateSucceeded, "  已核对账号  ", "admin"); err != nil {
			t.Fatal(err)
		}
		g := mustOrder(t, o.OrderNo)
		if g.State != StateSucceeded || g.ResolvedBy != "admin" || g.ResolveNote != "已核对账号" {
			t.Fatalf("定论结果不符: %+v", g)
		}
		if g.FinishedAt == nil || *g.FinishedAt != 12345 {
			t.Fatalf("不应重写 finished_at: %v", g.FinishedAt)
		}
		ev := eventStates(t, o.OrderNo)
		if last := ev[len(ev)-1]; last.State != StateSucceeded || !strings.Contains(last.Message, "admin") {
			t.Fatalf("应记定论事件: %+v", last)
		}
		if len(h.pool.released()) != 0 || h.pool.total() != 0 || len(h.br.browsers()) != 0 {
			t.Fatal("人工定论只改本单结果，不碰卡池与浏览器")
		}
	})
	t.Run("标记失败", func(t *testing.T) {
		h := newPayHarness(t, nil)
		o := needsReviewOrder(t, h)
		if err := h.svc.Resolve(ctx, o.OrderNo, StateFailed, "发卡行确认未扣款", "admin"); err != nil {
			t.Fatal(err)
		}
		g := mustOrder(t, o.OrderNo)
		if g.State != StateFailed || g.Failure != FailureDeclined || g.ResolvedBy != "admin" {
			t.Fatalf("已提交的单不能记成未提交: %+v", g)
		}
	})
	t.Run("参数校验", func(t *testing.T) {
		h := newPayHarness(t, nil)
		o := needsReviewOrder(t, h)
		for name, call := range map[string]func() error{
			"备注空白":  func() error { return h.svc.Resolve(ctx, o.OrderNo, StateSucceeded, " \t\n", "admin") },
			"结果不合法": func() error { return h.svc.Resolve(ctx, o.OrderNo, StateNeedsReview, "备注", "admin") },
			"缺操作人":  func() error { return h.svc.Resolve(ctx, o.OrderNo, StateSucceeded, "备注", "  ") },
		} {
			if err := call(); !errors.Is(err, ErrBadRequest) {
				t.Fatalf("%s 应为 ErrBadRequest: %v", name, err)
			}
		}
		if g := mustOrder(t, o.OrderNo); g.State != StateNeedsReview || g.ResolvedBy != "" {
			t.Fatalf("校验失败不应改单: %+v", g)
		}
		if err := h.svc.Resolve(ctx, "PO-none", StateSucceeded, "备注", "admin"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("不存在应为 ErrNotFound: %v", err)
		}
	})
	t.Run("只允许待人工", func(t *testing.T) {
		h := newPayHarness(t, nil)
		o := needsReviewOrder(t, h)
		if err := h.svc.Resolve(ctx, o.OrderNo, StateSucceeded, "第一次", "admin"); err != nil {
			t.Fatal(err)
		}
		if err := h.svc.Resolve(ctx, o.OrderNo, StateFailed, "第二次", "admin"); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("对 succeeded 单应为 ErrBadRequest: %v", err)
		}
		v := presetOrder(t, "v@x.com", StateVerifying, nil)
		if err := h.svc.Resolve(ctx, v.OrderNo, StateFailed, "备注", "admin"); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("对确认中的单应为 ErrBadRequest: %v", err)
		}
	})
}

func TestEvidence(t *testing.T) {
	h := newPayHarness(t, nil)
	if _, err := h.svc.Evidence(ctx, "PO-none"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在应为 ErrNotFound: %v", err)
	}
	at := h.clock.Now().Unix()
	all := make([]cardpool.Transaction, 0, 130)
	for i := 0; i < 120; i++ {
		all = append(all, txnAt(fmt.Sprintf("N%d", i), "approved", at+1000-int64(i)))
	}
	all = append(all, txnAt("OLD", "approved", at-3600))
	h.pool.listTxns = pagedTxns(all)

	notSubmitted := presetOrder(t, "n@x.com", StateFilling, map[string]any{"card_id": testCardID})
	if list, err := h.svc.Evidence(ctx, notSubmitted.OrderNo); err != nil || list == nil || len(list) != 0 {
		t.Fatalf("未提交应返回空: %v %v", list, err)
	}
	if h.pool.count("ListTransactions") != 0 {
		t.Fatal("未提交不应查交易")
	}

	o := presetOrder(t, "a@x.com", StateVerifying, map[string]any{"submitted_at": at, "card_id": testCardID})
	list, err := h.svc.Evidence(ctx, o.OrderNo)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 120 {
		t.Fatalf("应翻完所有页，只留提交之后的交易: %d", len(list))
	}
	for _, x := range list {
		if x.TransactionID == "OLD" {
			t.Fatal("不应含提交之前的交易")
		}
	}
	if f := h.pool.lastArgs("ListTransactions")[0].(cardpool.TxnFilter); f.CardID != testCardID || f.PageSize > 100 {
		t.Fatalf("查询条件不符: %+v", f)
	}

	h.pool.listTxns = func(cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
		return nil, 0, errors.New("读交易表失败")
	}
	if _, err := h.svc.Evidence(ctx, o.OrderNo); err == nil {
		t.Fatal("查询失败应报错，不能当成没有交易")
	}
}

// R18：Evidence 与定论用同一个窗口，管理员才看得到让单据转人工的那笔授权；窗口外的不算。
func TestEvidenceIncludesSkewWindow(t *testing.T) {
	h := newPayHarness(t, nil)
	at := h.clock.Now().Unix()
	h.pool.listTxns = pagedTxns([]cardpool.Transaction{
		txnAt("AFTER", "approved", at+10),
		txnAt("SKEW30", "approved", at-30),
		txnAt("EDGE", "approved", at-300),
		txnAt("OUT", "approved", at-301),
		txnAt("OLD", "approved", at-3600),
	})
	o := presetOrder(t, "a@x.com", StateVerifying, map[string]any{"submitted_at": at, "card_id": testCardID})
	list, err := h.svc.Evidence(ctx, o.OrderNo)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(list))
	for _, x := range list {
		got = append(got, x.TransactionID)
	}
	if strings.Join(got, ",") != "AFTER,SKEW30,EDGE" {
		t.Fatalf("窗口应为提交前 5 分钟起: %v", got)
	}
}

// 卡池实际每页给得比请求的少时也要翻完，不能按请求的页大小算总数。
func TestEvidenceHonorsPoolPageCap(t *testing.T) {
	h := newPayHarness(t, nil)
	at := h.clock.Now().Unix()
	all := make([]cardpool.Transaction, 0, 50)
	for i := 0; i < 50; i++ {
		all = append(all, txnAt(fmt.Sprintf("N%d", i), "approved", at+100-int64(i)))
	}
	paged := pagedTxns(all)
	h.pool.listTxns = func(f cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
		f.PageSize = 20
		return paged(f)
	}
	o := presetOrder(t, "a@x.com", StateVerifying, map[string]any{"submitted_at": at, "card_id": testCardID})
	list, err := h.svc.Evidence(ctx, o.OrderNo)
	if err != nil || len(list) != 50 {
		t.Fatalf("应翻完所有页: %d %v", len(list), err)
	}
}

// 没有卡的单不能查交易：TxnFilter 的卡 ID 为空等于不过滤，会查到别的卡。
func TestEvidenceWithoutCard(t *testing.T) {
	h := newPayHarness(t, nil)
	o := presetOrder(t, "a@x.com", StateVerifying, map[string]any{"submitted_at": h.clock.Now().Unix()})
	list, err := h.svc.Evidence(ctx, o.OrderNo)
	if err != nil || len(list) != 0 || h.pool.count("ListTransactions") != 0 {
		t.Fatalf("没有卡应返回空且不查交易: %v %v", list, err)
	}
}

func TestShotCleanup(t *testing.T) {
	h := newPayHarness(t, nil)
	root := h.shotDir
	mkdir := func(name string, age time.Duration) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "01-opening.png"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := h.clock.Now().Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return p
	}
	exists := func(p string) bool {
		_, err := os.Lstat(p)
		return err == nil
	}
	const day = 24 * time.Hour
	old := mkdir("PO261001000000abcdef01", 8*day)
	fresh := mkdir("PO261101000000abcdef02", 6*day)
	otherName := mkdir("not-an-order", 30*day)
	badHex := mkdir("PO261001000000ABCDEF03", 30*day)

	outside := t.TempDir()
	target := filepath.Join(outside, "keep.png")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := h.clock.Now().Add(-30 * day)
	if err := os.Chtimes(outside, past, past); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "PO261001000000abcdef04")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	plainFile := filepath.Join(root, "PO261001000000abcdef05")
	if err := os.WriteFile(plainFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(plainFile, past, past); err != nil {
		t.Fatal(err)
	}

	h.svc.cleanShots()

	if exists(old) {
		t.Fatal("早于 7 天的单号目录应删除")
	}
	for _, p := range []string{fresh, otherName, badHex, link, target, plainFile} {
		if !exists(p) {
			t.Fatalf("%s 应保留", p)
		}
	}

	// ShotDir 不存在时不报错、不崩溃
	h.svc.opts.ShotDir = filepath.Join(root, "missing")
	h.svc.cleanShots()
}

func TestShotCleanupRunsOnStart(t *testing.T) {
	h := newPayHarness(t, nil)
	old := filepath.Join(h.shotDir, "PO261001000000abcdef01")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	past := h.clock.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Lstat(old); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("启动后应清理一次")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
