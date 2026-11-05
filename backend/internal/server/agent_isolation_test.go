package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
	"github.com/tuzi/cdk-recharge-system/internal/auth"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/handler"
)

func TestAgentTokenRejectedByAdminMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/admin", JWTAuthMiddleware(), AdminAuthMiddleware())
	g.GET("/x", func(c *gin.Context) { c.String(200, "ok") })
	tok, _, err := agent.SignSession(1, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*http.Request){
		"cookie": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "auth_token", Value: tok}) },
		"bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) },
	} {
		req := httptest.NewRequest("GET", "/admin/x", nil)
		mod(req)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("%s: %d", name, w.Code)
		}
	}
}

func TestAdminTokenRejectedByAgentMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbtest.Open(t)
	r := gin.New()
	api := r.Group("/api/v1")
	handler.RegisterAgentRoutes(api)
	claims := auth.CustomClaims{UserID: 1, IsAdmin: true, Username: "admin", RegisteredClaims: jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(auth.JWTSecret()))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/agent/me", nil)
	req.AddCookie(&http.Cookie{Name: "agent_token", Value: tok})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// 管理员流水的两条新路由在 admin 组内：无凭证、代理商会话都被拒。
func TestAdminAgentLedgerRoutesRejectNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("WEB_DIR", "")
	r := gin.New()
	setupRoutes(r)
	tok, _, err := agent.SignSession(1, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/admin/agents/1/ledger", "/api/v1/admin/agents/1/ledger/export"} {
		for name, mod := range map[string]func(*http.Request){
			"无凭证":         func(*http.Request) {},
			"代理商 cookie":  func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "auth_token", Value: tok}) },
			"代理商 bearer":  func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) },
			"agent_token": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "agent_token", Value: tok}) },
		} {
			req := httptest.NewRequest("GET", path, nil)
			mod(req)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s 应 401，实际 %d", name, path, w.Code)
			}
		}
	}
}
