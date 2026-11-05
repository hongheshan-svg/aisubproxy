package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// AG-B-3：代理商登录的失败计数独立于管理员登录（规格 6.2「单独的计数器」）。
func TestAgentLoginFailuresDoNotUseAdminLimiter(t *testing.T) {
	e := newAgentEnv(t)
	e.seedAgent("alice", e.seedTier("普通"))
	for i := 0; i < loginMaxAttempts; i++ {
		w := e.send("POST", agentBase+"/auth/login", `{"username":"alice","password":"Wrong-Pass-0001"}`, nil, nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误密码应 401: %d %s", i+1, w.Code, w.Body)
		}
	}
	if n := loginLimiter.size(); n != 0 {
		t.Fatalf("代理商登录失败不应写进管理员限流表: %d 个键", n)
	}
	if n := agentLoginLimiter.size(); n != 1 {
		t.Fatalf("代理商登录失败应记在代理商限流表: %d 个键", n)
	}
	// 反向：管理员的失败也不占代理商的表
	loginFailed("203.0.113.9|root")
	if n := agentLoginLimiter.size(); n != 1 {
		t.Fatalf("管理员登录失败不应写进代理商限流表: %d 个键", n)
	}
}

// L2：读库失败（不是账号不存在）时返回 503 busy，且不计密码限流，免得数据库抖动把用户自己锁住。
func TestAgentLoginDBReadErrorIs503NotCounted(t *testing.T) {
	e := newAgentEnv(t)
	e.seedAgent("alice", e.seedTier("普通"))
	// 让按登录名查账号的查询失败，但不是「没有这个账号」
	if _, err := db.DB.Exec(`ALTER TABLE agents RENAME COLUMN username TO username_x`); err != nil {
		t.Skip("SQLite 不支持改列名: ", err)
	}
	for i := 0; i < loginMaxAttempts+2; i++ {
		w := e.send("POST", agentBase+"/auth/login", `{"username":"alice","password":"Wrong-Pass-0001"}`, nil, nil)
		if w.Code != http.StatusServiceUnavailable || jsonField(t, w.Body.String(), "error") != "busy" {
			t.Fatalf("第 %d 次：读库失败应 503 busy: %d %s", i+1, w.Code, w.Body)
		}
	}
	if n := loginLimiter.size() + agentLoginLimiter.size(); n != 0 {
		t.Fatalf("读库失败不应计入密码限流: %d 个键", n)
	}
}

// AG-B-4：受信反代转来 X-Forwarded-Proto=https 时，代理商三个 cookie 都带 Secure；
// 没配 TRUSTED_PROXIES、或对端不在名单里，头不算数，Secure 为 false。
func TestAgentCookiesSecureOnlyBehindTrustedProxy(t *testing.T) {
	cases := []struct {
		name    string
		trusted string
		want    bool
	}{
		{"受信反代转来 https", "192.0.2.1", true},
		{"没配 TRUSTED_PROXIES 时头不算数", "", false},
		{"对端不在 TRUSTED_PROXIES 里时头不算数", "10.0.0.1", false},
	}
	headers := map[string]string{"X-Forwarded-Proto": "https"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TRUSTED_PROXIES", tc.trusted)
			e := newAgentEnv(t)
			_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
			e.clock = e.clock.Add(30 * time.Second)
			w := e.send("POST", agentBase+"/auth/login", `{"username":"alice","password":"`+pw+`"}`, nil, headers)
			pre := findCookie(liveCookies(w), agentPreCookie)
			if pre == nil || pre.Secure != tc.want {
				t.Fatalf("agent_pre 的 Secure 应为 %v: %+v", tc.want, pre)
			}
			code := agent.TOTPCode(secret, agent.TOTPStep(e.clock))
			w = e.send("POST", agentBase+"/auth/totp", `{"code":"`+code+`"}`, liveCookiesOne(pre), headers)
			cs := liveCookies(w)
			tok := findCookie(cs, agentTokenCookie)
			csrf := findCookie(cs, agentCSRFCookie)
			if tok == nil || tok.Secure != tc.want {
				t.Fatalf("agent_token 的 Secure 应为 %v: %+v", tc.want, tok)
			}
			if csrf == nil || csrf.Secure != tc.want {
				t.Fatalf("agent_csrf 的 Secure 应为 %v: %+v", tc.want, csrf)
			}
		})
	}
}
