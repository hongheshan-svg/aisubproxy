package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/provider/providertest"
)

const (
	payAccessToken  = "at-secret-123"
	paySessionToken = "st-secret-456"
)

func paySession(email string) string {
	b, _ := json.Marshal(map[string]any{
		"user": map[string]string{"email": email}, "accessToken": payAccessToken,
		"sessionToken": paySessionToken, "expires": "2099-01-01T00:00:00Z",
	})
	return string(b)
}

// payEnv 起内存库与主密钥，启用 plus/PH，装好一个不启动后台协程的引擎。
func payEnv(t *testing.T) (*payengine.Service, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	tbl := payengine.DefaultPlanTable()
	for i := range tbl {
		if tbl[i].Key == "plus" {
			tbl[i].Regions["PH"] = payengine.RegionConf{Enabled: true, Currency: "PHP", CapMinor: 120000, FundCents: 3000}
		}
	}
	if err := payengine.SavePlanTable(tbl); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// 引擎不启动，卡池不会被调用；只是让 MarsWallex 的单能建
	s := payengine.New(payengine.Options{Pools: map[string]payengine.Pool{string(provider.MarsWallex): cardpool.New(cardpool.Options{})}, ShotDir: dir, Alert: func(string) {}})
	SetPayEngine(s)
	t.Cleanup(func() { SetPayEngine(nil) })
	return s, dir
}

func payDo(h gin.HandlerFunc, method, route, path, body string) *httptest.ResponseRecorder {
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("username", "alice") })
	r.Handle(method, route, h)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// payCtxDo 直接给处理器塞路径参数，用来测路由本身匹配不到的恶意参数。
func payCtxDo(h gin.HandlerFunc, params gin.Params) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/x", nil)
	c.Params = params
	c.Set("username", "alice")
	h(c)
	return w
}

func paySubmitBody(plan, region, session string, dry bool) string {
	b, _ := json.Marshal(map[string]any{"plan_key": plan, "region": region, "session": session, "dry_run": dry})
	return string(b)
}

func payAudit(t *testing.T) string {
	t.Helper()
	return cpAudit(t, db.DB)
}

func TestPayEngineNilReturns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	SetPayEngine(nil)
	for name, h := range map[string]gin.HandlerFunc{
		"submit": AdminPaySubmit, "list": AdminPayList, "get": AdminPayGet, "resolve": AdminPayResolve, "shot": AdminPayShot,
		"getConfig": AdminPayGetConfig, "putConfig": AdminPayPutConfig, "getProxies": AdminPayGetProxies,
		"putProxies": AdminPayPutProxies, "browserCheck": AdminPayBrowserCheck, "retryRenewal": AdminPayRetryRenewal,
	} {
		w := payDo(h, "GET", "/x", "/x", "{}")
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "付款引擎未启动") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
}

func TestPaySubmitMapsErrors(t *testing.T) {
	payEnv(t)
	post := func(body string) *httptest.ResponseRecorder {
		return payDo(AdminPaySubmit, "POST", "/orders", "/orders", body)
	}
	w := post(paySubmitBody("plus", "PH", paySession("a@example.com"), false))
	if w.Code != 200 {
		t.Fatalf("成功应 200: %d %s", w.Code, w.Body.String())
	}
	var ok struct {
		OrderNo string `json:"order_no"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &ok)
	if !strings.HasPrefix(ok.OrderNo, "PO") || len(ok.OrderNo) != 22 {
		t.Fatalf("order_no: %s", w.Body.String())
	}
	if w := post(paySubmitBody("plus", "PH", paySession("a@example.com"), false)); w.Code != 409 {
		t.Fatalf("同邮箱进行中应 409: %d %s", w.Code, w.Body.String())
	}
	if w := post(paySubmitBody("pro_50x", "JP", paySession("b@example.com"), false)); w.Code != 400 {
		t.Fatalf("未启用套餐应 400: %d %s", w.Code, w.Body.String())
	}
	bad := `{"user":{"email":"c@example.com"},"accessToken":"LEAK-AT"}`
	w = post(paySubmitBody("plus", "PH", bad, false))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "会话无效") || strings.Contains(w.Body.String(), "LEAK-AT") {
		t.Fatalf("坏会话应 400 且不回显: %d %s", w.Code, w.Body.String())
	}
	if w := post(`{not json`); w.Code != 400 || strings.Contains(w.Body.String(), "not json") {
		t.Fatalf("坏请求体: %d %s", w.Code, w.Body.String())
	}
	big := `{"plan_key":"plus","region":"PH","dry_run":true,"session":"` + strings.Repeat("x", 70<<10) + `"}`
	if w := post(big); w.Code != 400 || !strings.Contains(w.Body.String(), "请求体无效") {
		t.Fatalf("超过 64KB 应 400「请求体无效」: %d %s", w.Code, w.Body.String())
	}
}

// 后台测试付款按服务商建单：不填为 MarsWallex；上游型、未注册的、没有卡池的都拒绝，不会换成别家。
func TestPaySubmitProvider(t *testing.T) {
	payEnv(t)
	providertest.Register(t, providertest.NewTestCard(), providertest.NewTestUp())
	post := func(prov, email string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"provider": prov, "plan_key": "plus", "region": "PH", "session": paySession(email), "dry_run": true})
		return payDo(AdminPaySubmit, "POST", "/orders", "/orders", string(b))
	}
	for _, tc := range []struct{ prov, want string }{
		{"zovocard", "只能选已注册的供卡型服务商"},
		{string(providertest.TestUp), "只能选已注册的供卡型服务商"},
		{"nope", "只能选已注册的供卡型服务商"},
		{string(providertest.TestCard), "该服务商没有可用的卡池"},
	} {
		if w := post(tc.prov, tc.prov+"@example.com"); w.Code != 400 || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("%s: %d %s", tc.prov, w.Code, w.Body.String())
		}
	}
	if _, total, _ := payEngine.List(context.Background(), "", 1, 10); total != 0 {
		t.Fatalf("拒绝时不应建单: %d", total)
	}
	for _, prov := range []string{"", "marswallex"} {
		w := post(prov, "ok"+prov+"@example.com")
		var resp struct {
			Order payengine.Order `json:"order"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if w.Code != 200 || resp.Order.Provider != "marswallex" {
			t.Fatalf("%q: %d %s", prov, w.Code, w.Body.String())
		}
	}
	if a := payAudit(t); !strings.Contains(a, "provider=marswallex") {
		t.Fatalf("审计应记服务商: %s", a)
	}
}

func TestPaySubmitAuditHasNoSession(t *testing.T) {
	payEnv(t)
	w := payDo(AdminPaySubmit, "POST", "/orders", "/orders", paySubmitBody("plus", "PH", paySession("a@example.com"), true))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var ok struct {
		OrderNo string `json:"order_no"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &ok)
	a := payAudit(t)
	for _, want := range []string{"pay_test_submit", "plus", "PH", "dry_run", ok.OrderNo} {
		if !strings.Contains(a, want) {
			t.Errorf("审计缺 %q: %s", want, a)
		}
	}
	for _, leak := range []string{payAccessToken, paySessionToken} {
		if strings.Contains(a, leak) || strings.Contains(w.Body.String(), leak) {
			t.Errorf("审计或响应含会话 %q", leak)
		}
	}
	// 设计 §5：来源是 admin_test，来源引用记管理员用户名
	var src, ref string
	if err := db.DB.QueryRow(`SELECT source, source_ref FROM pay_orders WHERE order_no = ?`, ok.OrderNo).Scan(&src, &ref); err != nil || src != "admin_test" || ref != "alice" {
		t.Fatalf("source = %q, source_ref = %q, %v", src, ref, err)
	}
}

func TestPayOrderDetailAndList(t *testing.T) {
	payEnv(t)
	w := payDo(AdminPaySubmit, "POST", "/orders", "/orders", paySubmitBody("plus", "PH", paySession("a@example.com"), false))
	var ok struct {
		OrderNo string `json:"order_no"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &ok)
	if _, err := db.DB.Exec(`UPDATE pay_orders SET lease_token = 'LEASE-SECRET' WHERE order_no = ?`, ok.OrderNo); err != nil {
		t.Fatal(err)
	}

	w = payDo(AdminPayGet, "GET", "/orders/:no", "/orders/"+ok.OrderNo, "")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var d struct {
		Order    map[string]any   `json:"order"`
		Events   []map[string]any `json:"events"`
		Evidence []any            `json:"evidence"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil || d.Order["order_no"] != ok.OrderNo || d.Events == nil || d.Evidence == nil {
		t.Fatalf("详情结构: %v %s", err, w.Body.String())
	}
	if _, err := db.DB.Exec(`UPDATE pay_orders SET checkout_url = 'https://pay.example/c/CHECKOUT-SECRET' WHERE order_no = ?`, ok.OrderNo); err != nil {
		t.Fatal(err)
	}
	w = payDo(AdminPayGet, "GET", "/orders/:no", "/orders/"+ok.OrderNo, "")
	if strings.Contains(w.Body.String(), "CHECKOUT-SECRET") || strings.Contains(w.Body.String(), "checkout_url") {
		t.Fatalf("详情含支付链接: %s", w.Body.String())
	}
	var sealed string
	_ = db.DB.QueryRow(`SELECT session_sealed FROM pay_orders WHERE order_no = ?`, ok.OrderNo).Scan(&sealed)
	if sealed == "" {
		t.Fatal("测试前提：应有密文会话")
	}
	for _, leak := range []string{"LEASE-SECRET", sealed, payAccessToken, paySessionToken} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("详情含敏感值 %q", leak)
		}
	}
	if w := payDo(AdminPayGet, "GET", "/orders/:no", "/orders/PO000000000000deadbeef", ""); w.Code != 404 {
		t.Fatalf("不存在应 404: %d", w.Code)
	}

	w = payDo(AdminPayList, "GET", "/orders", "/orders?state=queued&page=1&page_size=10", "")
	var l struct {
		Orders []map[string]any `json:"orders"`
		Total  int              `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil || l.Total != 1 || len(l.Orders) != 1 {
		t.Fatalf("列表: %v %s", err, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "LEASE-SECRET") || strings.Contains(w.Body.String(), sealed) {
		t.Fatal("列表含敏感值")
	}
}

func TestPayProxiesWriteOnly(t *testing.T) {
	payEnv(t)
	w := payDo(AdminPayPutProxies, "PUT", "/proxies", "/proxies", `{"PH":"http://user:pw@px.secret-host.example:8080"}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret-host") {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	w = payDo(AdminPayGetProxies, "GET", "/proxies", "/proxies", "")
	var got map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || !got["PH"] || got["US"] {
		t.Fatalf("GET: %v %s", err, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret-host") || strings.Contains(w.Body.String(), "pw") {
		t.Fatal("响应含代理信息")
	}
	if a := payAudit(t); !strings.Contains(a, "pay_proxies_update") || !strings.Contains(a, "PH") ||
		strings.Contains(a, "secret-host") || strings.Contains(a, "pw@") {
		t.Fatalf("审计: %s", a)
	}
	if w := payDo(AdminPayPutProxies, "PUT", "/proxies", "/proxies", `{"PH":"-"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if !strings.Contains(payAudit(t), "已清空") {
		t.Fatal("清空应记入审计")
	}
	if w := payDo(AdminPayPutProxies, "PUT", "/proxies", "/proxies", `{"PH":"socks5://u:p@h:1080"}`); w.Code != 400 || strings.Contains(w.Body.String(), "h:1080") {
		t.Fatalf("带认证 socks5 应 400 且不回显: %d %s", w.Code, w.Body.String())
	}
}

func TestPayPutProxiesRejectsEmptyBodyWithoutAudit(t *testing.T) {
	payEnv(t)
	for _, body := range []string{`{}`, `null`} {
		if w := payDo(AdminPayPutProxies, "PUT", "/proxies", "/proxies", body); w.Code != 400 {
			t.Errorf("%s 应 400: %d", body, w.Code)
		}
	}
	if a := payAudit(t); strings.Contains(a, "pay_proxies_update") {
		t.Fatalf("空体不应写审计: %s", a)
	}
}

// 任何错误响应都不得回显输入：邮箱、套餐键、代理地址与密码。
func TestPayErrorsDoNotEchoInput(t *testing.T) {
	payEnv(t)
	post := func(body string) *httptest.ResponseRecorder {
		return payDo(AdminPaySubmit, "POST", "/orders", "/orders", body)
	}
	const email = "echo.me@example.com"
	if w := post(paySubmitBody("plus", "PH", paySession(email), false)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cases := map[string]*httptest.ResponseRecorder{
		"inflight":  post(paySubmitBody("plus", "PH", paySession(email), false)),
		"plan":      post(paySubmitBody("ECHO-PLAN", "PH", paySession("x@example.com"), false)),
		"region":    post(paySubmitBody("plus", "ECHO-REGION", paySession("x@example.com"), false)),
		"proxyKey":  payDo(AdminPayPutProxies, "PUT", "/proxies", "/proxies", `{"http://user:pw@px.example:8080":"PH"}`),
		"proxyAddr": payDo(AdminPayPutProxies, "PUT", "/proxies", "/proxies", `{"PH":"ftp://user:pw@px.example:8080"}`),
		"cfgKey":    payDo(AdminPayPutConfig, "PUT", "/config", "/config", payConfigBodyWithKey("ECHO-KEY", "PH")),
		"cfgRegion": payDo(AdminPayPutConfig, "PUT", "/config", "/config", payConfigBodyWithKey("plus", "ECHO-KEY")),
	}
	for name, w := range cases {
		if w.Code < 400 {
			t.Errorf("%s 应为错误: %d", name, w.Code)
		}
		for _, leak := range []string{email, "echo.me", "ECHO-PLAN", "ECHO-REGION", "ECHO-KEY", "pw", "px.example", "user:"} {
			if strings.Contains(w.Body.String(), leak) {
				t.Errorf("%s 响应回显了 %q: %s", name, leak, w.Body.String())
			}
		}
	}
	if cases["inflight"].Code != 409 {
		t.Errorf("inflight: %d", cases["inflight"].Code)
	}
}

// payConfigBodyWithKey 给出一份完整的套餐表，其中第一个套餐的键或某个地区键被替换成指定值。
func payConfigBodyWithKey(key, region string) string {
	plans := payengine.DefaultPlanTable()
	if key != "plus" {
		plans[0].Key = key
	} else {
		plans[1].Regions[region] = payengine.RegionConf{Currency: "USD"}
	}
	b, _ := json.Marshal(map[string]any{"plans": plans, "concurrency": 1})
	return string(b)
}

func TestPayShotRejectsTraversal(t *testing.T) {
	_, dir := payEnv(t)
	const no = "PO260101000000abcdef01"
	if err := os.MkdirAll(filepath.Join(dir, no), 0o755); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\nfake")
	if err := os.WriteFile(filepath.Join(dir, no, "01-form.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("S"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []gin.Params{
		{{Key: "no", Value: "../x"}, {Key: "name", Value: "01-form.png"}},
		{{Key: "no", Value: no}, {Key: "name", Value: "../../etc"}},
		{{Key: "no", Value: no}, {Key: "name", Value: "..%2f..%2fetc"}},
		{{Key: "no", Value: no}, {Key: "name", Value: "a.png"}},
		{{Key: "no", Value: no}, {Key: "name", Value: "01-Form.png"}},
		{{Key: "no", Value: no}, {Key: "name", Value: "01-form.png/"}},
	} {
		if w := payCtxDo(AdminPayShot, p); w.Code != 400 {
			t.Errorf("%v 应 400: %d", p, w.Code)
		}
	}
	w := payDo(AdminPayShot, "GET", "/orders/:no/shots/:name", "/orders/"+no+"/shots/01-form.png", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "no-store" || !bytes.Equal(w.Body.Bytes(), png) {
		t.Fatalf("合法文件: %d %v", w.Code, w.Header())
	}
	// 不存在 → 404
	if w := payDo(AdminPayShot, "GET", "/orders/:no/shots/:name", "/orders/"+no+"/shots/02-none.png", ""); w.Code != 404 {
		t.Fatalf("不存在应 404: %d", w.Code)
	}
	// 符号链接与目录都不给
	if err := os.Symlink(filepath.Join(dir, "secret.txt"), filepath.Join(dir, no, "03-link.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, no, "04-dir.png"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"03-link.png", "04-dir.png"} {
		if w := payDo(AdminPayShot, "GET", "/orders/:no/shots/:name", "/orders/"+no+"/shots/"+name, ""); w.Code != 404 {
			t.Errorf("%s 应 404: %d", name, w.Code)
		}
	}
}

func TestPayResolveRequiresNote(t *testing.T) {
	payEnv(t)
	for _, body := range []string{`{"result":"succeeded","note":""}`, `{"result":"succeeded","note":"   "}`, `{"result":"succeeded"}`} {
		if w := payDo(AdminPayResolve, "POST", "/orders/:no/resolve", "/orders/PO000000000000deadbeef/resolve", body); w.Code != 400 {
			t.Errorf("%s 应 400: %d %s", body, w.Code, w.Body.String())
		}
	}
	// 不存在的单据 → 404
	if w := payDo(AdminPayResolve, "POST", "/orders/:no/resolve", "/orders/PO000000000000deadbeef/resolve", `{"result":"succeeded","note":"已核对"}`); w.Code != 404 {
		t.Errorf("不存在应 404: %d %s", w.Code, w.Body.String())
	}
}

func payResolveSetup(t *testing.T, state string) string {
	t.Helper()
	w := payDo(AdminPaySubmit, "POST", "/orders", "/orders", paySubmitBody("plus", "PH", paySession("r@example.com"), false))
	var ok struct {
		OrderNo string `json:"order_no"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &ok)
	if _, err := db.DB.Exec(`UPDATE pay_orders SET state = ? WHERE order_no = ?`, state, ok.OrderNo); err != nil {
		t.Fatal(err)
	}
	return ok.OrderNo
}

func TestPayResolveStateChangedIs409(t *testing.T) {
	payEnv(t)
	no := payResolveSetup(t, "queued")
	w := payDo(AdminPayResolve, "POST", "/orders/:no/resolve", "/orders/"+no+"/resolve", `{"result":"succeeded","note":"已核对"}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "单据状态已变化") {
		t.Fatalf("状态冲突应 409: %d %s", w.Code, w.Body.String())
	}
}

func TestPayResolveOKAuditsTruncatedNote(t *testing.T) {
	payEnv(t)
	no := payResolveSetup(t, "needs_review")
	note := strings.Repeat("备", 300)
	w := payDo(AdminPayResolve, "POST", "/orders/:no/resolve", "/orders/"+no+"/resolve", `{"result":"failed","note":"`+note+`"}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	a := payAudit(t)
	if !strings.Contains(a, "pay_order_resolve") || !strings.Contains(a, no) || !strings.Contains(a, "failed") {
		t.Fatalf("审计: %s", a)
	}
	if strings.Contains(a, strings.Repeat("备", 201)) || !strings.Contains(a, strings.Repeat("备", 200)) {
		t.Fatalf("备注应截断到 200 字: %d", strings.Count(a, "备"))
	}
}

func savePoolLease(t *testing.T, minutes int) {
	t.Helper()
	p := cardpool.DefaultPolicy()
	p.LeaseMinutes = minutes
	if err := cardpool.SavePolicy(p); err != nil {
		t.Fatal(err)
	}
}

func TestPayConfigLeaseWarning(t *testing.T) {
	payEnv(t)
	get := func() map[string]any {
		w := payDo(AdminPayGetConfig, "GET", "/config", "/config", "")
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	savePoolLease(t, 30)
	m := get()
	if m["max_concurrency"] != float64(3) || m["lease_warning"] != true || m["lease_minutes"] != float64(30) || m["max_pay_minutes"] != float64(33) {
		t.Fatalf("30 分钟应提示: %v", m)
	}
	savePoolLease(t, 35)
	if m := get(); m["lease_warning"] != false {
		t.Fatalf("35 分钟不应提示: %v", m)
	}
	if m := get(); m["plans"] == nil || m["concurrency"] == nil {
		t.Fatalf("缺 plans 或 concurrency: %v", m)
	}
}

func TestPayPutConfig(t *testing.T) {
	payEnv(t)
	plans := payengine.DefaultPlanTable()
	for i := range plans {
		if plans[i].Key == "plus" {
			plans[i].Regions["US"] = payengine.RegionConf{Enabled: true, Currency: "USD", CapMinor: 3000, FundCents: 3000}
		}
	}
	pb, _ := json.Marshal(map[string]any{"plans": plans, "concurrency": 2})
	if w := payDo(AdminPayPutConfig, "PUT", "/config", "/config", string(pb)); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if payengine.LoadConcurrency() != 2 {
		t.Fatal("并发数未保存")
	}
	if a := payAudit(t); !strings.Contains(a, "pay_config_update") || !strings.Contains(a, "plus") || !strings.Contains(a, "US") {
		t.Fatalf("审计: %s", a)
	}
	// 并发数非法：整体拒绝，套餐表不变
	bad := payengine.DefaultPlanTable()
	pb, _ = json.Marshal(map[string]any{"plans": bad, "concurrency": 9})
	if w := payDo(AdminPayPutConfig, "PUT", "/config", "/config", string(pb)); w.Code != 400 {
		t.Fatalf("并发数非法应 400: %d", w.Code)
	}
	cur, _ := payengine.LoadPlanTable()
	found := false
	for _, p := range cur {
		if p.Key == "plus" && p.Regions["US"].Enabled {
			found = true
		}
	}
	if !found {
		t.Fatal("并发数非法时套餐表不应被改")
	}
	pb, _ = json.Marshal(map[string]any{"plans": plans})
	if w := payDo(AdminPayPutConfig, "PUT", "/config", "/config", string(pb)); w.Code != 400 {
		t.Fatalf("缺并发数应 400: %d", w.Code)
	}
}

func plusPHCap(t *testing.T) int64 {
	t.Helper()
	tbl, err := payengine.LoadPlanTable()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range tbl {
		if p.Key == "plus" {
			return p.Regions["PH"].CapMinor
		}
	}
	return -1
}

func TestPayPutConfigRejectsIncompletePlans(t *testing.T) {
	payEnv(t)
	if plusPHCap(t) != 120000 {
		t.Fatal("前提：plus/PH 上限应为 120000")
	}
	// 套餐数量够，但某个套餐的地区缺失：该套餐的金额配置同样会被清掉
	withRegions := func(edit func(p *payengine.PlanConf)) string {
		plans := payengine.DefaultPlanTable()
		for i := range plans {
			if plans[i].Key == "plus" {
				edit(&plans[i])
			}
		}
		b, _ := json.Marshal(map[string]any{"plans": plans, "concurrency": 1})
		return string(b)
	}
	for name, body := range map[string]string{
		"缺 plans":    `{"concurrency":2}`,
		"plans null": `{"plans":null,"concurrency":2}`,
		"空数组":        `{"plans":[],"concurrency":2}`,
		"部分套餐":       `{"plans":[{"key":"go"}],"concurrency":1}`,
		"套餐没有地区":     withRegions(func(p *payengine.PlanConf) { p.Regions = nil }),
		"套餐缺一个地区":    withRegions(func(p *payengine.PlanConf) { delete(p.Regions, "PH") }),
	} {
		w := payDo(AdminPayPutConfig, "PUT", "/config", "/config", body)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "套餐表不完整") {
			t.Errorf("%s 应 400「套餐表不完整」: %d %s", name, w.Code, w.Body.String())
		}
		if plusPHCap(t) != 120000 {
			t.Fatalf("%s 后套餐配置被改动", name)
		}
	}
	if payengine.LoadConcurrency() != 1 {
		t.Fatal("并发数不应被改")
	}
}

// 套餐名、期望类型被传成空串时用默认值，不清空已有的 ChatGPT 套餐名。
func TestPayPutConfigKeepsPlanNamesWhenBlank(t *testing.T) {
	payEnv(t)
	plans := payengine.DefaultPlanTable()
	for i := range plans {
		plans[i].ChatGPTPlanName, plans[i].ExpectPlanType = "", ""
	}
	pb, _ := json.Marshal(map[string]any{"plans": plans, "concurrency": 1})
	if w := payDo(AdminPayPutConfig, "PUT", "/config", "/config", string(pb)); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	tbl, _ := payengine.LoadPlanTable()
	for _, p := range tbl {
		if p.Key == "plus" && (p.ChatGPTPlanName != "chatgptplusplan" || p.ExpectPlanType != "plus") {
			t.Fatalf("plus 的套餐名被清空: %+v", p)
		}
	}
}

func TestPayBrowserCheckNoChrome(t *testing.T) {
	payEnv(t)
	t.Setenv("CDK_DEPLOY_MODE", "")
	t.Setenv("CHROME_PATH", filepath.Join(t.TempDir(), "no-such-chrome"))
	w := payDo(AdminPayBrowserCheck, "POST", "/browser-check", "/browser-check", "")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var r paybrowser.CheckResult
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || r.OK || !strings.Contains(r.InstallHint, "--browser-deps") {
		t.Fatalf("结果: %v %s", err, w.Body.String())
	}
}

func TestPayBrowserCheckOneAtATime(t *testing.T) {
	payEnv(t)
	started, release := make(chan struct{}), make(chan struct{})
	old := payBrowserCheck
	payBrowserCheck = func(ctx context.Context) paybrowser.CheckResult {
		close(started)
		<-release
		return paybrowser.CheckResult{OK: true}
	}
	t.Cleanup(func() { payBrowserCheck = old })
	var wg sync.WaitGroup
	wg.Add(1)
	var first *httptest.ResponseRecorder
	go func() {
		defer wg.Done()
		first = payDo(AdminPayBrowserCheck, "POST", "/browser-check", "/browser-check", "")
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("第一个检查未开始")
	}
	w := payDo(AdminPayBrowserCheck, "POST", "/browser-check", "/browser-check", "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "已有检查在进行") {
		t.Fatalf("并发检查应 409: %d %s", w.Code, w.Body.String())
	}
	close(release)
	wg.Wait()
	if first.Code != 200 {
		t.Fatalf("第一个检查: %d", first.Code)
	}
}

func TestPayShotRejectsSymlinkedOrderDir(t *testing.T) {
	_, dir := payEnv(t)
	const no = "PO260101000000abcdef02"
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "01-form.png"), []byte("OUTSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(dir, no)); err != nil {
		t.Fatal(err)
	}
	w := payDo(AdminPayShot, "GET", "/orders/:no/shots/:name", "/orders/"+no+"/shots/01-form.png", "")
	if w.Code != 404 || strings.Contains(w.Body.String(), "OUTSIDE") {
		t.Fatalf("单号目录是符号链接应 404: %d", w.Code)
	}
}

// 只传 dry_run_fill 时绝不能变成真实付款单：这个模式要真的打开支付页填表，建错就会动钱。
func TestPaySubmitDryRunFillAloneIsNeverReal(t *testing.T) {
	payEnv(t)
	body := `{"plan_key":"plus","region":"PH","dry_run_fill":true,"session":` + paySession("fill@example.com") + `}`
	w := payDo(AdminPaySubmit, "POST", "/orders", "/orders", body)
	if w.Code != 200 {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Order struct {
			DryRun     bool `json:"dry_run"`
			DryRunFill bool `json:"dry_run_fill"`
		} `json:"order"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Order.DryRun || !resp.Order.DryRunFill {
		t.Fatalf("应是试运行到填表单: %s", w.Body.String())
	}
}

func TestPaySubmitSessionForms(t *testing.T) {
	payEnv(t)
	obj := `{"plan_key":"plus","region":"PH","dry_run":true,"session":` + paySession("obj@example.com") + `}`
	if w := payDo(AdminPaySubmit, "POST", "/orders", "/orders", obj); w.Code != 200 {
		t.Fatalf("对象形式应 200: %d %s", w.Code, w.Body.String())
	}
	for _, sess := range []string{`null`, `123`, `[]`, `""`} {
		body := `{"plan_key":"plus","region":"PH","dry_run":true,"session":` + sess + `}`
		w := payDo(AdminPaySubmit, "POST", "/orders", "/orders", body)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "会话无效") {
			t.Errorf("session=%s 应 400「会话无效」: %d %s", sess, w.Code, w.Body.String())
		}
	}
}

func TestPayInternalErrorIsGeneric(t *testing.T) {
	payEnv(t)
	if err := db.SetSecretSetting("pay_region_proxies", "not-json-INTERNAL"); err != nil {
		t.Fatal(err)
	}
	w := payDo(AdminPayGetProxies, "GET", "/proxies", "/proxies", "")
	if w.Code != 500 || !strings.Contains(w.Body.String(), "操作失败") || strings.Contains(w.Body.String(), "损坏") || strings.Contains(w.Body.String(), "INTERNAL") {
		t.Fatalf("500 应只给「操作失败」: %d %s", w.Code, w.Body.String())
	}
}

// 套餐表已写入、并发数写失败时补一条审计。
func TestPayPutConfigPartialSaveAudited(t *testing.T) {
	payEnv(t)
	old := paySaveConcurrency
	paySaveConcurrency = func(int) error { return errors.New("db down") }
	t.Cleanup(func() { paySaveConcurrency = old })
	pb, _ := json.Marshal(map[string]any{"plans": payengine.DefaultPlanTable(), "concurrency": 2})
	w := payDo(AdminPayPutConfig, "PUT", "/config", "/config", string(pb))
	if w.Code != 500 || strings.Contains(w.Body.String(), "db down") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if a := payAudit(t); !strings.Contains(a, "pay_config_update") || !strings.Contains(a, "套餐表已保存，并发数保存失败") {
		t.Fatalf("审计: %s", a)
	}
}
