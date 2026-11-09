package handler

import "testing"

// 卡池策略接口：单日注资笔数上限默认 100，0 和负数拒绝，旧前端不传时沿用当前值。
func TestPolicyAPIDailyFundCountCap(t *testing.T) {
	cpEnv(t, false)
	get := func() map[string]any { return mwJSON(t, cpDo(AdminCardPoolGetPolicy, "GET", "/x", "/x", ``)) }
	if m := get(); m["daily_fund_count_cap"] != float64(100) {
		t.Fatalf("默认值应为 100: %v", m["daily_fund_count_cap"])
	}
	base := `"card_name_prefix":"POOL2","validity_months":12,"max_uses_per_card":1,"lease_minutes":30,"cancel_after_days":7,"health_window_hours":24,"product_fail_threshold":3,"card_fail_threshold":2`
	for _, bad := range []string{`"daily_fund_count_cap":0`, `"daily_fund_count_cap":-5`} {
		if w := cpDo(AdminCardPoolPutPolicy, "PUT", "/x", "/x", `{`+base+`,`+bad+`}`); w.Code != 400 {
			t.Fatalf("%s 应 400: %d %s", bad, w.Code, w.Body.String())
		}
	}
	if w := cpDo(AdminCardPoolPutPolicy, "PUT", "/x", "/x", `{`+base+`,"daily_fund_count_cap":37}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := cpDo(AdminCardPoolPutPolicy, "PUT", "/x", "/x", `{`+base+`}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if m := get(); m["daily_fund_count_cap"] != float64(37) {
		t.Fatalf("旧前端不传时应沿用当前值 37: %v", m["daily_fund_count_cap"])
	}
}
