package handler

import (
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

func TestAdminCardLimitSetAndAudit(t *testing.T) {
	const path = "/v1/card/limit"
	c, mw, _ := cpEnv(t, false)
	cpInsertCard(t, c, "c1", cardpool.PoolAvailable, "")
	mw.On("PUT", path, mwtest.OK(`{}`))

	w := cpDo(AdminCardPoolSetCardLimit, "PUT", "/c/:id/limit", "/c/c1/limit", `{"single_limit":"50","daily_limit":"100.00"}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	reqs := mw.Requests("PUT", path)
	if len(reqs) != 1 || string(reqs[0].Body) != `{"card_id":"c1","limit":{"single_limit":"50.00","daily_limit":"100.00"}}` {
		t.Fatalf("平台请求不符: %+v", reqs)
	}
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.set_card_limit card_id=c1") || !strings.Contains(a, `daily="100.00"`) {
		t.Fatalf("应写审计: %s", a)
	}
}

func TestAdminCardLimitRejections(t *testing.T) {
	const path = "/v1/card/limit"
	c, mw, _ := cpEnv(t, false)
	cpInsertCard(t, c, "c1", cardpool.PoolAvailable, "")
	cpInsertCard(t, c, "c2", cardpool.PoolLeased, "tok")
	mw.On("PUT", path, mwtest.OK(`{}`))

	for body, want := range map[string]int{
		`{}`:                      400,
		`{"daily_limit":"0"}`:     400,
		`{"daily_limit":"1.001"}`: 400,
		`not json`:                400,
	} {
		if w := cpDo(AdminCardPoolSetCardLimit, "PUT", "/c/:id/limit", "/c/c1/limit", body); w.Code != want {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := cpDo(AdminCardPoolSetCardLimit, "PUT", "/c/:id/limit", "/c/c2/limit", `{"daily_limit":"10"}`); w.Code != 409 {
		t.Errorf("租出中的卡应 409: %d %s", w.Code, w.Body.String())
	}
	if w := cpDo(AdminCardPoolSetCardLimit, "PUT", "/c/:id/limit", "/c/nope/limit", `{"daily_limit":"10"}`); w.Code != 404 {
		t.Errorf("未知卡应 404: %d %s", w.Code, w.Body.String())
	}
	if n := len(mw.Requests("PUT", path)); n != 0 {
		t.Fatalf("被拒的请求不应到达平台: %d", n)
	}
}

// 平台结果不明：如实告诉前端（带 outcome），并留下失败审计供事后核对。
func TestAdminCardLimitUnknownOutcome(t *testing.T) {
	const path = "/v1/card/limit"
	c, mw, _ := cpEnv(t, false)
	cpInsertCard(t, c, "c1", cardpool.PoolAvailable, "")
	mw.On("PUT", path, mwtest.Unknown())
	w := cpDo(AdminCardPoolSetCardLimit, "PUT", "/c/:id/limit", "/c/c1/limit", `{"daily_limit":"10"}`)
	if w.Code != 502 || mwJSON(t, w)["outcome"] == nil {
		t.Fatalf("应 502 且带 outcome: %d %s", w.Code, w.Body.String())
	}
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.set_card_limit_failed card_id=c1") {
		t.Fatalf("应写失败审计: %s", a)
	}
}

func TestAdminCardLimitUsage(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	cpInsertCard(t, c, "c1", cardpool.PoolAvailable, "")
	mw.On("GET", "/v1/card/limit-usage/c1", mwtest.OK(`{"card_id":"c1","limit":{"daily_limit":"100.00"},"limit_remaining":{"daily_remaining":"60.00","monthly_remaining":"250.00"}}`))
	w := cpDo(AdminCardPoolCardLimitUsage, "GET", "/c/:id/limit-usage", "/c/c1/limit-usage", ``)
	m := mwJSON(t, w)
	rem, _ := m["limit_remaining"].(map[string]any)
	if w.Code != 200 || rem["daily_remaining"] != "60.00" || rem["monthly_remaining"] != "250.00" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := cpDo(AdminCardPoolCardLimitUsage, "GET", "/c/:id/limit-usage", "/c/nope/limit-usage", ``); w.Code != 404 {
		t.Fatalf("未知卡应 404: %d", w.Code)
	}
}
