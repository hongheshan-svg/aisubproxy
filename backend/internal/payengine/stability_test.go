package payengine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// panicSecret 混进 panic 的值里：日志、告警、单据说明都不能带出来。
const panicSecret = "4242424242424242"

func panicNow() { panic("内部出错，卡号 " + panicSecret) }

// assertNoPanicValue 检查单据说明、时间线和告警里没有 panic 的值。
func assertNoPanicValue(t *testing.T, h *harness, no string) {
	t.Helper()
	g := mustOrder(t, no)
	if strings.Contains(g.ErrorMessage, panicSecret) {
		t.Fatalf("单据说明不应带 panic 的值: %q", g.ErrorMessage)
	}
	for _, e := range eventStates(t, no) {
		if strings.Contains(e.Message, panicSecret) {
			t.Fatalf("时间线不应带 panic 的值: %+v", e)
		}
	}
	for _, a := range h.alerts.Texts() {
		if strings.Contains(a, panicSecret) {
			t.Fatalf("告警不应带 panic 的值: %q", a)
		}
	}
}

// waitNotRunning 等名额归还，最多 5 秒。
func waitNotRunning(t *testing.T, h *harness, no string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.svc.isRunning(no) {
		if time.Now().After(deadline) {
			t.Fatalf("单据 %s 的名额没有归还", no)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// F2：单据协程在提交之前 panic（还没开浏览器）：按提交前放弃收尾，记 internal_error，名额归还，后面排队的单照常处理。
func TestHandlePanicBeforeSubmitAbandons(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.accountFn = func(n int) (chatgpt.Account, error) {
		if n == 1 {
			panicNow()
		}
		return accountFree, nil
	}
	h.start(t)
	o := h.submit(t, false)
	g := waitDone(t, o.OrderNo)
	assertNotSubmitted(t, g, "internal_error")
	if g.ErrorMessage != "内部错误，已放弃（没有点支付）" {
		t.Fatalf("说明不符: %q", g.ErrorMessage)
	}
	waitNotRunning(t, h, o.OrderNo)
	assertNoPanicValue(t, h, o.OrderNo)
	a := waitAlerts(t, h, 1)
	if !strings.Contains(a[0], o.OrderNo) || !strings.Contains(a[0], "内部错误") {
		t.Fatalf("应告警并带单号: %v", a)
	}
	// 名额已归还：下一张单照常派发、走完
	next := h.submitAs(t, sessionFor("next@example.com"), true)
	if g := waitDone(t, next.OrderNo); g.State != StatePreviewed {
		t.Fatalf("panic 之后下一张单应照常处理: %+v", g)
	}
}

// F2：注资之后、提交之前 panic，浏览器能证明没点过：放弃收尾，释放租约，关浏览器，记 internal_error。
func TestHandlePanicAfterFundingReleasesLease(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.fill = func(paybrowser.Card, paybrowser.Billing) error {
		panicNow()
		return nil
	}
	h.br.lockSubmit = func() bool { return false }
	h.start(t)
	o := h.submit(t, false)
	g := waitDone(t, o.OrderNo)
	assertNotSubmitted(t, g, "internal_error")
	if g.SubmittedAt != nil {
		t.Fatalf("不应写 submitted_at: %+v", g)
	}
	assertReleases(t, h, cardpool.ResultAborted)
	if b := theBrowser(t, h); b.count("Close") == 0 {
		t.Fatal("浏览器应关闭")
	}
	assertSubmitCount(t, h, 0)
	waitNotRunning(t, h, o.OrderNo)
	assertNoPanicValue(t, h, o.OrderNo)
}

// F2：浏览器不能证明没点过（LockSubmit 返回 true）：没写 submitted_at 也一律转确认，不能当成提交前放弃。
func TestHandlePanicUnprovenClickGoesToVerify(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.fill = func(paybrowser.Card, paybrowser.Billing) error {
		panicNow()
		return nil
	}
	h.start(t)
	o := h.submit(t, false)
	g := waitState(t, o.OrderNo, StateVerifying)
	if g.PageOutcome != "unknown" || g.Failure != "" {
		t.Fatalf("应按结果不明转确认: %+v", g)
	}
	if len(h.pool.released()) != 0 {
		t.Fatal("转确认不应释放租约，由确认阶段定论后释放")
	}
	waitNotRunning(t, h, o.OrderNo)
	assertNoPanicValue(t, h, o.OrderNo)
}

// F2：写过 submitted_at 之后 panic：转确认，绝不当成提交前放弃，也不再点支付。
func TestHandlePanicAfterSubmitGoesToVerify(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.waitOutcome = func(context.Context, int) (paybrowser.Outcome, string, error) {
		panicNow()
		return paybrowser.OutcomeUnknown, "", nil
	}
	h.br.lockSubmit = func() bool { return false } // 就算浏览器说没点过，写过 submitted_at 也转确认
	h.start(t)
	o := h.submit(t, false)
	g := waitState(t, o.OrderNo, StateVerifying)
	if g.SubmittedAt == nil || g.PageOutcome != "unknown" {
		t.Fatalf("应保留提交时间并转确认: %+v", g)
	}
	assertSubmitCount(t, h, 1)
	if b := theBrowser(t, h); b.count("Close") == 0 {
		t.Fatal("浏览器应关闭")
	}
	waitNotRunning(t, h, o.OrderNo)
	assertNoPanicValue(t, h, o.OrderNo)
}

// F2：确认阶段检查一张单时 panic：这张单转人工（internal_error），释放租约，下一轮不再处理它；别的确认单照常定论。
func TestVerifyPanicEscalates(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeUnknown})
	var armed atomic.Bool
	var badCalls atomic.Int32
	h.gpt.accountSess = func(s chatgpt.Session, _ int) (chatgpt.Account, error) {
		if armed.Load() && strings.EqualFold(s.Email, "buyer@example.com") {
			badCalls.Add(1)
			panicNow()
		}
		return accountFree, nil
	}
	// 预置单在启动之前写好：确认协程跑起来以后再插，可能在写入 verify_until 之前就被拿去定论
	now := h.clock.Now().Unix()
	good := presetOrder(t, "good@example.com", StateVerifying, map[string]any{
		"submitted_at": now, "card_id": "card-g", "lease_token": "tok-g",
		"page_outcome": "unknown", "verify_until": now + 600, "next_check_at": now,
	})
	h.start(t)
	bad := h.submit(t, false)
	waitState(t, bad.OrderNo, StateVerifying)
	armed.Store(true) // 之后坏单每次查账号都 panic
	g := waitDone(t, bad.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "internal_error" || g.Failure != "" {
		t.Fatalf("应转人工并记 internal_error: %+v", g)
	}
	if !strings.HasPrefix(g.ErrorMessage, "确认时内部错误，请人工核对账号是否已开通") || strings.Contains(g.ErrorMessage, "账号未开通") {
		t.Fatalf("说明不符: %q", g.ErrorMessage)
	}
	rel := assertReleases(t, h, cardpool.ResultAborted)
	if rel[0].Lease.OrderRef != bad.OrderNo {
		t.Fatalf("应释放坏单的租约: %+v", rel)
	}
	assertNoPanicValue(t, h, bad.OrderNo)
	// 好单照常检查、到期定论；坏单已转人工，下一轮不再处理
	waitAccountCalls(t, h, h.gpt.count("Account")+3)
	h.clock.Advance(h.timeouts.Verify + time.Second)
	if g := waitDone(t, good.OrderNo); g.State != StateNeedsReview || g.ErrorCode != "verify_timeout" {
		t.Fatalf("别的确认单应照常定论: %+v", g)
	}
	if n := badCalls.Load(); n != 1 {
		t.Fatalf("转人工之后不应再处理坏单: %d", n)
	}
}

// F2：关闭续费时 panic：这张单的续费记 failed 并告警（人工可在后台重试），续费协程不退出，下一轮不再拿到它。
func TestRenewalPanicMarksFailed(t *testing.T) {
	h := newRenewalHarness(t)
	var calls atomic.Int32
	h.gpt.cancelFn = func(int) error {
		calls.Add(1)
		panicNow()
		return nil
	}
	bad := presetSucceeded(t, h, "bad@example.com", nil)
	h.start(t)
	g := waitRenewal(t, bad.OrderNo, RenewalFailed)
	if g.State != StateSucceeded || g.SessionSealed == "" {
		t.Fatalf("单据仍是成功，会话留给人工重试: %+v", g)
	}
	waitEvent(t, bad.OrderNo, "关闭续费时内部错误")
	assertNoPanicValue(t, h, bad.OrderNo)
	if a := waitAlerts(t, h, 1); !strings.Contains(a[0], bad.OrderNo) || !strings.Contains(a[0], "重试关闭续费") {
		t.Fatalf("应告警并提示人工重试: %v", a)
	}
	// 续费协程照常运行：后来的单还会被处理，坏单不再被拿到
	other := presetSucceeded(t, h, "other@example.com", nil)
	waitRenewal(t, other.OrderNo, RenewalFailed)
	if n := calls.Load(); n != 2 {
		t.Fatalf("每张单只应处理一次: %d", n)
	}
}

// S4：启动浏览器卡住（工厂一直阻塞、尊重 ctx）：到时限按 browser_failed 放弃并告警，名额归还；还没取卡，不涉及资金。
func TestBrowserLaunchTimeout(t *testing.T) {
	h := newPayHarness(t, func(tm *Timeouts) { tm.Launch = 100 * time.Millisecond })
	h.br.blockNew.Store(true)
	h.start(t)
	o := h.submit(t, false)
	g := waitDone(t, o.OrderNo)
	assertNotSubmitted(t, g, "browser_failed")
	if !strings.Contains(g.ErrorMessage, "没有启动完成") {
		t.Fatalf("说明应写明启动超时: %q", g.ErrorMessage)
	}
	waitNotRunning(t, h, o.OrderNo)
	if a := waitAlerts(t, h, 1); !strings.Contains(a[0], o.OrderNo) || !strings.Contains(a[0], "浏览器") {
		t.Fatalf("应告警并带单号: %v", a)
	}
	if n := h.pool.count("Acquire"); n != 0 {
		t.Fatalf("启动浏览器失败时还没取卡: Acquire %d 次", n)
	}
	assertReleases(t, h)
	// 名额已归还：下一张单照常处理
	h.br.blockNew.Store(false)
	next := h.submitAs(t, sessionFor("next@example.com"), true)
	if g := waitDone(t, next.OrderNo); g.State != StatePreviewed {
		t.Fatalf("超时之后下一张单应照常处理: %+v", g)
	}
}

// 修复第 1 轮：接管已提交单时一单 panic，只跳过这一单（记日志），后面的单照常接管，整轮不中断。
func TestAdoptSubmittedIsolatesPanic(t *testing.T) {
	h := newPayHarness(t, nil)
	now := h.clock.Now().Unix()
	bad := presetOrder(t, "bad@example.com", StateSubmitted, map[string]any{"submitted_at": now})
	good := presetOrder(t, "good@example.com", StateRequires3DS, map[string]any{"submitted_at": now})
	var calls atomic.Int32
	clock := h.svc.opts.Now
	h.svc.opts.Now = func() time.Time {
		if calls.Add(1) == 1 { // 第一单转确认时读时钟就 panic
			panicNow()
		}
		return clock()
	}
	if safego.Run("测试接管", func() { h.svc.adoptSubmitted(ctx) }) {
		t.Fatal("一单 panic 不应让整轮接管中断")
	}
	if g := mustOrder(t, bad.OrderNo); g.State != StateSubmitted {
		t.Fatalf("panic 的那单应原样留着，下一轮再试: %+v", g)
	}
	if g := mustOrder(t, good.OrderNo); g.State != StateVerifying {
		t.Fatalf("后面的单应照常接管: %+v", g)
	}
	// 下一轮那单照常接管
	h.svc.adoptSubmitted(ctx)
	if g := mustOrder(t, bad.OrderNo); g.State != StateVerifying {
		t.Fatalf("下一轮应接管: %+v", g)
	}
}

// 修复第 1 轮：扫描停滞单时一单 panic，只跳过这一单（记日志），后面的单照常收尾。
func TestSweepStaleIsolatesPanic(t *testing.T) {
	h := newPayHarness(t, nil)
	bad := presetStale(t, "bad@example.com", StateFilling, 10*time.Minute, map[string]any{"card_id": "card-b", "lease_token": "tok-b"})
	good := presetStale(t, "good@example.com", StateFunding, 10*time.Minute, map[string]any{"card_id": "card-g", "lease_token": "tok-g"})
	h.pool.release = func(l *cardpool.Lease, _ cardpool.Outcome) error {
		if l.OrderRef == bad.OrderNo {
			panicNow()
		}
		return nil
	}
	h.svc.recovered.Store(true)
	if safego.Run("测试扫描", func() { h.svc.sweepStale(ctx) }) {
		t.Fatal("一单 panic 不应让整轮扫描中断")
	}
	if g := mustOrder(t, bad.OrderNo); g.State != StateFilling {
		t.Fatalf("panic 的那单应原样留着，下一轮再试: %+v", g)
	}
	assertNotSubmitted(t, mustOrder(t, good.OrderNo), "stale_order")
	assertNoPanicValue(t, h, bad.OrderNo)
}

// 修复第 1 轮：单据协程 panic 后先收尾、再告警；告警函数自己 panic，单据也照样收尾，名额归还。
func TestHandlePanicSettlesEvenIfAlertPanics(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.fill = func(paybrowser.Card, paybrowser.Billing) error {
		panicNow()
		return nil
	}
	h.br.lockSubmit = func() bool { return false }
	var alertCalls atomic.Int32
	h.svc.opts.Alert = func(string) {
		alertCalls.Add(1)
		panic("告警坏了")
	}
	h.start(t)
	o := h.submit(t, false)
	g := waitDone(t, o.OrderNo)
	assertNotSubmitted(t, g, "internal_error")
	assertReleases(t, h, cardpool.ResultAborted)
	waitNotRunning(t, h, o.OrderNo)
	if b := theBrowser(t, h); b.count("Close") == 0 {
		t.Fatal("浏览器应关闭")
	}
	if alertCalls.Load() == 0 {
		t.Fatal("收尾之后仍应尝试告警")
	}
}

// panicAlerts 返回告警里提到单号 no、说的是内部错误（panic）的那几条。
func panicAlerts(h *harness, no string) []string {
	var out []string
	for _, a := range h.alerts.Texts() {
		if strings.Contains(a, no) && strings.Contains(a, "panic") {
			out = append(out, a)
		}
	}
	return out
}

// 最终修复 B10：接管已提交单时 panic，除日志外还要告警：确定性 panic 的单会一直卡着，没人知道。
// 同一单、同一阶段按北京日去重（每轮都会 panic，不能刷屏），第二天再提醒一次。告警不带邮箱、链接和 panic 的值。
func TestAdoptPanicAlertsDaily(t *testing.T) {
	h := newPayHarness(t, nil)
	bad := presetOrder(t, "bad@example.com", StateSubmitted, map[string]any{"submitted_at": h.clock.Now().Unix()})
	var armed atomic.Bool
	clock := h.svc.opts.Now
	h.svc.opts.Now = func() time.Time {
		if armed.CompareAndSwap(true, false) { // 只让接管时第一次读时钟 panic
			panicNow()
		}
		return clock()
	}
	round := func() {
		armed.Store(true)
		h.svc.adoptSubmitted(ctx)
	}
	round()
	got := panicAlerts(h, bad.OrderNo)
	if len(got) != 1 || !strings.Contains(got[0], "接管") {
		t.Fatalf("接管 panic 应告警一次: %q", got)
	}
	round()
	if got := panicAlerts(h, bad.OrderNo); len(got) != 1 {
		t.Fatalf("同一天同一单不应重复告警: %q", got)
	}
	h.clock.Advance(24 * time.Hour)
	round()
	if got := panicAlerts(h, bad.OrderNo); len(got) != 2 {
		t.Fatalf("第二天应再提醒一次: %q", got)
	}
	if g := mustOrder(t, bad.OrderNo); g.State != StateSubmitted {
		t.Fatalf("panic 的那单应原样留着: %+v", g)
	}
	assertNoPanicValue(t, h, bad.OrderNo)
	for _, a := range h.alerts.Texts() {
		if strings.Contains(a, "@") || strings.Contains(a, "http") {
			t.Fatalf("告警不应带邮箱或链接: %q", a)
		}
	}
}

// 最终修复 B10：扫描停滞单时 panic 同样告警，按单号 + 阶段每天一次；和接管的告警互不吞掉。
func TestSweepPanicAlertsDaily(t *testing.T) {
	h := newPayHarness(t, nil)
	bad := presetStale(t, "bad@example.com", StateFilling, 10*time.Minute, map[string]any{"card_id": "card-b", "lease_token": "tok-b"})
	h.pool.release = func(l *cardpool.Lease, _ cardpool.Outcome) error {
		if l.OrderRef == bad.OrderNo {
			panicNow()
		}
		return nil
	}
	h.svc.recovered.Store(true)
	h.svc.sweepStale(ctx)
	h.svc.sweepStale(ctx)
	got := panicAlerts(h, bad.OrderNo)
	if len(got) != 1 || !strings.Contains(got[0], "停滞") {
		t.Fatalf("扫描 panic 同一天应只告警一次: %q", got)
	}
	h.clock.Advance(24 * time.Hour)
	h.svc.sweepStale(ctx)
	if got := panicAlerts(h, bad.OrderNo); len(got) != 2 {
		t.Fatalf("第二天应再提醒一次: %q", got)
	}
	assertNoPanicValue(t, h, bad.OrderNo)
	for _, a := range h.alerts.Texts() {
		if strings.Contains(a, "@") || strings.Contains(a, "http") {
			t.Fatalf("告警不应带邮箱或链接: %q", a)
		}
	}
}
