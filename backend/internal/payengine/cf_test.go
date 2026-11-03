package payengine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
)

func blockedErr() error {
	return fmt.Errorf("%w（HTTP 403）", chatgpt.ErrBlocked)
}

// PF-4：Cloudflare 挑战在预检、建支付会话阶段不能报成「会话已失效」：错误码 cf_challenge，并告警。
func TestCloudflareBlockedIsNotSessionInvalid(t *testing.T) {
	cases := map[string]func(*fakeChatGPT){
		"刷新被拦":  func(g *fakeChatGPT) { g.refreshErr = blockedErr() },
		"查账号被拦": func(g *fakeChatGPT) { g.accountErr = blockedErr() },
		"建会话被拦": func(g *fakeChatGPT) { g.checkoutErr = blockedErr() },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			set(h.gpt)
			h.start(t)
			g := waitDone(t, h.submit(t, false).OrderNo)
			assertNotSubmitted(t, g, "cf_challenge")
			if strings.Contains(g.ErrorMessage, "会话已失效") || !strings.Contains(g.ErrorMessage, "Cloudflare") {
				t.Fatalf("提示应指向代理出口 IP，不是会话失效: %q", g.ErrorMessage)
			}
			a := h.alerts.Texts()
			if len(a) != 1 || !strings.Contains(a[0], "Cloudflare") || !strings.Contains(a[0], g.OrderNo) {
				t.Fatalf("应告警 1 次并带单号: %v", a)
			}
			if len(h.br.browsers()) != 0 || h.pool.total() != 0 {
				t.Fatal("预检阶段被拦不应启动浏览器或碰卡池")
			}
		})
	}
}

// PF-4：确认阶段查账号被 Cloudflare 拦下，不是「账号未开通」的证据。页面显示被拒、授权交易也干净，
// 也不能定成 declined：只记事件并告警，一直等到到期转人工。
func TestCloudflareBlockedInVerifyNeverConcludesDeclined(t *testing.T) {
	h := declinedHarness(t)
	h.gpt.accountFn = func(n int) (chatgpt.Account, error) {
		if n == 1 {
			return accountFree, nil // 预检
		}
		return chatgpt.Account{}, blockedErr()
	}
	h.start(t)
	o := h.submit(t, false)
	waitState(t, o.OrderNo, StateVerifying)
	waitAccountCalls(t, h, 3)

	h.clock.Advance(h.timeouts.DeclineGrace + time.Second) // 被拒的宽限已过：账号能查就会定论失败
	waitAccountCalls(t, h, h.gpt.count("Account")+2)
	if got := mustOrder(t, o.OrderNo); got.State != StateVerifying || len(h.pool.released()) != 0 {
		t.Fatalf("查不了账号时不能定论: %+v", got)
	}

	h.clock.Advance(h.timeouts.Verify + time.Second)
	g := waitDone(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "evidence_unavailable" || g.Failure != "" {
		t.Fatalf("应转待人工，不能定 declined: %+v", g)
	}
	assertReleases(t, h, cardpool.ResultAborted)
	waitEvent(t, o.OrderNo, "Cloudflare")
	cf := 0
	for _, a := range h.alerts.Texts() {
		if strings.Contains(a, "Cloudflare") {
			cf++
		}
	}
	if cf != 1 {
		t.Fatalf("同样的拦截只告警 1 次（不刷屏）: %v", h.alerts.Texts())
	}
}
