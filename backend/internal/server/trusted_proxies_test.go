package server

import (
	"bytes"
	"log"
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

// 审查修复：坏项里带账号口令或协议时，日志和告警都不能出现原文。
func TestTrustedProxiesMasksBadEntries(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	var alerts []string
	clientIPVia(t, "user:S3cretPass@proxy.example.com,http://u:S3cretPass@10.0.0.1:3128,"+strings.Repeat("x", 200)+",172.16.0.0/12", "172.16.0.5", func(s string) { alerts = append(alerts, s) })
	all := buf.String() + strings.Join(alerts, "\n")
	if strings.Contains(all, "S3cretPass") || strings.Contains(all, "proxy.example.com") || strings.Contains(all, strings.Repeat("x", 65)) {
		t.Fatalf("日志或告警泄露坏项原文: %s", all)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "已隐去") {
		t.Fatalf("应有一条告警并说明已隐去: %v", alerts)
	}
}
