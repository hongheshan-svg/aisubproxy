package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func clientIPVia(t *testing.T, raw, remote string, alert func(string)) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	applyTrustedProxies(r, raw, alert)
	r.GET("/ip", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })
	req := httptest.NewRequest("GET", "/ip", nil)
	req.RemoteAddr = remote + ":5000"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Body.String()
}

// B1-01：写错的项跳过，后面的项照常生效，并发一条告警；全对时不告警。
func TestTrustedProxiesSkipsBadEntries(t *testing.T) {
	var alerts []string
	alert := func(s string) { alerts = append(alerts, s) }

	if got := clientIPVia(t, "caddy,10.0.0.0/33,172.16.0.0/12", "172.16.0.5", alert); got != "203.0.113.9" {
		t.Fatalf("坏项后面的网段应照常生效，客户端地址 = %q", got)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "caddy") || !strings.Contains(alerts[0], "10.0.0.0/33") {
		t.Fatalf("应发一条告警并点出坏项: %v", alerts)
	}

	alerts = nil
	if got := clientIPVia(t, "172.16.0.0/12, 127.0.0.1", "172.16.0.5", alert); got != "203.0.113.9" || len(alerts) != 0 {
		t.Fatalf("全对时应生效且不告警: %q %v", got, alerts)
	}
	// 不在信任范围内的来源，不采信 X-Forwarded-For
	if got := clientIPVia(t, "caddy,172.16.0.0/12", "198.51.100.7", alert); got != "198.51.100.7" {
		t.Fatalf("非信任来源应取对端地址: %q", got)
	}
	// 没配：谁都不信
	alerts = nil
	if got := clientIPVia(t, "", "172.16.0.5", alert); got != "172.16.0.5" || len(alerts) != 0 {
		t.Fatalf("没配时不信任任何代理: %q %v", got, alerts)
	}
}
