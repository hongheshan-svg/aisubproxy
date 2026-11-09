package payengine

import (
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
)

func TestSubscriptionEligibility(t *testing.T) {
	if f := subscriptionEligibility(chatgpt.Account{PlanType: "free"}); f != nil {
		t.Fatalf("没有订阅应放行: %v", f)
	}
	f := subscriptionEligibility(chatgpt.Account{PlanType: "plus", HasActiveSubscription: true})
	if f == nil || f.code != "already_subscribed" || !strings.Contains(f.msg, "plus") {
		t.Fatalf("%v", f)
	}
}

func TestCreditAccountEligibility(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mut          func(*chatgpt.Account)
		code, msgHas string
	}{
		{"账号ID空", func(a *chatgpt.Account) { a.AccountID = " " }, "credit_account_unknown", "账号 ID"},
		{"没订阅", func(a *chatgpt.Account) { a.HasActiveSubscription = false }, "credit_no_subscription", "生效中的订阅"},
		{"team 套餐", func(a *chatgpt.Account) { a.PlanType = "team" }, "credit_plan_ineligible", `"team"`},
		{"套餐大小写和空白", func(a *chatgpt.Account) { a.PlanType = " PRO " }, "", ""},
		{"没返回账号类型", func(a *chatgpt.Account) { a.Structure = "" }, "credit_not_personal", "没有返回账号类型"},
		{"不是个人账号", func(a *chatgpt.Account) { a.Structure = "workspace" }, "credit_not_personal", "workspace"},
		{"符合", func(*chatgpt.Account) {}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc := creditAccount()
			tc.mut(&acc)
			f := creditAccountEligibility(acc)
			if tc.code == "" {
				if f != nil {
					t.Fatalf("应放行: %v", f)
				}
				return
			}
			if f == nil || f.code != tc.code || !strings.Contains(f.msg, tc.msgHas) {
				t.Fatalf("得到 %v，期望 %s 且含 %q", f, tc.code, tc.msgHas)
			}
		})
	}
}

func TestCreditBillingEligibility(t *testing.T) {
	for _, tc := range []struct {
		name       string
		delinquent *bool
		billing    string
		currency   string
		code       string
		msgHas     []string
	}{
		{"欠费状态未知", nil, "PHP", "PHP", "credit_delinquency_unknown", []string{"都没有返回欠费状态"}},
		{"有欠费", boolPtr(true), "PHP", "PHP", "credit_delinquent", nil},
		{"币种缺省按美元", boolPtr(false), "", "USD", "", nil},
		{"缺省美元与地区币种不符", boolPtr(false), "", "PHP", "credit_currency_mismatch", []string{"USD", "PHP"}},
		{"币种不分大小写", boolPtr(false), "php", "PHP", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc := creditAccount()
			acc.BillingCurrency = tc.billing
			f := creditBillingEligibility(acc, tc.delinquent, tc.currency)
			if tc.code == "" {
				if f != nil {
					t.Fatalf("应放行: %v", f)
				}
				return
			}
			if f == nil || f.code != tc.code {
				t.Fatalf("得到 %v，期望 %s", f, tc.code)
			}
			for _, s := range tc.msgHas {
				if !strings.Contains(f.msg, s) {
					t.Fatalf("msg %q 应含 %q", f.msg, s)
				}
			}
		})
	}
}
