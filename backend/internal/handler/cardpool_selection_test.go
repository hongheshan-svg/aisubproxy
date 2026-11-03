package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/db"
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

// 读不出当前策略时，新卡池页（不带选卡字段）的保存要拒绝：
// 否则选卡字段会被默认值顶掉，plan_products 清空，MarsWallex 就停止供卡。
// 旧卡池页整份提交时照旧生效。
func TestPutPolicyRefusesWhenPolicyUnreadable(t *testing.T) {
	cpEnv(t, false)
	putSelection(t, `{"plan_products":{"plus":["p1"]},"max_uses_per_card":5}`)
	const bad = `{"plan_products":`
	if err := db.SetSetting(cardpool.PolicySettingKey, bad); err != nil {
		t.Fatal(err)
	}
	w := cpDo(AdminCardPoolPutPolicy, "PUT", "/x", "/x", `{`+policyWithoutSelection+`}`)
	if w.Code == http.StatusOK {
		t.Fatalf("读不出当前策略时应拒绝保存，得到 200 %s", w.Body.String())
	}
	if v, _ := db.GetSetting(cardpool.PolicySettingKey); v != bad {
		t.Fatalf("拒绝时不应写库，库里变成了 %s", v)
	}
	putPolicy(t, `{`+policyWithoutSelection+`,"plan_products":{"plus":["p2"]},"max_uses_per_card":2,"auto_suspend":true,"health_window_hours":24,"product_fail_threshold":3,"card_fail_threshold":2}`)
	if p := loadPolicy(t); !reflect.DeepEqual(p.PlanProducts, map[string][]string{"plus": {"p2"}}) {
		t.Fatalf("整份提交应照旧生效: %+v", p.Selection())
	}
}

// 用管道当请求体：先送一半，等处理函数开始读请求体，再放行剩下的。
func startSlowPut(t *testing.T, h gin.HandlerFunc, body string) (finish func() *httptest.ResponseRecorder) {
	t.Helper()
	pr, pw := io.Pipe()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("username", "alice") })
		r.Handle("PUT", "/x", h)
		req := httptest.NewRequest("PUT", "/x", pr)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		done <- w
	}()
	half := len(body) / 2
	if _, err := pw.Write([]byte(body[:half])); err != nil {
		t.Fatal(err)
	}
	return func() *httptest.ResponseRecorder {
		if _, err := pw.Write([]byte(body[half:])); err != nil {
			t.Fatal(err)
		}
		pw.Close()
		return <-done
	}
}

// 卡池页的请求体还在上传时，选卡规则页保存了：卡池页保存完，选卡规则不能被冲回旧值。
func TestPutPolicyDoesNotClobberConcurrentSelection(t *testing.T) {
	cpEnv(t, false)
	putPolicy(t, `{`+policyWithoutSelection+`}`)
	putSelection(t, `{"plan_products":{"plus":["p1"]},"max_uses_per_card":2}`)

	finish := startSlowPut(t, AdminCardPoolPutPolicy, `{`+strings.Replace(policyWithoutSelection, "12345", "33333", 1)+`}`)
	putSelection(t, `{"plan_products":{"plus":["p9"]},"max_uses_per_card":7}`)
	if w := finish(); w.Code != http.StatusOK {
		t.Fatalf("PUT /policy: %d %s", w.Code, w.Body.String())
	}
	p := loadPolicy(t)
	if p.MaxFundCents != 33333 {
		t.Fatalf("卡池页的改动没生效: %d", p.MaxFundCents)
	}
	if !reflect.DeepEqual(p.PlanProducts, map[string][]string{"plus": {"p9"}}) || p.MaxUsesPerCard != 7 {
		t.Fatalf("选卡规则被冲回旧值: %+v", p.Selection())
	}
}

// 选卡规则页的请求体还在上传时，卡池页保存了：选卡规则页保存完，卡池字段不能被冲回旧值。
func TestPutSelectionDoesNotClobberConcurrentPolicy(t *testing.T) {
	cpEnv(t, false)
	putPolicy(t, `{`+policyWithoutSelection+`}`)

	finish := startSlowPut(t, AdminCardPoolPutSelection, `{"plan_products":{"plus":["p9"]},"max_uses_per_card":7}`)
	putPolicy(t, `{`+strings.Replace(policyWithoutSelection, "12345", "44444", 1)+`}`)
	if w := finish(); w.Code != http.StatusOK {
		t.Fatalf("PUT /selection: %d %s", w.Code, w.Body.String())
	}
	p := loadPolicy(t)
	if p.MaxFundCents != 44444 {
		t.Fatalf("卡池字段被冲回旧值: %d", p.MaxFundCents)
	}
	if !reflect.DeepEqual(p.PlanProducts, map[string][]string{"plus": {"p9"}}) || p.MaxUsesPerCard != 7 {
		t.Fatalf("选卡规则没生效: %+v", p.Selection())
	}
}
