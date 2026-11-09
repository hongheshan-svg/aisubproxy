package payengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

func inspectReq(plan string) InspectRequest {
	return InspectRequest{PlanKey: plan, Region: "PH", Session: testSession}
}

func TestInspectAccountSubscription(t *testing.T) {
	h := newHarness(t)
	v, err := h.svc.InspectAccount(ctx, inspectReq("plus"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != VerdictEligible || v.CurrentPlan != "free" || v.HasActiveSubscription || v.Email != "buyer@example.com" {
		t.Fatalf("free 账号应可开通: %+v", v)
	}

	h.gpt.account = chatgpt.Account{AccountID: "acc-1", PlanType: "Plus", HasActiveSubscription: true}
	v, err = h.svc.InspectAccount(ctx, inspectReq("plus"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != VerdictIneligible || v.BlockingCode != "already_subscribed" || v.CurrentPlan != "plus" || !v.HasActiveSubscription {
		t.Fatalf("已订阅应不符合: %+v", v)
	}

	h.gpt.account = chatgpt.Account{AccountID: "acc-1"}
	if v, _ = h.svc.InspectAccount(ctx, inspectReq("plus")); v.CurrentPlan != "free" {
		t.Fatalf("套餐为空应显示 free: %+v", v)
	}
}

func TestInspectAccountCredit(t *testing.T) {
	h := newCreditHarness(t)
	v, err := h.svc.InspectAccount(ctx, inspectReq("credit250"))
	if err != nil || v.Kind != VerdictEligible || v.CurrentPlan != "plus" || !v.HasActiveSubscription || v.BlockingCode != "" {
		t.Fatalf("应可买点数: %+v %v", v, err)
	}
	cases := []struct {
		name string
		acc  func(*chatgpt.Account)
		code string
	}{
		{"没有账号 ID", func(a *chatgpt.Account) { a.AccountID = "" }, "credit_account_unknown"},
		{"没有生效订阅", func(a *chatgpt.Account) { a.HasActiveSubscription, a.PlanType = false, "free" }, "credit_no_subscription"},
		{"套餐类型", func(a *chatgpt.Account) { a.PlanType = "team" }, "credit_plan_ineligible"},
		{"不是个人账号", func(a *chatgpt.Account) { a.Structure = "workspace" }, "credit_not_personal"},
		{"欠费", func(a *chatgpt.Account) { a.IsDelinquent = boolPtr(true) }, "credit_delinquent"},
		{"币种不一致", func(a *chatgpt.Account) { a.BillingCurrency = "EUR" }, "credit_currency_mismatch"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			acc := creditAccount()
			c.acc(&acc)
			h.gpt.account = acc
			v, err := h.svc.InspectAccount(ctx, inspectReq("credit250"))
			if err != nil || v.Kind != VerdictIneligible || v.BlockingCode != c.code {
				t.Fatalf("期望 ineligible/%s: %+v %v", c.code, v, err)
			}
		})
	}
	acc := creditAccount()
	acc.BillingCurrency = ""
	h.gpt.account = acc
	v, _ = h.svc.InspectAccount(ctx, inspectReq("credit250"))
	if v.BlockingCode != "credit_currency_mismatch" || v.AccountCurrency != "USD" || v.CodeCurrency != "PHP" {
		t.Fatalf("币种缺省按 USD: %+v", v)
	}
}

func TestInspectAccountCreditCallsSubscriptionStatus(t *testing.T) {
	h := newCreditHarness(t)
	acc := creditAccount()
	acc.IsDelinquent = nil
	h.gpt.account = acc
	h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{IsDelinquent: boolPtr(false)}, nil }
	v, err := h.svc.InspectAccount(ctx, inspectReq("credit250"))
	if err != nil || v.Kind != VerdictEligible {
		t.Fatalf("订阅接口说没欠费应可买: %+v %v", v, err)
	}
	if n := h.gpt.count("SubscriptionStatus"); n != 1 {
		t.Fatalf("应调一次 SubscriptionStatus: %d", n)
	}
	// 订阅接口也没有欠费状态：照付款时的规则不符合
	h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{}, nil }
	v, _ = h.svc.InspectAccount(ctx, inspectReq("credit250"))
	if v.Kind != VerdictIneligible || v.BlockingCode != "credit_delinquency_unknown" {
		t.Fatalf("两个接口都没有欠费状态: %+v", v)
	}
	// 订阅接口出错：未知，不是不符合
	h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{}, chatgpt.ErrUnauthorized }
	v, _ = h.svc.InspectAccount(ctx, inspectReq("credit250"))
	if v.Kind != VerdictSessionInvalid {
		t.Fatalf("订阅接口未授权应为 session_invalid: %+v", v)
	}
	h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{}, errors.New("boom") }
	v, _ = h.svc.InspectAccount(ctx, inspectReq("credit250"))
	if v.Kind != VerdictUnknown || v.Cause != "network" {
		t.Fatalf("订阅接口出错应为 unknown/network: %+v", v)
	}
}

func TestInspectAccountSessionInvalid(t *testing.T) {
	h := newHarness(t)
	r := inspectReq("plus")
	r.Session = "{}"
	if v, err := h.svc.InspectAccount(ctx, r); err != nil || v.Kind != VerdictSessionInvalid {
		t.Fatalf("解析失败: %+v %v", v, err)
	}
	h.gpt.refreshErr = chatgpt.ErrUnauthorized
	if v, err := h.svc.InspectAccount(ctx, inspectReq("plus")); err != nil || v.Kind != VerdictSessionInvalid {
		t.Fatalf("Refresh 未授权: %+v %v", v, err)
	}
	h.gpt.refreshErr = nil
	h.gpt.accountErr = chatgpt.ErrUnauthorized
	if v, err := h.svc.InspectAccount(ctx, inspectReq("plus")); err != nil || v.Kind != VerdictSessionInvalid {
		t.Fatalf("Account 未授权: %+v %v", v, err)
	}
}

func TestInspectAccountUnknown(t *testing.T) {
	want := func(t *testing.T, v AccountVerdict, err error, cause string) {
		t.Helper()
		if err != nil || v.Kind != VerdictUnknown || v.Cause != cause {
			t.Fatalf("期望 unknown/%s: %+v %v", cause, v, err)
		}
	}
	t.Run("plan", func(t *testing.T) {
		h := newHarness(t)
		v, err := h.svc.InspectAccount(ctx, inspectReq("no_such_plan"))
		want(t, v, err, "plan")
	})
	t.Run("proxy", func(t *testing.T) {
		h := newHarness(t)
		if err := db.SetSecretSetting(settingProxies, "not-json"); err != nil {
			t.Fatal(err)
		}
		v, err := h.svc.InspectAccount(ctx, inspectReq("plus"))
		want(t, v, err, "proxy")
	})
	t.Run("client", func(t *testing.T) {
		h := newHarness(t)
		svc := New(Options{ChatGPT: func(string) (ChatGPT, error) { return nil, errors.New("boom") }, Alert: h.alerts.add})
		v, err := svc.InspectAccount(ctx, inspectReq("plus"))
		want(t, v, err, "client")
	})
	t.Run("cloudflare", func(t *testing.T) {
		h := newHarness(t)
		h.gpt.accountErr = chatgpt.ErrBlocked
		v, err := h.svc.InspectAccount(ctx, inspectReq("plus"))
		want(t, v, err, "cloudflare")
		if n := len(h.alerts.Texts()); n != 0 {
			t.Fatalf("核对不应告警: %v", h.alerts.Texts())
		}
	})
	t.Run("rate_limited", func(t *testing.T) {
		h := newHarness(t)
		h.gpt.accountErr = &chatgpt.HTTPError{Status: 429}
		v, err := h.svc.InspectAccount(ctx, inspectReq("plus"))
		want(t, v, err, "rate_limited")
	})
	t.Run("network", func(t *testing.T) {
		h := newHarness(t)
		h.gpt.accountErr = errors.New("connection reset")
		v, err := h.svc.InspectAccount(ctx, inspectReq("plus"))
		want(t, v, err, "network")
	})
	t.Run("timeout", func(t *testing.T) {
		old := inspectTimeout
		inspectTimeout = 30 * time.Millisecond
		defer func() { inspectTimeout = old }()
		h := newHarness(t)
		h.gpt.accountCtx = func(c context.Context, _ chatgpt.Session, _ int) (chatgpt.Account, error) {
			<-c.Done()
			return chatgpt.Account{}, c.Err()
		}
		v, err := h.svc.InspectAccount(ctx, inspectReq("plus"))
		want(t, v, err, "timeout")
	})
}

func TestInspectAccountCallerCanceled(t *testing.T) {
	h := newHarness(t)
	c, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := h.svc.InspectAccount(c, inspectReq("plus")); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消应返回 Canceled: %v", err)
	}
	// 查询中途被调用方取消
	c, cancel = context.WithCancel(ctx)
	h.gpt.accountCtx = func(cc context.Context, _ chatgpt.Session, _ int) (chatgpt.Account, error) {
		cancel()
		<-cc.Done()
		return chatgpt.Account{}, cc.Err()
	}
	v, err := h.svc.InspectAccount(c, inspectReq("plus"))
	if !errors.Is(err, context.Canceled) || v != (AccountVerdict{}) {
		t.Fatalf("中途取消: %+v %v", v, err)
	}
}

func TestInspectAccountNilService(t *testing.T) {
	var s *Service
	v, err := s.InspectAccount(ctx, inspectReq("plus"))
	if err != nil || v.Kind != VerdictUnknown || v.Cause != "engine" {
		t.Fatalf("nil 服务: %+v %v", v, err)
	}
}

func TestInspectAccountReadOnly(t *testing.T) {
	h := newCreditHarness(t)
	acc := creditAccount()
	acc.IsDelinquent = nil
	h.gpt.account = acc
	h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{IsDelinquent: boolPtr(false)}, nil }
	if v, err := h.svc.InspectAccount(ctx, inspectReq("credit250")); err != nil || v.Kind != VerdictEligible {
		t.Fatalf("%+v %v", v, err)
	}
	want := []string{"New", "Refresh", "Account", "SubscriptionStatus"}
	got := h.gpt.order()
	if len(got) != len(want) {
		t.Fatalf("调用序列: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("调用序列: %v", got)
		}
	}
	if _, total, _ := listOrders(ctx, "", 1, 0); total != 0 {
		t.Fatalf("不应建单: %d", total)
	}
	if h.pool.total() != 0 || len(h.br.browsers()) != 0 || len(h.alerts.Texts()) != 0 {
		t.Fatal("不应碰卡池、浏览器、告警")
	}
}
