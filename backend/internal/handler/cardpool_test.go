package handler

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

const cpFullPAN = "4111111111111234"

// cpEnv 起内存库、模拟平台与卡池服务（可选启动后台协程）。
func cpEnv(t *testing.T, start bool) (*sql.DB, *mwtest.Server, *cardpool.Service) {
	t.Helper()
	c := mwSetup(t)
	mw := mwtest.New(t)
	pool := cardpool.New(cardpool.Options{
		Client: func() (*marswallex.Client, error) { return mw.Client(), nil },
		Alert:  func(string) {},
	})
	SetCardPool(pool)
	t.Cleanup(func() { SetCardPool(nil) })
	if start {
		ctx, cancel := context.WithCancel(context.Background())
		pool.Start(ctx)
		// 在 mwSetup 的清理之后注册：LIFO，协程先停、再还原 db.DB。
		t.Cleanup(func() { cancel(); pool.Wait() })
	}
	return c, mw, pool
}

func cpInsertCard(t *testing.T, c *sql.DB, id, state, token string) {
	t.Helper()
	now := time.Now().Unix()
	var exp any
	if state == cardpool.PoolLeased {
		exp = now + 1800
	}
	_, err := c.Exec(`INSERT INTO mw_cards (card_id, product_id, currency, last4, expiry, platform_status, source, pool_state,
		lease_token, lease_owner, lease_expires_at, created_at, updated_at) VALUES (?, 'p1', 'USD', '1234', '1228', 'ACTIVE', 'opened', ?, ?, ?, ?, ?, ?)`,
		id, state, token, map[bool]string{true: "someone", false: ""}[token != ""], exp, now, now)
	if err != nil {
		t.Fatal(err)
	}
}

// cpDo 以管理员 alice 的身份调用一个带路径参数的处理器。
func cpDo(h gin.HandlerFunc, method, route, path, body string) *httptest.ResponseRecorder {
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("username", "alice") })
	r.Handle(method, route, h)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func cpAudit(t *testing.T, c *sql.DB) string {
	t.Helper()
	rows, err := c.Query(`SELECT action || ' ' || COALESCE(detail,'') FROM admin_audit_logs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sb strings.Builder
	for rows.Next() {
		var s string
		rows.Scan(&s)
		sb.WriteString(s + "\n")
	}
	return sb.String()
}

func TestCardPoolRoutesRequireService(t *testing.T) {
	mwSetup(t)
	SetCardPool(nil)
	cases := []struct {
		h             gin.HandlerFunc
		method, route string
	}{
		{AdminCardPoolOverview, "GET", "/x"},
		{AdminCardPoolCards, "GET", "/x"},
		{AdminCardPoolSensitive, "POST", "/x"},
		{AdminCardPoolPutPolicy, "PUT", "/x"},
		{AdminReprocessMarsWallexWebhookEvent, "POST", "/x"},
	}
	for _, tc := range cases {
		w := cpDo(tc.h, tc.method, tc.route, "/x", `{}`)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d %s", tc.route, w.Code, w.Body.String())
		}
	}
}

func TestSensitiveEndpointAuditsWithoutSecrets(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	cpInsertCard(t, c, "c1", cardpool.PoolAvailable, "")
	mw.On("GET", "/v1/card/sensitive/c1", mwtest.OK(`{"card_id":"c1","card_no":"`+cpFullPAN+`","cvv2":"987","expiry_date":"1228"}`))

	if w := cpDo(AdminCardPoolLeaseCard, "POST", "/c/:id", "/c/c1", ``); w.Code != 200 {
		t.Fatalf("lease %d %s", w.Code, w.Body.String())
	} else if strings.Contains(w.Body.String(), "token") {
		t.Errorf("lease leaked token: %s", w.Body.String())
	}
	w := cpDo(AdminCardPoolSensitive, "POST", "/c/:id", "/c/c1", ``)
	if w.Code != 200 {
		t.Fatalf("sensitive %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control=%q", w.Header().Get("Cache-Control"))
	}
	m := mwJSON(t, w)
	if len(m) != 3 || m["card_no"] != cpFullPAN || m["cvv2"] != "987" || m["expiry_date"] != "1228" {
		t.Errorf("body %v", m)
	}
	audit := cpAudit(t, c)
	if !strings.Contains(audit, "cardpool.view_sensitive card_id=c1") {
		t.Errorf("audit missing: %q", audit)
	}
	if strings.Contains(audit, cpFullPAN) || strings.Contains(audit, "987") {
		t.Errorf("audit leaked secrets: %q", audit)
	}
}

func TestOtherEndpointsNeverReturnCardNumber(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	cpInsertCard(t, c, "c1", cardpool.PoolLeased, "SECRET-LEASE-TOKEN")
	mw.On("GET", "/v1/card/detail/c1", mwtest.OK(`{"card_id":"c1","card_no":"`+cpFullPAN+`","card_currency":"USD","expiry_date":"1228","status":"ACTIVE","available_balance":"0.00","freeze_balance":"0.00"}`))

	list := cpDo(AdminCardPoolCards, "GET", "/x", "/x?page=1", ``)
	detail := cpDo(AdminCardPoolCardDetail, "GET", "/c/:id", "/c/c1", ``)
	refresh := cpDo(AdminCardPoolRefreshCard, "POST", "/c/:id", "/c/c1", ``)
	for name, w := range map[string]*httptest.ResponseRecorder{"list": list, "detail": detail, "refresh": refresh} {
		if w.Code != 200 {
			t.Errorf("%s %d %s", name, w.Code, w.Body.String())
		}
		b := w.Body.String()
		if strings.Contains(b, cpFullPAN) || strings.Contains(b, "SECRET-LEASE-TOKEN") {
			t.Errorf("%s leaked: %s", name, b)
		}
	}
	m := mwJSON(t, list)
	items, _ := m["items"].([]any)
	if len(items) != 1 || m["total"] != float64(1) {
		t.Fatalf("list %v", m)
	}
	if it := items[0].(map[string]any); it["pool_state"] != "leased" || it["last4"] != "1234" {
		t.Errorf("item %v", it)
	}
}

func TestConfirmFailedRequiresNote(t *testing.T) {
	cpEnv(t, false)
	for _, body := range []string{`{}`, `{"note":"   "}`} {
		w := cpDo(AdminCardPoolConfirmFailed, "POST", "/o/:no/x", "/o/MI1/x", body)
		if w.Code != 400 {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
}

func TestErrorMapping(t *testing.T) {
	c, _, _ := cpEnv(t, false)
	cpInsertCard(t, c, "leased", cardpool.PoolLeased, "tok")
	if w := cpDo(AdminCardPoolDisableCard, "POST", "/c/:id", "/c/leased", `{"reason":"x"}`); w.Code != 409 {
		t.Errorf("disable leased: %d %s", w.Code, w.Body.String())
	}
	if w := cpDo(AdminCardPoolApplyCards, "POST", "/x", "/x", `{"product_id":"p1","card_holder_id":"h","count":21}`); w.Code != 400 {
		t.Errorf("count 21: %d %s", w.Code, w.Body.String())
	}
	if w := cpDo(AdminCardPoolRefreshCard, "POST", "/c/:id", "/c/nope", ``); w.Code != 404 {
		t.Errorf("missing card: %d %s", w.Code, w.Body.String())
	}
	if w := cpDo(AdminCardPoolEnableCard, "POST", "/c/:id", "/c/nope", ``); w.Code != 404 {
		t.Errorf("enable missing: %d %s", w.Code, w.Body.String())
	}
}

func TestErrorMappingPlatformErrors(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	cpInsertCard(t, c, "c1", cardpool.PoolAvailable, "")
	mw.On("GET", "/v1/card/detail/c1", mwtest.Unknown())
	w := cpDo(AdminCardPoolRefreshCard, "POST", "/c/:id", "/c/c1", ``)
	if w.Code != 502 || mwJSON(t, w)["outcome"] == nil {
		t.Errorf("platform error: %d %s", w.Code, w.Body.String())
	}

	// 客户端配置错误 → 503
	SetCardPool(cardpool.New(cardpool.Options{Client: MarsWallexClient, Alert: func(string) {}}))
	w = cpDo(AdminCardPoolRefreshCard, "POST", "/c/:id", "/c/c1", ``)
	if w.Code != 503 {
		t.Errorf("config error: %d %s", w.Code, w.Body.String())
	}
}

func TestFundParsesAmount(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	cpInsertCard(t, c, "c1", cardpool.PoolAvailable, "")
	if w := cpDo(AdminCardPoolLeaseCard, "POST", "/c/:id", "/c/c1", ``); w.Code != 200 {
		t.Fatalf("lease %d %s", w.Code, w.Body.String())
	}
	for _, amt := range []string{"1.005", "0", "-1.00", "abc", ""} {
		w := cpDo(AdminCardPoolFundCard, "POST", "/c/:id", "/c/c1", `{"amount":"`+amt+`"}`)
		if w.Code != 400 {
			t.Errorf("amount %q: %d %s", amt, w.Code, w.Body.String())
		}
	}
	if n := mwCount(t, c, `SELECT COUNT(*) FROM mw_card_orders`); n != 0 {
		t.Fatalf("orders=%d", n)
	}
	mw.On("POST", "/v1/card/recharge-transaction", mwtest.OK(`{"status":"APPLY","transaction_id":"t1"}`))
	w := cpDo(AdminCardPoolFundCard, "POST", "/c/:id", "/c/c1", `{"amount":"1.00"}`)
	if w.Code != 200 {
		t.Fatalf("fund %d %s", w.Code, w.Body.String())
	}
	if n := mwCount(t, c, `SELECT COUNT(*) FROM mw_card_orders WHERE kind='recharge' AND amount_cents=100 AND card_id='c1'`); n != 1 {
		t.Errorf("orders with 100 cents = %d", n)
	}
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.fund_card card_id=c1") {
		t.Errorf("audit %q", a)
	}
}

func TestPolicyPutValidates(t *testing.T) {
	c, _, _ := cpEnv(t, false)
	before := cpDo(AdminCardPoolGetPolicy, "GET", "/x", "/x", ``)
	if before.Code != 200 {
		t.Fatal(before.Body.String())
	}
	w := cpDo(AdminCardPoolPutPolicy, "PUT", "/x", "/x", `{"card_name_prefix":"bad-prefix!"}`)
	if w.Code != 400 {
		t.Errorf("bad prefix: %d %s", w.Code, w.Body.String())
	}
	if w := cpDo(AdminCardPoolPutPolicy, "PUT", "/x", "/x", `not json`); w.Code != 400 {
		t.Errorf("bad json: %d", w.Code)
	}
	after := cpDo(AdminCardPoolGetPolicy, "GET", "/x", "/x", ``)
	if before.Body.String() != after.Body.String() {
		t.Errorf("policy changed:\n%s\n%s", before.Body.String(), after.Body.String())
	}
	ok := cpDo(AdminCardPoolPutPolicy, "PUT", "/x", "/x", `{"card_name_prefix":"POOL2","validity_months":12,"max_uses_per_card":1,"lease_minutes":30,
		"cancel_after_days":7,"health_window_hours":24,"product_fail_threshold":3,"card_fail_threshold":2}`)
	if ok.Code != 200 {
		t.Fatalf("valid put: %d %s", ok.Code, ok.Body.String())
	}
	if m := mwJSON(t, cpDo(AdminCardPoolGetPolicy, "GET", "/x", "/x", ``)); m["card_name_prefix"] != "POOL2" {
		t.Errorf("not saved: %v", m)
	}
	// 审计内容为保存后的完整策略 JSON
	a := cpAudit(t, c)
	if !strings.Contains(a, `cardpool.policy {`) || !strings.Contains(a, `"card_name_prefix":"POOL2"`) ||
		!strings.Contains(a, `"validity_months":12`) || !strings.Contains(a, `"plan_products":{}`) {
		t.Errorf("audit %q", a)
	}
}

func TestWebhookKicksService(t *testing.T) {
	c, mw, _ := cpEnv(t, true)
	if err := db.SetSetting(db.SettingMarsWallexPlatformPublicKey, mw.PlatformPublicKeyPEM()); err != nil {
		t.Fatal(err)
	}
	// 等启动时的那一轮积压处理走完，之后只有 Kick 能让新事件在 1 秒内被处理。
	time.Sleep(200 * time.Millisecond)
	body := `{"topic":"CARD_STATUS_CHANGED","messageId":"m-kick-1","data":{"card_id":"ghost","card_status":"ACTIVE"}}`
	r := gin.New()
	r.POST("/hook", MarsWallexWebhook)
	req := httptest.NewRequest("POST", "/hook", strings.NewReader(body))
	req.Header.Set("F-Signature", mw.SignWebhook([]byte(body), time.Now()))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("webhook %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for {
		if mwCount(t, c, `SELECT COUNT(*) FROM marswallex_webhook_events WHERE message_id='m-kick-1' AND processed_at IS NOT NULL`) == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("事件未在 1 秒内被处理")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestReprocessEndpoint(t *testing.T) {
	c, _, _ := cpEnv(t, false)
	if _, err := db.InsertMarsWallexWebhookEvent("m1", "CARD_STATUS_CHANGED", `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(`UPDATE marswallex_webhook_events SET processed_at = CURRENT_TIMESTAMP, process_attempts = 10, process_error = 'boom', process_note = 'n'`); err != nil {
		t.Fatal(err)
	}
	list := mwJSON(t, cpDo(AdminListMarsWallexWebhookEvents, "GET", "/x", "/x", ``))
	ev := list["events"].([]any)[0].(map[string]any)
	if ev["process_attempts"] != float64(10) || ev["process_note"] != "n" {
		t.Errorf("list fields %v", ev)
	}
	if w := cpDo(AdminReprocessMarsWallexWebhookEvent, "POST", "/e/:id", "/e/999", ``); w.Code != 404 {
		t.Errorf("missing event: %d", w.Code)
	}
	if w := cpDo(AdminReprocessMarsWallexWebhookEvent, "POST", "/e/:id", "/e/abc", ``); w.Code != 400 {
		t.Errorf("bad id: %d", w.Code)
	}
	w := cpDo(AdminReprocessMarsWallexWebhookEvent, "POST", "/e/:id", "/e/1", ``)
	if w.Code != 200 {
		t.Fatalf("reprocess %d %s", w.Code, w.Body.String())
	}
	if n := mwCount(t, c, `SELECT COUNT(*) FROM marswallex_webhook_events WHERE processed_at IS NULL AND process_attempts = 0 AND process_error IS NULL`); n != 1 {
		t.Errorf("event not reset")
	}
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.reprocess_event event_id=1") {
		t.Errorf("audit %q", a)
	}
}

func TestApplyCardsPartialFailureReturnsCreatedOrders(t *testing.T) {
	c := mwSetup(t)
	mw := mwtest.New(t)
	calls := 0
	// ApplyCards 取 1 次客户端，每张卡的 submitApply 各取 1 次：第 3 次（第二张卡）起报配置错误。
	pool := cardpool.New(cardpool.Options{
		Client: func() (*marswallex.Client, error) {
			calls++
			if calls >= 3 {
				return nil, marsWallexConfigErr("未配置 apiToken")
			}
			return mw.Client(), nil
		},
		Alert: func(string) {},
	})
	SetCardPool(pool)
	t.Cleanup(func() { SetCardPool(nil) })
	if _, err := c.Exec(`INSERT INTO mw_cardholders (card_holder_id, first_name, last_name, country_code, status, remark, note, synced_at, created_at)
		VALUES ('h1','A','B','US','APPROVED','','',1,1)`); err != nil {
		t.Fatal(err)
	}
	mw.On("GET", "/v1/card/product", mwtest.OK(`[{"product_id":"p1","card_label":"VISA","card_country":"US","card_mode":"PREPAID","card_currency":"USD"}]`))
	mw.On("POST", "/v2/card", mwtest.OK(`{"transaction_id":"T1"}`))

	w := cpDo(AdminCardPoolApplyCards, "POST", "/x", "/x", `{"product_id":"p1","card_holder_id":"h1","count":3}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	m := mwJSON(t, w)
	orders, _ := m["orders"].([]any)
	if m["created"] != float64(1) || len(orders) != 1 || m["error"] == nil {
		t.Fatalf("body %v", m)
	}
	var no string
	if err := c.QueryRow(`SELECT out_order_no FROM mw_card_orders WHERE kind='apply'`).Scan(&no); err != nil {
		t.Fatal(err)
	}
	if orders[0].(map[string]any)["out_order_no"] != no {
		t.Errorf("order mismatch: %v vs %s", orders[0], no)
	}
	if a := cpAudit(t, c); !strings.Contains(a, "created=1") {
		t.Errorf("audit %q", a)
	}
}
