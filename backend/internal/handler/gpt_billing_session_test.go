package handler

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/gptcheck"
)

// stubSessionCheck 把登录信息查询换成固定结果，并确认传进去的就是用户粘贴的那份。
func stubSessionCheck(t *testing.T, res *gptcheck.Result) {
	t.Helper()
	old := billingSessionCheck
	billingSessionCheck = func(raw string) (*gptcheck.Result, error) {
		if raw != "pasted-token" {
			t.Errorf("应只用用户粘贴的登录信息: %q", raw)
		}
		return res, nil
	}
	t.Cleanup(func() { billingSessionCheck = old })
}

// 登录信息查询：不给金额、币种、账单描述（Stripe 的描述里带单价）、账单链接和 PDF（点开就是金额），
// 也不给计费币种（能看出地区）。代理商进价和最终买家付的钱不一样，显示出来会误导。
func TestBillingBySessionHidesAmounts(t *testing.T) {
	stubSessionCheck(t, &gptcheck.Result{
		Summary: map[string]interface{}{
			"plan_type": "plus", "subscription_plan": "chatgptplusplan", "has_active_subscription": true,
			"expires_at": "2026-12-10T00:00:00Z", "renews_at": "2026-12-10T00:00:00Z", "cancels_at": nil,
			"billing_currency": "php", "billing_period": "monthly", "will_renew": false, "account_id": "acct-123",
		},
		Invoices: []map[string]interface{}{{
			"id": "in_1", "number": "ABCD-0001", "status": "paid", "paid": true, "created": float64(1700000000),
			"currency": "php", "total": float64(112000), "amount_paid": float64(112000),
			"description":        "1 × ChatGPT Plus (at ₱1,120.00 / month)",
			"hosted_invoice_url": "https://invoice.stripe.com/i/acct_x/live_y",
			"invoice_pdf":        "https://pay.stripe.com/invoice/acct_x/live_y/pdf",
		}},
	})
	w := cdkRequest(SessionBillingCheck, "POST", "/billing", `{"token_input":"pasted-token"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	raw := w.Body.String()
	for _, bad := range []string{"112000", "1120", "php", "PHP", "₱", "currency", "total", "amount", "description", "stripe.com", "invoice_url", "invoice_pdf", "acct-123", "account_id"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("登录信息查询的返回不该带 %q: %s", bad, raw)
		}
	}
	var d struct {
		AuthSource string           `json:"auth_source"`
		Summary    map[string]any   `json:"summary"`
		Invoices   []map[string]any `json:"invoices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	wantSummary := []string{"billing_period", "cancels_at", "expires_at", "has_active_subscription", "plan_type", "renews_at", "subscription_plan", "will_renew"}
	if got := sortedKeys(d.Summary); !reflect.DeepEqual(got, wantSummary) {
		t.Fatalf("摘要字段应只有 %v: %v", wantSummary, got)
	}
	if d.AuthSource != "session" || d.Summary["plan_type"] != "plus" || d.Summary["billing_period"] != "monthly" || d.Summary["will_renew"] != false {
		t.Fatalf("摘要的值不对: %s", raw)
	}
	if len(d.Invoices) != 1 {
		t.Fatalf("应有 1 张账单: %s", raw)
	}
	inv := d.Invoices[0]
	if got, want := sortedKeys(inv), []string{"created", "id", "number", "paid", "status"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("账单字段应只有 %v: %v", want, got)
	}
	if inv["status"] != "paid" || inv["paid"] != true || inv["created"] != float64(1700000000) {
		t.Fatalf("账单的值不对: %v", inv)
	}
}

// 查不到账单（ChatGPT 账单接口出错时 gptcheck 给 nil）：给空列表，不给 null；摘要缺的字段不补。
func TestBillingBySessionNoInvoices(t *testing.T) {
	stubSessionCheck(t, &gptcheck.Result{Summary: map[string]interface{}{"plan_type": "free"}})
	w := cdkRequest(SessionBillingCheck, "POST", "/billing", `{"token_input":"pasted-token"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if string(d["invoices"]) != "[]" || string(d["summary"]) != `{"plan_type":"free"}` {
		t.Fatalf("应给空列表、摘要不补字段: %s", w.Body.String())
	}
}

// 上游哪天把白名单里的字段改成嵌套对象或列表：里面可能带金额，整项丢掉，只放行字符串、布尔、数字和空值。
func TestBillingBySessionDropsNestedValues(t *testing.T) {
	stubSessionCheck(t, &gptcheck.Result{
		Summary: map[string]interface{}{
			"plan_type":         "plus",
			"expires_at":        map[string]interface{}{"at": "2026-12-10", "amount": float64(112000), "currency": "php"},
			"subscription_plan": []interface{}{"plus", "₱1,120.00"},
			"cancels_at":        nil,
			"will_renew":        true,
			"billing_period":    "monthly",
		},
		Invoices: []map[string]interface{}{{
			"id": "in_1", "status": map[string]interface{}{"label": "paid", "total": float64(112000)}, "paid": true, "created": float64(1700000000),
		}},
	})
	w := cdkRequest(SessionBillingCheck, "POST", "/billing", `{"token_input":"pasted-token"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	raw := w.Body.String()
	for _, bad := range []string{"112000", "1120", "php", "₱", "amount", "total"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("嵌套的值不该透传 %q: %s", bad, raw)
		}
	}
	var d struct {
		Summary  map[string]any   `json:"summary"`
		Invoices []map[string]any `json:"invoices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if got, want := sortedKeys(d.Summary), []string{"billing_period", "cancels_at", "plan_type", "will_renew"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("摘要应只留简单值 %v: %v", want, got)
	}
	if got, want := sortedKeys(d.Invoices[0]), []string{"created", "id", "paid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("账单应只留简单值 %v: %v", want, got)
	}
}
