package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func postFrom(r http.Handler, path, ip, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// F1：账单检查的旧路径 /api/v1/billing/check 和新路径一样，先限流、再读请求体，请求体超过 256 KB 返回 413。
// 请求体 {} 在处理函数里直接 400（没填卡密或 session），不会访问网络。
func TestLegacyBillingCheckGuarded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("WEB_DIR", "")
	r := gin.New()
	setupRoutes(r)

	for _, path := range []string{"/api/v1/billing/check", "/api/v1/public/billing/check"} {
		big := `{"pad":"` + strings.Repeat("x", 300<<10) + `"}`
		if w := postFrom(r, path, "203.0.113.90", big); w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "请求太大") {
			t.Errorf("%s 请求体超过 256 KB 应 413: %d %s", path, w.Code, w.Body.String())
		}
	}

	const ip = "203.0.113.91"
	limited := 0
	for i := 0; i < 400; i++ {
		w := postFrom(r, "/api/v1/billing/check", ip, `{}`)
		if w.Code == http.StatusTooManyRequests {
			limited = i
			break
		}
		if w.Code != http.StatusBadRequest {
			t.Fatalf("第 %d 次: %d %s", i, w.Code, w.Body.String())
		}
	}
	if limited == 0 {
		t.Fatal("旧路径应被限流")
	}
	// 新旧路径同一个额度：旧路径用完，新路径也被挡住
	if w := postFrom(r, "/api/v1/public/billing/check", ip, `{}`); w.Code != http.StatusTooManyRequests {
		t.Fatalf("新旧路径应共用额度: %d", w.Code)
	}
}
