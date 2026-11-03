package cardpool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

const (
	pathCardLimit = "/v1/card/limit"
	pathLimitUse  = "/v1/card/limit-usage/c1"
)

func TestSetCardLimitSendsNormalizedBody(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	mw.On("PUT", pathCardLimit, mwtest.OK(`{}`))
	got, err := s.SetCardLimit(context.Background(), "c1", marswallex.CardLimit{SingleLimit: " 50 ", DailyLimit: "100.5"})
	if err != nil {
		t.Fatal(err)
	}
	reqs := mw.Requests("PUT", pathCardLimit)
	if len(reqs) != 1 || string(reqs[0].Body) != `{"card_id":"c1","limit":{"single_limit":"50.00","daily_limit":"100.50"}}` {
		t.Fatalf("请求不符: %+v", reqs)
	}
	if got.SingleLimit != "50.00" || got.DailyLimit != "100.50" || got.MonthlyLimit != "" {
		t.Fatalf("返回的限额: %+v", got)
	}
}

func TestSetCardLimitRejectsBadInputWithoutCallingPlatform(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	mw.On("PUT", pathCardLimit, mwtest.OK(`{}`))
	for name, l := range map[string]marswallex.CardLimit{
		"全空":   {},
		"全是空白": {SingleLimit: "  "},
		"零":    {DailyLimit: "0"},
		"负数":   {SingleLimit: "-1"},
		"三位小数": {MonthlyLimit: "1.234"},
		"文字":   {SingleLimit: "abc"},
	} {
		_, err := s.SetCardLimit(context.Background(), "c1", l)
		var br *badRequestError
		if !errors.As(err, &br) {
			t.Errorf("%s: 应是 400 类错误: %v", name, err)
		}
	}
	if n := len(mw.Requests("PUT", pathCardLimit)); n != 0 {
		t.Fatalf("平台不应收到请求: %d", n)
	}
}

func TestSetCardLimitRefusesLeasedAndUnknownCard(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolLeased})
	mw.On("PUT", pathCardLimit, mwtest.OK(`{}`))
	if _, err := s.SetCardLimit(context.Background(), "c1", marswallex.CardLimit{DailyLimit: "10"}); !errors.Is(err, ErrCardBusy) {
		t.Fatalf("租出中的卡不能改限额: %v", err)
	}
	if _, err := s.SetCardLimit(context.Background(), "nope", marswallex.CardLimit{DailyLimit: "10"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不在池里的卡: %v", err)
	}
	if n := len(mw.Requests("PUT", pathCardLimit)); n != 0 {
		t.Fatalf("平台不应收到请求: %d", n)
	}
}

func TestSetCardLimitSurfacesUnknownOutcome(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	mw.On("PUT", pathCardLimit, mwtest.Unknown())
	_, err := s.SetCardLimit(context.Background(), "c1", marswallex.CardLimit{DailyLimit: "10"})
	if err == nil || marswallex.OutcomeOf(err) != marswallex.OutcomeUnknown {
		t.Fatalf("结果不明应如实返回: %v", err)
	}
}

func TestCardLimitUsage(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	mw.On("GET", pathLimitUse, mwtest.OK(`{"card_id":"c1","limit":{"daily_limit":"100.00"},"limit_remaining":{"daily_remaining":"60.00","monthly_remaining":"250.00"}}`))
	u, err := s.CardLimitUsage(context.Background(), "c1")
	if err != nil || u.Limit == nil || u.Limit.DailyLimit != "100.00" || u.LimitRemaining.DailyRemaining != "60.00" || u.LimitRemaining.MonthlyRemaining != "250.00" {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err := s.CardLimitUsage(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不在池里的卡: %v", err)
	}
}

// 开卡不自动设限：申卡请求里没有 limit。真实付款还没验证过，自动设限可能造成拒付。
func TestApplyCardsNeverSendsLimit(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	savePolicy(t, func(p *Policy) { p.OpenAmountCents = 500 })
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("POST", pathApply, mwtest.OK(`{"transaction_id":"T1"}`))
	if _, err := s.ApplyCards(context.Background(), "p1", "h1", 2, "admin"); err != nil {
		t.Fatal(err)
	}
	reqs := mw.Requests("POST", pathApply)
	if len(reqs) != 2 {
		t.Fatalf("申卡请求 %d", len(reqs))
	}
	for _, r := range reqs {
		if strings.Contains(string(r.Body), "limit") {
			t.Fatalf("申卡请求不应带限额: %s", r.Body)
		}
	}
	if n := len(mw.Requests("PUT", pathCardLimit)); n != 0 {
		t.Fatalf("开卡不应调用改限额接口: %d", n)
	}
}
