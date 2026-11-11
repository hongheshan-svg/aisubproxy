package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"golang.org/x/crypto/bcrypt"
)

const (
	agentSwapPasswordKey       = "agent_swap_password_hash"
	agentSwapDailyLimitKey     = "agent_swap_daily_limit"
	defaultAgentSwapDailyLimit = 20
)

// swapRateAllow 每 IP（IPv6 按 /64）每小时最多 20 次换码尝试。
// 复用公开接口的限流表（localRateAllow）：有键数上限，过期键由 StartLocalRateSweep 每分钟清扫，不会随来源 IP 无限增长。
func swapRateAllow(ip string) bool {
	return localRateAllow(localRateSwap, ip)
}

const (
	agentSwapWeakKey        = "agent_swap_password_weak"
	minAgentSwapPasswordLen = 12
)

// 换码口令是所有代理共用的一个口令，分布式猜口令换 IP 就能绕开每 IP 限流，所以输错按口令全局计数：
// 1 小时内错 30 次，整个换码接口暂停 1 小时（锁定期间不再比对口令），并告警一次。
// 正确口令不清零计数，否则猜口令的人能借代理的正常使用无限续命；管理员重设口令立即解锁。
const (
	swapPasswordMaxFailures = 30
	swapPasswordLockWindow  = time.Hour
	swapPasswordFailKey     = "agent_swap_password"
)

var swapPasswordFails = newFailLimiter(swapPasswordMaxFailures, swapPasswordLockWindow, 1)

// noteSwapPasswordFailure 记一次口令错误；只在这一次刚好触发锁定时告警（并发输错时后续的失败不重复发）。
func noteSwapPasswordFailure() {
	if swapPasswordFails.failed(swapPasswordFailKey) {
		opsAlert(fmt.Sprintf("代理换码口令 1 小时内输错 %d 次，换码接口已暂停 1 小时。如果不是代理自己输错，可能有人在猜口令：请在后台「CDK 卡密 → 代理换码」重设换码口令（重设后立即恢复）。", swapPasswordMaxFailures))
	}
}

// swapLockedText 是锁定期间给请求方的提示，wait 向上取整到分钟。
func swapLockedText(wait time.Duration) string {
	return fmt.Sprintf("换码口令错误次数过多，已暂停换码，请 %d 分钟后再试", swapLockedMinutes(wait))
}

// swapLockedMinutes 把剩余锁定时间向上取整到分钟，至少 1；error_code=locked 的响应用它给 retry_after_minutes。
func swapLockedMinutes(wait time.Duration) int {
	mins := int((wait + time.Minute - 1) / time.Minute)
	if mins < 1 {
		mins = 1
	}
	return mins
}

// 口令错误按北京日计数：只记次数和来源，不记口令内容。
var (
	swapWrongMu  sync.Mutex
	swapWrongDay string
	swapWrongN   int
)

func agentSwapWrongPassword(ip string) {
	day := time.Now().In(beijingZone).Format("2006-01-02")
	swapWrongMu.Lock()
	if swapWrongDay != day {
		swapWrongDay, swapWrongN = day, 0
	}
	swapWrongN++
	n := swapWrongN
	swapWrongMu.Unlock()
	log.Printf("[agent-exchange] 换码口令错误 ip=%s 今日第 %d 次", ip, n)
}

func agentSwapWrongToday() int {
	day := time.Now().In(beijingZone).Format("2006-01-02")
	swapWrongMu.Lock()
	defer swapWrongMu.Unlock()
	if swapWrongDay != day {
		return 0
	}
	return swapWrongN
}

// noteAgentSwapPasswordUsed 口令验证通过时调用：库里存的是哈希看不出长度，只能这时用明文判断。
// 短于新规则的老口令继续可用，但打上标记，后台设置页提示尽快更换。
func noteAgentSwapPasswordUsed(plain string) {
	if len(strings.TrimSpace(plain)) >= minAgentSwapPasswordLen || agentSwapPasswordWeak() {
		return
	}
	_ = db.SetSetting(agentSwapWeakKey, "1")
}

func agentSwapPasswordWeak() bool {
	v, _ := db.GetSetting(agentSwapWeakKey)
	return strings.TrimSpace(v) == "1"
}

// validateAgentSwapPassword 只校验不写库：长度和弱口令。空串表示清除口令，视为合格。
func validateAgentSwapPassword(plain string) error {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return nil
	}
	if len(plain) < minAgentSwapPasswordLen {
		return errPasswordTooShort
	}
	if db.IsWeakPassword(plain) {
		return errPasswordWeak
	}
	return nil
}

// setAgentSwapPassword 校验不过返回 errPasswordTooShort / errPasswordWeak，写库失败返回原始错误。
func setAgentSwapPassword(plain string) error {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return db.DeleteSetting(agentSwapPasswordKey)
	}
	if err := validateAgentSwapPassword(plain); err != nil {
		return err
	}
	hash, err := db.HashAdminPassword(plain)
	if err != nil {
		return err
	}
	if err := db.SetSetting(agentSwapPasswordKey, hash); err != nil {
		return err
	}
	swapPasswordFails.succeeded(swapPasswordFailKey) // 换了口令，旧口令上的猜测计数作废，立即解锁
	if err := db.DeleteSetting(agentSwapWeakKey); err != nil {
		// 口令已经换成功；只是「弱口令」标记没清掉，不能因此报失败
		log.Printf("[settings] 清除换码口令弱标记失败: %v", err)
	}
	return nil
}

var (
	errPasswordTooShort = &simpleError{"代理换码密码至少 12 位"}
	errPasswordWeak     = &simpleError{"代理换码密码太简单：不能用常见口令、同一字符重复或连续数字，请换一个"}
)

type simpleError struct{ s string }

func (e *simpleError) Error() string { return e.s }

func agentSwapPasswordConfigured() bool {
	v, _ := db.GetSetting(agentSwapPasswordKey)
	return strings.TrimSpace(v) != ""
}

func verifyAgentSwapPassword(plain string) bool {
	stored, _ := db.GetSetting(agentSwapPasswordKey)
	stored = strings.TrimSpace(stored)
	if stored == "" || strings.TrimSpace(plain) == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(stored), []byte(plain)) == nil
}

// agentSwapDailyLimit 同一换码密码每个北京日最多换多少张。库里的值非法或不大于 0 时按默认值，绝不变成不限。
func agentSwapDailyLimit() int {
	v, _ := db.GetSetting(agentSwapDailyLimitKey)
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return defaultAgentSwapDailyLimit
	}
	return n
}

// agentSwapPasswordFP 当前换码密码的指纹，用来按密码统计额度。对存储的 bcrypt 哈希再取摘要，不含密码本身。
func agentSwapPasswordFP() string {
	stored, _ := db.GetSetting(agentSwapPasswordKey)
	sum := sha256.Sum256([]byte(strings.TrimSpace(stored)))
	return hex.EncodeToString(sum[:8])
}

// agentExchangeConflict 这张旧码已有占位时的响应。
func agentExchangeConflict(c *gin.Context, state string) {
	msg := "该卡密已换过新码，不可重复兑换"
	switch state {
	case db.AgentExchangePending:
		msg = "该卡密正在换码，请勿重复提交"
	case db.AgentExchangeUnknown:
		msg = "该卡密上次换码结果不明，已转人工核对，请勿重试"
	}
	c.JSON(http.StatusConflict, gin.H{"error": msg, "error_code": "conflict", "state": state})
}

// PublicAgentCDKExchange POST /api/v1/public/cdk/exchange
// body: { password, code }  校验代理密码后，未扣款失败的 CDK 换一张同套餐新码。
func PublicAgentCDKExchange(c *gin.Context) {
	ip := c.ClientIP()
	if !swapRateAllow(ip) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "请求过于频繁，请稍后再试", "error_code": "rate_limited"})
		return
	}
	if !agentSwapPasswordConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "管理员尚未启用代理换码功能", "error_code": "disabled"})
		return
	}
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request", "error_code": "invalid_request"})
		return
	}
	if ok, wait := swapPasswordFails.allowed(swapPasswordFailKey); !ok {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": swapLockedText(wait), "error_code": "locked", "retry_after_minutes": swapLockedMinutes(wait)})
		return
	}
	if !verifyAgentSwapPassword(req.Password) {
		agentSwapWrongPassword(ip)
		noteSwapPasswordFailure()
		c.JSON(http.StatusUnauthorized, gin.H{"error": "密码错误", "error_code": "wrong_password"})
		return
	}
	noteAgentSwapPasswordUsed(req.Password)
	code := strings.TrimSpace(req.Code)
	if code == "" || len(code) < 10 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入完整卡密", "error_code": "code_required"})
		return
	}
	p, ok := routeCode(c, code)
	if !ok {
		return
	}
	if cs, ok := provider.AsCardSupply(p); ok {
		withCardSupply(c, cs)
		localCDKExchange(c, cs, code, ip)
		return
	}
	// 代理换码只有本站码（供卡型）一条路；认领了这张码的上游型服务商一律不支持
	c.JSON(http.StatusBadRequest, gin.H{"error": "该服务商不支持代理换码", "error_code": "provider_unsupported"})
}

// AdminListAgentExchanges GET /api/v1/admin/agent-exchanges?state=unknown
// 列出换码记录，state 可选 pending、unknown、done，不传列出全部。
func AdminListAgentExchanges(c *gin.Context) {
	list, err := db.ListAgentExchanges(c.Query("state"), 200)
	if err != nil {
		log.Printf("[agent-exchange] 列表失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"list": list})
}

// AdminResolveAgentExchange POST /api/v1/admin/agent-exchanges/:id/resolve
// 人工定论结果不明的换码。released = 确认没发新码，释放占位让代理重试；
// issued = 确认已发新码，记下新码 ID（本站码填 local_cdks.id），记录转 disabling，由维护循环补成 done。
// 只能处理 unknown，每次写审计。
func AdminResolveAgentExchange(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的记录 ID"})
		return
	}
	var req struct {
		Result   string `json:"result"`
		NewCDKID int64  `json:"new_cdk_id"`
		Note     string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	note := strings.TrimSpace(req.Note)
	if note == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写核对依据（note）"})
		return
	}
	switch req.Result {
	case "released":
		err = db.ResolveAgentExchangeReleased(id)
	case "issued":
		if req.NewCDKID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请填写新码 ID（new_cdk_id）"})
			return
		}
		err = db.ResolveAgentExchangeIssued(id, req.NewCDKID, note)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "result 只能是 released 或 issued"})
		return
	}
	switch {
	case errors.Is(err, db.ErrAgentExchangeNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "换码记录不存在"})
		return
	case errors.Is(err, db.ErrAgentExchangeState):
		c.JSON(http.StatusConflict, gin.H{"error": "只能定论结果不明的记录"})
		return
	case err != nil:
		log.Printf("[agent-exchange] 定论失败 id=%d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}

	out := gin.H{"ok": true, "result": req.Result}
	detail := fmt.Sprintf("id=%d result=%s new_cdk=%d note=%s", id, req.Result, req.NewCDKID, note)
	auditAdmin(c, "agent_exchange_resolve", detail)
	c.JSON(http.StatusOK, out)
}
