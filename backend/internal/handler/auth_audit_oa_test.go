package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
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

func oaHCreateAdmin(t *testing.T, username string) {
	t.Helper()
	if err := db.CreateAdminUser(username, oaHPassword, ""); err != nil {
		t.Fatal(err)
	}
}

// oaHCountVerify 把密码校验换成计数的包装，返回调用次数的指针。
func oaHCountVerify(t *testing.T) *int {
	t.Helper()
	n := 0
	orig := verifyAdminPassword
	verifyAdminPassword = func(stored, plain string) (bool, string) {
		n++
		return orig(stored, plain)
	}
	t.Cleanup(func() { verifyAdminPassword = orig })
	return &n
}

func oaHLogin(r http.Handler, username, password string) *httptest.ResponseRecorder {
	return oaHPost(r, "/login", fmt.Sprintf(`{"username":%q,"password":%q}`, username, password), nil)
}

// A-8：用户名不存在、账号禁用、密码错误三种情况响应一样（401、同一句话），都跑一次 bcrypt，都计失败。
func TestOALoginDoesNotRevealUsername(t *testing.T) {
	r := oaHEngine(t)
	oaHCreateAdmin(t, "oa_enum")
	calls := oaHCountVerify(t)

	type result struct {
		code int
		body string
	}
	got := map[string]result{}
	for name, req := range map[string][2]string{
		"不存在": {"oa_nobody", oaHPassword},
		"密码错": {"oa_enum", "Wr0ngPassword-x"},
	} {
		*calls = 0
		w := oaHLogin(r, req[0], req[1])
		got[name] = result{w.Code, w.Body.String()}
		if *calls != 1 {
			t.Errorf("%s：应跑一次密码校验（耗时一致），实际 %d 次", name, *calls)
		}
	}
	if _, err := db.DB.Exec(`UPDATE admin_users SET is_active = 0 WHERE username = 'oa_enum'`); err != nil {
		t.Fatal(err)
	}
	*calls = 0
	w := oaHLogin(r, "oa_enum", oaHPassword)
	got["已禁用"] = result{w.Code, w.Body.String()}
	if *calls != 1 {
		t.Errorf("已禁用：应跑一次密码校验，实际 %d 次", *calls)
	}
	want := got["密码错"]
	if want.code != http.StatusUnauthorized || !strings.Contains(want.body, "用户名或密码错误") {
		t.Fatalf("密码错应 401 用户名或密码错误：%+v", want)
	}
	for name, g := range got {
		if g != want {
			t.Errorf("%s 的响应和密码错不一样：%+v vs %+v", name, g, want)
		}
	}
	// 禁用账号的失败也计数：再 4 次就锁定（同一 IP+用户名）
	for i := 0; i < 4; i++ {
		oaHLogin(r, "oa_enum", oaHPassword)
	}
	if w := oaHLogin(r, "oa_enum", oaHPassword); w.Code != http.StatusTooManyRequests {
		t.Fatalf("禁用账号连续失败也应被锁定，实际 %d", w.Code)
	}
}

// A-22：同一 IP+用户名失败 5 次后锁定，锁定期间正确密码也 429；成功登录清零计数。
func TestOALoginLockout(t *testing.T) {
	r := oaHEngine(t)
	oaHCreateAdmin(t, "oa_lock")
	for i := 0; i < 4; i++ {
		if w := oaHLogin(r, "oa_lock", "Wr0ngPassword-x"); w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错密码应 401，实际 %d", i+1, w.Code)
		}
	}
	if w := oaHLogin(r, "oa_lock", oaHPassword); w.Code != http.StatusOK {
		t.Fatalf("锁定前正确密码应 200，实际 %d", w.Code)
	}
	for i := 0; i < 5; i++ {
		oaHLogin(r, "oa_lock", "Wr0ngPassword-x")
	}
	if w := oaHLogin(r, "oa_lock", oaHPassword); w.Code != http.StatusTooManyRequests {
		t.Fatalf("锁定期间正确密码也应 429，实际 %d", w.Code)
	}
}

const oaHSetupToken = "oa-h-setup-token-0123456789"

// A-11：安装自检失败回滚后，同一个安装令牌仍可用，不用重启就能重试。
func TestOASetupRollbackKeepsToken(t *testing.T) {
	t.Setenv("SETUP_BOOTSTRAP_TOKEN", oaHSetupToken)
	t.Setenv("INSTALL_MODE", "")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("SETUP_ALLOW_CIDRS", "")
	r := oaHEngine(t)
	hdr := map[string]string{"X-Setup-Token": oaHSetupToken}

	orig := verifyAdminPassword
	verifyAdminPassword = func(string, string) (bool, string) { return false, "" }
	w := oaHPost(r, "/bootstrap", `{"mode":"generate","username":"oa_rb"}`, hdr)
	verifyAdminPassword = orig
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("自检失败应 500，实际 %d：%s", w.Code, w.Body.String())
	}
	if db.IsInstalled() {
		t.Fatal("回滚后应仍是未安装")
	}
	if !db.VerifySetupToken(oaHSetupToken) {
		t.Fatal("回滚后原安装令牌应仍可用")
	}
	w = oaHPost(r, "/bootstrap", `{"mode":"generate","username":"oa_rb"}`, hdr)
	if w.Code != http.StatusOK {
		t.Fatalf("回滚后用同一令牌重试应 200，实际 %d：%s", w.Code, w.Body.String())
	}
	if db.VerifySetupToken(oaHSetupToken) {
		t.Fatal("安装成功后令牌应作废")
	}
}

// A-22：已安装再调 bootstrap 一律 410；错令牌 401 并计失败，5 次后 429。
func TestOASetupBootstrapGuards(t *testing.T) {
	t.Setenv("SETUP_BOOTSTRAP_TOKEN", oaHSetupToken)
	t.Setenv("INSTALL_MODE", "")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("SETUP_ALLOW_CIDRS", "")
	r := oaHEngine(t)
	for i := 0; i < 5; i++ {
		if w := oaHPost(r, "/bootstrap", `{"mode":"generate"}`, map[string]string{"X-Setup-Token": "wrong"}); w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错令牌应 401，实际 %d", i+1, w.Code)
		}
	}
	if w := oaHPost(r, "/bootstrap", `{"mode":"generate"}`, map[string]string{"X-Setup-Token": oaHSetupToken}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("错 5 次后应 429，实际 %d", w.Code)
	}
	setupLimiter.reset()
	if w := oaHPost(r, "/bootstrap", `{"mode":"generate","username":"oa_guard"}`, map[string]string{"X-Setup-Token": oaHSetupToken}); w.Code != http.StatusOK {
		t.Fatalf("安装应 200，实际 %d：%s", w.Code, w.Body.String())
	}
	if w := oaHPost(r, "/bootstrap", `{"mode":"generate"}`, map[string]string{"X-Setup-Token": oaHSetupToken}); w.Code != http.StatusGone {
		t.Fatalf("已安装再调应 410，实际 %d", w.Code)
	}
}
