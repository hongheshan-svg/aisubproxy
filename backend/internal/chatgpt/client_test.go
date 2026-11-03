package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	c.BaseURL = srv.URL
	return c
}

func TestNewRejectsBadProxy(t *testing.T) {
	if _, err := New("ftp://x"); err == nil {
		t.Error("ftp 代理应报错")
	}
	for _, p := range []string{"socks5://u:p@127.0.0.1:1080", ""} {
		if _, err := New(p); err != nil {
			t.Errorf("New(%q) 不应报错: %v", p, err)
		}
	}
}

func TestRefreshUpdatesAccessToken(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/session" {
			t.Errorf("路径 %s", r.URL.Path)
		}
		ck, err := r.Cookie("__Secure-next-auth.session-token")
		if err != nil || ck.Value != "st-1" {
			t.Errorf("cookie 不符: %v %v", ck, err)
		}
		w.Write([]byte(`{"accessToken":"new"}`))
	}))
	s := Session{AccessToken: "old", SessionToken: "st-1", Email: "a@b.c"}
	if err := c.Refresh(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if s.AccessToken != "new" {
		t.Fatalf("AccessToken = %q", s.AccessToken)
	}
}

func TestAccountParsesPlan(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/backend-api/accounts/check/v4-2023-04-27") {
			t.Errorf("路径 %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer at-1" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"accounts":{"default":{"account":{"account_id":"acc-1","plan_type":"plus"},"entitlement":{"has_active_subscription":false}}}}`))
	}))
	got, err := c.Account(context.Background(), Session{AccessToken: "at-1", Email: "a@b.c"})
	if err != nil {
		t.Fatal(err)
	}
	want := Account{AccountID: "acc-1", Email: "a@b.c", PlanType: "free", HasActiveSubscription: false}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestCreateCheckoutRequestShape(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/backend-api/payments/checkout" {
			t.Errorf("请求 %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer at-1" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var body struct {
			PlanName       string `json:"plan_name"`
			BillingDetails struct {
				Country  string `json:"country"`
				Currency string `json:"currency"`
			} `json:"billing_details"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("解析请求体: %v", err)
		}
		if body.PlanName != "chatgptplusplan" || body.BillingDetails.Country != "PH" || body.BillingDetails.Currency != "PHP" {
			t.Errorf("请求体不符: %+v", body)
		}
		w.Write([]byte(`{"checkout_session_id":"cs_live_x","url":"https://pay.openai.com/c/pay/cs_live_x"}`))
	}))
	got, err := c.CreateCheckout(context.Background(), Session{AccessToken: "at-1"}, "chatgptplusplan", "PH", "PHP")
	if err != nil {
		t.Fatal(err)
	}
	want := Checkout{SessionID: "cs_live_x", URL: "https://pay.openai.com/c/pay/cs_live_x"}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestUnauthorizedMapsToErr(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	s := Session{AccessToken: "x", SessionToken: "y"}
	if err := c.Refresh(context.Background(), &s); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Refresh: %v", err)
	}
	if _, err := c.Account(context.Background(), s); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Account: %v", err)
	}
	if _, err := c.CreateCheckout(context.Background(), s, "p", "PH", "PHP"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("CreateCheckout: %v", err)
	}
}
