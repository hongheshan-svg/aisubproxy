package handler

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
)

func loadPolicy(t *testing.T) cardpool.Policy {
	t.Helper()
	p, err := cardpool.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func putSelection(t *testing.T, body string) cardpool.Selection {
	t.Helper()
	w := cpDo(AdminCardPoolPutSelection, "PUT", "/x", "/x", body)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /selection: %d %s", w.Code, w.Body.String())
	}
	var s cardpool.Selection
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func putPolicy(t *testing.T, body string) {
	t.Helper()
	if w := cpDo(AdminCardPoolPutPolicy, "PUT", "/x", "/x", body); w.Code != http.StatusOK {
		t.Fatalf("PUT /policy: %d %s", w.Code, w.Body.String())
	}
}

// 卡池页提交的策略里不再带选卡字段。
const policyWithoutSelection = `"card_name_prefix":"POOL2","validity_months":12,"lease_minutes":30,"cancel_after_days":7,"max_fund_cents":12345,"max_wallet_op_cents":50000,"daily_fund_cap_cents":90000`

// PUT /selection 只改 6 个选卡字段，其他字段保持不变，并记审计。
func TestPutSelectionOnlyTouchesSelection(t *testing.T) {
	c, _, _ := cpEnv(t, false)
	putPolicy(t, `{`+policyWithoutSelection+`}`)

	s := putSelection(t, `{"plan_products":{"plus":["p1","p2"],"default":["p3"]},"auto_suspend":false,"max_uses_per_card":3,"health_window_hours":48,"product_fail_threshold":5,"card_fail_threshold":4}`)
	if !reflect.DeepEqual(s.PlanProducts, map[string][]string{"plus": {"p1", "p2"}, "default": {"p3"}}) || s.AutoSuspend || s.MaxUsesPerCard != 3 {
		t.Fatalf("返回 = %+v", s)
	}
	p := loadPolicy(t)
	if p.CardNamePrefix != "POOL2" || p.MaxFundCents != 12345 || p.ValidityMonths != 12 || p.LeaseMinutes != 30 {
		t.Fatalf("其他字段被改动: %+v", p)
	}
	if p.AutoSuspend || p.MaxUsesPerCard != 3 || p.HealthWindowHours != 48 || p.ProductFailThreshold != 5 || p.CardFailThreshold != 4 || len(p.PlanProducts) != 2 {
		t.Fatalf("选卡字段没生效: %+v", p)
	}
	if !strings.Contains(cpAudit(t, c), "cardpool.selection") {
		t.Fatal("审计缺少 cardpool.selection")
	}

	// 去掉一个套餐要真的去掉（不能和原来的合并）；没带的字段保持当前值
	s = putSelection(t, `{"plan_products":{"plus":["p2"]}}`)
	if !reflect.DeepEqual(s.PlanProducts, map[string][]string{"plus": {"p2"}}) || s.MaxUsesPerCard != 3 || s.AutoSuspend {
		t.Fatalf("第二次保存 = %+v", s)
	}

	w := cpDo(AdminCardPoolGetSelection, "GET", "/x", "/x", ``)
	var got cardpool.Selection
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("GET /selection: %d %s", w.Code, w.Body.String())
	}
}

// 卡池页（新前端）提交的策略不带选卡字段：选卡字段保持当前值。
func TestPutPolicyKeepsSelectionWhenAbsent(t *testing.T) {
	cpEnv(t, false)
	putSelection(t, `{"plan_products":{"plus":["p1"]},"max_uses_per_card":5,"auto_suspend":false}`)
	putPolicy(t, `{`+strings.Replace(policyWithoutSelection, "POOL2", "POOL3", 1)+`}`)
	p := loadPolicy(t)
	if p.CardNamePrefix != "POOL3" {
		t.Fatalf("前缀没改: %s", p.CardNamePrefix)
	}
	if !reflect.DeepEqual(p.PlanProducts, map[string][]string{"plus": {"p1"}}) || p.MaxUsesPerCard != 5 || p.AutoSuspend {
		t.Fatalf("选卡字段被冲掉: %+v", p.Selection())
	}
}

// 升级前就开着的旧卡池页提交整份策略：选卡字段照旧生效。
func TestPutPolicyFullBodyStillApplies(t *testing.T) {
	cpEnv(t, false)
	putSelection(t, `{"plan_products":{"plus":["p1"]},"max_uses_per_card":5}`)
	putPolicy(t, `{`+policyWithoutSelection+`,"plan_products":{},"max_uses_per_card":2,"auto_suspend":true,"health_window_hours":24,"product_fail_threshold":3,"card_fail_threshold":2}`)
	p := loadPolicy(t)
	if len(p.PlanProducts) != 0 || p.MaxUsesPerCard != 2 || !p.AutoSuspend {
		t.Fatalf("整份提交没生效: %+v", p.Selection())
	}
}

// 两个页面交替保存，互不覆盖。
func TestSelectionAndPolicyInterleave(t *testing.T) {
	cpEnv(t, false)
	putPolicy(t, `{`+policyWithoutSelection+`}`)
	putSelection(t, `{"plan_products":{"plus":["p1"]}}`)
	putPolicy(t, `{`+strings.Replace(policyWithoutSelection, "12345", "22222", 1)+`}`)
	putSelection(t, `{"max_uses_per_card":7}`)
	p := loadPolicy(t)
	if p.MaxFundCents != 22222 || p.MaxUsesPerCard != 7 || !reflect.DeepEqual(p.PlanProducts, map[string][]string{"plus": {"p1"}}) {
		t.Fatalf("交替保存后 = %+v", p)
	}
}

func TestPutSelectionValidates(t *testing.T) {
	cpEnv(t, false)
	putSelection(t, `{"max_uses_per_card":4}`)
	for _, body := range []string{`{"max_uses_per_card":0}`, `{"health_window_hours":721}`, `{"card_fail_threshold":0}`, `{"plan_products":`} {
		if w := cpDo(AdminCardPoolPutSelection, "PUT", "/x", "/x", body); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: 应返回 400，得到 %d %s", body, w.Code, w.Body.String())
		}
	}
	if p := loadPolicy(t); p.MaxUsesPerCard != 4 {
		t.Fatalf("校验不通过不应写入: %d", p.MaxUsesPerCard)
	}
}
