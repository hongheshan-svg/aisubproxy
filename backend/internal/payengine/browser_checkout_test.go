package payengine

import (
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// 订阅单在浏览器里建会话：不调接口，链接用 processor_entity 和会话号拼成内嵌结账页，后续照常打开、读金额。
func TestBrowserCreatesCheckoutLink(t *testing.T) {
	h := newHarness(t)
	h.svc.opts.BrowserCheckout = true
	h.br.checkout = func(req paybrowser.CheckoutRequest) (paybrowser.CheckoutResult, error) {
		if req.PlanName != "chatgptplusplan" || req.Country != "PH" || req.Currency != "PHP" {
			t.Errorf("建会话参数不对: %+v", req)
		}
		return paybrowser.CheckoutResult{SessionID: "oaics_abc", ProcessorEntity: "openai_llc", Tag: "custom_checkout_session"}, nil
	}
	h.start(t)

	done := waitDone(t, h.submit(t, true).OrderNo)
	if done.State != StatePreviewed {
		t.Fatalf("期望试运行完成，得到 %s（%s：%s）", done.State, done.ErrorCode, done.ErrorMessage)
	}
	if want := "https://chatgpt.com/checkout/openai_llc/oaics_abc"; done.CheckoutURL != want {
		t.Fatalf("结账页链接 = %q，期望 %q", done.CheckoutURL, want)
	}
	if n := h.gpt.count("CreateCheckout"); n != 0 {
		t.Fatalf("订阅单不应再调接口建会话，实际调用 %d 次", n)
	}
}
