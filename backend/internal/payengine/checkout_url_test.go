package payengine

import (
	"errors"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// 不可信的支付链接：子域名、http、带端口、相似域名、带账号信息、末尾带点、没有协议等，一律拒绝。
// chatgpt.com 只放行 /checkout/<processor_entity>/<会话号>：路径段数不对、段不合格、带查询串或片段、路径里有转义字符的，同样拒绝。
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
	"https://user:pw@pay.openai.com/c/pay/cs_live_abc",
	"https://pay.openai.com@evil.example/c/pay/cs_live_abc",
	"https://pay.openai.com./c/pay/cs_live_abc",
	"//pay.openai.com/c/pay/cs_live_abc",
	"https:pay.openai.com/c/pay/cs_live_abc",
	"javascript:alert(1)",
	"wss://pay.openai.com/c/pay/cs_live_abc",
	"https://%70ay.openai.com/c/pay/cs_live_abc",
	"https://chatgpt.com/checkout/openai_llc",
	"https://chatgpt.com/checkout/a/b/c",
	"https://chatgpt.com/backend-api/x/y",
	"https://chatgpt.com/checkout/openai_llc/cs_live_abc?x=1",
	"https://chatgpt.com/checkout/openai_llc/cs_live_abc#f",
	"https://www.chatgpt.com/checkout/openai_llc/cs_live_abc",
	"https://chatgpt.com.evil.example/checkout/openai_llc/cs_live_abc",
	"http://chatgpt.com/checkout/openai_llc/cs_live_abc",
	"https://chatgpt.com:443/checkout/openai_llc/cs_live_abc",
	"https://chatgpt.com/checkout/OpenAI/cs_live_abc",
	"https://chatgpt.com/checkout/openai_llc/cs%2Flive",
	"https://chatgpt.com:/checkout/openai_llc/cs_live_abc",
	// 主机名含非 ASCII 字符：转小写会把 İ（U+0130）折成 i、开尔文符号（U+212A）折成 k，浏览器做国际化域名转换后去的是别的域名
	"https://pay.openaİ.com/c/pay/cs_live_abc",
	"https://checkout.strİpe.com/c/pay/cs_live_abc",
	"https://chec\u212aout.stripe.com/c/pay/cs_live_abc",
	"https://pay.opena%C4%B0.com/c/pay/cs_live_abc",
}

func TestCheckCheckoutURL(t *testing.T) {
	for _, ok := range []string{
		"https://pay.openai.com/c/pay/cs_live_abc#fid=x",
		"https://checkout.stripe.com/c/pay/cs_live_abc?k=v",
		"HTTPS://PAY.OPENAI.COM/c/pay/cs_live_abc",
		" https://pay.openai.com/c/pay/cs_live_abc ",
		"https://chatgpt.com/checkout/openai_llc/cs_live_abc",
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
		{" https://chatgpt.com/checkout/openai_llc/cs_live_abc", paybrowser.PageEmbedded},
		{"\nhttps://chatgpt.com/checkout/openai_llc/cs_live_abc", paybrowser.PageEmbedded},
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

// 内嵌结账页链接由 processor_entity 和会话号拼出。两样带路径字符、大写（processor_entity）、空白或为空的一律不拼，
// 以 checkout_failed 放弃，说明里不写会话号。拼出来的链接要能通过白名单。
func TestEmbeddedCheckoutURL(t *testing.T) {
	got, err := embeddedCheckoutURL("openai_llc", "cs_live_abc")
	if err != nil || got != "https://chatgpt.com/checkout/openai_llc/cs_live_abc" {
		t.Fatalf("应拼出内嵌页链接: %q %v", got, err)
	}
	if err := checkCheckoutURL(got); err != nil {
		t.Fatalf("拼出的链接应通过白名单: %v", err)
	}
	for _, c := range []struct{ entity, session string }{
		{"", "cs_live_abc"},
		{"a/b", "cs_live_abc"},
		{"..", "cs_live_abc"},
		{"OpenAI", "cs_live_abc"},
		{"openai_llc", "cs/1"},
		{"openai_llc", "cs?1"},
		{"openai_llc", "cs#1"},
		{"openai_llc", "cs 1"},
		{"openai_llc", ""},
	} {
		got, err := embeddedCheckoutURL(c.entity, c.session)
		var f *stepFailure
		if !errors.As(err, &f) || f.code != "checkout_failed" || got != "" {
			t.Errorf("(%q, %q) 应以 checkout_failed 放弃: %q %v", c.entity, c.session, got, err)
			continue
		}
		if c.session != "" && strings.Contains(f.msg, c.session) {
			t.Errorf("(%q, %q) 的说明带出了会话号: %s", c.entity, c.session, f.msg)
		}
	}
}

// chatgpt.com 的规则要严格：空的「?」「#」、末尾或开头多斜杠、转义过的普通字符、「..」段、大小写不对的固定段、超长的段，都不放行；
// 两段取到长度上限时照样放行。
func TestCheckCheckoutURLEmbeddedStrict(t *testing.T) {
	long := "https://chatgpt.com/checkout/" + strings.Repeat("a", 64) + "/" + strings.Repeat("A", 255)
	if err := checkCheckoutURL(long); err != nil {
		t.Fatalf("两段在长度上限内应通过: %v", err)
	}
	for _, bad := range []string{
		"https://chatgpt.com/checkout/openai_llc/cs_live_abc?",
		"https://chatgpt.com/checkout/openai_llc/cs_live_abc#",
		"https://chatgpt.com/checkout/openai_llc/cs_live_abc/",
		"https://chatgpt.com//checkout/openai_llc/cs_live_abc",
		"https://chatgpt.com/checkout/openai_llc/cs%5Flive_abc",
		"https://chatgpt.com/checkout/openai_llc/cs_live_abc%20",
		"https://chatgpt.com/checkout/../cs_live_abc",
		"https://chatgpt.com/Checkout/openai_llc/cs_live_abc",
		"https://chatgpt.com/checkout/" + strings.Repeat("a", 65) + "/cs_live_abc",
		"https://chatgpt.com/checkout/openai_llc/cs_live_abc" + strings.Repeat("A", 245),
	} {
		err := checkCheckoutURL(bad)
		var f *stepFailure
		if !errors.As(err, &f) || f.code != "checkout_url_untrusted" {
			t.Errorf("%q 应以 checkout_url_untrusted 拒绝: %v", bad, err)
			continue
		}
		if strings.Contains(f.msg, "cs_live_abc") || strings.Contains(f.msg, "openai_llc") {
			t.Errorf("%q 的说明带出了链接内容: %s", bad, f.msg)
		}
	}
}
