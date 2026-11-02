package handler

import (
	"bytes"
	"context"
	"encoding/json"
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
	s := payengine.New(payengine.Options{ShotDir: dir, Alert: func(string) {}})
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
		"submit": AdminPaySubmit, "list": AdminPayList, "config": AdminPayGetConfig, "proxies": AdminPayGetProxies,
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
	big := `{"session":"` + strings.Repeat("x", 70<<10) + `"}`
	if w := post(big); w.Code != 400 {
		t.Fatalf("超过 64KB 应 400: %d", w.Code)
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
	// 来源记为管理员
	var src string
	if err := db.DB.QueryRow(`SELECT source FROM pay_orders WHERE order_no = ?`, ok.OrderNo).Scan(&src); err != nil || src != "admin:alice" {
		t.Fatalf("source = %q, %v", src, err)
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

	w = payDo(AdminPayList, "GET", "/orders", "/orders?state=queued&page=1&size=10", "")
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

func TestPayShotRejectsTraversal(t *testing.T) {
	s, dir := payEnv(t)
	_ = s
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
		if w := payDo(AdminPayShot, "GET", "/orders/:no/shots/:name", "/orders/"+no+"/shots/"+name, ""); w.Code != 404 || strings.Contains(w.Body.String(), "S") && w.Body.Len() == 1 {
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
	if m["lease_warning"] != true || m["lease_minutes"] != float64(30) || m["max_pay_minutes"] != float64(32) {
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

func TestPayBrowserCheckNoChrome(t *testing.T) {
	payEnv(t)
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
