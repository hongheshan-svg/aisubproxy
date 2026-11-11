package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// H1-02：登录类限流按 IPv6 /64 归并；IPv4 按完整地址；IPv4 映射的 IPv6 按 IPv4 处理。
func TestRateIPKeyGrouping(t *testing.T) {
	if rateIPKey("2001:db8::1") != rateIPKey("2001:db8::ffff") {
		t.Fatal("同一 /64 应归并")
	}
	if rateIPKey("2001:db8:0:1::1") == rateIPKey("2001:db8:0:2::1") {
		t.Fatal("不同 /64 不应归并")
	}
	if rateIPKey("1.2.3.4") == rateIPKey("1.2.3.5") {
		t.Fatal("IPv4 按完整地址")
	}
	if rateIPKey("::ffff:1.2.3.4") != rateIPKey("1.2.3.4") {
		t.Fatal("IPv4 映射的 IPv6 应按 IPv4 处理")
	}
}

func TestAdminLoginRateKeyMergesIPv6(t *testing.T) {
	a := adminLoginRateKey("2001:db8::1", " Admin ")
	b := adminLoginRateKey("2001:db8::2", "admin")
	if a != b {
		t.Fatalf("同 /64 同用户名应同键: %q %q", a, b)
	}
}

func TestAgentRateKeyMergesIPv6(t *testing.T) {
	mk := func(remote string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/", nil)
		c.Request.RemoteAddr = "[" + remote + "]:1234"
		return c
	}
	if agentRateKey(mk("2001:db8::1"), "bob") != agentRateKey(mk("2001:db8::9"), "bob") {
		t.Fatal("代理商登录限流键应按 /64 归并")
	}
}

func TestSetupLimiterMergesIPv6(t *testing.T) {
	setupLimiter.succeeded(rateIPKey("2001:db8:77::1"))
	for i := 0; i < setupMaxAttempts; i++ {
		setupFailed("2001:db8:77::" + string(rune('a'+i)))
	}
	if ok, _ := setupAllowed("2001:db8:77::ffff"); ok {
		t.Fatal("同 /64 换地址不应绕过安装接口限流")
	}
	setupSucceeded("2001:db8:77::1")
}
