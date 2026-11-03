package payengine

import (
	"errors"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// 不可信的支付链接：子域名、http、带端口、相似域名、chatgpt.com、带账号信息、末尾带点、没有协议等，一律拒绝。
var untrustedCheckoutURLs = []string{
	"https://sub.pay.openai.com/c/pay/cs_live_abc",
	"https://www.checkout.stripe.com/c/pay/cs_live_abc",
	"http://pay.openai.com/c/pay/cs_live_abc",
	"http://checkout.stripe.com/c/pay/cs_live_abc",
	"https://pay.openai.com:8443/c/pay/cs_live_abc",
	"https://pay.openai.com:443/c/pay/cs_live_abc",
	"https://pay.openai.com.evil.example/c/pay/cs_live_abc",
	"https://checkout.stripe.com.evil.example/c/pay/cs_live_abc",
	"https://pay-openai.com/c/pay/cs_live_abc",
	"https://evil.example/pay.openai.com/c/pay/cs_live_abc",
	"https://chatgpt.com/checkout/openai_llc/cs_live_abc",
	"https://user:pw@pay.openai.com/c/pay/cs_live_abc",
	"https://pay.openai.com@evil.example/c/pay/cs_live_abc",
	"https://pay.openai.com./c/pay/cs_live_abc",
	"//pay.openai.com/c/pay/cs_live_abc",
	"https:pay.openai.com/c/pay/cs_live_abc",
	"javascript:alert(1)",
	"wss://pay.openai.com/c/pay/cs_live_abc",
	"https://%70ay.openai.com/c/pay/cs_live_abc",
}

func TestCheckCheckoutURL(t *testing.T) {
	for _, ok := range []string{
		"https://pay.openai.com/c/pay/cs_live_abc#fid=x",
		"https://checkout.stripe.com/c/pay/cs_live_abc?k=v",
		"HTTPS://PAY.OPENAI.COM/c/pay/cs_live_abc",
		" https://pay.openai.com/c/pay/cs_live_abc ",
	} {
		if err := checkCheckoutURL(ok); err != nil {
			t.Errorf("%q 应通过: %v", ok, err)
		}
	}
	for _, bad := range untrustedCheckoutURLs {
		err := checkCheckoutURL(bad)
		var f *stepFailure
		if !errors.As(err, &f) || f.code != "checkout_url_untrusted" {
			t.Errorf("%q 应以 checkout_url_untrusted 拒绝: %v", bad, err)
			continue
		}
		// 说明里只写域名，不写完整链接（会话号、路径、账号密码都不能出现）
		for _, leak := range []string{"cs_live_abc", "/c/pay", "pw", "alert", "user"} {
			if strings.Contains(f.msg, leak) {
				t.Errorf("%q 的说明带出了链接内容 %q: %s", bad, leak, f.msg)
			}
		}
	}
}

// 订阅开通单拿到不可信链接：在建链接这一步放弃，没取卡、没注资、没启动浏览器，链接不落库。
func TestUntrustedCheckoutURLAbortsBeforeFunding(t *testing.T) {
	for _, bad := range untrustedCheckoutURLs {
		t.Run(bad, func(t *testing.T) {
			h := newPayHarness(t, nil)
			h.gpt.checkout.URL = bad
			h.start(t)
			o := waitDone(t, h.submit(t, false).OrderNo)
			assertNotSubmitted(t, o, "checkout_url_untrusted")
			if o.CheckoutURL != "" {
				t.Fatalf("不可信链接不应落库: %q", o.CheckoutURL)
			}
			if strings.Contains(o.ErrorMessage, "cs_live_abc") || strings.Contains(o.ErrorMessage, "/c/pay") {
				t.Fatalf("错误信息只能有域名: %s", o.ErrorMessage)
			}
			if n := h.pool.total(); n != 0 {
				t.Fatalf("不应碰卡池（含注资）: %d 次", n)
			}
			if n := len(h.br.browsers()); n != 0 {
				t.Fatalf("不应启动浏览器: %d", n)
			}
			for _, e := range eventStates(t, o.OrderNo) {
				if strings.Contains(e.Message, "cs_live_abc") {
					t.Fatalf("时间线不应有完整链接: %s", e.Message)
				}
			}
		})
	}
}

// 打开支付页之前再校验一次：链接哪怕绕过了建链接这一步，也不会被打开。
func TestOpenPageRechecksCheckoutURL(t *testing.T) {
	h := newPayHarness(t, nil)
	o := presetOrder(t, "re@example.com", StateLinkCreated, map[string]any{"checkout_url": "https://pay.openai.com.evil.example/c/pay/cs_live_abc"})
	j := &job{o: o}
	err := h.svc.openPage(ctx, j)
	var f *stepFailure
	if !errors.As(err, &f) || f.code != "checkout_url_untrusted" {
		t.Fatalf("应拒绝: %v", err)
	}
	for _, b := range h.br.browsers() {
		if b.count("Open") != 0 {
			t.Fatal("不可信链接不能被打开")
		}
	}
	j.closeBrowser()
}

func TestCheckoutPageKind(t *testing.T) {
	cases := []struct {
		url  string
		want paybrowser.PageKind
	}{
		{"https://chatgpt.com/checkout/openai_llc/cs_live_abc", paybrowser.PageEmbedded},
		{"HTTPS://CHATGPT.COM/checkout/a/b", paybrowser.PageEmbedded},
		{"https://pay.openai.com/c/pay/x", paybrowser.PageHosted},
		{"https://checkout.stripe.com/c/pay/x", paybrowser.PageHosted},
		{"", paybrowser.PageHosted},
		{"::bad", paybrowser.PageHosted},
	}
	for _, c := range cases {
		if got := checkoutPageKind(c.url); got != c.want {
			t.Errorf("checkoutPageKind(%q) = %v，期望 %v", c.url, got, c.want)
		}
	}
}
