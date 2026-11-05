package handler

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

const (
	agentMsgBadCreds  = "登录名或密码错误"
	agentMsgPreGone   = "登录已过期，请重新输入密码"
	agentMsgBadTOTP   = "动态码错误，请重新输入"
	agentMsgRate      = "尝试次数过多，请稍后再试"
	agentMsgMaint     = "系统维护中，请稍后再试"
	agentPasswordMax  = 72 // bcrypt 只取前 72 字节
	agentPasswordMin  = 12
	agentTOTPIssuer   = "AISubProxy"
	agentBodyFailText = "请求格式不正确"
)

// agentTOTPAAD 把两步验证密文绑到代理商 ID 上。
func agentTOTPAAD(id int64) string { return "agent_totp:" + strconv.FormatInt(id, 10) }

var (
	agentDummyOnce sync.Once
	agentDummyHash []byte
)

// 用户不存在时也做一次 bcrypt 比对，抹平响应时间。
func agentBurnPassword(pw string) {
	agentDummyOnce.Do(func() {
		agentDummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing"), bcrypt.DefaultCost)
	})
	_ = bcrypt.CompareHashAndPassword(agentDummyHash, []byte(pw))
}

func agentRateKey(c *gin.Context, username string) string {
	return "agent:" + c.ClientIP() + "|" + username
}

func clipName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func agentAuditRaw(c *gin.Context, username, action, detail string) {
	db.WriteAudit("agent:"+username, action, detail, c.ClientIP())
}

// AgentLogin 第一步：校验密码，发临时凭证并告知下一步。
func AgentLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		agentError(c, http.StatusBadRequest, "invalid_request", agentBodyFailText)
		return
	}
	name, valid := agent.NormalizeUsername(req.Username)
	if !valid {
		name = clipName(req.Username)
	}
	key := agentRateKey(c, name)
	if ok, _ := agentLoginAllowed(key); !ok {
		agentError(c, http.StatusTooManyRequests, "rate_limited", agentMsgRate)
		return
	}
	fail := func(detail string, exists bool) {
		agentLoginFailed(key)
		if exists { // 只给存在的账号写审计，免得误填的密码进审计、也防刷审计表
			agentAuditRaw(c, name, "agent_login_failed", detail)
		}
		agentError(c, http.StatusUnauthorized, "invalid_credentials", agentMsgBadCreds)
	}
	var a db.Agent
	var sec db.AgentSecrets
	var err error
	if valid {
		a, sec, err = db.GetAgentByUsername(c.Request.Context(), name)
	}
	// 读库失败不是「账号不存在」：返回 503 且不计密码限流，免得数据库抖动把用户自己锁住（审计 L2）
	if valid && err != nil && !errors.Is(err, db.ErrAgentNotFound) {
		agentDBBusy(c, err)
		return
	}
	if !valid || err != nil {
		agentBurnPassword(req.Password)
		fail("账号不存在", false)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(sec.PasswordHash), []byte(req.Password)) != nil {
		fail("密码错误", true)
		return
	}
	if a.Status != db.AgentActive {
		fail("账号已停用", true)
		return
	}
	stage := agent.StageTOTP
	switch {
	case a.MustChangePassword:
		stage = agent.StageChangePassword
	case !a.TOTPBound:
		stage = agent.StageSetupTOTP
	}
	issueAgentPre(c, a, stage)
}

// issueAgentPre 签发新的临时凭证并响应 {"next": stage}。
func issueAgentPre(c *gin.Context, a db.Agent, stage string) {
	tok, _, err := agent.SignPre(a.ID, a.SessionVersion, stage, agentNow())
	if err != nil {
		log.Printf("[agent] 签发临时凭证失败")
		agentError(c, http.StatusInternalServerError, "internal", "服务异常，请稍后再试")
		return
	}
	setAgentPreCookie(c, tok)
	c.JSON(http.StatusOK, gin.H{"next": stage})
}

// agentPreCheck 校验临时凭证：stage 对、账号启用、会话版本一致、动态码错误没超限。
// agentDBBusy 读库失败：记日志（只写错误），返回 503，不是认证失败。
func agentDBBusy(c *gin.Context, err error) {
	log.Printf("[agent] 读库失败: %v", err)
	agentError(c, http.StatusServiceUnavailable, "busy", "服务繁忙，请稍后再试")
}

func agentPreCheck(c *gin.Context, stage string) (agent.Claims, db.Agent, bool) {
	gone := func() (agent.Claims, db.Agent, bool) {
		agentError(c, http.StatusUnauthorized, "pre_expired", agentMsgPreGone)
		return agent.Claims{}, db.Agent{}, false
	}
	tok, err := c.Cookie(agentPreCookie)
	if err != nil || tok == "" {
		return gone()
	}
	cl, err := agent.ParsePre(tok, agentNow())
	if err != nil || cl.Stage != stage || cl.Nonce == "" {
		return gone()
	}
	if agentFailCountOf(cl.Nonce) >= agentTOTPMaxFails {
		return gone()
	}
	a, err := db.GetAgent(c.Request.Context(), cl.AgentID)
	if err != nil && !errors.Is(err, db.ErrAgentNotFound) {
		agentDBBusy(c, err)
		return agent.Claims{}, db.Agent{}, false
	}
	if err != nil || a.Status != db.AgentActive || a.SessionVersion != cl.SV {
		return gone()
	}
	return cl, a, true
}

type agentCodeReq struct {
	Code string `json:"code"`
}

func checkNewAgentPassword(newPw, oldHash string) (string, bool) {
	switch {
	case utf8.RuneCountInString(newPw) < agentPasswordMin:
		return "新密码至少 12 个字符", false
	case len(newPw) > agentPasswordMax:
		return "新密码太长：最多 72 字节（英文、数字每个占 1 字节，中文每个占 3 字节）", false
	case db.IsWeakPassword(newPw):
		return "新密码太简单，请换一个更复杂的密码", false
	case bcrypt.CompareHashAndPassword([]byte(oldHash), []byte(newPw)) == nil:
		return "新密码不能和旧密码相同", false
	}
	return "", true
}

// AgentPasswordInitial 首次或被重置后改密码，之后换发下一阶段的临时凭证。
func AgentPasswordInitial(c *gin.Context) {
	cl, a, ok := agentPreCheck(c, agent.StageChangePassword)
	if !ok {
		return
	}
	var req struct {
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		agentError(c, http.StatusBadRequest, "invalid_request", agentBodyFailText)
		return
	}
	sec, err := db.GetAgentSecrets(c.Request.Context(), a.ID)
	if errors.Is(err, db.ErrAgentNotFound) {
		agentError(c, http.StatusUnauthorized, "pre_expired", agentMsgPreGone)
		return
	}
	if err != nil {
		agentDBBusy(c, err)
		return
	}
	if msg, ok := checkNewAgentPassword(req.NewPassword, sec.PasswordHash); !ok {
		agentError(c, http.StatusBadRequest, "weak_password", msg)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		agentError(c, http.StatusInternalServerError, "internal", "服务异常，请稍后再试")
		return
	}
	sv, err := db.SetAgentPassword(c.Request.Context(), a.ID, string(hash), false, agentNow())
	if err != nil {
		log.Printf("[agent] 改密码写库失败: %v", err)
		agentError(c, http.StatusInternalServerError, "internal", "服务异常，请稍后再试")
		return
	}
	agentForgetNonce(cl.Nonce)
	a.SessionVersion = sv
	a.MustChangePassword = false
	c.Set("agent_username", a.Username)
	auditAgent(c, "agent_password_change", "首次改密码")
	next := agent.StageTOTP
	if !a.TOTPBound {
		next = agent.StageSetupTOTP
	}
	issueAgentPre(c, a, next)
}

func agentMasterKey(c *gin.Context) ([]byte, bool) {
	key, err := secretbox.LoadKey()
	if err != nil {
		log.Printf("[agent] 主密钥不可用: %s", secretErrKind(err))
		agentError(c, http.StatusServiceUnavailable, "maintenance", agentMsgMaint)
		return nil, false
	}
	return key, true
}

// AgentTOTPSetup 取绑定用的密钥和 otpauth 链接，同一个临时凭证重复取得到同一个密钥。
func AgentTOTPSetup(c *gin.Context) {
	cl, a, ok := agentPreCheck(c, agent.StageSetupTOTP)
	if !ok {
		return
	}
	if a.TOTPBound {
		agentError(c, http.StatusConflict, "totp_bound", "已绑定两步验证")
		return
	}
	if _, ok := agentMasterKey(c); !ok {
		return
	}
	secret, err := agentPendingSecretFor(cl.Nonce, agent.NewTOTPSecret)
	if err != nil {
		agentError(c, http.StatusInternalServerError, "internal", "服务异常，请稍后再试")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"secret":      agent.EncodeSecret(secret),
		"otpauth_uri": agent.OtpauthURI(agentTOTPIssuer, a.Username, secret),
	})
}

// agentTOTPAllowed 先查密码限流键，再占账号级动态码名额；任一不通过就写 429。
// 放行时返回名额，调用方必须 defer slot.release()，并在校验后调 fail/ok。
func agentTOTPAllowed(c *gin.Context, a db.Agent) (*agentTOTPSlot, bool) {
	okPw, _ := agentLoginAllowed(agentRateKey(c, a.Username))
	var g *agentTOTPGate
	if okPw {
		g = agentTOTPReserve(a.ID)
	}
	if g == nil {
		agentError(c, http.StatusTooManyRequests, "rate_limited", agentMsgRate)
		return nil, false
	}
	return &agentTOTPSlot{id: a.ID, gate: g}, true
}

// agentCodeFailed 记一次动态码错误（调用时已经占了这张凭证的第 n 次机会和账号级名额）：
// 名额转成一次失败；这张凭证的机会用完就作废，并给密码限流也计一次。
func agentCodeFailed(c *gin.Context, a db.Agent, slot *agentTOTPSlot, n int) {
	slot.fail()
	agentAuditRaw(c, a.Username, "agent_login_failed", "动态码错误")
	if n >= agentTOTPMaxFails {
		agentLoginFailed(agentRateKey(c, a.Username))
	}
	agentError(c, http.StatusUnauthorized, "totp_invalid", agentMsgBadTOTP)
}

func finishAgentLogin(c *gin.Context, cl agent.Claims, a db.Agent) {
	now := agentNow()
	if err := db.TouchAgentLogin(c.Request.Context(), a.ID, now); err != nil {
		log.Printf("[agent] 记录登录时间失败: %v", err)
	}
	tok, _, err := agent.SignSession(a.ID, a.SessionVersion, now)
	csrf := newCSRFToken()
	if err != nil || csrf == "" {
		agentError(c, http.StatusInternalServerError, "internal", "服务异常，请稍后再试")
		return
	}
	agentForgetNonce(cl.Nonce)
	agentLoginSucceeded(agentRateKey(c, a.Username))
	setAgentSessionCookies(c, tok, csrf)
	clearAgentPreCookie(c)
	c.Set("agent_username", a.Username)
	auditAgent(c, "agent_login", "登录成功")
	c.JSON(http.StatusOK, gin.H{"next": "done"})
}

// AgentTOTPConfirm 绑定两步验证：输一次动态码确认，加密存库并发正式会话。
func AgentTOTPConfirm(c *gin.Context) {
	cl, a, ok := agentPreCheck(c, agent.StageSetupTOTP)
	if !ok {
		return
	}
	if a.TOTPBound {
		agentError(c, http.StatusConflict, "totp_bound", "已绑定两步验证")
		return
	}
	var req agentCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		agentError(c, http.StatusBadRequest, "invalid_request", agentBodyFailText)
		return
	}
	slot, ok := agentTOTPAllowed(c, a)
	if !ok {
		return
	}
	defer slot.release()
	key, ok := agentMasterKey(c)
	if !ok {
		return
	}
	secret, have := agentPendingSecretGet(cl.Nonce)
	if !have {
		agentError(c, http.StatusUnauthorized, "pre_expired", agentMsgPreGone)
		return
	}
	sec, err := db.GetAgentSecrets(c.Request.Context(), a.ID)
	if err != nil {
		log.Printf("[agent] 读取账号失败: %v", err)
		agentError(c, http.StatusServiceUnavailable, "busy", "服务繁忙，请稍后再试")
		return
	}
	n, allowed := agentTakeAttempt(cl.Nonce)
	if !allowed {
		agentError(c, http.StatusUnauthorized, "pre_expired", agentMsgPreGone)
		return
	}
	step, good := agent.VerifyTOTP(secret, req.Code, agentNow(), sec.TOTPLastStep)
	if !good {
		agentCodeFailed(c, a, slot, n)
		return
	}
	sealed, err := secretbox.Seal(key, agentTOTPAAD(a.ID), agent.EncodeSecret(secret))
	if err != nil {
		agentRefundAttempt(cl.Nonce)
		log.Printf("[agent] 两步验证密钥加密失败: %s", secretErrKind(err))
		agentError(c, http.StatusServiceUnavailable, "maintenance", agentMsgMaint)
		return
	}
	if err := db.BindAgentTOTP(c.Request.Context(), a.ID, sealed, step, agentNow()); err != nil {
		agentRefundAttempt(cl.Nonce)
		if errors.Is(err, db.ErrAgentTOTPAlreadyBound) {
			agentError(c, http.StatusConflict, "totp_bound", "已绑定两步验证")
			return
		}
		log.Printf("[agent] 绑定两步验证写库失败: %v", err)
		agentError(c, http.StatusServiceUnavailable, "busy", "服务繁忙，请稍后再试")
		return
	}
	slot.ok()
	c.Set("agent_username", a.Username)
	auditAgent(c, "agent_totp_bind", "绑定两步验证")
	finishAgentLogin(c, cl, a)
}

// agentTOTPResult 是已绑定账号动态码校验的结果。
type agentTOTPResult int

const (
	totpAccepted  agentTOTPResult = iota // 动态码正确，时间窗已占用
	totpRejected                         // 动态码错误或重放，调用方负责记失败、写响应
	totpErrorSent                        // 数据库或密钥出错，已经写好 503/500，不算动态码错误
)

// verifyAgentTOTP 校验已绑定账号的动态码，成功时占用时间窗（并发重放的最后一道防线）。
// 只有 VerifyTOTP 不通过、UseAgentTOTPStep 返回 false 才是 totpRejected；其余错误只记错误种类、不带密钥。
func verifyAgentTOTP(c *gin.Context, a db.Agent, code string) agentTOTPResult {
	key, ok := agentMasterKey(c)
	if !ok {
		return totpErrorSent
	}
	sec, err := db.GetAgentSecrets(c.Request.Context(), a.ID)
	if err != nil {
		log.Printf("[agent] 读取两步验证信息失败: %v", err)
		agentError(c, http.StatusServiceUnavailable, "busy", "服务繁忙，请稍后再试")
		return totpErrorSent
	}
	internal := func(kind string) agentTOTPResult {
		log.Printf("[agent] 两步验证密钥不可用: %s", kind)
		agentError(c, http.StatusInternalServerError, "internal", "服务异常，请联系管理员")
		return totpErrorSent
	}
	if sec.TOTPSealed == "" {
		return internal("未绑定")
	}
	plain, err := secretbox.Open(key, agentTOTPAAD(a.ID), sec.TOTPSealed)
	if err != nil {
		return internal(secretErrKind(err))
	}
	secret, err := agent.DecodeSecret(plain)
	if err != nil {
		return internal("解码失败")
	}
	step, ok := agent.VerifyTOTP(secret, code, agentNow(), sec.TOTPLastStep)
	if !ok {
		return totpRejected
	}
	used, err := db.UseAgentTOTPStep(c.Request.Context(), a.ID, step)
	if err != nil {
		log.Printf("[agent] 占用时间窗失败: %v", err)
		agentError(c, http.StatusServiceUnavailable, "busy", "服务繁忙，请稍后再试")
		return totpErrorSent
	}
	if !used {
		return totpRejected
	}
	return totpAccepted
}

// AgentTOTPLogin 日常登录的最后一步：输动态码，发正式会话。
func AgentTOTPLogin(c *gin.Context) {
	cl, a, ok := agentPreCheck(c, agent.StageTOTP)
	if !ok {
		return
	}
	var req agentCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		agentError(c, http.StatusBadRequest, "invalid_request", agentBodyFailText)
		return
	}
	slot, ok := agentTOTPAllowed(c, a)
	if !ok {
		return
	}
	defer slot.release()
	n, allowed := agentTakeAttempt(cl.Nonce) // 先占次数再校验，并发的错码也不会超过上限
	if !allowed {
		agentError(c, http.StatusUnauthorized, "pre_expired", agentMsgPreGone)
		return
	}
	switch verifyAgentTOTP(c, a, req.Code) {
	case totpErrorSent:
		agentRefundAttempt(cl.Nonce)
	case totpRejected:
		agentCodeFailed(c, a, slot, n)
	default:
		slot.ok()
		finishAgentLogin(c, cl, a)
	}
}

// AgentLogout 清掉三个 cookie。
func AgentLogout(c *gin.Context) {
	clearAgentCookies(c)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// AgentMe 当前代理商的资料和余额。
func AgentMe(c *gin.Context) {
	a, err := db.GetAgent(c.Request.Context(), agentID(c))
	if errors.Is(err, db.ErrAgentNotFound) {
		agentError(c, http.StatusUnauthorized, "unauthorized", "未登录或登录已过期")
		return
	}
	if err != nil {
		agentDBBusy(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"username":      a.Username,
		"display_name":  a.DisplayName,
		"balance_cents": a.BalanceCents,
		"tier_name":     a.TierName,
		"last_login_at": a.LastLoginAt,
	})
}

// AgentChangePassword 登录状态下改密码：当前密码 + 新密码 + 动态码。成功后会话失效，前端回登录页。
func AgentChangePassword(c *gin.Context) {
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
		Code            string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		agentError(c, http.StatusBadRequest, "invalid_request", agentBodyFailText)
		return
	}
	a, err := db.GetAgent(c.Request.Context(), agentID(c))
	if errors.Is(err, db.ErrAgentNotFound) {
		agentError(c, http.StatusUnauthorized, "unauthorized", "未登录或登录已过期")
		return
	}
	if err != nil {
		agentDBBusy(c, err)
		return
	}
	sec, err := db.GetAgentSecrets(c.Request.Context(), a.ID)
	if errors.Is(err, db.ErrAgentNotFound) {
		agentError(c, http.StatusUnauthorized, "unauthorized", "未登录或登录已过期")
		return
	}
	if err != nil {
		agentDBBusy(c, err)
		return
	}
	key := agentRateKey(c, a.Username)
	if okPw, _ := agentLoginAllowed(key); !okPw {
		agentError(c, http.StatusTooManyRequests, "rate_limited", agentMsgRate)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(sec.PasswordHash), []byte(req.CurrentPassword)) != nil {
		agentLoginFailed(key)
		auditAgent(c, "agent_password_change_failed", "当前密码错误")
		// 会话接口的 401 前端会当作会话失效，所以这里用 400
		agentError(c, http.StatusBadRequest, "invalid_credentials", "当前密码错误")
		return
	}
	if msg, ok := checkNewAgentPassword(req.NewPassword, sec.PasswordHash); !ok {
		agentError(c, http.StatusBadRequest, "weak_password", msg)
		return
	}
	slot, ok := agentTOTPAllowed(c, a)
	if !ok {
		return
	}
	defer slot.release()
	switch verifyAgentTOTP(c, a, req.Code) {
	case totpErrorSent:
		return
	case totpRejected:
		slot.fail()
		agentLoginFailed(key)
		auditAgent(c, "agent_password_change_failed", "动态码错误")
		agentError(c, http.StatusBadRequest, "totp_invalid", agentMsgBadTOTP)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		agentError(c, http.StatusInternalServerError, "internal", "服务异常，请稍后再试")
		return
	}
	if _, err := db.SetAgentPassword(c.Request.Context(), a.ID, string(hash), false, agentNow()); err != nil {
		log.Printf("[agent] 改密码写库失败: %v", err)
		agentError(c, http.StatusInternalServerError, "internal", "服务异常，请稍后再试")
		return
	}
	slot.ok()
	agentLoginSucceeded(key)
	auditAgent(c, "agent_password_change", "在账号页改密码")
	clearAgentCookies(c)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
