package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// featureRouter 挂上受开关控制的三组路由和开关接口；管理员组用假中间件直接当成已登录的 admin。
func featureRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbtest.Open(t)
	resetAgentAuthState()
	resetSharedState()
	r := gin.New()
	RegisterSharedRoutes(&r.RouterGroup)
	RegisterAgentRoutes(r.Group("/api/v1"))
	adm := r.Group("/api/v1/admin")
	adm.Use(func(c *gin.Context) {
		c.Set("username", "admin")
		c.Set("is_admin", true)
		c.Next()
	})
	RegisterAgentAdminRoutes(adm)
	adm.GET("/features", AdminGetFeatures)
	adm.PUT("/features", AdminPutFeatures)
	return r
}

func featureDo(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAgentFeature_DefaultOff(t *testing.T) {
	dbtest.Open(t)
	setAgentFeatureForTest(t, true)
	if err := LoadFeatureFlags(); err != nil {
		t.Fatal(err)
	}
	if AgentFeatureEnabled() {
		t.Fatal("新库没有 agent_enabled 设置时应为关闭")
	}
}

func TestAgentFeature_LoadFromSetting(t *testing.T) {
	dbtest.Open(t)
	setAgentFeatureForTest(t, false)
	if err := db.SetSetting(SettingAgentEnabled, "1"); err != nil {
		t.Fatal(err)
	}
	if err := LoadFeatureFlags(); err != nil {
		t.Fatal(err)
	}
	if !AgentFeatureEnabled() {
		t.Fatal("agent_enabled=1 时应为打开")
	}
}

// 关闭时：代理商接口、异次元对接、管理员的代理商接口一律 404 agent_disabled；状态接口照常返回。
func TestAgentFeature_GateBlocksWhenOff(t *testing.T) {
	r := featureRouter(t)
	setAgentFeatureForTest(t, false)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/v1/agent/auth/login", `{"username":"a","password":"b"}`},
		{"GET", "/api/v1/agent/me", ""},
		{"GET", "/api/v1/agent/ledger", ""},
		{"POST", "/shared/authentication/connect", ""},
		{"POST", "/shared/commodity/trade", ""},
		{"GET", "/api/v1/admin/agents", ""},
		{"GET", "/api/v1/admin/agents/1/shared", ""},
		{"GET", "/api/v1/admin/agent-tiers", ""},
		{"GET", "/api/v1/admin/agent-products", ""},
		{"GET", "/api/v1/admin/agent-prices", ""},
		{"GET", "/api/v1/admin/agent-summary", ""},
	} {
		w := featureDo(r, tc.method, tc.path, tc.body)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s 应 404，实际 %d %s", tc.method, tc.path, w.Code, w.Body)
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Errorf("%s %s 响应不是 JSON: %s", tc.method, tc.path, w.Body)
			continue
		}
		if m["error"] != "agent_disabled" || m["message"] != "代理商功能未开放" {
			t.Errorf("%s %s 响应不对: %v", tc.method, tc.path, m)
		}
	}
	w := featureDo(r, "GET", "/api/v1/agent/status", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"enabled":false}` {
		t.Fatalf("状态接口: %d %s", w.Code, w.Body)
	}
}

func TestAgentFeature_GateOpenWhenOn(t *testing.T) {
	r := featureRouter(t)
	setAgentFeatureForTest(t, true)
	if w := featureDo(r, "GET", "/api/v1/agent/me", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("打开后没登录应 401，实际 %d %s", w.Code, w.Body)
	}
	if w := featureDo(r, "GET", "/api/v1/admin/agents", ""); w.Code != 200 {
		t.Fatalf("打开后管理员列表应 200，实际 %d %s", w.Code, w.Body)
	}
	if w := featureDo(r, "GET", "/api/v1/agent/status", ""); strings.TrimSpace(w.Body.String()) != `{"enabled":true}` {
		t.Fatalf("状态接口: %s", w.Body)
	}
}

func lastAudit(t *testing.T) (action, detail string) {
	t.Helper()
	if err := db.DB.QueryRow(`SELECT action, detail FROM admin_audit_logs ORDER BY id DESC LIMIT 1`).Scan(&action, &detail); err != nil {
		t.Fatal(err)
	}
	return action, detail
}

func TestAdminFeatures_Toggle(t *testing.T) {
	r := featureRouter(t)
	setAgentFeatureForTest(t, false)

	if w := featureDo(r, "GET", "/api/v1/admin/features", ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"agent_enabled":false}` {
		t.Fatalf("读开关: %d %s", w.Code, w.Body)
	}
	w := featureDo(r, "PUT", "/api/v1/admin/features", `{"agent_enabled":true}`)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"agent_enabled":true}` {
		t.Fatalf("打开: %d %s", w.Code, w.Body)
	}
	if !AgentFeatureEnabled() {
		t.Fatal("打开后内存里的开关没变")
	}
	if v, _ := db.GetSetting(SettingAgentEnabled); v != "1" {
		t.Fatalf("设置表应存 1，实际 %q", v)
	}
	if a, d := lastAudit(t); a != "admin_feature_toggle" || d != "agent_enabled=1" {
		t.Fatalf("审计: %s %s", a, d)
	}
	// 打开后受控接口立即可用，不用重启
	if w := featureDo(r, "GET", "/api/v1/admin/agents", ""); w.Code != 200 {
		t.Fatalf("打开后应立即生效，实际 %d", w.Code)
	}

	w = featureDo(r, "PUT", "/api/v1/admin/features", `{"agent_enabled":false}`)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"agent_enabled":false}` {
		t.Fatalf("关闭: %d %s", w.Code, w.Body)
	}
	if v, _ := db.GetSetting(SettingAgentEnabled); v != "0" {
		t.Fatalf("设置表应存 0，实际 %q", v)
	}
	if a, d := lastAudit(t); a != "admin_feature_toggle" || d != "agent_enabled=0" {
		t.Fatalf("审计: %s %s", a, d)
	}
	if w := featureDo(r, "GET", "/api/v1/admin/agents", ""); w.Code != http.StatusNotFound {
		t.Fatalf("关闭后应立即 404，实际 %d", w.Code)
	}

	// 缺字段、不是布尔值：400，开关不变
	for _, body := range []string{`{}`, `{"agent_enabled":"1"}`, `not json`} {
		if w := featureDo(r, "PUT", "/api/v1/admin/features", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s 应 400，实际 %d %s", body, w.Code, w.Body)
		}
	}
	if AgentFeatureEnabled() {
		t.Fatal("非法请求不应改变开关")
	}
}

// setAgentFeatureForTest 设定代理商功能开关，测试结束还原。
func setAgentFeatureForTest(t testing.TB, on bool) {
	t.Helper()
	old := AgentFeatureEnabled()
	SetAgentFeatureEnabled(on)
	t.Cleanup(func() { SetAgentFeatureEnabled(old) })
}
