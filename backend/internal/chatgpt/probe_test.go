package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// 白名单之外的请求必须在发出前就被拒绝：假服务器一个请求都不该收到。
func TestProbeAllowlistRejectsBeforeSending(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	p := NewProbe(newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Write([]byte(`{}`))
	})))
	s := Session{AccessToken: "at", SessionToken: "st", Email: "a@b.c"}

	bad := []struct{ method, path string }{
		{"POST", "/backend-api/subscriptions/update"},
		{"POST", "/backend-api/subscriptions/cancel"},
		{"POST", "/backend-api/subscriptions/renew"},
		{"POST", "/backend-api/payments/payment_method"},
		{"DELETE", "/backend-api/payments/payment_method/x"},
		{"POST", "/backend-api/payments/payment_method/default"},
		{"POST", "/backend-api/payments/payment_method/backup"},
		{"POST", "/backend-api/payments/checkout/confirm"},
		{"GET", "/backend-api/subscriptions/update"},
		{"POST", "/backend-api/subscriptions"},
		{"GET", "/backend-api/payments/checkout"},
		{"POST", "/backend-api/payments/checkout/"},
		{"POST", "/backend-api/payments/checkout?x=1"},
		{"post", "/backend-api/payments/checkout"},
		{"POST", "/backend-api/payments/../subscriptions/cancel"},
		{"POST", "/backend-api/payments/checkout%2Fconfirm"},
		{"GET", "/backend-api/conversation"},
	}
	for _, c := range bad {
		if err := ProbeCheckAllowed(c.method, c.path); !errors.Is(err, ErrProbeForbidden) {
			t.Errorf("%s %s 应被拒绝，得到 %v", c.method, c.path, err)
		}
		if _, err := p.request(context.Background(), c.method, c.path, nil, bearer(s), nil); !errors.Is(err, ErrProbeForbidden) {
			t.Errorf("request %s %s 应被拒绝，得到 %v", c.method, c.path, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 0 {
		t.Fatalf("被拒绝的请求不应到达服务器: %v", hits)
	}
}

func TestProbeAllowlistAcceptsListed(t *testing.T) {
	for _, e := range ProbeAllowlist {
		parts := strings.SplitN(e, " ", 2)
		if err := ProbeCheckAllowed(parts[0], parts[1]); err != nil {
			t.Errorf("%s 应放行: %v", e, err)
		}
		// 写操作只有建会话一个
		if !strings.HasPrefix(e, "GET ") && e != "POST /backend-api/payments/checkout" {
			t.Errorf("白名单里出现了非 GET 条目: %s", e)
		}
	}
}

func TestProbeCheckoutBodiesAndShape(t *testing.T) {
	var bodies []map[string]interface{}
	var hdrs []http.Header
	p := NewProbe(newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/backend-api/payments/checkout" {
			t.Errorf("意外请求 %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var m map[string]interface{}
		_ = json.Unmarshal(b, &m)
		bodies = append(bodies, m)
		hdrs = append(hdrs, r.Header.Clone())
		if m["checkout_ui_mode"] == "redirect" {
			w.Write([]byte(`{"checkout_session_id":"cs_live_a1SECRETSECRET","url":"https://pay.openai.com/c/pay/cs_live_a1SECRETSECRET#fragsecret","currency":"php"}`))
			return
		}
		w.Write([]byte(`{"tag":"custom_checkout_session","processor_entity":"openai_llc","checkout_session_id":"cs_live_b2SECRET"}`))
	})))
	s := Session{AccessToken: "at", SessionToken: "st", Email: "a@b.c"}

	plain, err := p.CreditCheckout(context.Background(), s, "acc-1", false)
	if err != nil || plain.Shape != ShapeCustom || plain.Processor != "openai_llc" {
		t.Fatalf("不带 redirect: %+v %v", plain, err)
	}
	red, err := p.CreditCheckout(context.Background(), s, "acc-1", true)
	if err != nil || red.Shape != ShapeURL || red.Host != "pay.openai.com" || red.Currency != "PHP" {
		t.Fatalf("带 redirect: %+v %v", red, err)
	}
	if strings.Contains(red.PathPrefix, "SECRET") || strings.Contains(red.SessionPfx, "SECRET") || red.SessionPfx != "cs_live_" {
		t.Fatalf("返回摘要里带了会话号: %+v", red)
	}
	if _, has := bodies[0]["checkout_ui_mode"]; has {
		t.Errorf("不带 redirect 的请求体不该有 checkout_ui_mode: %v", bodies[0])
	}
	cp, _ := bodies[1]["credit_purchase_data"].(map[string]interface{})
	if cp["quantity"] != float64(250) || cp["unit"] != "credit" || cp["account_id"] != "acc-1" || bodies[1]["entry_point"] != "codex_purchase_credits_route" {
		t.Errorf("点数请求体不对: %v", bodies[1])
	}
	if hdrs[1].Get("Authorization") != "Bearer at" || hdrs[1].Get("ChatGPT-Account-ID") != "acc-1" || hdrs[1].Get("X-OpenAI-Expected-Account-Id") != "acc-1" {
		t.Errorf("请求头不对: %v", hdrs[1])
	}

	mode, err := p.ModeCheckout(context.Background(), s, "chatgptplusplan", "PH", "PHP", true)
	if err != nil || mode.Shape != ShapeURL {
		t.Fatalf("开通会话: %+v %v", mode, err)
	}
	if bodies[2]["plan_name"] != "chatgptplusplan" || bodies[2]["checkout_ui_mode"] != "redirect" {
		t.Errorf("开通请求体不对: %v", bodies[2])
	}
}

func TestProbeGetJSON(t *testing.T) {
	var seen string
	p := NewProbe(newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Method + " " + r.URL.RequestURI()
		w.Write([]byte(`{"will_renew":true}`))
	})))
	s := Session{AccessToken: "at"}
	q := url.Values{"account_id": {"acc-1"}}
	m, err := p.GetJSON(context.Background(), s, ProbeSubscriptions, q, "acc-1")
	if err != nil || m["will_renew"] != true || seen != "GET /backend-api/subscriptions?account_id=acc-1" {
		t.Fatalf("%v %v %q", m, err, seen)
	}
	if _, err := p.GetJSON(context.Background(), s, "/backend-api/subscriptions/cancel", nil, ""); !errors.Is(err, ErrProbeForbidden) {
		t.Fatalf("GetJSON 不应放行白名单外的路径: %v", err)
	}
}

func TestProbeURLParts(t *testing.T) {
	h, p := ProbeURLParts("https://user:pw@Pay.OpenAI.com/c/pay/cs_live_secret/more?token=abc#frag")
	if h != "pay.openai.com" || p != "/c/pay/…" {
		t.Fatalf("%q %q", h, p)
	}
	if h, p := ProbeURLParts("https://chatgpt.com/"); h != "chatgpt.com" || p != "/" {
		t.Fatalf("%q %q", h, p)
	}
	if h, _ := ProbeURLParts("not a url"); h != "" {
		t.Fatalf("坏链接应返回空: %q", h)
	}
}
