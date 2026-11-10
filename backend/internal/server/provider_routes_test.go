package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/provider/providertest"
)

var providerAdminRoutes = []struct{ method, path string }{
	{"GET", "/api/v1/admin/providers"},
	{"PUT", "/api/v1/admin/providers/config"},
	{"GET", "/api/v1/admin/providers/default"},
	{"PUT", "/api/v1/admin/providers/default"},
	{"GET", "/api/v1/admin/providers/readiness"},
	{"GET", "/api/v1/admin/providers/:name/card-heads"},
	{"POST", "/api/v1/admin/providers/:name/card-heads"},
	{"POST", "/api/v1/admin/providers/:name/card-heads/sync"},
	{"PUT", "/api/v1/admin/providers/:name/card-heads/:product_id"},
	{"DELETE", "/api/v1/admin/providers/:name/card-heads/:product_id"},
	{"POST", "/api/v1/admin/cdks/issue"},
	{"GET", "/api/v1/admin/cdks"},
	{"POST", "/api/v1/admin/cdks/:id/disable"},
	{"POST", "/api/v1/admin/cdks/:id/enable"},
}

func TestProviderRoutesRegisteredAndRequireAdminAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("WEB_DIR", "")
	r := gin.New()
	setupRoutes(r)
	have := map[string]bool{}
	for _, rt := range r.Routes() {
		have[rt.Method+" "+rt.Path] = true
	}
	for _, rt := range providerAdminRoutes {
		if !have[rt.method+" "+rt.path] {
			t.Errorf("路由缺失: %s %s", rt.method, rt.path)
			continue
		}
		req := httptest.NewRequest(rt.method, strings.NewReplacer(":id", "1", ":name", "marswallex", ":product_id", "P1").Replace(rt.path), strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("无凭证请求 %s %s 应 401，实际 %d", rt.method, rt.path, w.Code)
		}
	}
}

// 代理换码记录接口挪到 /api/v1/admin/agent-exchanges：新路径要管理员登录，登录后列表和定论都能用；
// 旧的 /api/v1/admin/cardplatform/agent-exchanges 不再存在，登录与否都是 404（不是处理函数回的 404）。
func TestAgentExchangeAdminRoutesMoved(t *testing.T) {
	r := oaRouter(t)
	const ip = "198.51.100.71"
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/admin/agent-exchanges"},
		{"POST", "/api/v1/admin/agent-exchanges/1/resolve"},
	} {
		if w := oaDo(r, oaReq{method: c.method, path: c.path, body: `{}`, ip: ip}); w.Code != http.StatusUnauthorized {
			t.Errorf("无凭证 %s %s 应 401，实际 %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}

	oaCreateAdmin(t, "swap-admin")
	cs := oaLogin(t, r, ip, "swap-admin", oaPassword)
	// 一条结果不明的本站码换码记录，用来走通定论
	id, err := db.ReserveAgentCDKExchange(db.AgentExchangeReservation{
		OldHash: db.HashCDKCode("MW-ROUTE-AAAA-BBBB-CCCC"), OldPrefix: "MW-ROUTE", Plan: "plus", OrderStatus: "not_submitted", PasswordFP: "fp",
	}, 20, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkAgentCDKExchangeUnknown(id, "占位超时未收尾，结果不明"); err != nil {
		t.Fatal(err)
	}
	list := oaDo(r, oaReq{method: "GET", path: "/api/v1/admin/agent-exchanges?state=unknown", cookies: cs, ip: ip})
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), fmt.Sprintf(`"id":%d`, id)) {
		t.Fatalf("登录后列表应 200 并含这条记录: %d %s", list.Code, list.Body.String())
	}
	res := oaDo(r, oaReq{method: "POST", path: fmt.Sprintf("/api/v1/admin/agent-exchanges/%d/resolve", id),
		body: `{"result":"released","note":"路由测试"}`, cookies: cs, header: oaCSRF(cs), ip: ip})
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"result":"released"`) {
		t.Fatalf("登录后定论应 200: %d %s", res.Code, res.Body.String())
	}

	// 旧路径：请求体故意不带 note，路由还在的话处理函数会回 400，不会是 404
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/admin/cardplatform/agent-exchanges"},
		{"POST", "/api/v1/admin/cardplatform/agent-exchanges/1/resolve"},
	} {
		for name, q := range map[string]oaReq{
			"已登录": {method: c.method, path: c.path, body: `{}`, cookies: cs, header: oaCSRF(cs), ip: ip},
			"无凭证": {method: c.method, path: c.path, body: `{}`, ip: ip},
		} {
			w := oaDo(r, q)
			if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "换码记录") {
				t.Errorf("%s %s %s 应 404（路由已删），实际 %d %s", name, c.method, c.path, w.Code, w.Body.String())
			}
		}
	}
}

// 防漏接线：卡池的闸要接到真实的判定函数上，否则自动补卡在未选用 MarsWallex 时还会花钱。
func TestCardPoolOptionsWiresProviderGate(t *testing.T) {
	dbtest.Open(t)
	opts := cardPoolOptions(func(string) {})
	if opts.MarsWallexSelected == nil {
		t.Fatal("MarsWallexSelected 没有接线")
	}
	if opts.Client == nil || opts.Alert == nil {
		t.Fatal("原有选项不能丢")
	}
	ctx := context.Background()
	if on, err := opts.MarsWallexSelected(ctx); err != nil || !on {
		t.Fatalf("出厂配置 GPT 用 MarsWallex，应选用: %v %v", on, err)
	}
	providertest.Register(t, providertest.NewTestUp())
	if err := provider.SaveDefault(providertest.TestUp); err != nil {
		t.Fatal(err)
	}
	if on, err := opts.MarsWallexSelected(ctx); err != nil || on {
		t.Fatalf("切到别家后应未选用: %v %v", on, err)
	}
	if err := provider.SaveDefault(provider.MarsWallex); err != nil {
		t.Fatal(err)
	}
	if on, err := opts.MarsWallexSelected(ctx); err != nil || !on {
		t.Fatalf("切到 MarsWallex 后应选用: %v %v", on, err)
	}
	if err := db.SetSetting(provider.SettingDefault, "bogus"); err != nil {
		t.Fatal(err)
	}
	// 值不合法时全局默认为空：对自动补卡来说是暂停，不会多花钱
	if on, err := opts.MarsWallexSelected(ctx); err != nil || on {
		t.Fatalf("值不合法应按未选用: %v %v", on, err)
	}
}

func TestRedactQuery(t *testing.T) {
	const secret = "MW-AAAAA-BBBBB"
	for _, tc := range []struct{ in, want string }{
		{"/lookup/cdk?code=" + secret, "/lookup/cdk?code=***"},
		{"/public/cdk/result?token=mwt_abc&x=1", "/public/cdk/result?token=***&x=1"},
		{"/public/cdk/result-by-code?a=1&cdk_code=" + secret + "&b=2", "/public/cdk/result-by-code?a=1&cdk_code=***&b=2"},
		{"/x?CODE=" + secret, "/x?CODE=***"},
		{"/x?%63ode=" + secret, "/x?%63ode=***"},
		{"/x?%63%6Fde=" + secret, "/x?%63%6Fde=***"},
		{"/x?code", "/x?code=***"},
		{"/x?page=2", "/x?page=2"},
		{"/x", "/x"},
		{"/x?", "/x?"},
	} {
		if got := redactQuery(tc.in); got != tc.want {
			t.Errorf("%s: %s，期望 %s", tc.in, got, tc.want)
		}
	}
}

// 真实走一遍引擎：访问日志和崩溃日志里都没有码和令牌，其余参数还在。
func TestEngineLogsDoNotLeakCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const code, token = "MW-AAAAA-BBBBB-CCCCC", "mwt_0123456789abcdef"
	var out, errOut bytes.Buffer
	oldOut, oldErr := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &out, &errOut
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = oldOut, oldErr })

	r := newEngine()
	r.GET("/ok", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	r.GET("/boom", func(*gin.Context) { panic("boom") })

	for _, target := range []string{
		"/ok?code=" + code + "&page=2",
		"/ok?%63ode=" + code,
		"/ok?token=" + token,
		"/ok?cdk_code=" + code,
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s: %d", target, w.Code)
		}
	}
	if s := out.String(); strings.Contains(s, code) || strings.Contains(s, token) || !strings.Contains(s, "page=2") {
		t.Fatalf("访问日志: %s", s)
	}

	// gin 只在调试模式下把请求行写进崩溃日志（线上没设 GIN_MODE=release 时就是调试模式）
	gin.SetMode(gin.DebugMode)
	t.Cleanup(func() { gin.SetMode(gin.TestMode) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/boom?code="+code+"&token="+token+"&page=3", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("崩溃应由 Recovery 兜住: %d", w.Code)
	}
	if !strings.Contains(errOut.String(), "boom") || !strings.Contains(errOut.String(), "page=3") {
		t.Fatalf("崩溃日志要写，且带请求行: %s", errOut.String())
	}
	if s := errOut.String() + out.String(); strings.Contains(s, code) || strings.Contains(s, token) {
		t.Fatalf("崩溃日志泄露: %s", s)
	}
}

// ZovoCard 的接口、Webhook 和统计接口已整体删除：登录与否都是 404（路由不存在，不是处理函数回的 404），
// 同时卡服务商列表接口照旧可用。
func TestZovoCardRoutesGone(t *testing.T) {
	r := oaRouter(t)
	const ip = "198.51.100.72"
	oaCreateAdmin(t, "zovo-gone-admin")
	cs := oaLogin(t, r, ip, "zovo-gone-admin", oaPassword)

	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/webhooks/cardplatform"},
		{"GET", "/api/v1/admin/cardplatform/ping"},
		{"GET", "/api/v1/admin/cardplatform/cdks/stored"},
		{"GET", "/api/v1/stats/system"},
		{"GET", "/api/v1/public/cdk/plans"},
		{"GET", "/api/v1/admin/card-health"},
		{"GET", "/api/v1/admin/card-selection/site-policy"},
	} {
		for name, q := range map[string]oaReq{
			"已登录": {method: c.method, path: c.path, body: `{}`, cookies: cs, header: oaCSRF(cs), ip: ip},
			"无凭证": {method: c.method, path: c.path, body: `{}`, ip: ip},
		} {
			if w := oaDo(r, q); w.Code != http.StatusNotFound {
				t.Errorf("%s %s %s 应 404（路由已删），实际 %d %s", name, c.method, c.path, w.Code, w.Body.String())
			}
		}
	}

	if w := oaDo(r, oaReq{method: "GET", path: "/api/v1/admin/providers", cookies: cs, ip: ip}); w.Code != http.StatusOK {
		t.Errorf("GET /api/v1/admin/providers 应照旧 200，实际 %d %s", w.Code, w.Body.String())
	}
}
