package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// 审计 OA 组（鉴权与会话）的 handler 层测试。

const oaHPassword = "Str0ngPassw0rd-OAH"

// oaHResetLimiters 清空登录、安装两张限流表，测试之间互不影响。
func oaHResetLimiters(t *testing.T) {
	t.Helper()
	loginLimiter.reset()
	setupLimiter.reset()
	t.Cleanup(func() {
		loginLimiter.reset()
		setupLimiter.reset()
	})
}

func oaHEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbtest.Open(t)
	oaHResetLimiters(t)
	r := gin.New()
	r.POST("/login", AdminLogin)
	r.POST("/bootstrap", SetupBootstrap)
	return r
}

func oaHPost(r http.Handler, path, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// oaHClock 是可拨动的假时钟。
type oaHClock struct{ t time.Time }

func (c *oaHClock) now() time.Time      { return c.t }
func (c *oaHClock) add(d time.Duration) { c.t = c.t.Add(d) }
func oaHNewClock() *oaHClock            { return &oaHClock{t: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)} }
func oaHLimiter(c *oaHClock, max int) *failLimiter {
	l := newFailLimiter(5, 15*time.Minute, max)
	l.now = c.now
	return l
}

// A-22：同一个键窗口内失败到上限就锁定，锁过期后放行；成功清掉计数。
func TestOAFailLimiterLocksAndUnlocks(t *testing.T) {
	c := oaHNewClock()
	l := oaHLimiter(c, 100)
	for i := 0; i < 4; i++ {
		l.failed("ip|admin")
	}
	if ok, _ := l.allowed("ip|admin"); !ok {
		t.Fatal("4 次失败还不该锁")
	}
	l.failed("ip|admin")
	if ok, wait := l.allowed("ip|admin"); ok || wait <= 0 {
		t.Fatalf("5 次失败应锁定，ok=%v wait=%v", ok, wait)
	}
	c.add(15*time.Minute + time.Second)
	if ok, _ := l.allowed("ip|admin"); !ok {
		t.Fatal("锁过期后应放行")
	}
	l.failed("ip|other")
	l.succeeded("ip|other")
	if l.size() != 0 {
		t.Fatalf("锁过期、成功之后表应为空，实际 %d", l.size())
	}
}

// A-4：大量不同键的失败（随机用户名、多 IP）不会让表无限长：过期的键会被清掉，表有上限。
func TestOAFailLimiterBoundedAndSwept(t *testing.T) {
	c := oaHNewClock()
	l := oaHLimiter(c, 50)
	for i := 0; i < 500; i++ {
		l.failed(fmt.Sprintf("10.0.0.1|user%d", i))
	}
	if n := l.size(); n > 50 {
		t.Fatalf("表应封顶 50 个键，实际 %d", n)
	}
	// 窗口过后再来一次失败，顺带把过期的键清掉
	c.add(16 * time.Minute)
	l.failed("10.0.0.2|admin")
	if n := l.size(); n != 1 {
		t.Fatalf("过期的键应被清掉，只剩新键，实际 %d", n)
	}
	// 被锁的键在锁定期内不被清理
	for i := 0; i < 5; i++ {
		l.failed("10.0.0.3|admin")
	}
	c.add(2 * time.Minute)
	if got := l.sweep(); got != 0 {
		t.Fatalf("锁定期内不应清掉任何键，清了 %d", got)
	}
	if ok, _ := l.allowed("10.0.0.3|admin"); ok {
		t.Fatal("清理后锁定仍应生效")
	}
}

// A-4：登录、安装两张表都有上限。
func TestOAAuthLimitersHaveCaps(t *testing.T) {
	for name, l := range map[string]*failLimiter{"login": loginLimiter, "setup": setupLimiter} {
		if l.maxKeys <= 0 || l.maxKeys > 100000 {
			t.Errorf("%s 限流表上限不合理：%d", name, l.maxKeys)
		}
	}
}
