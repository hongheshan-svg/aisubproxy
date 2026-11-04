package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sort"
	"testing"
	"time"
)

// recorded 是测试服务器收到的一次请求。
type recorded struct {
	method, path, query string
	header              http.Header
	body                map[string]interface{}
}

// recordServer 记下收到的请求，按 reply 回复。
func recordServer(t *testing.T, status int, reply string) (*Client, *[]recorded) {
	t.Helper()
	var got []recorded
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recorded{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone()}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			if err := json.Unmarshal(b, &rec.body); err != nil {
				t.Errorf("请求体不是 JSON: %s", b)
			}
		}
		got = append(got, rec)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(reply))
	}))
	return c, &got
}

func keysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var testSess = Session{AccessToken: "at-1", SessionToken: "st-1", Email: "a@b.c"}

// 订阅开通的请求体保持原样：只有 plan_name 与 billing_details，不带任何点数字段。
func TestCreateCheckoutBodyUnchanged(t *testing.T) {
	c, got := recordServer(t, 200, `{"checkout_session_id":"cs_1","url":"https://pay.openai.com/c/pay/cs_1"}`)
	if _, err := c.CreateCheckout(context.Background(), testSess, "chatgptplusplan", "PH", "PHP"); err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	if k := keysOf(r.body); !reflect.DeepEqual(k, []string{"billing_details", "plan_name"}) {
		t.Fatalf("请求体的键变了: %v", k)
	}
	if r.header.Get(headerChatGPTAccount) != "" || r.header.Get(headerExpectedAccount) != "" {
		t.Fatalf("订阅开通请求不该多带账号头: %v", r.header)
	}
}

func TestCancelRenewalRequestShape(t *testing.T) {
	c, got := recordServer(t, 200, `{}`)
	if err := c.CancelRenewal(context.Background(), testSess, " acc-1 "); err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	if r.method != http.MethodPost || r.path != "/backend-api/subscriptions/cancel" || r.query != "" {
		t.Fatalf("请求 %s %s?%s", r.method, r.path, r.query)
	}
	if r.header.Get("Authorization") != "Bearer at-1" || r.header.Get("X-OpenAI-Expected-Account-Id") != "acc-1" {
		t.Fatalf("请求头不符: %v", r.header)
	}
	// 只带 account_id：绝不能带 cancellation_outcome（deactivate 会立即终止当期权益）
	if !reflect.DeepEqual(r.body, map[string]interface{}{"account_id": "acc-1"}) {
		t.Fatalf("请求体不符: %v", r.body)
	}
	if err := c.CancelRenewal(context.Background(), testSess, " "); err == nil || len(*got) != 1 {
		t.Fatalf("没有账号 ID 不能发请求: %v", err)
	}
}

func TestCancelRenewalErrors(t *testing.T) {
	c, _ := recordServer(t, http.StatusForbidden, `{"detail":"no"}`)
	if err := c.CancelRenewal(context.Background(), testSess, "acc-1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("403 应为未授权: %v", err)
	}
	c, _ = recordServer(t, http.StatusInternalServerError, `{}`)
	if err := c.CancelRenewal(context.Background(), testSess, "acc-1"); err == nil {
		t.Fatal("500 应报错")
	}
}

func TestSubscriptionStatus(t *testing.T) {
	c, got := recordServer(t, 200, `{"id":"sub_1","will_renew":false,"is_delinquent":false}`)
	st, err := c.SubscriptionStatus(context.Background(), testSess, "acc 1")
	if err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	if r.method != http.MethodGet || r.path != "/backend-api/subscriptions" || r.query != "account_id=acc+1" {
		t.Fatalf("请求 %s %s?%s", r.method, r.path, r.query)
	}
	if r.header.Get("X-OpenAI-Expected-Account-Id") != "acc 1" || r.body != nil {
		t.Fatalf("请求不符: %v %v", r.header, r.body)
	}
	if st.WillRenew == nil || *st.WillRenew || st.IsDelinquent == nil || *st.IsDelinquent {
		t.Fatalf("解析不符: %+v", st)
	}
	// 字段缺失或不是布尔：一律为 nil，不能当成「不续费」
	c, _ = recordServer(t, 200, `{"will_renew":"false"}`)
	if st, err := c.SubscriptionStatus(context.Background(), testSess, "acc-1"); err != nil || st.WillRenew != nil || st.IsDelinquent != nil {
		t.Fatalf("缺字段应为 nil: %+v %v", st, err)
	}
	c, _ = recordServer(t, 200, `not json`)
	if _, err := c.SubscriptionStatus(context.Background(), testSess, "acc-1"); err == nil {
		t.Fatal("非 JSON 应报错")
	}
}

func TestCreateCreditCheckoutRequestShape(t *testing.T) {
	c, got := recordServer(t, 200, `{"tag":"hosted_checkout_session","checkout_session_id":"cs_live_c","url":"https://checkout.stripe.com/c/pay/cs_live_c","processor_entity":"openai_llc"}`)
	res, err := c.CreateCreditCheckout(context.Background(), testSess, "acc-1", 250)
	if err != nil {
		t.Fatal(err)
	}
	want := CreditCheckoutResult{Tag: CheckoutTagHosted, SessionID: "cs_live_c", URL: "https://checkout.stripe.com/c/pay/cs_live_c", ProcessorEntity: "openai_llc"}
	if res != want {
		t.Fatalf("got %+v want %+v", res, want)
	}
	r := (*got)[0]
	if r.method != http.MethodPost || r.path != "/backend-api/payments/checkout" {
		t.Fatalf("请求 %s %s", r.method, r.path)
	}
	if r.header.Get("ChatGPT-Account-ID") != "acc-1" || r.header.Get("X-OpenAI-Expected-Account-Id") != "acc-1" || r.header.Get("Authorization") != "Bearer at-1" {
		t.Fatalf("请求头不符: %v", r.header)
	}
	wantBody := map[string]interface{}{
		"credit_purchase_data": map[string]interface{}{"account_id": "acc-1", "quantity": float64(250), "unit": "credit"},
		"entry_point":          "codex_purchase_credits_route",
		"checkout_ui_mode":     "redirect",
	}
	if !reflect.DeepEqual(r.body, wantBody) {
		t.Fatalf("请求体不符: %v", r.body)
	}
	if _, err := c.CreateCreditCheckout(context.Background(), testSess, "acc-1", 0); err == nil || len(*got) != 1 {
		t.Fatal("数量为 0 不能发请求")
	}
	if _, err := c.CreateCreditCheckout(context.Background(), testSess, "", 250); err == nil || len(*got) != 1 {
		t.Fatal("没有账号 ID 不能发请求")
	}
}

// 自定义结账页原样返回 tag，由引擎决定放弃；客户端不报错、不编造链接。
func TestCreateCreditCheckoutCustomSession(t *testing.T) {
	c, _ := recordServer(t, 200, `{"tag":"custom_checkout_session","checkout_session_id":"cs_x","processor_entity":"openai_llc"}`)
	res, err := c.CreateCreditCheckout(context.Background(), testSess, "acc-1", 500)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tag != CheckoutTagCustom || res.URL != "" || res.SessionID != "cs_x" {
		t.Fatalf("%+v", res)
	}
}

func TestSavedCards(t *testing.T) {
	c, got := recordServer(t, 200, `{"payment_methods":[
		{"id":"pm_1","type":"card","card":{"brand":"visa","last4":"4242","exp_month":8,"exp_year":2028}},
		{"payment_method_id":"pm_2","brand":"mastercard","last4":"5555","exp_month":"1","exp_year":"2030"},
		{"id":"pm_3","type":"link","link":{"email":"x@y.z"}}
	],"default_payment_method_id":"pm_1"}`)
	list, err := c.SavedCards(context.Background(), testSess, "acc-1")
	if err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	if r.method != http.MethodGet || r.path != "/backend-api/payments/payment_methods" || r.query != "account_id=acc-1" {
		t.Fatalf("请求 %s %s?%s", r.method, r.path, r.query)
	}
	want := []SavedCard{
		{ID: "pm_1", Type: "card", Brand: "visa", Last4: "4242", ExpMonth: 8, ExpYear: 2028},
		{ID: "pm_2", Brand: "mastercard", Last4: "5555", ExpMonth: 1, ExpYear: 2030},
		{ID: "pm_3", Type: "link"},
	}
	if !reflect.DeepEqual(list, want) {
		t.Fatalf("got %+v", list)
	}
}

func TestCreditBalance(t *testing.T) {
	c, got := recordServer(t, 200, `{"credits":{"has_credits":true,"unlimited":false,"balance":"1250.5"},"rate_limit":{"allowed":true,"limit_reached":false}}`)
	b, err := c.CreditBalance(context.Background(), testSess, "acc-1")
	if err != nil || !b.Available || b.Balance != "1250.5" {
		t.Fatalf("%+v %v", b, err)
	}
	r := (*got)[0]
	if r.method != http.MethodGet || r.path != "/backend-api/wham/usage" || r.header.Get("x-openai-codex-pricing-chooser") != "1" || r.header.Get("ChatGPT-Account-ID") != "acc-1" {
		t.Fatalf("请求不符: %s %s %v", r.method, r.path, r.header)
	}
	for _, reply := range []string{`{}`, `{"credits":null}`, `{"credits":{"has_credits":false,"unlimited":true,"balance":"0"}}`, `{"credits":{"has_credits":false,"unlimited":false,"balance":null}}`} {
		c, _ := recordServer(t, 200, reply)
		if b, err := c.CreditBalance(context.Background(), testSess, "acc-1"); err != nil || b.Available {
			t.Fatalf("%s 应视为余额不可用: %+v %v", reply, b, err)
		}
	}
}

func TestAccountParsesCreditFields(t *testing.T) {
	c, _ := recordServer(t, 200, `{"accounts":{"default":{"account":{"account_id":"acc-1","plan_type":"prolite","structure":"Personal"},
		"entitlement":{"has_active_subscription":true,"billing_currency":"php","is_delinquent":false},
		"last_active_subscription":{"will_renew":true}}}}`)
	got, err := c.Account(context.Background(), testSess)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanType != "prolite" || got.Structure != "personal" || got.BillingCurrency != "PHP" ||
		got.IsDelinquent == nil || *got.IsDelinquent || got.WillRenew == nil || !*got.WillRenew {
		t.Fatalf("%+v", got)
	}
	c, _ = recordServer(t, 200, `{"accounts":{"default":{"account":{"account_id":"acc-1","plan_type":"plus"},"entitlement":{"has_active_subscription":true}}}}`)
	got, err = c.Account(context.Background(), testSess)
	if err != nil || got.Structure != "" || got.BillingCurrency != "" || got.IsDelinquent != nil || got.WillRenew != nil {
		t.Fatalf("缺字段应为空: %+v %v", got, err)
	}
}

func TestParseActiveUntil(t *testing.T) {
	want := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   any
		want *time.Time
	}{
		{"RFC3339", "2026-12-04T10:00:00Z", &want},
		{"带小数和偏移", "2026-12-04T10:00:00.123456+00:00", func() *time.Time { w := want.Add(123456 * time.Microsecond); return &w }()},
		{"Unix 秒", float64(want.Unix()), &want},
		{"Unix 毫秒", float64(want.Unix() * 1000), &want},
		{"数字文本", "1796378400", &want},
		{"nil", nil, nil},
		{"空串", "", nil},
		{"乱码", "abc", nil},
		{"零", float64(0), nil},
		{"1970 年", float64(86400), nil},
		{"2200 年", float64(7258118400), nil},
		{"2200 年毫秒", float64(7258118400000), nil},
		{"布尔", true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseActiveUntil(c.in)
			if c.want == nil {
				if got != nil {
					t.Fatalf("应为 nil: %v", got)
				}
				return
			}
			if got == nil || !got.Equal(*c.want) {
				t.Fatalf("得到 %v，期望 %v", got, c.want)
			}
		})
	}
}

func TestSubscriptionStatusReadsActiveUntil(t *testing.T) {
	c, _ := recordServer(t, 200, `{"will_renew":false,"active_until":"2026-12-04T10:00:00Z"}`)
	st, err := c.SubscriptionStatus(context.Background(), testSess, "acc-1")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveUntil == nil || !st.ActiveUntil.Equal(time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("ActiveUntil 不符: %v", st.ActiveUntil)
	}
	c, _ = recordServer(t, 200, `{"will_renew":false}`)
	st, err = c.SubscriptionStatus(context.Background(), testSess, "acc-1")
	if err != nil || st.ActiveUntil != nil {
		t.Fatalf("缺字段应为 nil: %v %v", st.ActiveUntil, err)
	}
}
