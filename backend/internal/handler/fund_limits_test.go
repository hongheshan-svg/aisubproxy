package handler

import (
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

func TestPolicyAPIFundLimits(t *testing.T) {
	cpEnv(t, false)
	get := func() map[string]any { return mwJSON(t, cpDo(AdminCardPoolGetPolicy, "GET", "/x", "/x", ``)) }
	if m := get(); m["max_fund_cents"] != float64(50000) || m["max_wallet_op_cents"] != float64(200000) || m["daily_fund_cap_cents"] != float64(200000) {
		t.Fatalf("默认值: %v", m)
	}
	base := `"card_name_prefix":"POOL2","validity_months":12,"max_uses_per_card":1,"lease_minutes":30,"cancel_after_days":7,"health_window_hours":24,"product_fail_threshold":3,"card_fail_threshold":2`

	// 显式传 0 或负数：拒绝，不改动
	for _, bad := range []string{`"max_fund_cents":0`, `"max_fund_cents":-1`, `"max_wallet_op_cents":0`, `"daily_fund_cap_cents":-100`} {
		if w := cpDo(AdminCardPoolPutPolicy, "PUT", "/x", "/x", `{`+base+`,`+bad+`}`); w.Code != 400 {
			t.Fatalf("%s 应 400: %d %s", bad, w.Code, w.Body.String())
		}
	}
	if m := get(); m["max_fund_cents"] != float64(50000) {
		t.Fatalf("不应改动: %v", m)
	}

	if w := cpDo(AdminCardPoolPutPolicy, "PUT", "/x", "/x", `{`+base+`,"max_fund_cents":12300,"max_wallet_op_cents":45600,"daily_fund_cap_cents":78900}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// 旧前端不传这三个字段：沿用刚才的值，不被重置回默认
	if w := cpDo(AdminCardPoolPutPolicy, "PUT", "/x", "/x", `{`+base+`}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if m := get(); m["max_fund_cents"] != float64(12300) || m["max_wallet_op_cents"] != float64(45600) || m["daily_fund_cap_cents"] != float64(78900) {
		t.Fatalf("不传时应沿用当前值: %v", m)
	}
}

func TestFundCardOverLimitAPI(t *testing.T) {
	const pathRecharge = "/v1/card/recharge-transaction"
	c, mw, _ := cpEnv(t, false)
	cpInsertCard(t, c, "c1", cardpool.PoolAvailable, "")
	mw.On("POST", pathRecharge, mwtest.OK(`{"transaction_id":"T1","status":"APPLY"}`))
	if w := cpDo(AdminCardPoolLeaseCard, "POST", "/c/:id", "/c/c1", ``); w.Code != 200 {
		t.Fatalf("租卡: %d %s", w.Code, w.Body.String())
	}
	w := cpDo(AdminCardPoolFundCard, "POST", "/c/:id", "/c/c1", `{"amount":"500.01"}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "上限") {
		t.Fatalf("超限应 400 并写明: %d %s", w.Code, w.Body.String())
	}
	if n := len(mw.Requests("POST", pathRecharge)); n != 0 {
		t.Fatalf("平台不应收到请求: %d", n)
	}
	// 上限之内照常发出，证明上面是被上限挡住的，也证明路径没写错
	if w := cpDo(AdminCardPoolFundCard, "POST", "/c/:id", "/c/c1", `{"amount":"500.00"}`); w.Code != 200 {
		t.Fatalf("上限之内应成功: %d %s", w.Code, w.Body.String())
	}
	if n := len(mw.Requests("POST", pathRecharge)); n != 1 {
		t.Fatalf("平台应收到 1 次: %d", n)
	}
}
