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
