package handler

import (
	"crypto/subtle"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

const (
	agentTokenCookie = "agent_token"
	agentPreCookie   = "agent_pre"
	agentCSRFCookie  = "agent_csrf"

	agentTokenPath = "/api/v1/agent"
	agentPrePath   = "/api/v1/agent/auth"

	agentTOTPMaxFails = 5
	agentStateTTL     = 5 * time.Minute
)

// agentNow 是代理商登录用的时钟，测试可以替换。
var agentNow = time.Now

// 进程内状态，以临时凭证的 nonce 为键，5 分钟后清掉。
type agentPendingSecret struct {
	secret []byte
	exp    time.Time
}

type agentFailCount struct {
	n   int
	exp time.Time
}

var (
	agentStateMu   sync.Mutex
	agentPending   = map[string]agentPendingSecret{} // 待绑定的两步验证密钥，不进日志
	agentTOTPFails = map[string]agentFailCount{}
)

func resetAgentAuthState() {
	agentStateMu.Lock()
	agentPending = map[string]agentPendingSecret{}
	agentTOTPFails = map[string]agentFailCount{}
	agentStateMu.Unlock()
	loginMu.Lock()
	loginAttempts = make(map[string]*loginAttempt)
	loginMu.Unlock()
}

// sweepAgentStateLocked 清掉过期项，调用方持有 agentStateMu。
func sweepAgentStateLocked(now time.Time) {
	for k, v := range agentPending {
		if !now.Before(v.exp) {
			delete(agentPending, k)
		}
	}
	for k, v := range agentTOTPFails {
		if !now.Before(v.exp) {
			delete(agentTOTPFails, k)
		}
	}
}

// agentFailCountOf 返回这个临时凭证已错的次数。
func agentFailCountOf(nonce string) int {
	agentStateMu.Lock()
	defer agentStateMu.Unlock()
	now := agentNow()
	sweepAgentStateLocked(now)
	return agentTOTPFails[nonce].n
}

func agentAddFail(nonce string) int {
	agentStateMu.Lock()
	defer agentStateMu.Unlock()
	now := agentNow()
	sweepAgentStateLocked(now)
	f := agentTOTPFails[nonce]
	if f.n == 0 {
		f.exp = now.Add(agentStateTTL)
	}
	f.n++
	agentTOTPFails[nonce] = f
	return f.n
}

func agentForgetNonce(nonce string) {
	agentStateMu.Lock()
	delete(agentPending, nonce)
	delete(agentTOTPFails, nonce)
	agentStateMu.Unlock()
}

// agentPendingSecretFor 取（没有就用 gen 生成并保存）这个临时凭证的待绑定密钥。
func agentPendingSecretFor(nonce string, gen func() ([]byte, error)) ([]byte, error) {
	agentStateMu.Lock()
	defer agentStateMu.Unlock()
	now := agentNow()
	sweepAgentStateLocked(now)
	if p, ok := agentPending[nonce]; ok {
		return p.secret, nil
	}
	s, err := gen()
	if err != nil {
		return nil, err
	}
	agentPending[nonce] = agentPendingSecret{secret: s, exp: now.Add(agentStateTTL)}
	return s, nil
}

func agentPendingSecretGet(nonce string) ([]byte, bool) {
	agentStateMu.Lock()
	defer agentStateMu.Unlock()
	sweepAgentStateLocked(agentNow())
	p, ok := agentPending[nonce]
	return p.secret, ok
}

// agentError 统一的错误响应：error 是编号，message 是中文提示。
func agentError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": code, "message": message})
}

func setAgentCookie(c *gin.Context, name, value, path string, maxAge int, httpOnly bool) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(name, value, maxAge, path, "", isSecureRequest(c), httpOnly)
}

func setAgentSessionCookies(c *gin.Context, token, csrf string) {
	secs := int(agent.SessionTTL / time.Second)
	setAgentCookie(c, agentTokenCookie, token, agentTokenPath, secs, true)
	setAgentCookie(c, agentCSRFCookie, csrf, "/", secs, false)
}

func setAgentPreCookie(c *gin.Context, token string) {
	setAgentCookie(c, agentPreCookie, token, agentPrePath, int(agent.PreTTL/time.Second), true)
}

func clearAgentPreCookie(c *gin.Context) {
	setAgentCookie(c, agentPreCookie, "", agentPrePath, -1, true)
}

func clearAgentCookies(c *gin.Context) {
	setAgentCookie(c, agentTokenCookie, "", agentTokenPath, -1, true)
	setAgentCookie(c, agentCSRFCookie, "", "/", -1, false)
	clearAgentPreCookie(c)
}

// AgentAuthMiddleware 代理商会话校验：只认 cookie，写请求要 CSRF，每次查库确认账号启用且会话版本一致。
func AgentAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		tok, err := c.Cookie(agentTokenCookie)
		if err != nil || tok == "" {
			agentError(c, http.StatusUnauthorized, "unauthorized", "未登录或登录已过期")
			return
		}
		claims, err := agent.ParseSession(tok, agentNow())
		if err != nil {
			agentError(c, http.StatusUnauthorized, "unauthorized", "未登录或登录已过期")
			return
		}
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			h := c.GetHeader("X-CSRF-Token")
			ck, _ := c.Cookie(agentCSRFCookie)
			if h == "" || ck == "" || subtle.ConstantTimeCompare([]byte(h), []byte(ck)) != 1 {
				agentError(c, http.StatusForbidden, "csrf_invalid", "请求校验失败，请刷新页面后重试")
				return
			}
		}
		a, err := db.GetAgent(c.Request.Context(), claims.AgentID)
		if err != nil || a.Status != db.AgentActive || a.SessionVersion != claims.SV {
			agentError(c, http.StatusUnauthorized, "unauthorized", "未登录或登录已过期")
			return
		}
		c.Set("agent_id", a.ID)
		c.Set("agent_username", a.Username)
		c.Next()
	}
}

func agentID(c *gin.Context) int64 { return c.GetInt64("agent_id") }

func agentUsername(c *gin.Context) string { return c.GetString("agent_username") }

// auditAgent 写代理商操作的审计，username 为 agent:<登录名>。detail 里不能有密码、密钥、动态码。
func auditAgent(c *gin.Context, action, detail string) {
	db.WriteAudit("agent:"+agentUsername(c), action, detail, c.ClientIP())
}
