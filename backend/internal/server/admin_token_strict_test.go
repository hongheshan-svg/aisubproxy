package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/tuzi/cdk-recharge-system/internal/auth"
)

// B1-11：管理员令牌校验要和代理商令牌一样严：只认 HS256，必须带 exp。
func TestAdminTokenRequiresHS256AndExp(t *testing.T) {
	r := oaRouter(t)
	oaCreateAdmin(t, "strictadmin")
	cookies := oaLogin(t, r, "10.9.1.1", "strictadmin", oaPassword)
	id := int64(1)

	sign := func(m jwt.SigningMethod, withExp bool) string {
		rc := jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(time.Now())}
		if withExp {
			rc.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
		}
		s, err := jwt.NewWithClaims(m, auth.CustomClaims{UserID: id, IsAdmin: true, Username: "strictadmin", RegisteredClaims: rc}).SignedString([]byte(auth.JWTSecret()))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	get := func(tok string) int {
		return oaDo(r, oaReq{method: "GET", path: "/api/v1/auth/admin/me", header: map[string]string{"Authorization": "Bearer " + tok}, ip: "10.9.1.2"}).Code
	}

	if c := get(sign(jwt.SigningMethodHS256, true)); c != http.StatusOK {
		t.Fatalf("正常 HS256 带 exp 的令牌应 200，实际 %d", c)
	}
	if c := get(sign(jwt.SigningMethodHS384, true)); c != http.StatusUnauthorized {
		t.Fatalf("HS384 令牌应 401，实际 %d", c)
	}
	if c := get(sign(jwt.SigningMethodHS256, false)); c != http.StatusUnauthorized {
		t.Fatalf("不带 exp 的令牌应 401，实际 %d", c)
	}
	// 正常登录拿到的 cookie 令牌照常可用
	if w := oaDo(r, oaReq{method: "GET", path: "/api/v1/auth/admin/me", cookies: cookies, ip: "10.9.1.3"}); w.Code != http.StatusOK {
		t.Fatalf("登录 cookie 应 200，实际 %d", w.Code)
	}
}
