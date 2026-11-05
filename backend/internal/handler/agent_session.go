package handler

import (
	"crypto/subtle"
	"errors"
	"log"
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

	agentTOTPMaxFails = 5 // 每张临时凭证最多错 5 次
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

// agentTakeAttempt 先占一次动态码尝试机会：检查和加 1 在同一把锁里，超过上限返回 false（凭证作废）。
// 返回占用后的次数。校验成功要用 agentForgetNonce 清掉，内部错误要用 agentRefundAttempt 退回。
func agentTakeAttempt(nonce string) (int, bool) {
	agentStateMu.Lock()
	defer agentStateMu.Unlock()
	now := agentNow()
	sweepAgentStateLocked(now)
	f := agentTOTPFails[nonce]
	if f.n >= agentTOTPMaxFails {
		return f.n, false
	}
	if f.n == 0 {
		f.exp = now.Add(agentStateTTL)
	}
	f.n++
	agentTOTPFails[nonce] = f
	return f.n, true
}

// agentRefundAttempt 退回一次机会：数据库或解密出错不是用户输错，不计入失败次数。
func agentRefundAttempt(nonce string) {
	agentStateMu.Lock()
	defer agentStateMu.Unlock()
	if f, ok := agentTOTPFails[nonce]; ok {
		f.n--
		if f.n <= 0 {
			delete(agentTOTPFails, nonce)
		} else {
			agentTOTPFails[nonce] = f
		}
	}
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

// 账号级动态码限流：先占名额再校验。「已失败 + 在途」达到上限就拒绝，所以并发也不会超过 5 次。
const (
	agentTOTPAcctMax  = 5
	agentTOTPAcctLock = 15 * time.Minute
)

type agentTOTPGate struct {
	fails, inflight int
	windowStart     time.Time
	lockedUntil     time.Time
}

var (
	agentGateMu sync.Mutex
	agentGates  = map[int64]*agentTOTPGate{}
)

// agentTOTPReserve 占一个名额，返回占的那个 gate；不放行返回 nil。名额之后都对这个指针结算，
// 不再按 id 回查 map（别的名额结算时可能已经把 map 里的项删掉或换成新的）。
func agentTOTPReserve(id int64) *agentTOTPGate {
	agentGateMu.Lock()
	defer agentGateMu.Unlock()
	now := agentNow()
	for k, g := range agentGates { // 顺手清掉空闲过期的项
		if g.inflight == 0 && !now.Before(g.lockedUntil) && now.Sub(g.windowStart) > agentTOTPAcctLock {
			delete(agentGates, k)
		}
	}
	g := agentGates[id]
	if g == nil {
		g = &agentTOTPGate{windowStart: now}
		agentGates[id] = g
	}
	if now.Before(g.lockedUntil) {
		return nil
	}
	if !g.lockedUntil.IsZero() || now.Sub(g.windowStart) > agentTOTPAcctLock { // 锁或窗口已过：重新计
		g.fails, g.lockedUntil, g.windowStart = 0, time.Time{}, now
	}
	if g.fails+g.inflight >= agentTOTPAcctMax {
		return nil
	}
	g.inflight++
	return g
}

// agentTOTPSlot 是一次已占用的账号级名额。用 defer slot.release() 兜底，没结算就退回。
type agentTOTPSlot struct {
	id   int64
	gate *agentTOTPGate // 占名额时的那个 gate，结算都落在它身上
	done bool
}

// fail 码错：占用转成一次失败。
func (s *agentTOTPSlot) fail() {
	if s.done {
		return
	}
	s.done = true
	agentGateMu.Lock()
	defer agentGateMu.Unlock()
	g := s.gate
	if g.inflight > 0 {
		g.inflight--
	}
	g.fails++
	if g.fails >= agentTOTPAcctMax {
		g.lockedUntil = agentNow().Add(agentTOTPAcctLock)
	}
}

// ok 码对：账号键清零。
func (s *agentTOTPSlot) ok() {
	if s.done {
		return
	}
	s.done = true
	agentGateMu.Lock()
	defer agentGateMu.Unlock()
	if s.gate.inflight > 0 {
		s.gate.inflight--
	}
	if agentGates[s.id] == s.gate { // 只清自己占的那个；已被换成新的就别动新的
		delete(agentGates, s.id)
	}
}

// release 内部错误或没走到校验：退回名额，不计失败。
func (s *agentTOTPSlot) release() {
	if s.done {
		return
	}
	s.done = true
	agentGateMu.Lock()
	defer agentGateMu.Unlock()
	if s.gate.inflight > 0 {
		s.gate.inflight--
	}
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
		if err != nil && !errors.Is(err, db.ErrAgentNotFound) {
			log.Printf("[agent] 会话校验读库失败: %v", err)
			agentError(c, http.StatusServiceUnavailable, "busy", "服务繁忙，请稍后再试")
			return
		}
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
