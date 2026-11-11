package payengine

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// newRenewalHarness 是续费协程每 5 毫秒扫一次的付款测试环境。
func newRenewalHarness(t *testing.T) *harness {
	t.Helper()
	return newPayHarness(t, func(to *Timeouts) { to.RenewalTick = 5 * time.Millisecond })
}

// waitRenewal 轮询到单据的续费状态为 want，最多 5 秒。
func waitRenewal(t *testing.T, no, want string) *Order {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		g, err := getOrder(ctx, no)
		if err == nil && g.RenewalState == want {
			return g
		}
		if time.Now().After(deadline) {
			t.Fatalf("单据 %s 的续费状态没有变成 %q: %+v %v", no, want, g, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitCalls 轮询到 name 至少被调用 n 次，最多 5 秒。
func waitCalls(t *testing.T, h *harness, name string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.gpt.count(name) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%s 未达到 %d 次: %d", name, n, h.gpt.count(name))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// presetSucceeded 插一张成功的真实开通单：带会话、账号 ID，续费待处理，结束时间取测试时钟。
func presetSucceeded(t *testing.T, h *harness, email string, set map[string]any) *Order {
	t.Helper()
	cols := map[string]any{"account_id": "acc-1", "submitted_at": h.clock.Now().Unix(), "card_id": testCardID,
		"renewal_state": RenewalPending, "renewal_next_at": h.clock.Now().Unix()}
	for k, v := range set {
		cols[k] = v
	}
	o := presetOrder(t, email, StateSucceeded, cols)
	if _, err := db.DB.Exec(`UPDATE pay_orders SET finished_at = ? WHERE order_no = ?`, h.clock.Now().Unix(), o.OrderNo); err != nil {
		t.Fatal(err)
	}
	return mustOrder(t, o.OrderNo)
}

func boolPtr(b bool) *bool { return &b }

// 真实开通单确认成功后：调用一次取消接口并核实，续费记 cancelled，会话立即清空；单据仍是成功，发码门槛随之通过。
func TestRenewalCancelledAfterSuccess(t *testing.T) {
	h := newRenewalHarness(t)
	h.gpt.accountFn = planFrom(2)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess, text: "Thanks"})
	h.start(t)
	o := h.submit(t, false)
	if g := waitDone(t, o.OrderNo); g.State != StateSucceeded {
		t.Fatalf("应成功: %+v", g)
	}
	g := waitRenewal(t, o.OrderNo, RenewalCancelled)
	if g.State != StateSucceeded || g.ErrorCode != "" || g.SessionSealed != "" || g.HasSession || g.RenewalAttempts != 1 || g.RenewalCheckedAt == nil || g.RenewalNextAt != nil {
		t.Fatalf("关闭续费后的单据不符: %+v", g)
	}
	if n := h.gpt.count("CancelRenewal"); n != 1 {
		t.Fatalf("应调用 1 次取消接口: %d", n)
	}
	if a := h.gpt.lastArgs("CancelRenewal"); a[0] != "acc-1" || a[1] != "at-refreshed" {
		t.Fatalf("取消接口应带账号 ID 与刷新后的 token: %v", a)
	}
	if h.gpt.count("SubscriptionStatus") < 1 {
		t.Fatal("取消之后必须核实")
	}
	waitEvent(t, o.OrderNo, "已关闭续费并核实")
	if ok, err := db.HasRealSucceededPayOrder(ctx, testProvider, db.PayKindSubscription); err != nil || !ok {
		t.Fatalf("关掉续费的成功单应过订阅开通码的门槛: %v %v", ok, err)
	}
	if len(h.alerts.Texts()) != 0 {
		t.Fatalf("不应告警: %v", h.alerts.Texts())
	}
}

// 核实失败：自动重试 3 次（间隔 1、3、10 分钟），之后转 failed 并告警，不再自动调用；单据始终是成功，会话留到 1 小时再清空。
func TestRenewalVerifyFailureRetriesThenFails(t *testing.T) {
	h := newRenewalHarness(t)
	h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{WillRenew: boolPtr(true)}, nil }
	o := presetSucceeded(t, h, "renew-fail@example.com", nil)
	h.start(t)

	for i, delay := range renewalRetryDelays {
		waitCalls(t, h, "CancelRenewal", i+1)
		g := waitRenewalAttempts(t, o.OrderNo, i+1)
		if g.RenewalState != RenewalPending || g.RenewalNextAt == nil || *g.RenewalNextAt != h.clock.Now().Add(delay).Unix() {
			t.Fatalf("第 %d 次失败后应排在 %v 之后重试: %+v", i+1, delay, g)
		}
		// 间隔没到不重试
		time.Sleep(30 * time.Millisecond)
		if n := h.gpt.count("CancelRenewal"); n != i+1 {
			t.Fatalf("间隔没到就重试了: %d", n)
		}
		h.clock.Advance(delay)
	}
	g := waitRenewal(t, o.OrderNo, RenewalFailed)
	if g.State != StateSucceeded || g.Failure != "" || g.ErrorCode != "" || g.RenewalAttempts != 1+renewalAutoRetries || g.RenewalNextAt != nil {
		t.Fatalf("关闭续费失败不能影响单据成功: %+v", g)
	}
	if g.SessionSealed == "" {
		t.Fatal("自动重试用完后会话留到 1 小时，给管理员手动重试")
	}
	if a := waitAlerts(t, h, 1); len(a) != 1 || !strings.Contains(a[0], o.OrderNo) || !strings.Contains(a[0], "关闭续费失败") || strings.Contains(a[0], testAccessToken) {
		t.Fatalf("应告警一次: %v", a)
	}
	waitEvent(t, o.OrderNo, "已自动重试 3 次")
	// 不再自动重试
	h.clock.Advance(30 * time.Minute)
	time.Sleep(50 * time.Millisecond)
	if n := h.gpt.count("CancelRenewal"); n != 1+renewalAutoRetries {
		t.Fatalf("自动尝试最多 1 + 3 次: %d", n)
	}
	// 满 1 小时清空会话，续费状态仍是 failed，单据仍成功
	h.clock.Advance(31 * time.Minute)
	g = waitSessionCleared(t, o.OrderNo)
	if g.RenewalState != RenewalFailed || g.State != StateSucceeded {
		t.Fatalf("清空会话不改其他字段: %+v", g)
	}
	if _, err := h.svc.RetryRenewal(ctx, o.OrderNo, "admin"); !errors.Is(err, ErrRenewalUnavailable) || !strings.Contains(err.Error(), "自行在 ChatGPT 设置里关闭续费") {
		t.Fatalf("会话清空后不能重试: %v", err)
	}
	if ok, _ := db.HasRealSucceededPayOrder(ctx, testProvider, db.PayKindSubscription); ok {
		t.Fatal("续费没关掉的单不能过门槛")
	}
}

func waitRenewalAttempts(t *testing.T, no string, n int) *Order {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		g, err := getOrder(ctx, no)
		if err == nil && g.RenewalAttempts >= n {
			return g
		}
		if time.Now().After(deadline) {
			t.Fatalf("单据 %s 的续费尝试没到 %d 次: %+v %v", no, n, g, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitSessionCleared(t *testing.T, no string) *Order {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		g, err := getOrder(ctx, no)
		if err == nil && g.SessionSealed == "" {
			return g
		}
		if time.Now().After(deadline) {
			t.Fatalf("单据 %s 的会话没有清空: %+v %v", no, g, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 核实依据：订阅接口没有 will_renew 时看账号接口；两边都给不出确定的 false 就算失败。
func TestRenewalVerifySources(t *testing.T) {
	t.Run("订阅接口没字段，账号接口说不续费", func(t *testing.T) {
		h := newRenewalHarness(t)
		h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{}, nil }
		h.gpt.account = chatgpt.Account{AccountID: "acc-1", PlanType: "plus", HasActiveSubscription: true, WillRenew: boolPtr(false)}
		o := presetSucceeded(t, h, "fallback@example.com", nil)
		h.start(t)
		waitRenewal(t, o.OrderNo, RenewalCancelled)
	})
	t.Run("订阅接口出错，账号接口也没字段", func(t *testing.T) {
		h := newRenewalHarness(t)
		h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{}, errors.New("HTTP 500") }
		h.gpt.account = chatgpt.Account{AccountID: "acc-1", PlanType: "plus", HasActiveSubscription: true}
		o := presetSucceeded(t, h, "unknown@example.com", nil)
		h.start(t)
		g := waitRenewalAttempts(t, o.OrderNo, 1)
		if g.RenewalState != RenewalPending {
			t.Fatalf("核实不了按失败处理，排重试: %+v", g)
		}
		waitEvent(t, o.OrderNo, "核实不了续费状态")
	})
	t.Run("订阅接口说仍续费，不看账号接口", func(t *testing.T) {
		h := newRenewalHarness(t)
		h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{WillRenew: boolPtr(true)}, nil }
		h.gpt.account = chatgpt.Account{AccountID: "acc-1", WillRenew: boolPtr(false)}
		o := presetSucceeded(t, h, "conflict@example.com", nil)
		h.start(t)
		if g := waitRenewalAttempts(t, o.OrderNo, 1); g.RenewalState != RenewalPending {
			t.Fatalf("%+v", g)
		}
		if n := h.gpt.count("Account"); n != 0 {
			t.Fatalf("订阅接口给了明确结果就不看账号接口: %d", n)
		}
	})
}

// 重试先读状态：上一次的取消请求其实已经生效时，不再发第二次取消。
func TestRenewalRetryChecksStatusFirst(t *testing.T) {
	h := newRenewalHarness(t)
	h.gpt.cancelFn = func(n int) error { return errors.New("请求超时") }
	h.gpt.subStatusFn = func(n int) (chatgpt.RenewalInfo, error) {
		return chatgpt.RenewalInfo{WillRenew: boolPtr(n == 1)}, nil // 第一次核实仍续费，重试前再读就不续费了
	}
	o := presetSucceeded(t, h, "late@example.com", nil)
	h.start(t)
	waitRenewalAttempts(t, o.OrderNo, 1)
	waitEvent(t, o.OrderNo, "关闭续费请求失败")
	h.clock.Advance(time.Minute)
	g := waitRenewal(t, o.OrderNo, RenewalCancelled)
	if n := h.gpt.count("CancelRenewal"); n != 1 || g.RenewalAttempts != 2 {
		t.Fatalf("重试读到已关闭就不再取消: %d 次, %+v", n, g)
	}
}

// 试运行单（两种）一律不调用取消接口；就算单据被写成待处理也不碰。
func TestRenewalNeverForDryRun(t *testing.T) {
	h := newRenewalHarness(t)
	h.start(t)
	for _, fill := range []bool{false, true} {
		var o *Order
		if fill {
			o = h.submitFillPreview(t)
		} else {
			o = h.submit(t, true)
		}
		if g := waitDone(t, o.OrderNo); g.State != StatePreviewed || g.RenewalState != RenewalNone || g.SessionSealed != "" {
			t.Fatalf("试运行单不涉及续费: %+v", g)
		}
	}
	// dry_run 和 renewal_state=pending 在同一条 UPDATE 里写入：续费协程已在跑，分两步写的话，
	// 它可能在中间把这单当成正常单处理（机器慢时 CI 上出现过）
	dry := presetSucceeded(t, h, "dry@example.com", map[string]any{"dry_run": 1})
	if !dry.DryRun || dry.RenewalState != RenewalPending {
		t.Fatalf("预置的试运行单不符: %+v", dry)
	}
	time.Sleep(50 * time.Millisecond)
	if n := h.gpt.count("CancelRenewal") + h.gpt.count("SubscriptionStatus"); n != 0 {
		t.Fatalf("试运行单不能调用续费接口: %d", n)
	}
	if _, err := h.svc.RetryRenewal(ctx, dry.OrderNo, "admin"); !errors.Is(err, ErrRenewalUnavailable) {
		t.Fatalf("试运行单不能手动重试: %v", err)
	}
}

// 会话最长保留 1 小时：续费仍待处理的转 failed 并告警；配置更长也不行。
// 待人工单的会话留到人工定论（审计 D-2，见 renewal_audit_ob_test.go），这里只核对它不被清。
func TestSessionKeptAtMostOneHour(t *testing.T) {
	h := newRenewalHarness(t)
	far := h.clock.Now().Add(2 * time.Hour).Unix()
	pending := presetSucceeded(t, h, "stuck@example.com", map[string]any{"renewal_next_at": far})
	review := presetOrder(t, "review-keep@example.com", StateNeedsReview, map[string]any{"submitted_at": h.clock.Now().Unix()})
	if _, err := db.DB.Exec(`UPDATE pay_orders SET finished_at = ? WHERE order_no = ?`, h.clock.Now().Unix(), review.OrderNo); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	h.clock.Advance(59 * time.Minute)
	time.Sleep(50 * time.Millisecond)
	if g := mustOrder(t, pending.OrderNo); g.SessionSealed == "" || g.RenewalState != RenewalPending {
		t.Fatalf("不到 1 小时不清: %+v", g)
	}
	if g := mustOrder(t, review.OrderNo); g.SessionSealed == "" {
		t.Fatalf("不到 1 小时不清: %+v", g)
	}
	h.clock.Advance(2 * time.Minute)
	g := waitSessionCleared(t, pending.OrderNo)
	if g.RenewalState != RenewalFailed || g.State != StateSucceeded {
		t.Fatalf("到点仍待处理应转 failed: %+v", g)
	}
	if g := mustOrder(t, review.OrderNo); g.SessionSealed == "" || g.State != StateNeedsReview {
		t.Fatalf("待人工单的会话要留到人工定论: %+v", g)
	}
	if a := waitAlerts(t, h, 1); len(a) != 1 || !strings.Contains(a[0], pending.OrderNo) {
		t.Fatalf("应为待处理的那张告警一次: %v", a)
	}
	if n := h.gpt.count("CancelRenewal"); n != 0 {
		t.Fatalf("没到处理时刻不应调用: %d", n)
	}
	svc := New(Options{Pools: map[string]Pool{testProvider: h.pool}, Timeouts: &Timeouts{SessionKeep: 3 * time.Hour}})
	if svc.Timeouts().SessionKeep != time.Hour {
		t.Fatalf("会话保留不能超过 1 小时: %v", svc.Timeouts().SessionKeep)
	}
}

// 停机超过会话保留时限后重启：同一轮里先清超期会话，续费转 failed，不能拿超期的会话再去调用取消接口。
func TestRenewalSkipsSessionExpiredDuringDowntime(t *testing.T) {
	h := newRenewalHarness(t)
	o := presetSucceeded(t, h, "downtime@example.com", nil)
	old := h.clock.Now().Add(-2 * time.Hour).Unix()
	if _, err := db.DB.Exec(`UPDATE pay_orders SET finished_at = ?, renewal_next_at = ? WHERE order_no = ?`, old, old, o.OrderNo); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	g := waitSessionCleared(t, o.OrderNo)
	if g.State != StateSucceeded || g.RenewalState != RenewalFailed {
		t.Fatalf("超期会话应清空并把续费转 failed: %+v", g)
	}
	time.Sleep(50 * time.Millisecond)
	if n := h.gpt.count("CancelRenewal"); n != 0 {
		t.Fatalf("超期会话不应再调用取消接口: %d", n)
	}
	if a := waitAlerts(t, h, 1); len(a) != 1 || !strings.Contains(a[0], o.OrderNo) {
		t.Fatalf("应告警一次让用户自己关续费: %v", a)
	}
}

// 手动重试：失败单（会话还在）重试成功；已关闭、不是开通单的都不能重试。
func TestRetryRenewal(t *testing.T) {
	h := newRenewalHarness(t)
	failed := presetSucceeded(t, h, "manual@example.com", map[string]any{"renewal_state": RenewalFailed, "renewal_attempts": 4, "renewal_next_at": nil})
	g, err := h.svc.RetryRenewal(ctx, failed.OrderNo, "alice")
	if err != nil || g.RenewalState != RenewalCancelled || g.SessionSealed != "" || g.RenewalAttempts != 5 {
		t.Fatalf("手动重试应关掉: %+v %v", g, err)
	}
	waitEvent(t, failed.OrderNo, "alice 手动重试关闭续费")
	if _, err := h.svc.RetryRenewal(ctx, failed.OrderNo, "alice"); !errors.Is(err, ErrRenewalUnavailable) {
		t.Fatalf("已关闭不能再重试: %v", err)
	}
	// 手动重试失败：记 failed，不排自动重试，不告警（管理员当场看到结果）
	h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{WillRenew: boolPtr(true)}, nil }
	again := presetSucceeded(t, h, "manual2@example.com", map[string]any{"renewal_state": RenewalFailed, "renewal_next_at": nil})
	g, err = h.svc.RetryRenewal(ctx, again.OrderNo, "alice")
	if err != nil || g.RenewalState != RenewalFailed || g.RenewalNextAt != nil || g.SessionSealed == "" {
		t.Fatalf("手动重试失败: %+v %v", g, err)
	}
	if len(h.alerts.Texts()) != 0 {
		t.Fatalf("手动重试失败不告警: %v", h.alerts.Texts())
	}
	credit := presetSucceeded(t, h, "credit@example.com", map[string]any{"kind": KindCredit, "renewal_state": RenewalNone})
	if _, err := h.svc.RetryRenewal(ctx, credit.OrderNo, "alice"); !errors.Is(err, ErrRenewalUnavailable) {
		t.Fatalf("点数单不能重试关闭续费: %v", err)
	}
	if _, err := h.svc.RetryRenewal(ctx, "PO-none", "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

// 人工定论：会话还在的开通单定论成功后照样关闭续费；定论失败清空会话。
func TestResolveTriggersRenewal(t *testing.T) {
	h := newRenewalHarness(t)
	h.start(t)
	ok := presetOrder(t, "resolve-ok@example.com", StateNeedsReview, map[string]any{"submitted_at": h.clock.Now().Unix(), "account_id": "acc-1"})
	if err := h.svc.Resolve(ctx, ok.OrderNo, StateSucceeded, "核对已开通", "alice"); err != nil {
		t.Fatal(err)
	}
	if g := waitRenewal(t, ok.OrderNo, RenewalCancelled); g.ResolvedBy != "alice" || g.SessionSealed != "" {
		t.Fatalf("%+v", g)
	}
	bad := presetOrder(t, "resolve-bad@example.com", StateNeedsReview, map[string]any{"submitted_at": h.clock.Now().Unix(), "account_id": "acc-2"})
	if err := h.svc.Resolve(ctx, bad.OrderNo, StateFailed, "未扣款", "alice"); err != nil {
		t.Fatal(err)
	}
	if g := mustOrder(t, bad.OrderNo); g.SessionSealed != "" || g.RenewalState != RenewalNone || g.Failure != FailureDeclined {
		t.Fatalf("定论失败应清会话: %+v", g)
	}
	if n := h.gpt.count("CancelRenewal"); n != 1 {
		t.Fatalf("只有定论成功的那张关闭续费: %d", n)
	}
}

// 续费待处理但会话已经没了（定论成功与会话到点同时发生）：记 skipped 并告警，不调用接口。
func TestRenewalPendingWithoutSessionSkipped(t *testing.T) {
	h := newRenewalHarness(t)
	o := presetSucceeded(t, h, "gone@example.com", nil)
	if _, err := db.DB.Exec(`UPDATE pay_orders SET session_sealed = '' WHERE order_no = ?`, o.OrderNo); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	waitRenewal(t, o.OrderNo, RenewalSkipped)
	if n := h.gpt.count("CancelRenewal"); n != 0 {
		t.Fatalf("没有会话不能调用: %d", n)
	}
	if a := waitAlerts(t, h, 1); len(a) != 1 || !strings.Contains(a[0], "自行在 ChatGPT 设置里关闭续费") {
		t.Fatalf("%v", a)
	}
}

// ACC2-7：没提交过的待人工单（重启时已取卡、但服务商没有卡池）人工判失败，记 not_submitted 并保留原错误码，不计入熔断。
func TestResolveFailedNotSubmitted(t *testing.T) {
	h := newPayHarness(t, nil)
	var nos []string
	for i := 0; i < breakerThreshold; i++ {
		o := presetOrder(t, "unpooled"+string(rune('a'+i))+"@example.com", StateFunding, map[string]any{"card_id": testCardID, "lease_token": testLeaseToken})
		if err := finish(ctx, o.OrderNo, StateFunding, StateNeedsReview, "error", "转待人工", map[string]any{"error_code": "provider_unavailable", "error_message": "没有卡池"}); err != nil {
			t.Fatal(err)
		}
		if err := h.svc.Resolve(ctx, o.OrderNo, StateFailed, "已到服务商后台核对余额", "alice"); err != nil {
			t.Fatal(err)
		}
		nos = append(nos, o.OrderNo)
	}
	for _, no := range nos {
		g := mustOrder(t, no)
		if g.State != StateFailed || g.Failure != FailureNotSubmitted || g.ErrorCode != "provider_unavailable" || g.ResolvedBy != "alice" {
			t.Fatalf("没提交的单人工判失败应记 not_submitted 并保留错误码: %+v", g)
		}
	}
	st, err := computeBreaker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Paused || st.FillFailureStreak != 0 || st.SubmittedFailureStreak != 0 {
		t.Fatalf("不能计入熔断: %+v", st)
	}
}

// 核实到不续费时，把 /subscriptions 给的到期时间记进单据。
func TestRenewalCancelledStoresAccessUntil(t *testing.T) {
	h := newRenewalHarness(t)
	until := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) {
		return chatgpt.RenewalInfo{WillRenew: boolPtr(false), ActiveUntil: &until}, nil
	}
	o := presetSucceeded(t, h, "until@example.com", nil)
	h.start(t)
	g := waitRenewal(t, o.OrderNo, RenewalCancelled)
	if g.AccessUntil == nil || *g.AccessUntil != until.Unix() {
		t.Fatalf("AccessUntil 应为 %d: %v", until.Unix(), g.AccessUntil)
	}
}

// 接口没给到期时间：仍判 cancelled，AccessUntil 留空。
func TestRenewalCancelledWithoutActiveUntil(t *testing.T) {
	h := newRenewalHarness(t)
	h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) {
		return chatgpt.RenewalInfo{WillRenew: boolPtr(false)}, nil
	}
	o := presetSucceeded(t, h, "nountil@example.com", nil)
	h.start(t)
	g := waitRenewal(t, o.OrderNo, RenewalCancelled)
	if g.AccessUntil != nil {
		t.Fatalf("AccessUntil 应为空: %v", *g.AccessUntil)
	}
}

// /subscriptions 出错、改走账号接口判为关闭：拿不到到期时间，AccessUntil 留空。
func TestRenewalFallbackAccountKeepsNil(t *testing.T) {
	h := newRenewalHarness(t)
	h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{}, errors.New("HTTP 500") }
	h.gpt.account = chatgpt.Account{AccountID: "acc-1", PlanType: "plus", HasActiveSubscription: true, WillRenew: boolPtr(false)}
	o := presetSucceeded(t, h, "fbnil@example.com", nil)
	h.start(t)
	g := waitRenewal(t, o.OrderNo, RenewalCancelled)
	if g.AccessUntil != nil {
		t.Fatalf("AccessUntil 应为空: %v", *g.AccessUntil)
	}
}

// 0 元开通的单关续费后复查账号（设计 2026-11-11 §4.4）。
const (
	zeroRecheckSame   = "0 元开通的单关闭续费后，账号仍是 plus"
	zeroRecheckLost   = "0 元开通的单关闭续费后，账号已不是 plus（现在是 free）"
	zeroRecheckErr    = "0 元开通的单关闭续费后，核实不了账号套餐"
	zeroRecheckAlertF = "付款单 %s 以 0 元开通，关闭续费后账号已不是 plus（现在是 free），可能关续费让优惠当场结束了，请人工处理"
)

// runRenewalFlow 在新的续费测试环境里走完一张真实开通单：页面金额 minor/text、支付页给成功、Account 按 accountFn(h) 返回，
// 等到单据成功、续费记 cancelled，并等这次关续费处理整个结束（复查的事件、告警在续费状态写库之后才写）。
func runRenewalFlow(t *testing.T, minor int64, text string, accountFn func(h *harness) func(int) (chatgpt.Account, error)) (*harness, *Order) {
	t.Helper()
	h := newRenewalHarness(t)
	h.gpt.accountFn = accountFn(h)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess, text: "Thanks"})
	setPage(h, minor, text)
	h.start(t)
	o := h.submit(t, false)
	if g := waitDone(t, o.OrderNo); g.State != StateSucceeded {
		t.Fatalf("应成功: %+v", g)
	}
	waitRenewal(t, o.OrderNo, RenewalCancelled)
	waitRenewalIdle(t, h, o.OrderNo)
	return h, mustOrder(t, o.OrderNo)
}

// waitRenewalIdle 轮询到这张单不在关续费中，最多 5 秒。续费已是 cancelled 的单不会再被领取，所以等到的就是这次处理的结束。
func waitRenewalIdle(t *testing.T, h *harness, no string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.svc.renewalBusy(no) {
		if time.Now().After(deadline) {
			t.Fatalf("单据 %s 的关续费处理没有结束", no)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// accountAfterCancel 数出第一次调用取消续费接口之后 Account 又被调用了几次，也就是关续费阶段查了几次账号。
func accountAfterCancel(h *harness) int {
	n, seen := 0, false
	for _, name := range h.gpt.order() {
		switch {
		case name == "CancelRenewal":
			seen = true
		case seen && name == "Account":
			n++
		}
	}
	return n
}

// plusOnConfirm 让确认成功时账号是 plus（第 1 次预检是 free）；取消续费接口调过之后，Account 改按 after 返回（nil 表示照旧 plus）。
func plusOnConfirm(after func() (chatgpt.Account, error)) func(h *harness) func(int) (chatgpt.Account, error) {
	return func(h *harness) func(int) (chatgpt.Account, error) {
		confirm := planFrom(2)
		return func(n int) (chatgpt.Account, error) {
			if after != nil && h.gpt.count("CancelRenewal") > 0 {
				return after()
			}
			return confirm(n)
		}
	}
}

// zeroRecheckEvents 返回时间线上复查留下的事件（原文以「0 元开通的单关闭续费后」开头）。
func zeroRecheckEvents(t *testing.T, no string) []Event {
	t.Helper()
	var out []Event
	for _, e := range eventStates(t, no) {
		if strings.HasPrefix(e.Message, "0 元开通的单关闭续费后") {
			out = append(out, e)
		}
	}
	return out
}

// assertNoSecrets 确认文字里没有会话、token 和账号邮箱。
func assertNoSecrets(t *testing.T, what string, texts ...string) {
	t.Helper()
	for _, s := range texts {
		for _, sec := range []string{testSession, testAccessToken, testSessionToken, "at-refreshed"} {
			if strings.Contains(s, sec) {
				t.Fatalf("%s 不能带会话或 token %q: %s", what, sec, s)
			}
		}
		if strings.Contains(strings.ToLower(s), "buyer@example.com") {
			t.Fatalf("%s 不能带账号邮箱: %s", what, s)
		}
	}
}

// 0 元开通、关续费后账号仍是 plus：续费记 cancelled，记一条 info，不告警；关续费阶段比同样设置下的付费单多查一次账号。
func TestZeroAmountRenewalRecheckStillPlus(t *testing.T) {
	var paid int
	if !t.Run("同样设置下的付费单", func(t *testing.T) {
		h, _ := runRenewalFlow(t, 112000, "₱1,120.00", plusOnConfirm(nil))
		paid = accountAfterCancel(h)
	}) {
		t.FailNow()
	}
	h, o := runRenewalFlow(t, 0, "₱0.00", plusOnConfirm(nil))
	if o.AmountMinor != 0 || o.AmountText != "₱0.00" || o.State != StateSucceeded || o.RenewalState != RenewalCancelled {
		t.Fatalf("应是成功的 0 元单、续费已关: %+v", o)
	}
	if n := countEventExact(t, o.OrderNo, "info", zeroRecheckSame); n != 1 {
		t.Fatalf("应有一条 info「%s」: %d %+v", zeroRecheckSame, n, eventStates(t, o.OrderNo))
	}
	if ev := zeroRecheckEvents(t, o.OrderNo); len(ev) != 1 {
		t.Fatalf("复查只记一条事件: %+v", ev)
	}
	if a := h.alerts.Texts(); len(a) != 0 {
		t.Fatalf("仍是 plus 不应告警: %v", a)
	}
	if got := accountAfterCancel(h); got != paid+1 {
		t.Fatalf("关续费阶段应比付费单多查一次账号: 0 元单 %d 次，付费单 %d 次", got, paid)
	}
}

// 0 元开通、关续费后账号掉回 free：续费仍记 cancelled、单据仍成功，记一条 warn 并告警一次；都不带会话和邮箱。
func TestZeroAmountRenewalRecheckLostPlan(t *testing.T) {
	h, o := runRenewalFlow(t, 0, "₱0.00", plusOnConfirm(func() (chatgpt.Account, error) {
		return chatgpt.Account{AccountID: "acc-1", PlanType: "free", HasActiveSubscription: false}, nil
	}))
	if o.State != StateSucceeded || o.RenewalState != RenewalCancelled {
		t.Fatalf("复查结果不影响续费状态和单据: %+v", o)
	}
	ev := zeroRecheckEvents(t, o.OrderNo)
	if len(ev) != 1 || ev[0].Level != "warn" || ev[0].Message != zeroRecheckLost {
		t.Fatalf("应有一条 warn「%s」: %+v", zeroRecheckLost, ev)
	}
	a := h.alerts.Texts()
	if want := fmt.Sprintf(zeroRecheckAlertF, o.OrderNo); len(a) != 1 || a[0] != want {
		t.Fatalf("应告警一次「%s」: %v", want, a)
	}
	assertNoSecrets(t, "告警", a...)
	assertNoSecrets(t, "事件", ev[0].Message)
}

// 0 元开通、复查时账号查询出错：续费仍记 cancelled，记一条 warn「核实不了账号套餐」（出错文字已脱敏），不告警。
func TestZeroAmountRenewalRecheckError(t *testing.T) {
	h, o := runRenewalFlow(t, 0, "₱0.00", plusOnConfirm(func() (chatgpt.Account, error) {
		return chatgpt.Account{}, fmt.Errorf("HTTP 502：token %s / %s，账号 %s", "at-refreshed", testSessionToken, "buyer@example.com")
	}))
	if o.State != StateSucceeded || o.RenewalState != RenewalCancelled {
		t.Fatalf("复查出错不影响续费状态和单据: %+v", o)
	}
	ev := zeroRecheckEvents(t, o.OrderNo)
	if len(ev) != 1 || ev[0].Level != "warn" || !strings.HasPrefix(ev[0].Message, zeroRecheckErr) || !strings.Contains(ev[0].Message, "HTTP 502") {
		t.Fatalf("应有一条 warn「%s：…」: %+v", zeroRecheckErr, ev)
	}
	assertNoSecrets(t, "事件", ev[0].Message)
	if a := h.alerts.Texts(); len(a) != 0 {
		t.Fatalf("查询出错不告警: %v", a)
	}
}

// 0 元开通、复查时账号查询成功但没带套餐类型：照样按「不是期望套餐」记 warn 并告警一次，现在的套餐显示「未知」而不是空。
func TestZeroAmountRenewalRecheckEmptyPlanType(t *testing.T) {
	h, o := runRenewalFlow(t, 0, "₱0.00", plusOnConfirm(func() (chatgpt.Account, error) {
		return chatgpt.Account{AccountID: "acc-1", PlanType: "", HasActiveSubscription: false}, nil
	}))
	if o.State != StateSucceeded || o.RenewalState != RenewalCancelled {
		t.Fatalf("复查结果不影响续费状态和单据: %+v", o)
	}
	const wantEvent = "0 元开通的单关闭续费后，账号已不是 plus（现在是 未知）"
	ev := zeroRecheckEvents(t, o.OrderNo)
	if len(ev) != 1 || ev[0].Level != "warn" || ev[0].Message != wantEvent {
		t.Fatalf("应有一条 warn「%s」: %+v", wantEvent, ev)
	}
	a := h.alerts.Texts()
	wantAlert := fmt.Sprintf("付款单 %s 以 0 元开通，关闭续费后账号已不是 plus（现在是 未知），可能关续费让优惠当场结束了，请人工处理", o.OrderNo)
	if len(a) != 1 || a[0] != wantAlert || !strings.Contains(a[0], "（现在是 未知）") {
		t.Fatalf("应告警一次「%s」: %v", wantAlert, a)
	}
}

// 0 元开通、复查出错文字很长：脱敏后截到 errorMessageLimit 个字再写时间线，和同文件其他出错文字一致。
func TestZeroAmountRenewalRecheckErrorTruncated(t *testing.T) {
	long := "HTTP 502：" + strings.Repeat("长", 2*errorMessageLimit)
	_, o := runRenewalFlow(t, 0, "₱0.00", plusOnConfirm(func() (chatgpt.Account, error) {
		return chatgpt.Account{}, errors.New(long)
	}))
	prefix := zeroRecheckErr + "："
	ev := zeroRecheckEvents(t, o.OrderNo)
	if len(ev) != 1 || ev[0].Level != "warn" || !strings.HasPrefix(ev[0].Message, prefix+"HTTP 502") {
		t.Fatalf("应有一条 warn「%s…」: %+v", prefix, ev)
	}
	if n := utf8.RuneCountInString(strings.TrimPrefix(ev[0].Message, prefix)); n != errorMessageLimit {
		t.Fatalf("出错文字应截到 %d 个字，实际 %d 个字", errorMessageLimit, n)
	}
}

// 关续费的重试读到「已经关掉」时也复查：0 元单手动重试，不再发取消请求，照样查一次账号并记事件。
func TestZeroAmountRenewalRecheckOnRetry(t *testing.T) {
	h := newRenewalHarness(t)
	h.gpt.account = accountPlus
	o := presetSucceeded(t, h, "zero-retry@example.com", map[string]any{"amount_minor": 0, "amount_text": "₱0.00",
		"renewal_state": RenewalFailed, "renewal_attempts": 4, "renewal_next_at": nil})
	g, err := h.svc.RetryRenewal(ctx, o.OrderNo, "alice")
	if err != nil || g.RenewalState != RenewalCancelled || g.State != StateSucceeded {
		t.Fatalf("手动重试应关掉: %+v %v", g, err)
	}
	if n := h.gpt.count("CancelRenewal"); n != 0 {
		t.Fatalf("读到已关掉就不再取消: %d", n)
	}
	// 没启动工作协程、订阅接口给了 will_renew：这里的 Account 只可能是复查
	if n := h.gpt.count("Account"); n != 1 {
		t.Fatalf("应复查一次账号: %d", n)
	}
	if n := countEventExact(t, o.OrderNo, "info", zeroRecheckSame); n != 1 {
		t.Fatalf("应有一条 info「%s」: %d %+v", zeroRecheckSame, n, eventStates(t, o.OrderNo))
	}
	if a := h.alerts.Texts(); len(a) != 0 {
		t.Fatalf("仍是 plus 不应告警: %v", a)
	}
}

// 付费单（默认页面 ₱1,120.00）关续费不复查：没有「0 元开通」的事件，不告警，关续费阶段不查账号（订阅接口给了 will_renew，本来就不查）。
func TestRenewalNoRecheckForPaidOrder(t *testing.T) {
	h, o := runRenewalFlow(t, 112000, "₱1,120.00", plusOnConfirm(nil))
	if o.AmountMinor != 112000 || o.State != StateSucceeded || o.RenewalState != RenewalCancelled {
		t.Fatalf("应是成功的付费单、续费已关: %+v", o)
	}
	if n := countEvents(t, o.OrderNo, "0 元开通"); n != 0 {
		t.Fatalf("付费单不应有 0 元开通的事件: %d %+v", n, eventStates(t, o.OrderNo))
	}
	if a := h.alerts.Texts(); len(a) != 0 {
		t.Fatalf("付费单不应告警: %v", a)
	}
	if n := accountAfterCancel(h); n != 0 {
		t.Fatalf("付费单关续费阶段不应多查账号: %d", n)
	}
}
