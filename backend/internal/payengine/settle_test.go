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
		return cardpool.CardInfo{Last4: "4242", Currency: "USD", HolderFirstName: "JOHN", HolderLastName: "DOE"}, nil
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
	h.pool.backfill = func(context.Context, int) (cardpool.BackfillState, error) { return cardpool.BackfillState{}, nil }
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

const postalIncomplete = "Your postal code is incomplete."

func formRejectedHarness(t *testing.T) *harness {
	t.Helper()
	h := newPayHarness(t, nil)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeFormRejected, text: postalIncomplete})
	return h
}

// MO-02：校验错误的词表没核实过，文字可能分错，所以等满 Verify 再查证据，不是 2 分钟。
// 证据干净时才定失败，定论和被拒不同：不计入健康度，不让配置问题停用卡产品。
func TestFormRejectedConcludes(t *testing.T) {
	h := formRejectedHarness(t)
	h.start(t)
	start := h.clock.Now().Unix()
	o := h.submit(t, false)
	g := waitState(t, o.OrderNo, StateVerifying)
	if g.PageOutcome != "form_rejected" || g.VerifyUntil == nil || *g.VerifyUntil != start+int64(h.timeouts.Verify/time.Second) {
		t.Fatalf("校验错误应记 page_outcome 并等满 Verify: %+v", g)
	}
	if g.ErrorMessage != postalIncomplete {
		t.Fatalf("应记下页面文字: %+v", g)
	}
	waitEvent(t, o.OrderNo, "校验未通过")
	waitAccountCalls(t, h, 2)
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second) // 过了被拒的宽限也不定论
	waitAccountCalls(t, h, h.gpt.count("Account")+2)
	if got := mustOrder(t, o.OrderNo); got.State != StateVerifying || len(h.pool.released()) != 0 {
		t.Fatalf("Verify 之内不应定论: %+v", got)
	}

	h.clock.Advance(h.timeouts.Verify)
	g = waitDone(t, o.OrderNo)
	if g.State != StateFailed || g.Failure != FailureDeclined || g.ErrorCode != "form_rejected" ||
		g.ErrorMessage != "页面校验未通过："+postalIncomplete || g.SessionSealed != "" {
		t.Fatalf("应 failed / form_rejected: %+v", g)
	}
	assertReleases(t, h, cardpool.ResultAborted)
	assertSubmitCount(t, h, 1)
	if n := len(h.alerts.Texts()); n != 0 {
		t.Fatalf("校验错误不告警: %d", n)
	}
	waitEvent(t, o.OrderNo, "页面校验未通过：")
}

func TestFormRejectedButApprovedAuthNeedsReview(t *testing.T) {
	h := formRejectedHarness(t)
	h.pool.listTxns = pagedTxns([]cardpool.Transaction{txnAt("A1", "approved", h.clock.Now().Unix()+10)})
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.Verify + time.Second)
	g := waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" || g.Failure != "" {
		t.Fatalf("有成功授权应转待人工: %+v", g)
	}
	assertReleases(t, h, cardpool.ResultAborted)
	if a := h.alerts.Texts(); len(a) != 1 {
		t.Fatalf("应告警 1 次: %v", a)
	}
}

func TestFormRejectedButAccountOpenedSucceeds(t *testing.T) {
	h := formRejectedHarness(t)
	h.gpt.accountFn = planFrom(2)
	h.start(t)
	g := waitDone(t, h.submit(t, false).OrderNo)
	if g.State != StateSucceeded || g.ErrorMessage != "" || g.ErrorCode != "" {
		t.Fatalf("账号已开通应成功，并清掉页面提示: %+v", g)
	}
	assertReleases(t, h, cardpool.ResultSuccess)
}

// 证据不全时转人工，不定论失败。校验错误已经等满 Verify，到期就转，不再多等一个 Verify（MaxPayDuration 才不用变）。
func TestFormRejectedEvidenceUnavailableEscalates(t *testing.T) {
	h := formRejectedHarness(t)
	h.pool.listTxns = func(cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
		return nil, 0, errors.New("读交易表失败")
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.Verify + time.Second)
	g := waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "evidence_unavailable" || g.Failure != "" {
		t.Fatalf("证据不全应转待人工: %+v", g)
	}
	waitEvent(t, o.OrderNo, "读交易表失败")
	assertReleases(t, h, cardpool.ResultAborted)
}

// MO-02：页面结果不明（认不出的提示）走满 Verify，提示文字留作说明，没有开通就转人工，绝不定失败。
func TestUnrecognizedNoticeNeverConcludesFailed(t *testing.T) {
	const notice = "Something went wrong."
	h := newPayHarness(t, nil)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeUnknown, text: notice})
	h.br.notices = func() []string { return []string{notice} }
	h.start(t)
	start := h.clock.Now().Unix()
	o := h.submit(t, false)
	g := waitState(t, o.OrderNo, StateVerifying)
	if g.PageOutcome != "unknown" || g.ErrorMessage != notice || g.VerifyUntil == nil || *g.VerifyUntil != start+int64(h.timeouts.Verify/time.Second) {
		t.Fatalf("认不出的提示应按结果不明等满 Verify，并留下文字: %+v", g)
	}
	waitEvent(t, o.OrderNo, "支付页提示（未识别，继续等待）："+notice)
	h.clock.Advance(h.timeouts.Verify + time.Second)
	g = waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "verify_timeout" || g.Failure != "" {
		t.Fatalf("应转待人工: %+v", g)
	}
	assertSubmitCount(t, h, 1)
}

// 未识别提示写进时间线之前再过一遍 scrub。
func TestNoticeEventScrubbed(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeUnknown})
	h.br.notices = func() []string { return []string{"card " + testCardNoSpaced + " failed"} }
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	waitEvent(t, o.OrderNo, "card *** failed")
	for _, e := range eventStates(t, o.OrderNo) {
		if strings.Contains(e.Message, testCardNoSpaced) {
			t.Fatalf("时间线含完整卡号: %+v", e)
		}
	}
}

// 过长的页面文字加上前缀后仍受 error_message 上限约束。
func TestFormRejectedMessageTruncated(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeFormRejected, text: "invalid " + strings.Repeat("字", 600)})
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.Verify + time.Second)
	g := waitDone(t, o.OrderNo)
	if n := len([]rune(g.ErrorMessage)); n > errorMessageLimit || !strings.HasPrefix(g.ErrorMessage, "页面校验未通过：") {
		t.Fatalf("error_message 应有前缀且不超过 %d 字: %d %q", errorMessageLimit, n, g.ErrorMessage)
	}
}

// 校验错误文字夹带卡号也要抹掉（定论写的是转确认时脱敏过的文字）。
func TestFormRejectedMessageScrubbed(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeFormRejected, text: "Your card number " + testCardNoSpaced + " is invalid."})
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.Verify + time.Second)
	g := waitDone(t, o.OrderNo)
	if strings.Contains(g.ErrorMessage, testCardNoSpaced) || !strings.Contains(g.ErrorMessage, "***") {
		t.Fatalf("文字应脱敏: %q", g.ErrorMessage)
	}
}

// waitBackfill 等到至少补拉过一次，最多 5 秒。
func waitBackfill(t *testing.T, h *harness) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.pool.count("BackfillRecent") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("没有补拉")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// R48：Webhook 不通时本地交易表只靠定时补拉，宽限期内看不到授权。定论失败前先补拉一次，再按新结果判断。
func TestBackfillFindsApprovedAuthNeedsReview(t *testing.T) {
	for name, mk := range map[string]func(*testing.T) *harness{"页面被拒": declinedHarness, "页面校验未通过": formRejectedHarness} {
		t.Run(name, func(t *testing.T) {
			h := mk(t)
			var txns atomic.Value
			txns.Store([]cardpool.Transaction{})
			h.pool.listTxns = func(f cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
				return pagedTxns(txns.Load().([]cardpool.Transaction))(f)
			}
			h.pool.backfill = func(context.Context, int) (cardpool.BackfillState, error) {
				txns.Store([]cardpool.Transaction{txnAt("A1", "approved", h.clock.Now().Unix())})
				return cardpool.BackfillState{}, nil
			}
			h.start(t)
			o := h.submit(t, false)
			waitState(t, o.OrderNo, StateVerifying)
			h.clock.Advance(h.timeouts.Verify + time.Second) // 盖住两种结果的 verify_until：被拒 DeclineGrace，校验错误 Verify
			g := waitDone(t, o.OrderNo)
			if g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" || g.Failure != "" {
				t.Fatalf("补拉后才出现成功授权应转待人工: %+v", g)
			}
			if want := map[string]string{"页面被拒": "被拒", "页面校验未通过": "校验未通过"}[name]; !strings.Contains(g.ErrorMessage, want) {
				t.Fatalf("转人工的说明应写明页面结果 %q: %q", want, g.ErrorMessage)
			}
			assertReleases(t, h, cardpool.ResultAborted)
			if n := h.pool.count("BackfillRecent"); n != 1 {
				t.Fatalf("应补拉 1 次: %d", n)
			}
		})
	}
}

// 补拉后还有处理中的授权：不定论，重排等下次检查；授权转失败后才定论。
func TestBackfillFindsPendingAuthReschedules(t *testing.T) {
	h := declinedHarness(t)
	var txns atomic.Value
	txns.Store([]cardpool.Transaction{})
	h.pool.listTxns = func(f cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
		return pagedTxns(txns.Load().([]cardpool.Transaction))(f)
	}
	h.pool.backfill = func(context.Context, int) (cardpool.BackfillState, error) {
		if h.pool.count("BackfillRecent") == 1 { // 第一次补拉拉回一笔处理中的授权，之后没有新交易
			txns.Store([]cardpool.Transaction{txnAt("A1", "pending", h.clock.Now().Unix())})
		}
		return cardpool.BackfillState{}, nil
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	waitBackfill(t, h)
	waitMoreCalls(t, func() int { return h.gpt.count("Account") })
	waitMoreCalls(t, func() int { return h.gpt.count("Account") })
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying || len(h.pool.released()) != 0 {
		t.Fatalf("补拉后还有处理中的授权不应定论: %+v", g)
	}
	if n := h.pool.count("BackfillRecent"); n != 1 {
		t.Fatalf("已知有处理中的授权时不必再补拉: %d", n)
	}
	txns.Store([]cardpool.Transaction{txnAt("A1", "failed", h.clock.Now().Unix())})
	g := waitDone(t, o.OrderNo)
	if g.State != StateFailed || g.Failure != FailureDeclined {
		t.Fatalf("授权转失败后应定论: %+v", g)
	}
}

// 补拉失败时拿不到完整证据：不定论，记事件，耗尽后转人工。方向偏向人工。
func TestBackfillFailureEscalates(t *testing.T) {
	h := declinedHarness(t)
	h.pool.backfill = func(context.Context, int) (cardpool.BackfillState, error) {
		return cardpool.BackfillState{}, errors.New("平台返回 HTTP 500")
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	e := waitEvent(t, o.OrderNo, "HTTP 500")
	if e.Level != "warn" || e.State != StateVerifying || !strings.Contains(e.Message, "补拉") {
		t.Fatalf("应记 warn 事件: %+v", e)
	}
	waitMoreCalls(t, func() int { return h.pool.count("BackfillRecent") }) // 补拉一直失败，每轮都再试
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying || len(h.pool.released()) != 0 {
		t.Fatalf("补拉失败不应定论: %+v", g)
	}
	if n := countEvents(t, o.OrderNo, "HTTP 500"); n != 1 {
		t.Fatalf("同样的错误只记 1 条: %d", n)
	}
	h.clock.Advance(h.timeouts.Verify)
	g := waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "evidence_unavailable" || g.Failure != "" {
		t.Fatalf("耗尽后应转待人工: %+v", g)
	}
	assertReleases(t, h, cardpool.ResultAborted)
	if a := h.alerts.Texts(); len(a) != 1 {
		t.Fatalf("应告警 1 次: %v", a)
	}
}

// 补拉恢复后可以定论。
func TestBackfillRecoveryConcludes(t *testing.T) {
	h := declinedHarness(t)
	var broken atomic.Bool
	broken.Store(true)
	h.pool.backfill = func(context.Context, int) (cardpool.BackfillState, error) {
		if broken.Load() {
			return cardpool.BackfillState{}, errors.New("平台返回 HTTP 500")
		}
		return cardpool.BackfillState{}, nil
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	waitEvent(t, o.OrderNo, "HTTP 500")
	broken.Store(false)
	g := waitDone(t, o.OrderNo)
	if g.State != StateFailed || g.Failure != FailureDeclined {
		t.Fatalf("补拉恢复后应定论: %+v", g)
	}
	assertReleases(t, h, cardpool.ResultFailed)
}

// 补拉超时同样不定论；补拉有时限，默认 30 秒，且时限不受可前进的测试时钟影响。
func TestBackfillTimeoutDoesNotDecline(t *testing.T) {
	if got := DefaultTimeouts().Backfill; got != 30*time.Second {
		t.Fatalf("默认补拉时限 = %v，期望 30 秒", got)
	}
	h := newPayHarness(t, func(to *Timeouts) { to.Backfill = 20 * time.Millisecond })
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeDeclined, text: "Your card was declined."})
	h.pool.backfill = func(c context.Context, _ int) (cardpool.BackfillState, error) {
		<-c.Done()
		return cardpool.BackfillState{}, c.Err()
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	waitEvent(t, o.OrderNo, "补拉")
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying || len(h.pool.released()) != 0 {
		t.Fatalf("补拉超时不应定论: %+v", g)
	}
	h.clock.Advance(h.timeouts.Verify)
	g := waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "evidence_unavailable" {
		t.Fatalf("耗尽后应转待人工: %+v", g)
	}
}

// 补拉参数：1 天；ctx 带时限。
func TestBackfillArgsAndDeadline(t *testing.T) {
	h := declinedHarness(t)
	var left atomic.Int64
	h.pool.backfill = func(c context.Context, _ int) (cardpool.BackfillState, error) {
		if d, ok := c.Deadline(); ok {
			left.Store(int64(time.Until(d)))
		}
		return cardpool.BackfillState{}, nil
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	g := waitDone(t, o.OrderNo)
	if g.State != StateFailed || g.Failure != FailureDeclined {
		t.Fatalf("补拉成功且没有授权应定论失败: %+v", g)
	}
	if a := h.pool.lastArgs("BackfillRecent"); h.pool.count("BackfillRecent") != 1 || a[0] != 1 {
		t.Fatalf("应补拉 1 次、1 天: %d %v", h.pool.count("BackfillRecent"), a)
	}
	if d := time.Duration(left.Load()); d <= 25*time.Second || d > 30*time.Second {
		t.Fatalf("补拉的 ctx 应带 30 秒时限，实际剩余 %v", d)
	}
}

// 停机较久时，补拉窗口要盖住提交时间，否则看不到当时的授权；超出卡池能补拉的范围就不定论，转待人工。
func TestBackfillWindowCoversSubmittedAt(t *testing.T) {
	for _, c := range []struct {
		name string
		idle time.Duration
		days int // 0 表示不应补拉
	}{
		{"30 小时", 30 * time.Hour, 3},
		{"4 天", 4 * 24 * time.Hour, 7},
		{"8 天", 8 * 24 * time.Hour, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := declinedHarness(t)
			h.start(t)
			o := h.submit(t, false)
			waitState(t, o.OrderNo, StateVerifying)
			h.clock.Advance(c.idle)
			g := waitDone(t, o.OrderNo)
			if c.days == 0 {
				if g.State != StateNeedsReview || g.ErrorCode != "evidence_unavailable" {
					t.Fatalf("补拉不到提交时间应转待人工: %+v", g)
				}
				if n := h.pool.count("BackfillRecent"); n != 0 {
					t.Fatalf("超出范围不应补拉: %d", n)
				}
				return
			}
			if g.State != StateFailed {
				t.Fatalf("补拉成功且没有授权应定论失败: %+v", g)
			}
			if a := h.pool.lastArgs("BackfillRecent"); len(a) != 1 || a[0] != c.days {
				t.Fatalf("应补拉 %d 天: %v", c.days, a)
			}
		})
	}
}

func TestDeclineBackfillDays(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(d time.Duration) *int64 { v := now.Add(-d).Unix(); return &v }
	for _, c := range []struct {
		name string
		at   *int64
		days int
		ok   bool
	}{
		{"刚提交", at(10 * time.Minute), 1, true},
		{"接近 1 天，留出余量", at(23*time.Hour + 30*time.Minute), 3, true},
		{"接近 3 天，留出余量", at(71*time.Hour + 30*time.Minute), 7, true},
		{"接近 7 天，留出余量", at(167*time.Hour + 30*time.Minute), 0, false},
		{"超过 7 天", at(200 * time.Hour), 0, false},
		{"没有提交时间", nil, 7, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			days, ok := declineBackfillDays(c.at, now)
			if days != c.days || ok != c.ok {
				t.Fatalf("got %d %v, want %d %v", days, ok, c.days, c.ok)
			}
		})
	}
}

// 补拉只在要定论失败时调用：宽限期内、账号已开通、已有成功或处理中的授权、证据查询出错、页面结果不是被拒，都不调用。
func TestBackfillOnlyBeforeConcluding(t *testing.T) {
	assertNoBackfill := func(t *testing.T, h *harness) {
		t.Helper()
		if n := h.pool.count("BackfillRecent"); n != 0 {
			t.Fatalf("不应补拉: %d", n)
		}
	}
	t.Run("宽限期内", func(t *testing.T) {
		h := declinedHarness(t)
		h.start(t)
		o := h.submit(t, false)
		waitState(t, o.OrderNo, StateVerifying)
		waitAccountCalls(t, h, 3)
		assertNoBackfill(t, h)
	})
	t.Run("账号已开通", func(t *testing.T) {
		h := declinedHarness(t)
		h.gpt.accountFn = planFrom(2)
		h.start(t)
		if g := waitDone(t, h.submit(t, false).OrderNo); g.State != StateSucceeded {
			t.Fatalf("应成功: %+v", g)
		}
		assertNoBackfill(t, h)
	})
	t.Run("第一次查就有成功授权", func(t *testing.T) {
		h := declinedHarness(t)
		h.pool.listTxns = pagedTxns([]cardpool.Transaction{txnAt("A1", "approved", h.clock.Now().Unix()+10)})
		h.start(t)
		o := h.submit(t, false)
		waitState(t, o.OrderNo, StateVerifying)
		h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
		if g := waitDone(t, o.OrderNo); g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" {
			t.Fatalf("应转待人工: %+v", g)
		}
		assertNoBackfill(t, h)
	})
	t.Run("有处理中的授权", func(t *testing.T) {
		h := declinedHarness(t)
		h.pool.listTxns = pagedTxns([]cardpool.Transaction{txnAt("A1", "pending", h.clock.Now().Unix()+10)})
		h.start(t)
		o := h.submit(t, false)
		waitState(t, o.OrderNo, StateVerifying)
		h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
		waitAccountCalls(t, h, h.gpt.count("Account")+3)
		assertNoBackfill(t, h)
	})
	t.Run("查授权出错", func(t *testing.T) {
		h := declinedHarness(t)
		h.pool.listTxns = func(cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
			return nil, 0, errors.New("读交易表失败")
		}
		h.start(t)
		o := h.submit(t, false)
		waitState(t, o.OrderNo, StateVerifying)
		h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
		waitEvent(t, o.OrderNo, "读交易表失败")
		assertNoBackfill(t, h)
	})
	t.Run("查账号出错", func(t *testing.T) {
		h := declinedHarness(t)
		var broken atomic.Bool
		h.gpt.accountFn = func(int) (chatgpt.Account, error) {
			if broken.Load() {
				return chatgpt.Account{}, errors.New("ChatGPT 返回 HTTP 502")
			}
			return accountFree, nil
		}
		h.start(t)
		o := h.submit(t, false)
		waitState(t, o.OrderNo, StateVerifying)
		broken.Store(true)
		h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
		waitEvent(t, o.OrderNo, "HTTP 502")
		waitAccountCalls(t, h, h.gpt.count("Account")+2)
		assertNoBackfill(t, h)
	})
	t.Run("页面结果不明", func(t *testing.T) {
		h := newPayHarness(t, nil)
		h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeUnknown})
		h.start(t)
		o := h.submit(t, false)
		waitState(t, o.OrderNo, StateVerifying)
		h.clock.Advance(h.timeouts.Verify + time.Second)
		if g := waitDone(t, o.OrderNo); g.State != StateNeedsReview || g.ErrorCode != "verify_timeout" {
			t.Fatalf("应转待人工: %+v", g)
		}
		assertNoBackfill(t, h)
	})
}

// 补拉成功后再查授权出错：证据不全，不定论。
func TestBackfillThenTxnErrorDoesNotDecline(t *testing.T) {
	h := declinedHarness(t)
	var broken atomic.Bool
	h.pool.listTxns = func(cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
		if broken.Load() {
			return nil, 0, errors.New("读交易表失败")
		}
		return []cardpool.Transaction{}, 0, nil
	}
	h.pool.backfill = func(context.Context, int) (cardpool.BackfillState, error) {
		broken.Store(true)
		return cardpool.BackfillState{}, nil
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	waitEvent(t, o.OrderNo, "读交易表失败")
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying || len(h.pool.released()) != 0 {
		t.Fatalf("补拉后查不到授权不应定论: %+v", g)
	}
}

// 补拉期间服务停止：不定论、不记事件，留给重启后接着查。
func TestBackfillInterruptedByShutdown(t *testing.T) {
	h := newPayHarness(t, nil)
	at := h.clock.Now().Unix() - 600
	o := presetOrder(t, "a@x.com", StateVerifying, map[string]any{
		"submitted_at": at, "card_id": testCardID, "lease_token": testLeaseToken,
		"page_outcome": "declined", "verify_until": at + 120, "next_check_at": at, "error_message": "Your card was declined.",
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.pool.backfill = func(context.Context, int) (cardpool.BackfillState, error) {
		cancel()
		return cardpool.BackfillState{}, context.Canceled
	}
	h.svc.verifyOrder(runCtx, o)
	if h.pool.count("BackfillRecent") != 1 {
		t.Fatalf("应走到补拉: %d", h.pool.count("BackfillRecent"))
	}
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying || len(h.pool.released()) != 0 {
		t.Fatalf("停机时不应定论: %+v", g)
	}
	if n := countEvents(t, o.OrderNo, "补拉"); n != 0 {
		t.Fatalf("停机不算故障，不记事件: %d", n)
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
			h.clock.Advance(h.timeouts.DeclineGrace + time.Second) // 预置的单是重启前遗留的，先过完重启后的宽限期
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

// R26：平台的状态取值未实测，判定默认不放行：只有 failed 才算没扣款，其余取值都算还没定论；退款与冲正不参与。
func TestAuthStatusDefaultsToPending(t *testing.T) {
	txn := func(kind, status string, at int64) cardpool.Transaction {
		return cardpool.Transaction{TransactionID: "T1", Kind: kind, CardID: testCardID, Status: status, OccurredAt: &at, UpdatedAt: at}
	}
	declinedAfterGrace := func(t *testing.T, kind, status string) (*harness, *Order) {
		t.Helper()
		h := declinedHarness(t)
		h.pool.listTxns = pagedTxns([]cardpool.Transaction{txn(kind, status, h.clock.Now().Unix()+5)})
		h.start(t)
		o := h.submit(t, false)
		waitState(t, o.OrderNo, StateVerifying)
		h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
		return h, o
	}
	for _, c := range [][2]string{{"SETTLE", "approved"}, {"settle", "APPROVED"}} {
		t.Run("算成功授权/"+c[0]+"/"+c[1], func(t *testing.T) {
			_, o := declinedAfterGrace(t, c[0], c[1])
			if g := waitDone(t, o.OrderNo); g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" {
				t.Fatalf("应转待人工: %+v", g)
			}
		})
	}
	for _, c := range [][2]string{{"AUTH", "reversed"}, {"AUTH", ""}, {"SETTLE", "pending"}, {"SETTLE", "reversed"}, {"SETTLE", ""}} {
		t.Run("算未定论/"+c[0]+"/"+c[1], func(t *testing.T) {
			h, o := declinedAfterGrace(t, c[0], c[1])
			base := h.pool.count("ListTransactions")
			for deadline := time.Now().Add(5 * time.Second); h.pool.count("ListTransactions") < base+3; {
				if time.Now().After(deadline) {
					t.Fatal("宽限期后应继续检查交易")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if g := mustOrder(t, o.OrderNo); g.State != StateVerifying {
				t.Fatalf("状态没定论时不应定被拒: %+v", g)
			}
			h.clock.Advance(h.timeouts.Verify)
			if g := waitDone(t, o.OrderNo); g.State != StateNeedsReview || g.ErrorCode != "evidence_unavailable" {
				t.Fatalf("一直没定论应转待人工: %+v", g)
			}
		})
	}
	for _, c := range [][2]string{{"AUTH", "failed"}, {"SETTLE", "FAILED"}, {"AUTH_REVERSAL", "approved"}, {"AUTH_REVERSAL", ""}, {"REFUND", "approved"}, {"REFUND", "pending"}} {
		t.Run("不算/"+c[0]+"/"+c[1], func(t *testing.T) {
			_, o := declinedAfterGrace(t, c[0], c[1])
			if g := waitDone(t, o.OrderNo); g.State != StateFailed || g.Failure != FailureDeclined {
				t.Fatalf("应定被拒: %+v", g)
			}
		})
	}
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

// countEvents 返回时间线上含 sub 的事件条数。
func countEvents(t *testing.T, no, sub string) int {
	t.Helper()
	n := 0
	for _, e := range eventStates(t, no) {
		if strings.Contains(e.Message, sub) {
			n++
		}
	}
	return n
}

// waitMoreCalls 等 count() 至少再增加 3 次，也就是确认协程又跑了几轮。
func waitMoreCalls(t *testing.T, count func() int) {
	t.Helper()
	base := count()
	for deadline := time.Now().Add(5 * time.Second); count() < base+3; {
		if time.Now().After(deadline) {
			t.Fatal("确认协程没有继续检查")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// R26：定时检查每 15 秒一轮，故障期间不能把时间线刷满。同一来源的错误文本没变就不再写，变了再写一条。
func TestVerifyErrorEventOnlyOnChange(t *testing.T) {
	h := newPayHarness(t, nil)
	var msg atomic.Value
	msg.Store("ChatGPT 返回 HTTP 502")
	h.gpt.accountFn = func(n int) (chatgpt.Account, error) {
		if n == 1 {
			return accountFree, nil
		}
		text := msg.Load().(string)
		if text == "" {
			return accountPlus, nil
		}
		return chatgpt.Account{}, errors.New(text)
	}
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeUnknown})
	h.start(t)
	o := h.submit(t, false)
	calls := func() int { return h.gpt.count("Account") }

	waitEvent(t, o.OrderNo, "HTTP 502")
	waitMoreCalls(t, calls)
	if n := countEvents(t, o.OrderNo, "查询账号失败"); n != 1 {
		t.Fatalf("连续相同的错误只应写 1 条事件: %d", n)
	}
	msg.Store("ChatGPT 返回 HTTP 503")
	waitEvent(t, o.OrderNo, "HTTP 503")
	waitMoreCalls(t, calls)
	if n := countEvents(t, o.OrderNo, "查询账号失败"); n != 2 {
		t.Fatalf("文本变化后应再写 1 条，共 2 条: %d", n)
	}

	msg.Store("")
	if g := waitDone(t, o.OrderNo); g.State != StateSucceeded {
		t.Fatalf("恢复后应成功: %+v", g)
	}
	h.svc.mu.Lock()
	left := len(h.svc.verifyErrs)
	h.svc.mu.Unlock()
	if left != 0 {
		t.Fatalf("定论后应删除该单的记录: %d", left)
	}
}

// 账号与交易同时出错时各记各的：共用一个槽位的话，两条文本会交替「变化」，每轮都写。
func TestVerifyErrorEventPerSource(t *testing.T) {
	h := declinedHarness(t)
	h.gpt.accountFn = func(n int) (chatgpt.Account, error) {
		if n == 1 {
			return accountFree, nil
		}
		return chatgpt.Account{}, errors.New("ChatGPT 返回 HTTP 502")
	}
	h.pool.listTxns = func(cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
		return nil, 0, errors.New("读交易表失败")
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	waitEvent(t, o.OrderNo, "读交易表失败")
	waitMoreCalls(t, func() int { return h.pool.count("ListTransactions") })
	if a, b := countEvents(t, o.OrderNo, "查询账号失败"), countEvents(t, o.OrderNo, "查询授权交易失败"); a != 1 || b != 1 {
		t.Fatalf("每种错误各 1 条事件: 账号 %d，交易 %d", a, b)
	}
}

// 错误恢复后再出现同样的错误是新的故障，要再记一条。
func TestVerifyErrorEventAfterRecovery(t *testing.T) {
	h := newPayHarness(t, nil)
	var broken atomic.Bool
	broken.Store(true)
	h.gpt.accountFn = func(n int) (chatgpt.Account, error) {
		if n == 1 || !broken.Load() {
			return accountFree, nil
		}
		return chatgpt.Account{}, errors.New("ChatGPT 返回 HTTP 502")
	}
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeUnknown})
	h.start(t)
	o := h.submit(t, false)
	calls := func() int { return h.gpt.count("Account") }
	waitEvent(t, o.OrderNo, "HTTP 502")
	broken.Store(false)
	waitMoreCalls(t, calls)
	broken.Store(true)
	waitMoreCalls(t, calls)
	if n := countEvents(t, o.OrderNo, "查询账号失败"); n != 2 {
		t.Fatalf("恢复后再出错应再写 1 条，共 2 条: %d", n)
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

// declinedBeforeRestart 预置一张页面被拒后服务停机的单：宽限期在停机期间已经过去，授权推送也没送达。
func declinedBeforeRestart(t *testing.T, h *harness) *Order {
	t.Helper()
	at := h.clock.Now().Unix() - 600
	return presetOrder(t, "a@x.com", StateVerifying, map[string]any{
		"submitted_at": at, "card_id": testCardID, "lease_token": testLeaseToken,
		"page_outcome": "declined", "verify_until": at + 120, "next_check_at": at, "error_message": "Your card was declined.",
	})
}

// R24：停机期间授权推送送不到，重启后要重新等够宽限期，不能按过期的 verify_until 立刻定成被拒。
func TestRestartRestoresDeclineGrace(t *testing.T) {
	// assertWaiting 确认 Start 后已跑过几轮检查，单据仍在等宽限期
	assertWaiting := func(t *testing.T, h *harness, no string) {
		t.Helper()
		want := h.clock.Now().Unix() + int64(h.timeouts.DeclineGrace/time.Second)
		if g := mustOrder(t, no); g.VerifyUntil == nil || *g.VerifyUntil != want {
			t.Fatalf("verify_until 应为 now + DeclineGrace (%d): %+v", want, g)
		}
		waitAccountCalls(t, h, 3)
		if g := mustOrder(t, no); g.State != StateVerifying || len(h.pool.released()) != 0 {
			t.Fatalf("重启后的宽限期内不应定论: %+v", g)
		}
	}
	t.Run("重启后才到达成功授权", func(t *testing.T) {
		h := newPayHarness(t, nil)
		o := declinedBeforeRestart(t, h)
		var txns atomic.Value
		txns.Store([]cardpool.Transaction{})
		h.pool.listTxns = func(f cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
			return pagedTxns(txns.Load().([]cardpool.Transaction))(f)
		}
		h.start(t)
		assertWaiting(t, h, o.OrderNo)
		txns.Store([]cardpool.Transaction{txnAt("A1", "approved", h.clock.Now().Unix())})
		h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
		g := waitDone(t, o.OrderNo)
		if g.State != StateNeedsReview || g.ErrorCode != "evidence_conflict" {
			t.Fatalf("重启后到达的成功授权应转待人工: %+v", g)
		}
		assertReleases(t, h, cardpool.ResultAborted)
	})
	t.Run("始终没有授权", func(t *testing.T) {
		h := newPayHarness(t, nil)
		o := declinedBeforeRestart(t, h)
		h.start(t)
		assertWaiting(t, h, o.OrderNo)
		h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
		g := waitDone(t, o.OrderNo)
		if g.State != StateFailed || g.Failure != FailureDeclined {
			t.Fatalf("过了宽限期才定被拒: %+v", g)
		}
		assertReleases(t, h, cardpool.ResultFailed)
	})
	t.Run("只推后不提前，也不动其它确认中的单", func(t *testing.T) {
		h := newPayHarness(t, nil)
		now := h.clock.Now().Unix()
		later := presetOrder(t, "later@x.com", StateVerifying, map[string]any{"page_outcome": "declined", "verify_until": now + 3600})
		unknown := presetOrder(t, "unknown@x.com", StateVerifying, map[string]any{"page_outcome": "unknown", "verify_until": now - 10})
		if err := h.svc.recoverOrders(ctx); err != nil {
			t.Fatal(err)
		}
		if g := mustOrder(t, later.OrderNo); *g.VerifyUntil != now+3600 {
			t.Fatalf("不应提前 verify_until: %+v", g)
		}
		if g := mustOrder(t, unknown.OrderNo); *g.VerifyUntil != now-10 {
			t.Fatalf("页面结果不明的单不应改动: %+v", g)
		}
	})
}

// I-1：校验错误的单重启后同样要补宽限期；补上之后定论不计入健康度。
func TestRestartRestoresGraceForFormRejected(t *testing.T) {
	h := newPayHarness(t, nil)
	at := h.clock.Now().Unix() - 600
	o := presetOrder(t, "a@x.com", StateVerifying, map[string]any{
		"submitted_at": at, "card_id": testCardID, "lease_token": testLeaseToken,
		"page_outcome": "form_rejected", "verify_until": at + 120, "next_check_at": at, "error_message": postalIncomplete,
	})
	h.start(t)
	want := h.clock.Now().Unix() + int64(h.timeouts.DeclineGrace/time.Second)
	if g := mustOrder(t, o.OrderNo); g.VerifyUntil == nil || *g.VerifyUntil != want {
		t.Fatalf("verify_until 应为 now + DeclineGrace (%d): %+v", want, g)
	}
	waitAccountCalls(t, h, 3)
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying || len(h.pool.released()) != 0 {
		t.Fatalf("重启后的宽限期内不应定论: %+v", g)
	}
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	g := waitDone(t, o.OrderNo)
	if g.State != StateFailed || g.ErrorCode != "form_rejected" || g.ErrorMessage != "页面校验未通过："+postalIncomplete {
		t.Fatalf("过了宽限期才定论: %+v", g)
	}
	assertReleases(t, h, cardpool.ResultAborted)
}

// I-1：恢复没做完时校验错误的单也不能定论：宽限期可能还没补上，按过期的 verify_until 会把已扣款的单定成失败。
func TestRecoveryPendingBlocksFormRejectedConclusion(t *testing.T) {
	h := newPayHarness(t, nil)
	at := h.clock.Now().Unix() - 600
	o := presetOrder(t, "a@x.com", StateVerifying, map[string]any{
		"submitted_at": at, "card_id": testCardID, "lease_token": testLeaseToken,
		"page_outcome": "form_rejected", "verify_until": at + 120, "next_check_at": at, "error_message": postalIncomplete,
	})
	h.svc.verifyDue(ctx) // recovered 还是 false
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying || len(h.pool.released()) != 0 {
		t.Fatalf("恢复没做完时不应定论: %+v", g)
	}
}

// R24：宽限期写不进去时计入恢复错误，由 R19 的重试接住；重试成功之前这张被拒单不能按过期的 verify_until 定论。
// 只有被拒单要等：其余确认中的单定论都是安全方向（成功或转人工），恢复失败期间照常确认。
// 恢复因别的单失败时会反复重试。同一张被拒单只补一次宽限期：否则每轮重试都写一条事件，还把 verify_until 一直往后推。
func TestRestartGraceRetryNotRepeated(t *testing.T) {
	h := newPayHarness(t, nil)
	o := declinedBeforeRestart(t, h)
	ctx := context.Background()
	if err := h.svc.recoverOrders(ctx); err != nil {
		t.Fatal(err)
	}
	first := mustOrder(t, o.OrderNo)
	if first.VerifyUntil == nil {
		t.Fatalf("应补上宽限期: %+v", first)
	}
	for i := 0; i < 3; i++ {
		h.clock.Advance(10 * time.Second)
		if err := h.svc.recoverOrders(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if g := mustOrder(t, o.OrderNo); *g.VerifyUntil != *first.VerifyUntil {
		t.Fatalf("重试不应再推后 verify_until: %d → %d", *first.VerifyUntil, *g.VerifyUntil)
	}
	if n := countEvents(t, o.OrderNo, "重新等待授权推送"); n != 1 {
		t.Fatalf("事件应只有 1 条，实际 %d", n)
	}
}

func TestRestartGraceWriteFailureRetried(t *testing.T) {
	h := newPayHarness(t, nil)
	declined := declinedBeforeRestart(t, h)
	stale := *declined.VerifyUntil
	now := h.clock.Now().Unix()
	other := presetOrder(t, "other@x.com", StateVerifying, map[string]any{
		"submitted_at": now - 900, "card_id": "card-o", "lease_token": "tok-o",
		"page_outcome": "unknown", "verify_until": now - 300, "next_check_at": now - 300,
	})
	// 只拦这张被拒单的宽限期写入，定论（不写 verify_until）不受影响：不加门控时它会被定成 declined
	if _, err := db.DB.Exec(`CREATE TRIGGER block_grace BEFORE UPDATE OF verify_until ON pay_orders
		WHEN OLD.order_no = '` + declined.OrderNo + `' BEGIN SELECT RAISE(ABORT, 'grace write blocked'); END`); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	recoveryAlerts := func() int {
		n := 0
		for _, a := range h.alerts.Texts() {
			if strings.Contains(a, "重启恢复失败") {
				n++
			}
		}
		return n
	}
	if recoveryAlerts() != 1 {
		t.Fatalf("写不进去应计入恢复失败并告警: %v", h.alerts.Texts())
	}
	if g := waitDone(t, other.OrderNo); g.State != StateNeedsReview || g.ErrorCode != "verify_timeout" {
		t.Fatalf("其余确认中的单照常定论: %+v", g)
	}
	time.Sleep(50 * time.Millisecond) // 是 VerifyTick 的 10 倍，确认协程已经扫描了多轮
	if g := mustOrder(t, declined.OrderNo); g.State != StateVerifying || *g.VerifyUntil != stale {
		t.Fatalf("恢复没做完时被拒单不应定论: %+v", g)
	}
	for _, r := range h.pool.released() {
		if r.Lease.Token == testLeaseToken {
			t.Fatalf("被拒单不应释放: %+v", r)
		}
	}

	if _, err := db.DB.Exec(`DROP TRIGGER block_grace`); err != nil {
		t.Fatal(err)
	}
	h.svc.Kick()
	want := h.clock.Now().Unix() + int64(h.timeouts.DeclineGrace/time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for *mustOrder(t, declined.OrderNo).VerifyUntil != want {
		if time.Now().After(deadline) {
			t.Fatalf("重试后应写入宽限期: %+v", mustOrder(t, declined.OrderNo))
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitAccountCalls(t, h, 3)
	if g := mustOrder(t, declined.OrderNo); g.State != StateVerifying {
		t.Fatalf("宽限期内不应定论: %+v", g)
	}
	h.clock.Advance(h.timeouts.DeclineGrace + time.Second)
	if g := waitDone(t, declined.OrderNo); g.State != StateFailed || g.Failure != FailureDeclined {
		t.Fatalf("过了宽限期应定被拒: %+v", g)
	}
	if recoveryAlerts() != 1 {
		t.Fatalf("恢复告警只应 1 次: %v", h.alerts.Texts())
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
	if err := finish(ctx, o.OrderNo, StateVerifying, StateNeedsReview, "error", "转待人工", map[string]any{"error_code": "verify_timeout", "error_message": "确认超时"}); err != nil {
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
		if g.ErrorCode != "" || g.ErrorMessage != "" {
			t.Fatalf("标记成功应清掉错误信息: %+v", g)
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
		if g.ErrorCode != "verify_timeout" || g.ErrorMessage != "确认超时" {
			t.Fatalf("标记失败应保留错误信息: %+v", g)
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
		err := h.svc.Resolve(ctx, o.OrderNo, StateFailed, "第二次", "admin")
		if !errors.Is(err, ErrBadRequest) || !errors.Is(err, ErrStateChanged) || !strings.Contains(err.Error(), "当前为 succeeded") {
			t.Fatalf("对 succeeded 单应报冲突并说明当前状态: %v", err)
		}
		if g := mustOrder(t, o.OrderNo); g.State != StateSucceeded || g.ResolveNote != "第一次" {
			t.Fatalf("冲突时不应改单: %+v", g)
		}
		v := presetOrder(t, "v@x.com", StateVerifying, nil)
		err = h.svc.Resolve(ctx, v.OrderNo, StateFailed, "备注", "admin")
		if !errors.Is(err, ErrBadRequest) || !errors.Is(err, ErrStateChanged) || !strings.Contains(err.Error(), "当前为 verifying") {
			t.Fatalf("对确认中的单应报冲突并说明当前状态: %v", err)
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

// R26：只有用默认的真实浏览器工厂时才清理 Chrome 临时目录。测试替身不碰它：
// go test ./... 并行时，别的包的测试可能正在用这些目录。
func TestStartCleansStaleProfilesOnlyWithRealBrowser(t *testing.T) {
	stale := func(t *testing.T) string {
		t.Helper()
		tmp := t.TempDir()
		t.Setenv("TMPDIR", tmp)
		dir := filepath.Join(tmp, "cdk-pay-chrome-123")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-2 * time.Hour) // R28：只有很久没变化的才算残留
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	t.Run("替身工厂不清理", func(t *testing.T) {
		h := newPayHarness(t, nil)
		dir := stale(t)
		h.start(t)
		if _, err := os.Lstat(dir); err != nil {
			t.Fatalf("不应清理: %v", err)
		}
	})
	t.Run("默认工厂在 Start 返回前清理", func(t *testing.T) {
		h := newPayHarness(t, nil)
		dir := stale(t)
		h.svc = New(Options{Pool: h.pool, Alert: h.alerts.add, ShotDir: h.shotDir, Timeouts: &h.timeouts})
		h.start(t)
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Fatalf("应清理: %v", err)
		}
	})
	t.Run("默认工厂不动刚建的目录", func(t *testing.T) { // R28：可能是同机器另一个实例正在用的
		h := newPayHarness(t, nil)
		tmp := t.TempDir()
		t.Setenv("TMPDIR", tmp)
		dir := filepath.Join(tmp, "cdk-pay-chrome-live")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		h.svc = New(Options{Pool: h.pool, Alert: h.alerts.add, ShotDir: h.shotDir, Timeouts: &h.timeouts})
		h.start(t)
		if _, err := os.Lstat(dir); err != nil {
			t.Fatalf("不应清理: %v", err)
		}
	})
}
