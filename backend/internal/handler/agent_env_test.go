package handler

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

// agentEnv 是代理商接口测试共用的环境：本站码环境 + 可调的时钟 + 带代理商路由的 gin 引擎。
type agentEnv struct {
	*localEnv
	r     *gin.Engine
	clock time.Time
}

// resetAgentAuthState 清掉进程内的动态码计数、待绑定密钥和登录限流。
func resetAgentAuthState() {
	agentStateMu.Lock()
	agentPending = map[string]agentPendingSecret{}
	agentTOTPFails = map[string]agentFailCount{}
	agentStateMu.Unlock()
	agentGateMu.Lock()
	agentGates = map[int64]*agentTOTPGate{}
	agentGateMu.Unlock()
	loginLimiter.reset()
	agentLoginLimiter.reset()
}

func newAgentEnv(t *testing.T) *agentEnv {
	t.Helper()
	return agentEnvOn(t, newLocalEnv(t))
}

// newAgentEnvFileDB 和 newAgentEnv 一样，但用临时文件库：DSN 和生产一样带 _txlock=immediate，多连接，测得出并发。
func newAgentEnvFileDB(t *testing.T) *agentEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "agent.db")+"?_journal_mode=WAL&_busy_timeout=10000&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(25)
	c.SetMaxIdleConns(25)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	// 先把连接开好放进池里：新连接打开时要等正在进行的写事务，不预热的话并发请求会被建连接排成一队，测不到真正的并发
	ctx := context.Background()
	conns := make([]*sql.Conn, 0, 16)
	for i := 0; i < 16; i++ {
		cn, err := c.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, cn)
	}
	for _, cn := range conns {
		cn.Close()
	}
	return agentEnvOn(t, setupLocalEnv(t))
}

func agentEnvOn(t *testing.T, le *localEnv) *agentEnv {
	t.Helper()
	e := &agentEnv{localEnv: le, clock: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	old := agentNow
	agentNow = func() time.Time { return e.clock }
	resetAgentAuthState()
	resetAgentOrderRate()
	resetSharedState()
	t.Cleanup(func() {
		agentNow = old
		resetAgentAuthState()
		resetAgentOrderRate()
		resetSharedState()
	})
	r := gin.New()
	RegisterSharedRoutes(&r.RouterGroup)
	RegisterAgentRoutes(r.Group("/api/v1"))
	adm := r.Group("/api/v1/admin")
	adm.Use(func(c *gin.Context) {
		c.Set("username", "admin")
		c.Set("is_admin", true)
		c.Next()
	})
	RegisterAgentAdminRoutes(adm)
	e.r = r
	return e
}

func (e *agentEnv) seedTier(name string) int64 {
	e.t.Helper()
	id, err := db.CreateAgentTier(context.Background(), name, "", e.clock)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// seedAgent 直接写库：密码已改过、两步验证已绑定。
func (e *agentEnv) seedAgent(username string, tierID int64) (int64, string, []byte) {
	e.t.Helper()
	pw, err := agent.GeneratePassword()
	if err != nil {
		e.t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		e.t.Fatal(err)
	}
	ctx := context.Background()
	id, err := db.CreateAgent(ctx, db.NewAgent{Username: username, DisplayName: username, PasswordHash: string(hash), TierID: tierID}, e.clock)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := db.SetAgentPassword(ctx, id, string(hash), false, e.clock); err != nil {
		e.t.Fatal(err)
	}
	secret, err := agent.NewTOTPSecret()
	if err != nil {
		e.t.Fatal(err)
	}
	key, err := secretbox.LoadKey()
	if err != nil {
		e.t.Fatal(err)
	}
	sealed, err := secretbox.Seal(key, agentTOTPAAD(id), agent.EncodeSecret(secret))
	if err != nil {
		e.t.Fatal(err)
	}
	if err := db.BindAgentTOTP(ctx, id, sealed, 0, e.clock); err != nil {
		e.t.Fatal(err)
	}
	return id, pw, secret
}

type agentSession struct {
	cookies []*http.Cookie
	csrf    string
}

// send 发一个请求，cookies 原样带上。
func (e *agentEnv) send(method, path, body string, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w
}

// liveCookies 取响应里仍有效的 cookie（过滤掉被清除的）。
func liveCookies(w *httptest.ResponseRecorder) []*http.Cookie {
	var out []*http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.MaxAge >= 0 && c.Value != "" {
			out = append(out, c)
		}
	}
	return out
}

func findCookie(cs []*http.Cookie, name string) *http.Cookie {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// login 每次先把时钟推后 30 秒（换一个新的动态码时间窗），再走「密码 → 动态码」两步。
func (e *agentEnv) login(username, password string, secret []byte) *agentSession {
	e.t.Helper()
	e.clock = e.clock.Add(30 * time.Second)
	w := e.send("POST", "/api/v1/agent/auth/login", `{"username":"`+username+`","password":"`+password+`"}`, nil, nil)
	pre := findCookie(liveCookies(w), agentPreCookie)
	if w.Code != 200 || pre == nil {
		e.t.Fatalf("登录第一步失败: %d %s", w.Code, w.Body.String())
	}
	code := agent.TOTPCode(secret, agent.TOTPStep(e.clock))
	w = e.send("POST", "/api/v1/agent/auth/totp", `{"code":"`+code+`"}`, []*http.Cookie{pre}, nil)
	cs := liveCookies(w)
	tok, csrf := findCookie(cs, agentTokenCookie), findCookie(cs, agentCSRFCookie)
	if w.Code != 200 || tok == nil || csrf == nil {
		e.t.Fatalf("登录第二步失败: %d %s", w.Code, w.Body.String())
	}
	return &agentSession{cookies: []*http.Cookie{tok, csrf}, csrf: csrf.Value}
}

// agentDo 以代理商会话发请求，写请求自动带 X-CSRF-Token。
func (e *agentEnv) agentDo(s *agentSession, method, path, body string) *httptest.ResponseRecorder {
	h := map[string]string{}
	if method != http.MethodGet && method != http.MethodHead {
		h["X-CSRF-Token"] = s.csrf
	}
	return e.send(method, path, body, s.cookies, h)
}

// adminDo 以（假的）管理员身份请求 /api/v1/admin 下的接口。
func (e *agentEnv) adminDo(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	return e.send(method, path, body, nil, headers)
}
