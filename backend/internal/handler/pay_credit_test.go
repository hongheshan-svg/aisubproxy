package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
)

// enableCredit 启用 credit250/PH。假浏览器的页面金额是 ₱1,120.00，所以预期金额和上限按它配；卡池给这一档配上 p1 产品。
func (e *localEnv) enableCredit() {
	e.t.Helper()
	tbl, err := payengine.LoadPlanTable()
	if err != nil {
		e.t.Fatal(err)
	}
	for i := range tbl {
		if tbl[i].Key == "credit250" {
			tbl[i].Regions["PH"] = payengine.RegionConf{Enabled: true, Currency: "PHP", ExpectMinor: 112000, CapMinor: 120000, FloorMinor: 100000, FundCents: 3000}
		}
	}
	if err := payengine.SavePlanTable(tbl); err != nil {
		e.t.Fatal(err)
	}
	pol, err := cardpool.LoadPolicy()
	if err != nil {
		e.t.Fatal(err)
	}
	pol.PlanProducts["credit250"] = []string{"p1"}
	if err := cardpool.SavePolicy(pol); err != nil {
		e.t.Fatal(err)
	}
}

func (e *localEnv) issueStatus(plan, region string) (int, string) {
	w := e.do("POST", "/admin/cdks/issue", localBody(e.t, map[string]any{"provider": "marswallex", "plan": plan, "region": region, "count": 1, "funding_confirmed": true}))
	return w.Code, w.Body.String()
}

// 点数码的发码门槛独立计算：只有订阅开通单不行，只有点数单也发不了订阅码；人工定论的点数单不算。
func TestCreditCodeIssueGate(t *testing.T) {
	t.Run("只有开通单发不了点数码", func(t *testing.T) {
		e := newLocalEnv(t)
		e.enableCredit()
		e.seedRealPayment()
		if code, body := e.issueStatus("credit250", "PH"); code != http.StatusBadRequest || !strings.Contains(body, "真实点数付款") {
			t.Fatalf("%d %s", code, body)
		}
		if code, body := e.issueStatus("plus", "PH"); code != http.StatusOK {
			t.Fatalf("开通码照常能发: %d %s", code, body)
		}
		e.seedRealCreditPayment()
		if code, body := e.issueStatus("credit250", "PH"); code != http.StatusOK {
			t.Fatalf("有了点数单就能发: %d %s", code, body)
		}
	})
	t.Run("只有点数单发不了开通码", func(t *testing.T) {
		e := newLocalEnv(t)
		e.enableCredit()
		e.seedRealCreditPayment()
		if code, body := e.issueStatus("plus", "PH"); code != http.StatusBadRequest || !strings.Contains(body, "系统自动确认成功的真实付款") {
			t.Fatalf("%d %s", code, body)
		}
		if code, body := e.issueStatus("credit250", "PH"); code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
	})
	t.Run("人工定论的点数单不算", func(t *testing.T) {
		e := newLocalEnv(t)
		e.enableCredit()
		e.seedRealCreditPayment()
		if _, err := db.DB.Exec(`UPDATE pay_orders SET resolved_by = 'alice' WHERE kind = 'credit'`); err != nil {
			t.Fatal(err)
		}
		if code, body := e.issueStatus("credit250", "PH"); code != http.StatusBadRequest || !strings.Contains(body, "真实点数付款") {
			t.Fatalf("%d %s", code, body)
		}
	})
}

// 预览、预检按码的种类给字段：点数码要求已订阅、要勾选同意；开通码只给无订阅账号、到期不自动续费。
func TestCreditCodePreviewFields(t *testing.T) {
	e := newLocalEnv(t)
	e.enableCredit()
	e.seedRealPayment()
	e.seedRealCreditPayment()
	code, _ := e.issueCode("credit250", "PH")
	m := localJSON(t, e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code})))
	for k, want := range map[string]any{
		"kind": "credit", "credit_quantity": float64(250), "requires_subscription": true, "requires_no_subscription": false,
		"requires_credit_terms": true, "currency": "PHP", "plan_note": "仅限已订阅 Plus 或 Pro 的账号，按账号计费币种付款",
	} {
		if m[k] != want {
			t.Errorf("点数码 %s: %v，期望 %v", k, m[k], want)
		}
	}
	if _, ok := m["auto_renew"]; ok {
		t.Error("点数码没有续费字段")
	}
	token := m["redemption_token"].(string)
	w := e.do("POST", "/public/cdk/preflight", localBody(t, map[string]any{"code": code, "redemption_token": token,
		"credential": map[string]any{"mode": "session", "session": localSession("c@example.com")}}))
	if p := localJSON(t, w); w.Code != http.StatusOK || p["kind"] != "credit" || p["requires_subscription"] != true || p["requires_no_subscription"] != false {
		t.Fatalf("预检 %d %v", w.Code, p)
	}

	sub, _ := e.issueCode("plus", "PH")
	s := localJSON(t, e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": sub})))
	for k, want := range map[string]any{"kind": "subscription", "auto_renew": false, "requires_no_subscription": true, "requires_subscription": false} {
		if s[k] != want {
			t.Errorf("开通码 %s: %v，期望 %v", k, s[k], want)
		}
	}
	if _, ok := s["requires_credit_terms"]; ok {
		t.Error("开通码不需要勾选点数须知")
	}
}

// 点数码没勾同意就不建单、不占码；勾了之后走完付款：只建点数结账会话、确认「保存付款信息」、授权相符后成功，不关闭续费。
func TestCreditCodeRedeemEndToEnd(t *testing.T) {
	e := newLocalEnv(t)
	e.enableCredit()
	e.seedRealPayment()
	e.seedRealCreditPayment()
	e.gpt.creditReady = true
	e.start()
	code, id := e.issueCode("credit250", "PH")
	token, pf := e.preflight(code, localSession("credit@example.com"))

	for _, agree := range []any{nil, false, "true", 1} {
		body := map[string]any{"redemption_token": token, "preflight_token": pf, "client_request_id": "c1"}
		if agree != nil {
			body["agree_credit_terms"] = agree
		}
		w := e.do("POST", "/public/cdk/redeem", localBody(t, body))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "点数兑换须知") {
			t.Fatalf("没勾同意（%v）应拒绝: %d %s", agree, w.Code, w.Body.String())
		}
	}
	if n := cdkOrderCount(t, id); n != 0 || e.gpt.creditBuyCount() != 0 {
		t.Fatalf("没勾同意不能建单: %d", n)
	}

	// 这张卡在提交前后有一笔金额相符的成功授权（平台状态原样入库）
	now := time.Now().Unix()
	if _, err := db.DB.Exec(`INSERT INTO mw_card_transactions (transaction_id, kind, card_id, status, txn_amount, txn_currency, occurred_at, updated_at)
		VALUES ('A1', 'AUTH', 'c1', 'APPROVED', '-1120.00', 'PHP', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	w := e.do("POST", "/public/cdk/redeem", localBody(t, map[string]any{
		"redemption_token": token, "preflight_token": pf, "client_request_id": "c1", "agree_credit_terms": true,
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("勾了同意应能兑换: %d %s", w.Code, w.Body.String())
	}
	e.waitResult(token, "completed")

	var no, kind, renewal, saved string
	var qty int
	if err := db.DB.QueryRow(`SELECT order_no, kind, credit_quantity, renewal_state, card_saved FROM pay_orders WHERE source = 'cdk' AND source_ref = ?`,
		db.LocalCDKSourceRef(id)).Scan(&no, &kind, &qty, &renewal, &saved); err != nil {
		t.Fatal(err)
	}
	if kind != "credit" || qty != 250 || renewal != "" || saved != "not_saved" {
		t.Fatalf("单据不符: %s %d %q %q", kind, qty, renewal, saved)
	}
	if n := e.gpt.creditBuyCount(); n != 1 {
		t.Fatalf("点数结账 %d 次", n)
	}
	time.Sleep(50 * time.Millisecond) // 续费协程 5 毫秒一轮
	if n := e.gpt.cancelCount(); n != 0 {
		t.Fatalf("点数单不能关闭续费: %d", n)
	}
	e.br.mu.Lock()
	checks := e.br.savePayChecks
	e.br.mu.Unlock()
	if _, fills, submits := e.br.counts(); fills != 1 || submits != 1 || checks != 1 {
		t.Fatalf("填表 %d 次、点支付 %d 次、确认保存付款信息 %d 次", fills, submits, checks)
	}
	if n := len(e.mw.Requests("POST", mwPathRecharge)); n != 1 {
		t.Fatalf("注资 %d 次", n)
	}
}

// ACC2-7：单据所属服务商没有卡池时，详情照常给出单据和时间线，交易部分说明「这家服务商没有卡池」，不报错。
func TestPayOrderDetailWithoutPool(t *testing.T) {
	e := newLocalEnv(t)
	no := e.insertProviderOrder("gonepay", "admin_test", "admin", 0, "gone@example.com", "needs_review", "", true)
	if _, err := db.DB.Exec(`UPDATE pay_orders SET card_id = 'c-gone' WHERE order_no = ?`, no); err != nil {
		t.Fatal(err)
	}
	w := payDo(AdminPayGet, "GET", "/orders/:no", "/orders/"+no, "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var d struct {
		Order         map[string]any `json:"order"`
		Events        []any          `json:"events"`
		Evidence      []any          `json:"evidence"`
		EvidenceError string         `json:"evidence_error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Order["order_no"] != no || d.Events == nil || d.Evidence == nil || d.EvidenceError != "这家服务商没有卡池，卡的交易记录无法查询" {
		t.Fatalf("%s", w.Body.String())
	}
}

// 手动重试关闭续费：成功的真实开通单、续费没关掉、会话还在才行；关掉之后再点说明原因。列表能按续费进度筛。
func TestPayRetryRenewalEndpoint(t *testing.T) {
	e := newLocalEnv(t)
	w := payDo(AdminPaySubmit, "POST", "/orders", "/orders", paySubmitBody("plus", "PH", paySession("r@example.com"), false))
	var ok struct {
		OrderNo string `json:"order_no"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ok); err != nil || ok.OrderNo == "" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	retry := func() *httptest.ResponseRecorder {
		return payDo(AdminPayRetryRenewal, "POST", "/orders/:no/renewal/retry", "/orders/"+ok.OrderNo+"/renewal/retry", "")
	}
	// 还没成功：不能重试，也不碰 ChatGPT
	if w := retry(); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "只有成功的真实开通单") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	now := time.Now().Unix()
	if _, err := db.DB.Exec(`UPDATE pay_orders SET state = 'succeeded', account_id = 'acc-1', submitted_at = ?, finished_at = ?,
		renewal_state = 'failed', renewal_attempts = 4 WHERE order_no = ?`, now, now, ok.OrderNo); err != nil {
		t.Fatal(err)
	}
	l := payDo(AdminPayList, "GET", "/orders", "/orders?renewal=failed", "")
	if !strings.Contains(l.Body.String(), ok.OrderNo) || !strings.Contains(l.Body.String(), `"total":1`) {
		t.Fatalf("按续费进度筛: %s", l.Body.String())
	}
	if l := payDo(AdminPayList, "GET", "/orders", "/orders?renewal=cancelled", ""); !strings.Contains(l.Body.String(), `"total":0`) {
		t.Fatalf("%s", l.Body.String())
	}

	w = retry()
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var r struct {
		Order map[string]any `json:"order"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || r.Order["renewal_state"] != "cancelled" || r.Order["has_session"] != false {
		t.Fatalf("应已关闭并清空会话: %s", w.Body.String())
	}
	if n := e.gpt.cancelCount(); n != 1 {
		t.Fatalf("关闭续费 %d 次", n)
	}
	if a := payAudit(t); !strings.Contains(a, "pay_renewal_retry") || !strings.Contains(a, ok.OrderNo) {
		t.Fatalf("应记审计: %s", a)
	}
	if w := retry(); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "已经关闭") {
		t.Fatalf("关掉之后再点: %d %s", w.Code, w.Body.String())
	}
	if w := payDo(AdminPayRetryRenewal, "POST", "/orders/:no/renewal/retry", "/orders/bogus/renewal/retry", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("单号无效: %d", w.Code)
	}
}

// 套餐配置：GET 带点数六档（种类、点数、PH 默认金额）；PUT 必须带全 11 档，上限不能低于预期金额。
func TestPayConfigCreditPlans(t *testing.T) {
	payEnv(t)
	w := payDo(AdminPayGetConfig, "GET", "/config", "/config", "")
	var cfg struct {
		Plans []payengine.PlanConf `json:"plans"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil || len(cfg.Plans) != 11 {
		t.Fatalf("%v %s", err, w.Body.String())
	}
	c := cfg.Plans[5]
	if c.Key != "credit250" || c.Kind != "credit" || c.CreditQuantity != 250 || c.Regions["PH"].ExpectMinor != 56500 || c.Regions["PH"].CapMinor != 63280 {
		t.Fatalf("%+v", c)
	}
	put := func(plans []payengine.PlanConf) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"plans": plans, "concurrency": 1})
		return payDo(AdminPayPutConfig, "PUT", "/config", "/config", string(b))
	}
	// 旧前端只传 5 档开通套餐：整体覆盖会清掉点数配置，拒绝
	if w := put(cfg.Plans[:5]); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "不完整") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	bad := append([]payengine.PlanConf(nil), cfg.Plans...)
	regions := map[string]payengine.RegionConf{}
	for k, v := range bad[5].Regions {
		regions[k] = v
	}
	regions["PH"] = payengine.RegionConf{Currency: "PHP", ExpectMinor: 56500, CapMinor: 50000}
	bad[5].Regions = regions
	if w := put(bad); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "预期金额") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	regions["PH"] = payengine.RegionConf{Enabled: true, Currency: "PHP", ExpectMinor: 56500, CapMinor: 70000, FloorMinor: 50000, FundCents: 1500}
	if w := put(bad); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(payAudit(t), "credit250:PH") {
		t.Fatalf("审计应列出启用的点数档: %s", payAudit(t))
	}
}
