package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/handler"
)

// 代理商功能关闭时，老的代理换码接口照常可用（不是 agent_disabled 的 404）；开关接口要管理员登录。
func TestAgentFeatureOff_PartnerSwapAndAdminSwitch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("WEB_DIR", "")
	dbtest.Open(t)
	old := handler.AgentFeatureEnabled()
	handler.SetAgentFeatureEnabled(false)
	t.Cleanup(func() { handler.SetAgentFeatureEnabled(old) })
	r := gin.New()
	setupRoutes(r)

	req := httptest.NewRequest("POST", "/api/v1/public/cdk/exchange", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound || strings.Contains(w.Body.String(), "agent_disabled") {
		t.Fatalf("代理换码不应受开关影响: %d %s", w.Code, w.Body)
	}

	for _, m := range []string{"GET", "PUT"} {
		req := httptest.NewRequest(m, "/api/v1/admin/features", strings.NewReader(`{"agent_enabled":true}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s /admin/features 无凭证应 401，实际 %d %s", m, w.Code, w.Body)
		}
	}
	if handler.AgentFeatureEnabled() {
		t.Fatal("未登录的请求不应改变开关")
	}

	req = httptest.NewRequest("GET", "/api/v1/agent/status", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"enabled":false}` {
		t.Fatalf("状态接口: %d %s", w.Code, w.Body)
	}
}
