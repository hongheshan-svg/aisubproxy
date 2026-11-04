package payengine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

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
	// 成功的真实开通单留着会话交给续费协程关闭续费（见 renewal_test.go），关掉或到点再清空
	if g.SessionSealed == "" || g.FinishedAt == nil || g.RenewalState != RenewalPending {
		t.Fatalf("成功应记结束时间，并留会话待关闭续费: %+v", g)
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

	if r := h.pool.lastArgs("Acquire")[0].(cardpool.AcquireRequest); r.PlanKey != "plus" || r.OrderRef != o.OrderNo ||
		r.AccountEmail != "buyer@example.com" || r.Currency != "USD" {
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
	// 真实开通单转待人工时会话留到 SessionKeep：人工在这之内定论成功还能关闭续费
	if g.State != StateNeedsReview || g.ErrorCode != "verify_timeout" || g.SessionSealed == "" || g.RenewalState != RenewalNone {
		t.Fatalf("应转待人工: %+v", g)
	}
	if a := waitAlerts(t, h, 1); len(a) != 1 || !strings.Contains(a[0], o.OrderNo) || !strings.Contains(a[0], "待人工") {
		t.Fatalf("应告警 1 次: %v", a)
	}
	assertReleases(t, h, cardpool.ResultAborted)
	assertSubmitCount(t, h, 1)
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

// PF-8：浏览器能证明按钮没被点击时才记 not_submitted，并清掉 submitted_at，释放租约，不告警，不进确认。
func TestSubmitNotClickedAbandons(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.submit = func() error { return errors.New("找不到支付按钮") }
	h.br.lockSubmit = func() bool { return false }
	h.start(t)
	o := h.submit(t, false)
	g := waitDone(t, o.OrderNo)
	assertNotSubmitted(t, g, "submit_not_clicked")
	if g.SubmittedAt != nil || g.VerifyUntil != nil || g.PageOutcome != "" {
		t.Fatalf("没点击的单不应留下提交痕迹: %+v", g)
	}
	assertReleases(t, h, cardpool.ResultAborted)
	assertSubmitCount(t, h, 1)
	if a := h.alerts.Texts(); len(a) != 0 {
		t.Fatalf("不应告警: %v", a)
	}
	if !strings.Contains(g.ErrorMessage, "找不到支付按钮") {
		t.Fatalf("应留下原因: %+v", g)
	}
	if b := theBrowser(t, h); b.count("LockSubmit") != 1 || b.count("WaitOutcome") != 0 || b.count("Close") != 1 {
		t.Fatalf("应锁定 1 次、不等结果、关闭浏览器: %d %d %d", b.count("LockSubmit"), b.count("WaitOutcome"), b.count("Close"))
	}
	waitEvent(t, o.OrderNo, "支付按钮没有被点击 地址：")
}

// PF-8：不能证明没点击（点击过程中出错）就必须走确认，绝不记 not_submitted，也绝不再点一次。
func TestSubmitClickAttemptedNeverNotSubmitted(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.submit = func() error { return errors.New("点击支付按钮失败: CDP 连接断开") }
	h.br.lockSubmit = func() bool { return true }
	h.start(t)
	o := h.submit(t, false)
	g := waitState(t, o.OrderNo, StateVerifying)
	if g.Failure != "" || g.SubmittedAt == nil || g.PageOutcome != "unknown" || !strings.Contains(g.ErrorMessage, "CDP 连接断开") {
		t.Fatalf("应转确认，不记未提交: %+v", g)
	}
	assertSubmitCount(t, h, 1)
	if b := theBrowser(t, h); b.count("LockSubmit") != 1 {
		t.Fatalf("应锁定 1 次: %d", b.count("LockSubmit"))
	}
	h.clock.Advance(h.timeouts.Verify + time.Second)
	if g = waitDone(t, o.OrderNo); g.State != StateNeedsReview || g.Failure != "" {
		t.Fatalf("查不到开通应转待人工: %+v", g)
	}
	assertSubmitCount(t, h, 1)
}

// PF-8：Submit 成功后，后面任何错误都不再问 LockSubmit：标志早已置位，也不会走「没点击」这条路。
func TestErrorAfterSubmitNeverAsksLock(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.waitOutcome = outcomes(outcomeStep{err: errors.New("Chrome 崩溃了")})
	h.br.lockSubmit = func() bool { return false } // 就算它说没点击也不能信：这里根本不该问
	h.start(t)
	o := h.submit(t, false)
	g := waitState(t, o.OrderNo, StateVerifying)
	if g.Failure != "" || g.SubmittedAt == nil {
		t.Fatalf("应转确认: %+v", g)
	}
	if n := theBrowser(t, h).count("LockSubmit"); n != 0 {
		t.Fatalf("Submit 成功后不应再问: %d", n)
	}
}

// 服务停止时不收尾，哪怕能证明没点击：单据留给重启恢复转确认，这是安全方向。
func TestNotClickedDuringShutdownLeftForRecovery(t *testing.T) {
	h := newPayHarness(t, nil)
	at := h.clock.Now().Unix()
	o := presetOrder(t, "a@x.com", StateSubmitted, map[string]any{"submitted_at": at, "card_id": testCardID, "lease_token": testLeaseToken})
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	h.svc.settle(stopped, &job{o: o}, &notClickedError{err: errors.New("超时")})
	g := mustOrder(t, o.OrderNo)
	if g.State != StateSubmitted || g.SubmittedAt == nil || g.Failure != "" || len(h.pool.released()) != 0 {
		t.Fatalf("应留给重启恢复: %+v", g)
	}
}

// 记「没点击」写库失败时单据还是 submitted，由确认协程接管，不会卡住，也不会被当成未提交。
func TestNotClickedWriteFailureAdopted(t *testing.T) {
	h := newPayHarness(t, nil)
	at := h.clock.Now().Unix()
	o := presetOrder(t, "a@x.com", StateSubmitted, map[string]any{"submitted_at": at, "card_id": testCardID, "lease_token": testLeaseToken})
	stale := *o
	stale.State = StateFilling // 内存里的状态过期，写库的条件更新必然失败
	h.svc.settle(ctx, &job{o: &stale}, &notClickedError{err: errors.New("找不到支付按钮")})
	g := mustOrder(t, o.OrderNo)
	if g.State != StateSubmitted || g.SubmittedAt == nil || g.Failure != "" {
		t.Fatalf("写库失败应保持原状: %+v", g)
	}
	if a := h.alerts.Texts(); len(a) != 1 || !strings.Contains(a[0], o.OrderNo) {
		t.Fatalf("应告警 1 次: %v", a)
	}
	h.svc.recovered.Store(true)
	h.svc.adoptSubmitted(ctx)
	if g = mustOrder(t, o.OrderNo); g.State != StateVerifying || g.Failure != "" {
		t.Fatalf("应由确认协程接管: %+v", g)
	}
}

// R16、R25：连转 verifying 也写不进去时单据保持原状，告警一次（文本脱敏），留给确认协程接管。
func TestSettleAfterSubmitWriteFailureAlerts(t *testing.T) {
	h := newPayHarness(t, nil)
	at := h.clock.Now().Unix()
	o := presetOrder(t, "a@x.com", StateRequires3DS, map[string]any{"submitted_at": at})
	stale := *o
	stale.State = StateSubmitted // 内存里的状态过期，写库的条件更新必然失败
	// 这条错误文本里只有状态名，把它当敏感串来验证告警走了脱敏
	h.svc.settle(ctx, &job{o: &stale, secrets: []string{StateRequires3DS}}, errors.New("boom"))
	g := mustOrder(t, o.OrderNo)
	if g.State != StateRequires3DS || g.Failure != "" || g.FinishedAt != nil {
		t.Fatalf("写库失败应保持原状: %+v", g)
	}
	a := h.alerts.Texts()
	if len(a) != 1 || !strings.Contains(a[0], o.OrderNo) || !strings.Contains(a[0], "转确认") {
		t.Fatalf("应告警 1 次: %v", a)
	}
	if strings.Contains(a[0], StateRequires3DS) {
		t.Fatalf("告警应脱敏: %q", a[0])
	}
}

// waitAlerts 轮询到至少有 n 条告警，最多 5 秒。
// 转人工、点数单存卡、续费失败等告警都在写完状态之后才发（写库失败就不告警），
// 等到单据状态时告警可能还没发出：断言「有告警」都要用它，不能直接读 h.alerts。
func waitAlerts(t *testing.T, h *harness, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.alerts.Texts()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("告警未达到 %d 条: %v", n, h.alerts.Texts())
		}
		time.Sleep(5 * time.Millisecond)
	}
	return h.alerts.Texts()
}

// waitAlertContaining 轮询到有一条告警含 sub，最多 5 秒；返回当时的全部告警。
func waitAlertContaining(t *testing.T, h *harness, sub string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		a := h.alerts.Texts()
		for _, s := range a {
			if strings.Contains(s, sub) {
				return a
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("没有含 %q 的告警: %v", sub, a)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// R25：两次转确认都失败时告警恰好一次；写库恢复后确认协程接管进 verifying 并最终定论，全程只点一次支付。
func TestBeginVerifyFailureAdopted(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.accountFn = planFrom(2)
	h.br.waitOutcome = func(context.Context, int) (paybrowser.Outcome, string, error) {
		_, err := db.DB.Exec(`ALTER TABLE pay_order_events RENAME TO pay_order_events_hidden`)
		return paybrowser.OutcomeSuccess, "", err
	}
	h.start(t)
	o := h.submit(t, false)
	a := waitAlerts(t, h, 1)
	if !strings.Contains(a[0], o.OrderNo) || !strings.Contains(a[0], "转确认") {
		t.Fatalf("告警内容不符: %q", a[0])
	}
	time.Sleep(50 * time.Millisecond) // 接管也写不进去，确认协程已经重试了多轮
	if g := mustOrder(t, o.OrderNo); g.State != StateSubmitted {
		t.Fatalf("写库恢复之前应保持 submitted: %+v", g)
	}
	if n := len(h.alerts.Texts()); n != 1 {
		t.Fatalf("接管重试不应重复告警: %v", h.alerts.Texts())
	}

	if _, err := db.DB.Exec(`ALTER TABLE pay_order_events_hidden RENAME TO pay_order_events`); err != nil {
		t.Fatal(err)
	}
	g := waitDone(t, o.OrderNo)
	if g.State != StateSucceeded {
		t.Fatalf("接管后应定论: %+v", g)
	}
	if !containsInOrder(eventStates(t, o.OrderNo), StateSubmitted, StateVerifying, StateSucceeded) {
		t.Fatalf("应经过 verifying: %+v", eventStates(t, o.OrderNo))
	}
	if n := len(h.alerts.Texts()); n != 1 {
		t.Fatalf("告警应恰好 1 次: %v", h.alerts.Texts())
	}
	assertSubmitCount(t, h, 1)
	assertReleases(t, h, cardpool.ResultSuccess)
}

// R25：没有工作协程在处理的已提交单都要接管；确认协程不持有浏览器。
func TestOrphanedSubmittedAdopted(t *testing.T) {
	for _, state := range []string{StateSubmitted, StateRequires3DS} {
		t.Run(state, func(t *testing.T) {
			h := newPayHarness(t, nil)
			h.start(t)
			o := presetOrder(t, "a@x.com", state, nil)
			g := waitState(t, o.OrderNo, StateVerifying)
			if g.PageOutcome != "unknown" || g.VerifyUntil == nil || g.NextCheckAt == nil {
				t.Fatalf("应按结果不明转确认: %+v", g)
			}
			if len(h.br.browsers()) != 0 {
				t.Fatal("接管不应启动浏览器")
			}
		})
	}
}

// R25：正在处理的单在 running 里，不能被接管：工作协程还要把页面结果写进去。
func TestRunningSubmittedNotAdopted(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.accountFn = planFrom(2)
	gate := make(chan struct{})
	h.br.waitOutcome = func(c context.Context, _ int) (paybrowser.Outcome, string, error) {
		select {
		case <-gate:
			return paybrowser.OutcomeSuccess, "", nil
		case <-c.Done():
			return paybrowser.OutcomeUnknown, "", c.Err()
		}
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateSubmitted)
	time.Sleep(50 * time.Millisecond) // 是 VerifyTick 的 10 倍，确认协程已经扫描了多轮
	if g := mustOrder(t, o.OrderNo); g.State != StateSubmitted {
		t.Fatalf("处理中的单不应被接管: %+v", g)
	}
	close(gate)
	if g := waitDone(t, o.OrderNo); g.State != StateSucceeded || g.PageOutcome != "success" {
		t.Fatalf("页面结果应由工作协程写入: %+v", g)
	}
}

// R25：提交之前放弃时写库失败，单据不收敛，该账号会一直报 ErrInflight，要告警一次；停机时不算故障。
func TestAbandonWriteFailureAlerts(t *testing.T) {
	h := newPayHarness(t, nil)
	o := presetOrder(t, "a@x.com", StateFilling, nil)
	stale := *o
	stale.State = StateFunding // 内存里的状态过期，写库的条件更新必然失败
	// 这条错误文本里只有状态名，把它当敏感串来验证告警走了脱敏
	h.svc.settle(ctx, &job{o: &stale, secrets: []string{StateFilling}}, abort("fill_failed", "填不上"))
	if g := mustOrder(t, o.OrderNo); g.State != StateFilling {
		t.Fatalf("写库失败应保持原状: %+v", g)
	}
	a := h.alerts.Texts()
	if len(a) != 1 || !strings.Contains(a[0], o.OrderNo) {
		t.Fatalf("应告警 1 次: %v", a)
	}
	if strings.Contains(a[0], StateFilling) {
		t.Fatalf("告警应脱敏: %q", a[0])
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	h.svc.abandon(canceled, &job{o: &stale}, &stepFailure{code: "fill_failed", msg: "填不上"})
	if n := len(h.alerts.Texts()); n != 1 {
		t.Fatalf("停机时不应告警: %v", h.alerts.Texts())
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

// R23：Browser 实现可能不守 timeout，已点支付的单不能因此停在 submitted、占住名额。
// 替身只在 ctx 结束时返回：引擎的外层时限到点后，单据进 verifying、浏览器关闭、下一张排队单接着处理。
func TestWaitOutcomeHangVerifies(t *testing.T) {
	hang := func(c context.Context) (paybrowser.Outcome, string, error) {
		<-c.Done()
		return paybrowser.OutcomeUnknown, "", c.Err()
	}
	cases := map[string]func(c context.Context, n int) (paybrowser.Outcome, string, error){
		"3DS 之前": func(c context.Context, _ int) (paybrowser.Outcome, string, error) { return hang(c) },
		"3DS 之后": func(c context.Context, n int) (paybrowser.Outcome, string, error) {
			if n == 1 {
				return paybrowser.OutcomeChallenge, "", nil
			}
			return hang(c)
		},
	}
	for name, wait := range cases {
		t.Run(name, func(t *testing.T) {
			h := newPayHarness(t, func(to *Timeouts) {
				to.SubmitResult = 20 * time.Millisecond
				to.OutcomeMargin = 20 * time.Millisecond
			})
			if err := SaveConcurrency(1); err != nil {
				t.Fatal(err)
			}
			h.pool.waitOTP = func(context.Context, string, time.Time) (string, error) { return "123456", nil }
			h.br.waitOutcome = wait
			h.start(t)
			a := h.submitAs(t, sessionFor("a@x.com"), false)
			g := waitState(t, a.OrderNo, StateVerifying)
			if g.PageOutcome != "unknown" || g.Failure != "" || g.SubmittedAt == nil || !strings.Contains(g.ErrorMessage, "超时") {
				t.Fatalf("应转确认并说明超时: %+v", g)
			}
			b := h.submitAs(t, sessionFor("b@x.com"), true)
			if g := waitDone(t, b.OrderNo); g.State != StatePreviewed {
				t.Fatalf("下一张排队单应被处理: %+v", g)
			}
			if first := h.br.browsers()[0]; first.count("Close") != 1 {
				t.Fatalf("应关闭浏览器: %d", first.count("Close"))
			}
			assertSubmitCount(t, h, 1)
		})
	}
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
		// 姓名要逐字键入账单表单：含控制字符（\n、\r 会被当成回车）就在注资之前放弃，不先把钱转进卡里
		{"持卡人姓名含控制字符", "card_info_failed", func(h *harness) {
			h.pool.cardInfo = func(string) (cardpool.CardInfo, error) {
				return cardpool.CardInfo{Last4: "4242", Currency: "USD", HolderFirstName: "JO\nHN", HolderLastName: "DOE"}, nil
			}
		}, 0},
		// fund_cents 是美元分，而 cardpool.Fund 按卡币种下单：非美元卡会多充或少充
		{"卡币种不是美元", "card_currency_unsupported", func(h *harness) {
			h.pool.cardInfo = func(string) (cardpool.CardInfo, error) {
				return cardpool.CardInfo{Last4: "4242", Currency: "PHP", HolderFirstName: "JOHN", HolderLastName: "DOE"}, nil
			}
		}, 0},
		{"卡币种为空", "card_currency_unsupported", func(h *harness) {
			h.pool.cardInfo = func(string) (cardpool.CardInfo, error) {
				return cardpool.CardInfo{Last4: "4242", HolderFirstName: "JOHN", HolderLastName: "DOE"}, nil
			}
		}, 0},
		{"取卡号失败", "card_sensitive_failed", func(h *harness) {
			h.pool.sensitive = func(*cardpool.Lease) (*cardpool.CardSensitive, error) { return nil, boom }
		}, 1},
		{"有效期无法解析", "card_expiry_invalid", func(h *harness) {
			h.pool.sensitive = func(*cardpool.Lease) (*cardpool.CardSensitive, error) {
				return &cardpool.CardSensitive{CardNo: testCardNo, CVV2: testCVV, ExpiryDate: "13/99"}, nil
			}
		}, 1},
		{"租约已失效", "card_sensitive_failed", func(h *harness) {
			h.pool.sensitive = func(*cardpool.Lease) (*cardpool.CardSensitive, error) { return nil, cardpool.ErrLeaseInvalid }
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

// 币种比较不区分大小写：小写的 usd 也是美元卡，照常注资。
func TestCardCurrencyCaseInsensitive(t *testing.T) {
	h := newPayHarness(t, nil)
	h.pool.cardInfo = func(string) (cardpool.CardInfo, error) {
		return cardpool.CardInfo{Last4: "4242", Currency: " usd ", HolderFirstName: "JOHN", HolderLastName: "DOE"}, nil
	}
	h.gpt.accountFn = planFrom(2)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess})
	h.start(t)
	if g := waitDone(t, h.submit(t, false).OrderNo); g.State != StateSucceeded {
		t.Fatalf("应成功: %+v", g)
	}
	if n := h.pool.count("Fund"); n != 1 {
		t.Fatalf("应注资 1 次: %d", n)
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

// 验证码同卡号一样不得出现在错误信息、事件、告警、日志里：页面报错可能把输入框内容带出来。
func TestOTPScrubbed(t *testing.T) {
	const otp = "918273"
	dirty := "页面回显验证码 " + otp + " 校验失败"
	cases := map[string]func(*harness){
		"填验证码报错": func(h *harness) {
			h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeChallenge})
			h.br.enterOTP = func(string) error { return errors.New(dirty) }
		},
		"填完之后等结果报错": func(h *harness) {
			h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeChallenge}, outcomeStep{err: errors.New(dirty)})
		},
		"页面提示夹带": func(h *harness) {
			h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeChallenge}, outcomeStep{text: dirty})
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			h := newPayHarness(t, nil)
			h.pool.waitOTP = func(context.Context, string, time.Time) (string, error) { return otp, nil }
			set(h)
			h.start(t)
			o := h.submit(t, false)
			waitState(t, o.OrderNo, StateVerifying)
			h.clock.Advance(h.timeouts.Verify + time.Second)
			g := waitDone(t, o.OrderNo)
			if g.State != StateNeedsReview || !strings.Contains(g.ErrorMessage, "校验失败") {
				t.Fatalf("应转待人工并保留说明: %+v", g)
			}
			if a := h.alerts.Texts(); len(a) != 1 {
				t.Fatalf("应告警 1 次: %v", a)
			}
			texts := append([]string{g.ErrorMessage, logs.String()}, h.alerts.Texts()...)
			for _, e := range eventStates(t, g.OrderNo) {
				texts = append(texts, e.Message)
			}
			for _, txt := range texts {
				if strings.Contains(strings.ReplaceAll(txt, g.OrderNo, ""), otp) {
					t.Fatalf("文本不应含验证码: %q", txt)
				}
			}
		})
	}
}

// CVV 可能恰好是卡号的一段：先抹 CVV 会把卡号切碎，正则再也匹配不上，卡号片段就留下了。
func TestCVVIsCardFragmentScrubbed(t *testing.T) {
	const card, spaced, cvv = "4539148803436467", "4539 1488 0343 6467", "803"
	logs := captureLog(t)
	h := newPayHarness(t, nil)
	h.pool.sensitive = func(*cardpool.Lease) (*cardpool.CardSensitive, error) {
		return &cardpool.CardSensitive{CardID: testCardID, CardNo: card, CVV2: cvv, ExpiryDate: "08/28"}, nil
	}
	h.br.fill = func(paybrowser.Card, paybrowser.Billing) error {
		return fmt.Errorf("输入框内容 %s / %s，cvc %s 校验失败", card, spaced, cvv)
	}
	h.start(t)
	g := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, g, "fill_failed")
	if !strings.Contains(g.ErrorMessage, "校验失败") {
		t.Fatalf("应保留其余说明: %q", g.ErrorMessage)
	}
	texts := append([]string{g.ErrorMessage, logs.String()}, h.alerts.Texts()...)
	for _, e := range eventStates(t, g.OrderNo) {
		texts = append(texts, e.Message)
	}
	for _, txt := range texts {
		txt = strings.ReplaceAll(txt, g.OrderNo, "")
		for _, bad := range []string{"4539", "1488", "0343", "6467", cvv} {
			if strings.Contains(txt, bad) {
				t.Fatalf("文本不应残留卡号片段 %q: %q", bad, txt)
			}
		}
	}
}

// 租约必须在注资之前落库：注资时服务崩溃，重启恢复要靠单据里的令牌释放卡。
func TestLeaseStoredBeforeFund(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.accountFn = planFrom(2)
	var seen atomic.Pointer[Order]
	h.pool.fund = func(context.Context, *cardpool.Lease, int64) (*cardpool.Order, error) {
		g, err := getOrder(ctx, mustNo(t, h))
		if err != nil {
			return nil, err
		}
		seen.Store(g)
		return &cardpool.Order{OutOrderNo: "FUND-1", State: cardpool.StateProcessing}, nil
	}
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess})
	h.start(t)
	waitDone(t, h.submit(t, false).OrderNo)
	g := seen.Load()
	if g == nil || g.LeaseToken != testLeaseToken || g.CardID != testCardID {
		t.Fatalf("注资时库里应已有租约与卡: %+v", g)
	}
}

// 注资金额取建单时的快照：建单后改套餐表，不影响排队中的单。
func TestFundUsesOrderSnapshot(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.accountFn = planFrom(2)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess})
	o := h.submit(t, false)
	tbl, err := LoadPlanTable()
	if err != nil {
		t.Fatal(err)
	}
	for i := range tbl {
		if tbl[i].Key == "plus" {
			r := tbl[i].Regions["PH"]
			r.FundCents = 9900
			tbl[i].Regions["PH"] = r
		}
	}
	if err := SavePlanTable(tbl); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	if g := waitDone(t, o.OrderNo); g.State != StateSucceeded {
		t.Fatalf("应成功: %+v", g)
	}
	if a := h.pool.lastArgs("Fund"); h.pool.count("Fund") != 1 || a[1] != int64(3000) {
		t.Fatalf("应按快照注资 3000 分: %v", a)
	}
}

// 服务在任一步骤中停止：单据不写错误终态，也不释放、不告警，全部留给下次启动。
// 新 Service 走 Start 恢复后，提交前的步骤到 failed / not_submitted 并释放，提交后的步骤到 verifying、不再点支付。
func TestCancelDuringStepLeavesForRecovery(t *testing.T) {
	cases := []struct {
		name, state string
		submits     int
		block       func(*harness, func() error)
	}{
		{"Fund", StateFunding, 0, func(h *harness, wait func() error) {
			h.pool.fund = func(context.Context, *cardpool.Lease, int64) (*cardpool.Order, error) { return nil, wait() }
		}},
		{"WaitOrder", StateFunding, 0, func(h *harness, wait func() error) {
			h.pool.waitOrder = func(context.Context, string) (*cardpool.Order, error) { return nil, wait() }
		}},
		{"Fill", StateFilling, 0, func(h *harness, wait func() error) {
			h.br.fill = func(paybrowser.Card, paybrowser.Billing) error { return wait() }
		}},
		{"Submit", StateSubmitted, 1, func(h *harness, wait func() error) {
			h.br.submit = wait
		}},
		{"WaitOTP", StateRequires3DS, 1, func(h *harness, wait func() error) {
			h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeChallenge})
			h.pool.waitOTP = func(context.Context, string, time.Time) (string, error) { return "", wait() }
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newPayHarness(t, nil)
			runCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			c.block(h, func() error {
				close(entered)
				<-runCtx.Done()
				return runCtx.Err()
			})
			h.svc.Start(runCtx)
			o := h.submit(t, false)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatalf("单据未走到 %s: %+v", c.name, mustOrder(t, o.OrderNo))
			}
			cancel()
			h.svc.Wait()

			g := mustOrder(t, o.OrderNo)
			if g.State != c.state || g.Failure != "" || g.ErrorCode != "" || g.FinishedAt != nil {
				t.Fatalf("停止时不应写错误终态: %+v", g)
			}
			if len(h.pool.released()) != 0 || len(h.alerts.Texts()) != 0 {
				t.Fatalf("停止时不应释放或告警: %+v %v", h.pool.released(), h.alerts.Texts())
			}
			assertSubmitCount(t, h, c.submits)

			h.rebuild(h.pool)
			h.start(t)
			g = mustOrder(t, o.OrderNo)
			if c.submits == 0 {
				if g.State != StateFailed || g.Failure != FailureNotSubmitted || g.ErrorMessage != restartMessage {
					t.Fatalf("提交前的步骤应 failed / not_submitted: %+v", g)
				}
				rel := assertReleases(t, h, cardpool.ResultAborted)
				if rel[0].Lease.Token != testLeaseToken {
					t.Fatalf("应按单据字段释放: %+v", rel[0])
				}
			} else {
				if g.State != StateVerifying || g.Failure != "" || g.PageOutcome != "unknown" {
					t.Fatalf("提交后的步骤应转 verifying: %+v", g)
				}
				if len(h.pool.released()) != 0 {
					t.Fatalf("确认结束前不应释放: %+v", h.pool.released())
				}
			}
			assertSubmitCount(t, h, c.submits)
		})
	}
}
