package handler

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/credential"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// 跨服务商互斥：同一个 ChatGPT 账号不能同时走两家，否则两边「已有订阅」的检查之间有时间窗，可能两边都付款。
// 通用规则，任意两家之间都成立：该账号在时间窗内有别家的绑定记录，或者有付款单在途、待人工，就拒绝。
//   - 绑定记录是上游型服务商预检时写的（上游的单在它自己那边，本站只知道绑定时间），只看别家的：自己的绑定不算冲突。
//   - 付款单是供卡型服务商的，不分哪家都算：待人工的单可能已经付了钱，引擎的在途索引不管它；
//     自家的在途单本来也会被引擎拒绝，这里一并拦下不改变结果。
//
// 查库和后面的动作之间有时间窗，所以再加一把进程内的账号锁：上游转发期间持共享锁（同一家之间互不影响，
// ZovoCard 的现有行为不变；不同的上游之间互斥），供卡型建单期间持独占锁。绑定在转发后才写，共享锁正好盖住这段。
const crossProviderWindow = 15 * time.Minute

const crossProviderBusyText = "该账号有正在处理的开通，请稍后再试"

type accountHold struct {
	owner     provider.Name // 持共享锁的上游，同一时刻只有一家
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

// holdAccountShared 上游 owner 取共享锁：同一家可以同时持有；有独占锁或别家的共享锁时失败。
func holdAccountShared(email string, owner provider.Name) (func(), bool) {
	accountHoldMu.Lock()
	defer accountHoldMu.Unlock()
	h := accountHolds[email]
	if h == nil {
		h = &accountHold{}
		accountHolds[email] = h
	}
	if h.exclusive || (h.shared > 0 && h.owner != owner) {
		return nil, false
	}
	h.owner = owner
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

// upstreamAccountEmail 从上游预检、兑换请求里取账号邮箱：优先请求自带的凭据，其次按令牌查绑定。
// X、Grok 的凭据不是 ChatGPT 账号，不参与互斥。取不到邮箱就不拦，现有流程不受影响。
func upstreamAccountEmail(body map[string]any) string {
	if cred, ok := body["credential"].(map[string]any); ok {
		if sess := str(cred["session"]); sess != "" {
			if credential.IsXPremiumCredential(sess) || credential.IsGrokCredential(sess) {
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
		// 解不开只会发生在主密钥不对时，这时供卡型也建不了单。这里不拦，现有流程照走，只记日志
		log.Printf("[cdk] 按令牌取绑定邮箱失败: %v", err)
		return ""
	}
	return normEmail(em)
}

// holdUpstreamAccount 是上游预检、兑换前的互斥检查，所有上游型服务商共用。被拦时已写好响应，第二个返回值为 false。
func holdUpstreamAccount(c *gin.Context, p provider.Name, body map[string]any) (func(), bool) {
	email := upstreamAccountEmail(body)
	if email == "" {
		return func() {}, true
	}
	release, ok := holdAccountShared(email, p)
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"error": crossProviderBusyText})
		return nil, false
	}
	blocked, err := crossProviderBlocked(c.Request.Context(), email, p)
	if err != nil {
		release()
		log.Printf("[cdk] 查询账号跨服务商状态失败: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "暂时无法确认账号状态，请稍后再试"})
		return nil, false
	}
	if blocked {
		release()
		c.JSON(http.StatusConflict, gin.H{"error": crossProviderBusyText})
		return nil, false
	}
	return release, true
}

// crossProviderBlocked 返回该账号现在能不能走 self 这家：时间窗内有别家的绑定，或者有在途、待人工的付款单，就不能。
// 查询出错由调用方按拒绝处理。
func crossProviderBlocked(ctx context.Context, email string, self provider.Name) (bool, error) {
	email = normEmail(email)
	if bound, err := db.HasRecentCDKBindingForEmail(ctx, email, time.Now().Add(-crossProviderWindow), string(self)); err != nil || bound {
		return bound, err
	}
	return db.HasOpenPayOrderForEmail(ctx, email)
}
