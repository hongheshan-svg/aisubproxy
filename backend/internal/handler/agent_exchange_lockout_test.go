package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// 已知问题清单【涉及资金】：代理换码共用口令要查弱口令表，输错按口令（不按 IP）全局计数并锁定。

func doExchangeWithPassword(password, code, ip string) *httptest.ResponseRecorder {
	r := exchangeRouter()
	req := httptest.NewRequest("POST", "/exchange", strings.NewReader(`{"password":"`+password+`","code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAgentSwapPasswordRejectsWeak(t *testing.T) {
	dbtest.Open(t)
	for _, pw := range []string{"password1234", "123456789012", "aaaaaaaaaaaa", "qwerty123456"} {
		err := setAgentSwapPassword(pw)
		if err == nil {
			t.Fatalf("弱口令 %q 应被拒", pw)
		}
		if !strings.Contains(err.Error(), "太简单") {
			t.Fatalf("弱口令 %q 的提示应说明原因，实际：%v", pw, err)
		}
	}
	if err := setAgentSwapPassword(exchangePassword); err != nil {
		t.Fatalf("正常口令应能保存: %v", err)
	}
}

// 30 个不同 IP 各错一次（绕开每 IP 限流）就锁住整个换码接口：之后正确口令也换不了，只告警一次。
func TestAgentSwapGlobalLockout(t *testing.T) {
	dbtest.Open(t)
	seedExchange(t, exchangeOldCode)
	alerts := captureOpsAlerts(t)

	for i := 0; i < swapPasswordMaxFailures; i++ {
		w := doExchangeWithPassword("wrong-password-x", exchangeOldCode, fmt.Sprintf("198.51.100.%d", i+1))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错口令应 401，实际 %d: %s", i+1, w.Code, w.Body.String())
		}
	}
	w := doExchangeWithPassword(exchangePassword, exchangeOldCode, "203.0.113.200")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "暂停") {
		t.Fatalf("锁定后正确口令也应 429 并说明已暂停，实际 %d: %s", w.Code, w.Body.String())
	}
	if w := doExchangeWithPassword("wrong-password-y", exchangeOldCode, "203.0.113.201"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("锁定期间错口令也应 429，实际 %d", w.Code)
	}
	if n := alerts.count(); n != 1 {
		t.Fatalf("锁定应只告警 1 次，实际 %d: %v", n, alerts.texts())
	}
	if txt := alerts.texts()[0]; !strings.Contains(txt, "换码") || !strings.Contains(txt, "重设") {
		t.Fatalf("告警要说明是换码口令并给出处理办法: %s", txt)
	}
	// 口令在后台「CDK 卡密」页的「代理换码」标签里改（AgentSwapSettings），告警要指到这里，运维照着能找到。
	if txt := alerts.texts()[0]; !strings.Contains(txt, "「CDK 卡密 → 代理换码」") || strings.Contains(txt, "系统设置") {
		t.Fatalf("告警要写对重设口令的位置: %s", txt)
	}
}

func TestAgentSwapLockoutExpiresAfterWindow(t *testing.T) {
	dbtest.Open(t)
	seedExchange(t, exchangeOldCode)
	captureOpsAlerts(t)
	now := time.Now()
	swapPasswordFails.now = func() time.Time { return now }
	t.Cleanup(func() { swapPasswordFails.now = time.Now })

	for i := 0; i < swapPasswordMaxFailures; i++ {
		doExchangeWithPassword("wrong-password-x", exchangeOldCode, fmt.Sprintf("198.51.100.%d", i+1))
	}
	if w := doExchangeWithPassword(exchangePassword, exchangeOldCode, "203.0.113.200"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("应已锁定，实际 %d", w.Code)
	}
	now = now.Add(swapPasswordLockWindow + time.Second)
	if w := doExchangeWithPassword(exchangePassword, exchangeOldCode, "203.0.113.202"); w.Code == http.StatusTooManyRequests || w.Code == http.StatusUnauthorized {
		t.Fatalf("锁定窗口过后正确口令应能继续，实际 %d: %s", w.Code, w.Body.String())
	}
}

func TestAgentSwapLockoutClearedByPasswordReset(t *testing.T) {
	dbtest.Open(t)
	seedExchange(t, exchangeOldCode)
	captureOpsAlerts(t)
	for i := 0; i < swapPasswordMaxFailures; i++ {
		doExchangeWithPassword("wrong-password-x", exchangeOldCode, fmt.Sprintf("198.51.100.%d", i+1))
	}
	const newPassword = "brand-new-swap-key-7"
	if err := setAgentSwapPassword(newPassword); err != nil {
		t.Fatal(err)
	}
	if w := doExchangeWithPassword(newPassword, exchangeOldCode, "203.0.113.203"); w.Code == http.StatusTooManyRequests || w.Code == http.StatusUnauthorized {
		t.Fatalf("重设口令后应立即解锁，实际 %d: %s", w.Code, w.Body.String())
	}
}

// 正确口令不清零全局计数：否则猜口令的人可以借代理的正常使用无限续命。
func TestAgentSwapSuccessDoesNotResetFailures(t *testing.T) {
	dbtest.Open(t)
	seedExchange(t, exchangeOldCode)
	captureOpsAlerts(t)
	for i := 0; i < swapPasswordMaxFailures-1; i++ {
		doExchangeWithPassword("wrong-password-x", exchangeOldCode, fmt.Sprintf("198.51.100.%d", i+1))
	}
	doExchangeWithPassword(exchangePassword, exchangeOldCode, "203.0.113.204")
	doExchangeWithPassword("wrong-password-x", exchangeOldCode, "198.51.100.250")
	if w := doExchangeWithPassword(exchangePassword, exchangeOldCode, "203.0.113.205"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("中间一次正确口令不应清零计数，第 %d 次错口令后应锁定，实际 %d", swapPasswordMaxFailures, w.Code)
	}
}
