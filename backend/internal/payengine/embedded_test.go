package payengine

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

const testEmbeddedURL = "https://chatgpt.com/checkout/openai_llc/cs_live_abc"

// embeddedCheckout 是建会话返回的内嵌结账页会话：没有链接，tag 为 custom_checkout_session，链接由引擎拼。
func embeddedCheckout() chatgpt.Checkout {
	return chatgpt.Checkout{SessionID: "cs_live_abc", Tag: chatgpt.CheckoutTagCustom, ProcessorEntity: "openai_llc"}
}

// openedKind 读浏览器最近一次 Open 收到的页面类型（假浏览器在工厂锁里写它）。
func openedKind(b *fakeBrowser) paybrowser.PageKind {
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	return b.openKind
}

// assertNoSessionID 断言错误说明和时间线都没有带出会话号。
func assertNoSessionID(t *testing.T, o *Order, sessionID string) {
	t.Helper()
	if strings.Contains(o.ErrorMessage, sessionID) {
		t.Fatalf("错误说明带出了会话号: %s", o.ErrorMessage)
	}
	for _, e := range eventStates(t, o.OrderNo) {
		if strings.Contains(e.Message, sessionID) {
			t.Fatalf("时间线带出了会话号: %s", e.Message)
		}
	}
}

// 内嵌会话：用 processor_entity 和会话号拼出 chatgpt.com 链接，按内嵌页打开。
func TestEmbeddedSessionOpensChatGPTPage(t *testing.T) {
	cases := map[string]chatgpt.Checkout{
		"内嵌会话":      embeddedCheckout(),
		"tag 带首尾空白": {SessionID: "cs_live_abc", Tag: " " + chatgpt.CheckoutTagCustom + " ", ProcessorEntity: "openai_llc"},
	}
	for name, co := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.gpt.checkout = co
			h.start(t)
			o := waitDone(t, h.submit(t, true).OrderNo)
			if o.State != StatePreviewed || o.CheckoutURL != testEmbeddedURL || o.CheckoutSessionID != "cs_live_abc" {
				t.Fatalf("应打开内嵌页并试运行完成: %+v", o)
			}
			b := theBrowser(t, h)
			if k := openedKind(b); k != paybrowser.PageEmbedded {
				t.Fatalf("应按内嵌页打开: %v", k)
			}
			if u := b.lastArgs("Open")[0]; u != testEmbeddedURL {
				t.Fatalf("打开的链接不符: %v", u)
			}
		})
	}
}

// 会话带了链接时不管 tag 是什么都用这个链接，照旧按托管页打开。
func TestCheckoutURLWinsOverCustomTag(t *testing.T) {
	h := newHarness(t)
	h.gpt.checkout = chatgpt.Checkout{SessionID: "cs_live_abc", URL: testCheckoutURL, Tag: chatgpt.CheckoutTagCustom, ProcessorEntity: "openai_llc"}
	h.start(t)
	o := waitDone(t, h.submit(t, true).OrderNo)
	if o.State != StatePreviewed || o.CheckoutURL != testCheckoutURL {
		t.Fatalf("应用会话返回的链接: %+v", o)
	}
	if k := openedKind(theBrowser(t, h)); k != paybrowser.PageHosted {
		t.Fatalf("应按托管页打开: %v", k)
	}
}

// 没有链接、也不是内嵌会话：照旧以 checkout_failed「创建支付会话未返回支付链接」放弃，不碰卡池、不启动浏览器。
func TestCheckoutWithoutURLNorCustomTagFails(t *testing.T) {
	for _, tag := range []string{"", chatgpt.CheckoutTagHosted, "something_else"} {
		t.Run(tag, func(t *testing.T) {
			h := newPayHarness(t, nil)
			h.gpt.checkout = chatgpt.Checkout{SessionID: "cs_live_abc", Tag: tag, ProcessorEntity: "openai_llc"}
			h.start(t)
			o := waitDone(t, h.submit(t, false).OrderNo)
			assertNotSubmitted(t, o, "checkout_failed")
			if !strings.Contains(o.ErrorMessage, "创建支付会话未返回支付链接") {
				t.Fatalf("说明不符: %s", o.ErrorMessage)
			}
			if h.pool.total() != 0 || len(h.br.browsers()) != 0 {
				t.Fatal("不应碰卡池或启动浏览器")
			}
		})
	}
}

// Review Focus 5：会话号或 processor_entity 带路径字符、大写时，在拼链接这一步放弃：不取卡、不启动浏览器，说明里没有会话号。
func TestEmbeddedSessionBadEntityAbortsBeforeFunding(t *testing.T) {
	cases := map[string]chatgpt.Checkout{
		"processor_entity 带斜杠":  {SessionID: "cs_live_abc", Tag: chatgpt.CheckoutTagCustom, ProcessorEntity: "a/b"},
		"processor_entity 是 ..": {SessionID: "cs_live_abc", Tag: chatgpt.CheckoutTagCustom, ProcessorEntity: ".."},
		"processor_entity 大写":   {SessionID: "cs_live_abc", Tag: chatgpt.CheckoutTagCustom, ProcessorEntity: "OpenAI"},
		"processor_entity 缺失":   {SessionID: "cs_live_abc", Tag: chatgpt.CheckoutTagCustom},
		"会话号带斜杠":                {SessionID: "cs_live_abc/../x", Tag: chatgpt.CheckoutTagCustom, ProcessorEntity: "openai_llc"},
		"会话号带问号":                {SessionID: "cs_live_abc?x=1", Tag: chatgpt.CheckoutTagCustom, ProcessorEntity: "openai_llc"},
		"会话号带井号":                {SessionID: "cs_live_abc#f", Tag: chatgpt.CheckoutTagCustom, ProcessorEntity: "openai_llc"},
	}
	for name, co := range cases {
		t.Run(name, func(t *testing.T) {
			h := newPayHarness(t, nil)
			h.gpt.checkout = co
			h.start(t)
			o := waitDone(t, h.submit(t, false).OrderNo)
			assertNotSubmitted(t, o, "checkout_failed")
			if !strings.Contains(o.ErrorMessage, "内嵌结账会话") {
				t.Fatalf("应在拼链接这一步放弃: %s", o.ErrorMessage)
			}
			if o.CheckoutURL != "" {
				t.Fatalf("不合格的链接不应落库: %q", o.CheckoutURL)
			}
			assertNoSessionID(t, o, "cs_live_abc")
			if n := h.pool.total(); n != 0 {
				t.Fatalf("不应碰卡池: %d 次", n)
			}
			if n := len(h.br.browsers()); n != 0 {
				t.Fatalf("不应启动浏览器: %d", n)
			}
		})
	}
}

// 打开支付页停在 Cloudflare 挑战页：记 cf_challenge，告警一次让人查出口 IP，不碰卡池。
func TestOpenCloudflareAbortsBeforeFunding(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.checkout = embeddedCheckout()
	h.br.openErr = fmt.Errorf("x: %w", paybrowser.ErrCFChallenge)
	h.start(t)
	o := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, o, "cf_challenge")
	if !strings.Contains(o.ErrorMessage, "Cloudflare") || !strings.Contains(o.ErrorMessage, "出口 IP") {
		t.Fatalf("说明应指向代理出口 IP: %s", o.ErrorMessage)
	}
	assertNoSessionID(t, o, "cs_live_abc")
	a := h.alerts.Texts()
	if len(a) != 1 || !strings.Contains(a[0], "出口 IP") || !strings.Contains(a[0], o.OrderNo) || !strings.Contains(a[0], "PH") {
		t.Fatalf("应告警 1 次，带单号、地区并提示出口 IP: %v", a)
	}
	if strings.Contains(a[0], "cs_live_abc") {
		t.Fatalf("告警带出了会话号: %s", a[0])
	}
	if n := h.pool.total(); n != 0 {
		t.Fatalf("不应碰卡池: %d 次", n)
	}
	assertSubmitCount(t, h, 0)
}

// 不是 Cloudflare 挑战的打不开：照旧记 page_failed，不告警。
func TestOpenFailureOtherThanCloudflareUnchanged(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.checkout = embeddedCheckout()
	h.br.openErr = errors.New("net::ERR_TIMED_OUT")
	h.start(t)
	o := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, o, "page_failed")
	if a := h.alerts.Texts(); len(a) != 0 {
		t.Fatalf("普通打不开不告警: %v", a)
	}
	if n := h.pool.total(); n != 0 {
		t.Fatalf("不应碰卡池: %d 次", n)
	}
}

// 内嵌页的真实单：填表之后、点支付之前确认「保存付款信息」没勾；托管页的开通单照旧不确认。
func TestEmbeddedRealOrderEnsuresNoSavePayment(t *testing.T) {
	cases := []struct {
		name     string
		checkout chatgpt.Checkout
		want     int
	}{
		{"内嵌页", embeddedCheckout(), 1},
		{"托管页", chatgpt.Checkout{SessionID: "cs_live_abc", URL: testCheckoutURL}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newPayHarness(t, nil)
			h.gpt.checkout = c.checkout
			h.gpt.accountFn = planFrom(2)
			h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess})
			h.start(t)
			o := waitDone(t, h.submit(t, false).OrderNo)
			if o.State != StateSucceeded {
				t.Fatalf("应开通成功: %+v", o)
			}
			assertSubmitCount(t, h, 1)
			b := theBrowser(t, h)
			if n := b.count("EnsureNoSavePayment"); n != c.want {
				t.Fatalf("EnsureNoSavePayment 应调用 %d 次: %d", c.want, n)
			}
			if c.want == 1 {
				seq := b.order()
				fill, save, submit := slices.Index(seq, "Fill"), slices.Index(seq, "EnsureNoSavePayment"), slices.Index(seq, "Submit")
				if !(fill < save && save < submit) {
					t.Fatalf("应在填表之后、点支付之前确认: %v", seq)
				}
			}
		})
	}
}

// 内嵌页的真实单确认不了「保存付款信息」：照旧记 save_payment_unconfirmed，不点支付，已注资的卡按放弃释放。
func TestEmbeddedSavePaymentUnconfirmedAborts(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.checkout = embeddedCheckout()
	h.br.savePayment = func() (string, error) { return "无法确认", errors.New("勾选框读不出") }
	h.start(t)
	o := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, o, "save_payment_unconfirmed")
	assertReleases(t, h, cardpool.ResultAborted)
	assertSubmitCount(t, h, 0)
}

// 试运行到填表：内嵌页同样确认「保存付款信息」，托管页的开通单照旧不确认；都不碰卡池、不点支付。
func TestEmbeddedPreviewFillEnsuresNoSavePayment(t *testing.T) {
	cases := []struct {
		name     string
		checkout chatgpt.Checkout
		want     int
	}{
		{"内嵌页", embeddedCheckout(), 1},
		{"托管页", chatgpt.Checkout{SessionID: "cs_live_abc", URL: testCheckoutURL}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.gpt.checkout = c.checkout
			h.br.fill = func(paybrowser.Card, paybrowser.Billing) error { return nil }
			h.start(t)
			o := waitDone(t, h.submitFillPreview(t).OrderNo)
			if o.State != StatePreviewed {
				t.Fatalf("应试运行到填表完成: %+v", o)
			}
			if n := theBrowser(t, h).count("EnsureNoSavePayment"); n != c.want {
				t.Fatalf("EnsureNoSavePayment 应调用 %d 次: %d", c.want, n)
			}
			assertNeverPaid(t, h)
		})
	}
}

// 注资之后内嵌页的 Submit 发现按钮不唯一（Task 5）：没点击、LockSubmit 返回 false，引擎记 submit_not_clicked 并释放租约。
func TestEmbeddedSubmitNotUniqueAbandons(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.checkout = embeddedCheckout()
	h.br.submit = func() error { return errors.New("支付按钮不唯一（找到 2 个），没有点击") }
	h.br.lockSubmit = func() bool { return false }
	h.start(t)
	o := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, o, "submit_not_clicked")
	if o.SubmittedAt != nil || o.PageOutcome != "" || !strings.Contains(o.ErrorMessage, "不唯一") {
		t.Fatalf("没点击的单不应留下提交痕迹，并写明原因: %+v", o)
	}
	assertReleases(t, h, cardpool.ResultAborted)
	assertSubmitCount(t, h, 1)
	if b := theBrowser(t, h); b.count("WaitOutcome") != 0 {
		t.Fatalf("没点击不应等结果: %d", b.count("WaitOutcome"))
	}
}

// 点数单只走托管页：白名单放行 chatgpt.com 之后，点数单的链接哪怕绕过了建链接这一步，打开之前也照样拦下，不打开。
func TestCreditOpenPageRefusesEmbeddedURL(t *testing.T) {
	h := newPayHarness(t, nil)
	o := presetOrder(t, "re@example.com", StateLinkCreated, map[string]any{"checkout_url": testEmbeddedURL, "kind": KindCredit})
	if o.Kind != KindCredit {
		t.Fatalf("预置单据应是点数单: %q", o.Kind)
	}
	j := &job{o: o}
	err := h.svc.openPage(ctx, j)
	var f *stepFailure
	if !errors.As(err, &f) || f.code != "checkout_url_untrusted" {
		t.Fatalf("应拒绝: %v", err)
	}
	if strings.Contains(f.msg, "cs_live_abc") {
		t.Fatalf("说明只能有域名: %s", f.msg)
	}
	for _, b := range h.br.browsers() {
		if b.count("Open") != 0 {
			t.Fatal("点数单不能打开内嵌页")
		}
	}
	j.closeBrowser()
}
