package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// 本文件是审计 OA 组（鉴权与会话）的接口级测试：走真实路由表和中间件，内存库。
// 登录限流表是 handler 包里的全局状态，这里每个测试用不同的来源 IP，互不影响。

const oaPassword = "Str0ngPassw0rd-OA"

// oaRouter 用内存库和真实路由表搭一个测试服务。
func oaRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("WEB_DIR", "")
	dbtest.Open(t)
	r := gin.New()
	setupRoutes(r)
	return r
}

func oaCreateAdmin(t *testing.T, username string) {
	t.Helper()
	if err := db.CreateAdminUser(username, oaPassword, ""); err != nil {
		t.Fatal(err)
	}
}

type oaReq struct {
	method, path, body string
	cookies            []*http.Cookie
	header             map[string]string
	ip                 string
}

func oaDo(r http.Handler, q oaReq) *httptest.ResponseRecorder {
	var body io.Reader
	if q.body != "" {
		body = strings.NewReader(q.body)
	}
	req := httptest.NewRequest(q.method, q.path, body)
	if q.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if q.ip != "" {
		req.RemoteAddr = q.ip + ":40000"
	}
	for k, v := range q.header {
		req.Header.Set(k, v)
	}
	for _, c := range q.cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func oaLoginBody(username, password string) string {
	return fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
}

// oaLogin 登录成功并返回下发的 cookie（auth_token、csrf_token）。
func oaLogin(t *testing.T, r http.Handler, ip, username, password string) []*http.Cookie {
	t.Helper()
	w := oaDo(r, oaReq{method: "POST", path: "/api/v1/auth/admin/login", body: oaLoginBody(username, password), ip: ip})
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200，实际 %d：%s", w.Code, w.Body.String())
	}
	return w.Result().Cookies()
}

func oaCookie(cs []*http.Cookie, name string) *http.Cookie {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// oaCSRF 取 csrf_token cookie 放进请求头，模拟前端的双提交。
func oaCSRF(cs []*http.Cookie) map[string]string {
	if c := oaCookie(cs, "csrf_token"); c != nil {
		return map[string]string{"X-CSRF-Token": c.Value}
	}
	return nil
}

func oaJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是 JSON：%v；%s", err, w.Body.String())
	}
	return m
}

// A-1：登录响应不再把 JWT 放进 JSON，只靠 HttpOnly cookie；cookie 属性和 CSRF 双提交值一并钉住（A-22）。
func TestOALoginResponseHasNoToken(t *testing.T) {
	r := oaRouter(t)
	oaCreateAdmin(t, "oa_login")
	w := oaDo(r, oaReq{method: "POST", path: "/api/v1/auth/admin/login", body: oaLoginBody("oa_login", oaPassword), ip: "198.51.100.1"})
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200，实际 %d：%s", w.Code, w.Body.String())
	}
	body := oaJSON(t, w)
	if _, ok := body["token"]; ok {
		t.Fatalf("登录响应不应含 token：%s", w.Body.String())
	}
	for _, k := range []string{"username", "name", "expires_at", "csrf_token"} {
		if s, _ := body[k].(string); s == "" {
			t.Errorf("登录响应缺 %s：%s", k, w.Body.String())
		}
	}
	cs := w.Result().Cookies()
	at := oaCookie(cs, "auth_token")
	if at == nil || at.Value == "" || !at.HttpOnly || at.Path != "/" || at.SameSite != http.SameSiteLaxMode || at.MaxAge <= 0 {
		t.Fatalf("auth_token cookie 属性不对：%+v", at)
	}
	ct := oaCookie(cs, "csrf_token")
	if ct == nil || ct.Value == "" || ct.HttpOnly || ct.Value != body["csrf_token"] {
		t.Fatalf("csrf_token cookie 应可被前端读取且与响应一致：%+v", ct)
	}
	// 直连 HTTP、没有可信反代：不带 Secure
	if at.Secure || ct.Secure {
		t.Fatalf("纯 HTTP 请求不应下发 Secure cookie")
	}
	// 拿 cookie 能访问 /me，并带上显示名和到期时间（前端刷新后靠它恢复登录态）
	me := oaDo(r, oaReq{method: "GET", path: "/api/v1/auth/admin/me", cookies: cs})
	if me.Code != http.StatusOK {
		t.Fatalf("/me 应 200，实际 %d：%s", me.Code, me.Body.String())
	}
	mb := oaJSON(t, me)
	if mb["username"] != "oa_login" || mb["name"] == "" || mb["expires_at"] == "" || mb["expires_at"] == nil {
		t.Fatalf("/me 回包不全：%s", me.Body.String())
	}
}

// A-22：cookie 认证的写操作缺 CSRF 头或头与 cookie 不一致一律 403；读操作不需要。
func TestOACookieWritesRequireCSRF(t *testing.T) {
	r := oaRouter(t)
	oaCreateAdmin(t, "oa_csrf")
	cs := oaLogin(t, r, "198.51.100.2", "oa_csrf", oaPassword)
	body := `{"old_password":"x","new_password":"y"}`
	for name, hdr := range map[string]map[string]string{
		"缺头":  nil,
		"不一致": {"X-CSRF-Token": "not-the-cookie"},
	} {
		w := oaDo(r, oaReq{method: "POST", path: "/api/v1/auth/admin/change-password", body: body, cookies: cs, header: hdr})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s：应 403，实际 %d：%s", name, w.Code, w.Body.String())
		}
	}
	if w := oaDo(r, oaReq{method: "GET", path: "/api/v1/auth/admin/me", cookies: cs}); w.Code != http.StatusOK {
		t.Fatalf("读操作不需要 CSRF 头，实际 %d", w.Code)
	}
}

const oaSetupToken = "oa-setup-token-0123456789"

// oaFreshRouter 是未安装的新库：安装令牌用固定值，方便测试带上。
func oaFreshRouter(t *testing.T) *gin.Engine {
	t.Helper()
	t.Setenv("SETUP_BOOTSTRAP_TOKEN", oaSetupToken)
	t.Setenv("INSTALL_MODE", "")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("SETUP_ALLOW_CIDRS", "")
	return oaRouter(t)
}

func oaBootstrap(r http.Handler, ip, token, body string) *httptest.ResponseRecorder {
	h := map[string]string{}
	if token != "" {
		h["X-Setup-Token"] = token
	}
	return oaDo(r, oaReq{method: "POST", path: "/api/v1/setup/bootstrap", body: body, ip: ip, header: h})
}

// A-1：安装向导自动登录也只靠 cookie，响应里不带 JWT。
func TestOASetupResponseHasNoToken(t *testing.T) {
	r := oaFreshRouter(t)
	w := oaBootstrap(r, "198.51.100.10", oaSetupToken, `{"mode":"generate","username":"oa_setup"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("安装应 200，实际 %d：%s", w.Code, w.Body.String())
	}
	body := oaJSON(t, w)
	if _, ok := body["token"]; ok {
		t.Fatalf("安装响应不应含 token：%s", w.Body.String())
	}
	cs := w.Result().Cookies()
	if c := oaCookie(cs, "auth_token"); c == nil || !c.HttpOnly {
		t.Fatalf("安装成功应下发 HttpOnly auth_token：%+v", c)
	}
	if me := oaDo(r, oaReq{method: "GET", path: "/api/v1/auth/admin/me", cookies: cs}); me.Code != http.StatusOK {
		t.Fatalf("安装后的 cookie 应能访问 /me，实际 %d", me.Code)
	}
}

// A-2：改密码后，改密前签发的所有会话（包括别处登录的）立刻失效；改密成功同时清掉本机 cookie。
func TestOAChangePasswordRevokesOldSessions(t *testing.T) {
	r := oaRouter(t)
	oaCreateAdmin(t, "oa_chpwd")
	a := oaLogin(t, r, "198.51.100.3", "oa_chpwd", oaPassword)
	b := oaLogin(t, r, "198.51.100.4", "oa_chpwd", oaPassword) // 另一处（例如入侵者）的会话

	const newPwd = "An0therStr0ngPass"
	w := oaDo(r, oaReq{method: "POST", path: "/api/v1/auth/admin/change-password", cookies: a, header: oaCSRF(a),
		body: fmt.Sprintf(`{"old_password":%q,"new_password":%q}`, oaPassword, newPwd)})
	if w.Code != http.StatusOK {
		t.Fatalf("改密应 200，实际 %d：%s", w.Code, w.Body.String())
	}
	if c := oaCookie(w.Result().Cookies(), "auth_token"); c == nil || c.MaxAge >= 0 {
		t.Fatalf("改密成功应清掉本机 auth_token：%+v", c)
	}
	for name, cs := range map[string][]*http.Cookie{"本机旧会话": a, "别处会话": b} {
		if me := oaDo(r, oaReq{method: "GET", path: "/api/v1/auth/admin/me", cookies: cs}); me.Code != http.StatusUnauthorized {
			t.Errorf("%s改密后应 401，实际 %d", name, me.Code)
		}
	}
	// Bearer 头同样作废
	tok := oaCookie(b, "auth_token").Value
	if me := oaDo(r, oaReq{method: "GET", path: "/api/v1/auth/admin/me", header: map[string]string{"Authorization": "Bearer " + tok}}); me.Code != http.StatusUnauthorized {
		t.Errorf("改密前的 Bearer 令牌应 401，实际 %d", me.Code)
	}
	// 新密码重新登录可用
	c := oaLogin(t, r, "198.51.100.3", "oa_chpwd", newPwd)
	if me := oaDo(r, oaReq{method: "GET", path: "/api/v1/auth/admin/me", cookies: c}); me.Code != http.StatusOK {
		t.Fatalf("新会话应 200，实际 %d", me.Code)
	}
}

// A-2：账号被禁用后，已签发的令牌立刻失效；账号被删也一样。
func TestOADisabledOrDeletedAdminTokenRejected(t *testing.T) {
	r := oaRouter(t)
	oaCreateAdmin(t, "oa_disable")
	cs := oaLogin(t, r, "198.51.100.5", "oa_disable", oaPassword)
	if _, err := db.DB.Exec(`UPDATE admin_users SET is_active = 0 WHERE username = 'oa_disable'`); err != nil {
		t.Fatal(err)
	}
	if me := oaDo(r, oaReq{method: "GET", path: "/api/v1/auth/admin/me", cookies: cs}); me.Code != http.StatusUnauthorized {
		t.Fatalf("禁用后应 401，实际 %d", me.Code)
	}
	if _, err := db.DB.Exec(`UPDATE admin_users SET is_active = 1 WHERE username = 'oa_disable'`); err != nil {
		t.Fatal(err)
	}
	if me := oaDo(r, oaReq{method: "GET", path: "/api/v1/auth/admin/me", cookies: cs}); me.Code != http.StatusOK {
		t.Fatalf("重新启用后同一令牌应恢复 200，实际 %d", me.Code)
	}
	if _, err := db.DB.Exec(`DELETE FROM admin_users WHERE username = 'oa_disable'`); err != nil {
		t.Fatal(err)
	}
	if me := oaDo(r, oaReq{method: "GET", path: "/api/v1/admin/settings", cookies: cs}); me.Code != http.StatusUnauthorized {
		t.Fatalf("账号删除后应 401，实际 %d", me.Code)
	}
}

// A-13：登出要求有效会话；cookie 会话还要带 CSRF 头，第三方页面不能随意把管理员登出。
func TestOALogoutRequiresSessionAndCSRF(t *testing.T) {
	r := oaRouter(t)
	oaCreateAdmin(t, "oa_logout")
	cs := oaLogin(t, r, "198.51.100.6", "oa_logout", oaPassword)

	if w := oaDo(r, oaReq{method: "POST", path: "/api/v1/auth/admin/logout"}); w.Code != http.StatusUnauthorized {
		t.Errorf("无会话登出应 401，实际 %d", w.Code)
	}
	w := oaDo(r, oaReq{method: "POST", path: "/api/v1/auth/admin/logout", cookies: cs})
	if w.Code != http.StatusForbidden {
		t.Fatalf("缺 CSRF 头的登出应 403，实际 %d", w.Code)
	}
	if c := oaCookie(w.Result().Cookies(), "auth_token"); c != nil {
		t.Fatalf("被拒的登出不应动 cookie：%+v", c)
	}
	w = oaDo(r, oaReq{method: "POST", path: "/api/v1/auth/admin/logout", cookies: cs, header: oaCSRF(cs)})
	if w.Code != http.StatusOK {
		t.Fatalf("带 CSRF 头的登出应 200，实际 %d：%s", w.Code, w.Body.String())
	}
	for _, name := range []string{"auth_token", "csrf_token"} {
		if c := oaCookie(w.Result().Cookies(), name); c == nil || c.MaxAge >= 0 {
			t.Errorf("登出应清掉 %s：%+v", name, c)
		}
	}
}

// A-5：登录、改密、安装接口的请求体有上限，超大请求体 413，不把整个字符串读进内存。
func TestOAAuthEndpointsLimitBodySize(t *testing.T) {
	r := oaFreshRouter(t)
	huge := strings.Repeat("a", 300<<10)

	w := oaDo(r, oaReq{method: "POST", path: "/api/v1/auth/admin/login", body: oaLoginBody(huge, "x"), ip: "198.51.100.20"})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("超大登录体应 413，实际 %d", w.Code)
	}
	w = oaBootstrap(r, "198.51.100.21", oaSetupToken, fmt.Sprintf(`{"mode":"manual","username":"oa_big","password":%q}`, huge))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("超大安装体应 413，实际 %d", w.Code)
	}
	// 上面被拒，库仍未安装；正常装好后再测改密
	w = oaBootstrap(r, "198.51.100.21", oaSetupToken, `{"mode":"generate","username":"oa_big"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("安装应 200，实际 %d：%s", w.Code, w.Body.String())
	}
	cs := w.Result().Cookies()
	w = oaDo(r, oaReq{method: "POST", path: "/api/v1/auth/admin/change-password", cookies: cs, header: oaCSRF(cs),
		body: fmt.Sprintf(`{"old_password":"x","new_password":%q}`, huge)})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("超大改密体应 413，实际 %d", w.Code)
	}
	// 不带 Content-Length（分块上传）也一样：边读边数，超限即停
	req := httptest.NewRequest("POST", "/api/v1/auth/admin/login", strings.NewReader(oaLoginBody(huge, "x")))
	req.ContentLength = -1
	req.RemoteAddr = "198.51.100.23:40000"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("未知长度的超大登录体应 413，实际 %d", rec.Code)
	}
	// 正常大小的坏请求仍是 400
	w = oaDo(r, oaReq{method: "POST", path: "/api/v1/auth/admin/login", body: `{"username":`, ip: "198.51.100.22"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("坏 JSON 应 400，实际 %d", w.Code)
	}
}

// A-9：安装（自设密码）和改密入库前与登录同样去掉首尾空白，首尾带空格的密码设好后照样登得进。
func TestOAPasswordWhitespaceConsistent(t *testing.T) {
	r := oaFreshRouter(t)
	const spaced = "  Passw0rd-OA-Trim  "
	w := oaBootstrap(r, "198.51.100.30", oaSetupToken,
		fmt.Sprintf(`{"mode":"manual","username":"oa_trim","password":%q,"confirm_password":%q}`, spaced, spaced))
	if w.Code != http.StatusOK {
		t.Fatalf("安装应 200，实际 %d：%s", w.Code, w.Body.String())
	}
	if pw, _ := oaJSON(t, w)["password"].(string); pw != strings.TrimSpace(spaced) {
		t.Fatalf("回显给管理员保存的密码应是去掉空白后的值，实际 %q", pw)
	}
	// 前端登录页会 Trim；直接带空格提交后端也会 Trim
	cs := oaLogin(t, r, "198.51.100.31", "oa_trim", strings.TrimSpace(spaced))
	oaLogin(t, r, "198.51.100.31", "oa_trim", spaced)

	const newSpaced = " N3wPassw0rd-Trim\t"
	w = oaDo(r, oaReq{method: "POST", path: "/api/v1/auth/admin/change-password", cookies: cs, header: oaCSRF(cs),
		body: fmt.Sprintf(`{"old_password":%q,"new_password":%q}`, spaced, newSpaced)})
	if w.Code != http.StatusOK {
		t.Fatalf("改密应 200，实际 %d：%s", w.Code, w.Body.String())
	}
	oaLogin(t, r, "198.51.100.32", "oa_trim", strings.TrimSpace(newSpaced))
}
