package payengine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// webhookCase 是「最近一次收到 MarsWallex Webhook」的几种情况；quiet 表示最近 7 天一条都没收到。
type webhookCase struct {
	name  string
	last  func(h *harness) (time.Time, bool, error)
	quiet bool
}

func webhookCases() []webhookCase {
	return []webhookCase{
		{"从未收到", func(*harness) (time.Time, bool, error) { return time.Time{}, false, nil }, true},
		{"8 天前收到过", func(h *harness) (time.Time, bool, error) { return h.clock.Now().Add(-8 * 24 * time.Hour), true, nil }, true},
		{"1 天前收到过", func(h *harness) (time.Time, bool, error) { return h.clock.Now().Add(-24 * time.Hour), true, nil }, false},
		// 查不到按不确定处理：不告警、不改说明
		{"查询出错", func(*harness) (time.Time, bool, error) { return time.Time{}, false, errors.New("数据库不可用") }, false},
	}
}

func no3DSWebhookAlert(no string) string {
	return "付款单 " + no + " 需要 3DS 验证码，但最近 7 天没收到过 MarsWallex Webhook，验证码很可能收不到"
}

// 进入 requires_3ds 时，最近 7 天没收到过 Webhook 就告警一次；付款流程不受影响。
func Test3DSAlertWhenNoRecentWebhook(t *testing.T) {
	for _, c := range webhookCases() {
		t.Run(c.name, func(t *testing.T) {
			h := newPayHarness(t, nil)
			h.gpt.accountFn = planFrom(2)
			h.pool.lastWebhook = func() (time.Time, bool, error) { return c.last(h) }
			h.pool.waitOTP = func(context.Context, string, time.Time) (string, error) { return "123456", nil }
			h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeChallenge}, outcomeStep{out: paybrowser.OutcomeSuccess, text: "ok"})
			h.start(t)
			o := h.submit(t, false)
			g := waitDone(t, o.OrderNo)
			if g.State != StateSucceeded {
				t.Fatalf("付款流程不应受影响: %+v", g)
			}
			got := h.alerts.Texts()
			if c.quiet {
				if len(got) != 1 || got[0] != no3DSWebhookAlert(o.OrderNo) {
					t.Fatalf("应告警一次 %q: %v", no3DSWebhookAlert(o.OrderNo), got)
				}
			} else if len(got) != 0 {
				t.Fatalf("不应告警: %v", got)
			}
			if h.pool.count("WaitOTP") != 1 || theBrowser(t, h).count("EnterOTP") != 1 {
				t.Fatalf("验证码照常等待并填写 1 次")
			}
		})
	}
}

// 没有 3DS 的付款不查 Webhook 时间。
func TestNoChallengeSkipsWebhookCheck(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.accountFn = planFrom(2)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess, text: "ok"})
	h.start(t)
	o := h.submit(t, false)
	if g := waitDone(t, o.OrderNo); g.State != StateSucceeded {
		t.Fatalf("应成功: %+v", g)
	}
	if n := h.pool.count("LastWebhookAt"); n != 0 {
		t.Fatalf("没有 3DS 不应查 Webhook 时间: %d", n)
	}
}

// 等验证码超时：最近 7 天没收到过 Webhook 时说明改为带原因的文字；超时之后的处理（转确认、到期转人工）不变。
func TestOTPTimeoutTextNoRecentWebhook(t *testing.T) {
	const plain = "等待验证码超时"
	const withHint = "等待验证码超时（最近 7 天没收到过 MarsWallex Webhook，可能没配置通知地址）"
	for _, c := range webhookCases() {
		t.Run(c.name, func(t *testing.T) {
			h := newPayHarness(t, func(to *Timeouts) { to.OTP = 20 * time.Millisecond })
			h.pool.lastWebhook = func() (time.Time, bool, error) { return c.last(h) }
			h.pool.waitOTP = func(c context.Context, _ string, _ time.Time) (string, error) {
				<-c.Done()
				return "", c.Err()
			}
			h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeChallenge})
			h.start(t)
			o := h.submit(t, false)
			want := plain
			if c.quiet {
				want = withHint
			}

			g := waitState(t, o.OrderNo, StateVerifying)
			if g.PageOutcome != "unknown" || g.FinishedAt != nil || g.ErrorMessage != want {
				t.Fatalf("验证码超时应转确认，说明为 %q: %+v", want, g)
			}
			found := false
			for _, e := range eventStates(t, o.OrderNo) {
				if e.Message == "提交后出错："+want {
					found = true
				}
			}
			if !found {
				t.Fatalf("时间线应记下 %q: %+v", "提交后出错："+want, eventStates(t, o.OrderNo))
			}
			if b := theBrowser(t, h); b.count("Close") != 1 || b.count("EnterOTP") != 0 {
				t.Fatalf("应关闭浏览器且未填验证码: close=%d enter=%d", b.count("Close"), b.count("EnterOTP"))
			}
			waitAccountCalls(t, h, 2)
			if got := mustOrder(t, o.OrderNo); got.State != StateVerifying || len(h.pool.released()) != 0 {
				t.Fatalf("Verify 时长内不应定论: %+v", got)
			}

			h.clock.Advance(h.timeouts.Verify + time.Second)
			g = waitDone(t, o.OrderNo)
			if g.State != StateNeedsReview || g.ErrorCode != "verify_timeout" || g.SessionSealed == "" || g.RenewalState != RenewalNone {
				t.Fatalf("应转待人工: %+v", g)
			}
			wantAlerts := 1
			if c.quiet {
				wantAlerts = 2
			}
			a := waitAlerts(t, h, wantAlerts)
			if len(a) != wantAlerts || !strings.Contains(a[len(a)-1], o.OrderNo) || !strings.Contains(a[len(a)-1], "待人工") {
				t.Fatalf("告警不符: %v", a)
			}
			if c.quiet && a[0] != no3DSWebhookAlert(o.OrderNo) {
				t.Fatalf("第一条应是 3DS 无 Webhook 告警: %v", a)
			}
			assertReleases(t, h, cardpool.ResultAborted)
			assertSubmitCount(t, h, 1)
		})
	}
}
