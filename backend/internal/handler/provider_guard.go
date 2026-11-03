package handler

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 跨服务商互斥（PR-7）：同一个 ChatGPT 账号不能同时走两家，否则两边「已有订阅」的检查之间有时间窗，可能两边都付款。
// 两个方向：
//   - MarsWallex 兑换前查 15 分钟内有没有同邮箱的 ZovoCard 绑定（ZovoCard 那边的单在卡台，本站只知道绑定时间），
//     以及同邮箱有没有待人工的付款单（可能已经付了钱，引擎的在途索引不管它）。
//   - ZovoCard 预检、兑换前查同邮箱有没有未结束或待人工的真实付款单。
//
// 查库和后面的动作之间有时间窗，所以再加一把进程内的账号锁：ZovoCard 转发期间持共享锁（ZovoCard 之间互不影响，
// 现有行为不变），MarsWallex 建单期间持独占锁。绑定在转发后才写，共享锁正好盖住这段。
const crossProviderWindow = 15 * time.Minute

const crossProviderBusyText = "该账号有正在处理的开通，请稍后再试"

type accountHold struct {
	shared    int
	exclusive bool
}

var (
	accountHoldMu sync.Mutex
	accountHolds  = map[string]*accountHold{}
)

func normEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func releaseAccountHold(email string, change func(*accountHold)) {
	accountHoldMu.Lock()
	defer accountHoldMu.Unlock()
	h := accountHolds[email]
	if h == nil {
		return
	}
	change(h)
	if h.shared <= 0 && !h.exclusive {
		delete(accountHolds, email)
	}
}

// holdAccountShared 取共享锁，有独占锁时失败。
func holdAccountShared(email string) (func(), bool) {
	accountHoldMu.Lock()
	defer accountHoldMu.Unlock()
	h := accountHolds[email]
	if h == nil {
		h = &accountHold{}
		accountHolds[email] = h
	}
	if h.exclusive {
		return nil, false
	}
	h.shared++
	return func() { releaseAccountHold(email, func(h *accountHold) { h.shared-- }) }, true
}

// holdAccountExclusive 取独占锁，有任何锁时失败。
func holdAccountExclusive(email string) (func(), bool) {
	accountHoldMu.Lock()
	defer accountHoldMu.Unlock()
	h := accountHolds[email]
	if h == nil {
		h = &accountHold{}
		accountHolds[email] = h
	}
	if h.exclusive || h.shared > 0 {
		return nil, false
	}
	h.exclusive = true
	return func() { releaseAccountHold(email, func(h *accountHold) { h.exclusive = false }) }, true
}

// zovoCardAccountEmail 从 ZovoCard 的预检、兑换请求里取账号邮箱：优先请求自带的凭据，其次按令牌查绑定。
// X、Grok 的凭据不是 ChatGPT 账号，不参与互斥。取不到邮箱就不拦，现有流程不受影响。
func zovoCardAccountEmail(body map[string]any) string {
	if cred, ok := body["credential"].(map[string]any); ok {
		if sess := str(cred["session"]); sess != "" {
			if cardplatform.IsXPremiumCredential(sess) || cardplatform.IsGrokCredential(sess) {
				return ""
			}
			if em := db.ExtractSessionEmail(sess); em != "" {
				return normEmail(em)
			}
		}
		if em := str(cred["email"]); em != "" {
			return normEmail(em)
		}
	}
	tok := str(body["redemption_token"])
	if tok == "" {
		return ""
	}
	em, err := db.GetAccountEmailByRedemptionToken(tok)
	if err != nil {
		// 解不开只会发生在主密钥不对时，这时 MarsWallex 也建不了单。这里不拦，现有流程照走，只记日志
		log.Printf("[cdk] 按令牌取绑定邮箱失败: %v", err)
		return ""
	}
	return normEmail(em)
}

// holdZovoCardAccount 是 ZovoCard 预检、兑换前的互斥检查。被拦时已写好响应，第二个返回值为 false。
func holdZovoCardAccount(c *gin.Context, body map[string]any) (func(), bool) {
	email := zovoCardAccountEmail(body)
	if email == "" {
		return func() {}, true
	}
	release, ok := holdAccountShared(email)
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"error": crossProviderBusyText})
		return nil, false
	}
	open, err := db.HasOpenPayOrderForEmail(c.Request.Context(), email)
	if err != nil {
		release()
		log.Printf("[cdk] 查询账号在途付款单失败: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "暂时无法确认账号状态，请稍后再试"})
		return nil, false
	}
	if open {
		release()
		c.JSON(http.StatusConflict, gin.H{"error": crossProviderBusyText})
		return nil, false
	}
	return release, true
}

// crossProviderBlocked 是 MarsWallex 这边的互斥检查，返回该账号现在能不能走 MarsWallex。查询出错由调用方按拒绝处理。
func crossProviderBlocked(ctx context.Context, email string) (bool, error) {
	if bound, err := db.HasRecentCDKBindingForEmail(ctx, email, time.Now().Add(-crossProviderWindow)); err != nil || bound {
		return bound, err
	}
	return db.HasOpenPayOrderForEmail(ctx, email)
}
