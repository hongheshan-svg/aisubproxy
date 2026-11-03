package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
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
		req := httptest.NewRequest(rt.method, strings.NewReplacer(":id", "1", ":name", "zovocard", ":product_id", "P1").Replace(rt.path), strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("无凭证请求 %s %s 应 401，实际 %d", rt.method, rt.path, w.Code)
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
	if on, err := opts.MarsWallexSelected(ctx); err != nil || on {
		t.Fatalf("初始默认是 zovocard，应未选用: %v %v", on, err)
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
	// 值不合法按现状（ZovoCard）算：对自动补卡来说是暂停，不会多花钱
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
