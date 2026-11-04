package payengine

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// S3：确认期间查账号一直出错直到到期：转人工记 verify_unreachable，说明带最后一次错误，不能说成「账号未开通」。
func TestVerifyUnreachableWhenAccountAlwaysErrors(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeUnknown})
	var broken atomic.Bool
	h.gpt.accountFn = func(int) (chatgpt.Account, error) {
		if broken.Load() {
			return chatgpt.Account{}, errors.New("代理连接失败 buyer@example.com") // 错误文字里夹带账号邮箱
		}
		return accountFree, nil
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	broken.Store(true)
	waitEvent(t, o.OrderNo, "代理连接失败")
	h.clock.Advance(h.timeouts.Verify + time.Second)
	g := waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "verify_unreachable" || g.Failure != "" {
		t.Fatalf("应转人工并记 verify_unreachable: %+v", g)
	}
	const head = "确认超时：一直查不到账号状态（最后一次错误："
	if !strings.HasPrefix(g.ErrorMessage, head) || !strings.Contains(g.ErrorMessage, "代理连接失败") ||
		!strings.Contains(g.ErrorMessage, "），请人工核对是否已开通") {
		t.Fatalf("说明不符: %q", g.ErrorMessage)
	}
	if strings.Contains(g.ErrorMessage, "账号未开通") || strings.Contains(g.ErrorMessage, "buyer@example.com") {
		t.Fatalf("说明不能说成未开通，错误要脱敏: %q", g.ErrorMessage)
	}
	a := waitAlertContaining(t, h, o.OrderNo)
	if !strings.Contains(strings.Join(a, "\n"), "一直查不到账号状态") {
		t.Fatalf("告警应写明查不到账号状态: %v", a)
	}
}

// S3：查到了「未开通」直到到期：照旧 verify_timeout。中途出过错、最后一次查到了，也按查到的算。
func TestVerifyTimeoutWhenAccountNotOpened(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeUnknown})
	var n atomic.Int32
	h.gpt.accountFn = func(int) (chatgpt.Account, error) {
		if n.Add(1) == 3 {
			return chatgpt.Account{}, errors.New("ChatGPT 返回 HTTP 502")
		}
		return accountFree, nil
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	waitAccountCalls(t, h, 4)
	h.clock.Advance(h.timeouts.Verify + time.Second)
	g := waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "verify_timeout" ||
		!strings.HasPrefix(g.ErrorMessage, "确认超时：账号未开通，支付页也没有明确拒绝") {
		t.Fatalf("查到未开通应照旧 verify_timeout: %+v", g)
	}
}

// waitNextCheck 轮询到单据的 next_check_at 等于 want，最多 5 秒。
func waitNextCheck(t *testing.T, no string, want int64, step string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		g := mustOrder(t, no)
		if g.NextCheckAt != nil && *g.NextCheckAt == want {
			return
		}
		if time.Now().After(deadline) {
			var got any = nil
			if g.NextCheckAt != nil {
				got = *g.NextCheckAt
			}
			t.Fatalf("%s：下次检查时间应为 %d，得到 %v", step, want, got)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// S3：查账号出错时下次检查按 15s → 30s → 60s 退避，封顶 60s；429 带 Retry-After 时按它推迟；成功一次恢复 15s；都不晚于到期时间。
func TestVerifyBackoffOnAccountErrors(t *testing.T) {
	h := newPayHarness(t, func(tm *Timeouts) { tm.VerifyEvery = 15 * time.Second })
	errNet := errors.New("代理连接失败")
	steps := []error{errNet, errNet, errNet, &chatgpt.HTTPError{Status: 429, RetryAfter: 90}, nil, errNet, errNet, errNet}
	var calls atomic.Int32
	h.gpt.accountFn = func(int) (chatgpt.Account, error) {
		i := int(calls.Add(1)) - 1
		if i >= len(steps) {
			i = len(steps) - 1
		}
		if steps[i] != nil {
			return chatgpt.Account{}, steps[i]
		}
		return accountFree, nil
	}
	t0 := h.clock.Now().Unix()
	until := t0 + 235
	o := presetOrder(t, "backoff@example.com", StateVerifying, map[string]any{
		"submitted_at": t0, "card_id": testCardID, "lease_token": testLeaseToken,
		"page_outcome": "unknown", "verify_until": until, "next_check_at": t0,
	})
	h.start(t)

	waitNextCheck(t, o.OrderNo, t0+15, "第 1 次出错") // 15s
	h.clock.Advance(15 * time.Second)
	waitNextCheck(t, o.OrderNo, t0+15+30, "第 2 次出错") // 30s
	h.clock.Advance(30 * time.Second)
	waitNextCheck(t, o.OrderNo, t0+45+60, "第 3 次出错") // 60s
	h.clock.Advance(60 * time.Second)
	waitNextCheck(t, o.OrderNo, t0+105+90, "429 Retry-After 90") // 按 Retry-After，超过封顶也照它
	h.clock.Advance(90 * time.Second)
	waitNextCheck(t, o.OrderNo, t0+195+15, "查到未开通") // 成功一次恢复 15s
	h.clock.Advance(15 * time.Second)
	waitNextCheck(t, o.OrderNo, t0+210+15, "恢复后再出错") // 重新从 15s 起
	h.clock.Advance(15 * time.Second)
	waitNextCheck(t, o.OrderNo, until, "退避不晚于到期时间") // 30s 会超过到期时间，按到期时间
	if c := calls.Load(); c != 7 {
		t.Fatalf("到期之前应只查 7 次: %d", c)
	}
	h.clock.Advance(10 * time.Second)
	g := waitDone(t, o.OrderNo)
	if g.ErrorCode != "verify_unreachable" {
		t.Fatalf("到期时最后一次出错应记 verify_unreachable: %+v", g)
	}
}

// S3：退避间隔的计算：不晚于到期时间，Retry-After 只在 429 时生效。
func TestVerifyDelay(t *testing.T) {
	every := 15 * time.Second
	cases := []struct {
		fails int
		err   error
		want  time.Duration
	}{
		{0, nil, 15 * time.Second},
		{1, errors.New("x"), 15 * time.Second},
		{2, errors.New("x"), 30 * time.Second},
		{3, errors.New("x"), 60 * time.Second},
		{9, errors.New("x"), 60 * time.Second},
		{1, &chatgpt.HTTPError{Status: 429, RetryAfter: 45}, 45 * time.Second},
		{3, &chatgpt.HTTPError{Status: 429, RetryAfter: 5}, 60 * time.Second},
		{1, &chatgpt.HTTPError{Status: 503, RetryAfter: 45}, 15 * time.Second},
	}
	for _, c := range cases {
		if got := verifyDelay(every, c.fails, c.err); got != c.want {
			t.Fatalf("fails=%d err=%v：应推迟 %v，得到 %v", c.fails, c.err, c.want, got)
		}
	}
	if got := verifyDelay(0, 3, errors.New("x")); got != 0 {
		t.Fatalf("测试里 VerifyEvery=0 时不应推迟: %v", got)
	}
}
