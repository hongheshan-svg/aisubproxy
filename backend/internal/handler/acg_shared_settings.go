package handler

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

// 代理商端的「对接异次元」设置（/api/v1/agent/shared）。
// 商户密钥明文只出现在 POST /shared/key 的响应里（no-store）：不进日志、审计和其他响应，库里只存密文。

const (
	sharedMinACGVersion    = "3.6.1"
	sharedAllowlistMax     = 5
	sharedLowBalanceMax    = 1000000 // 分，即 10000 美元
	sharedFailuresWindow   = 30 * 24 * time.Hour
	sharedFailures24h      = 24 * time.Hour
	sharedAppIDAttempts    = 3
	sharedMsgKeyMaint      = "系统维护中，暂不能生成密钥"
	sharedMsgSetupAdminOff = "对接已被管理员关闭"
	sharedAlnum            = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

// sharedRandom 用 crypto/rand 均匀取 n 个 [A-Za-z0-9]。
func sharedRandom(n int) (string, error) {
	limit := big.NewInt(int64(len(sharedAlnum)))
	out := make([]byte, n)
	for i := range out {
		v, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		out[i] = sharedAlnum[v.Int64()]
	}
	return string(out), nil
}

// sharedAgentCreds 读代理商和它的对接设置；失败时已写好响应。
func sharedAgentCreds(c *gin.Context) (db.Agent, db.AgentSharedCreds, bool) {
	ctx := c.Request.Context()
	a, err := db.GetAgent(ctx, agentID(c))
	if errors.Is(err, db.ErrAgentNotFound) {
		agentError(c, http.StatusUnauthorized, "unauthorized", "未登录或登录已过期")
		return db.Agent{}, db.AgentSharedCreds{}, false
	}
	if err != nil {
		agentDBBusy(c, err)
		return db.Agent{}, db.AgentSharedCreds{}, false
	}
	creds, err := db.GetAgentSharedCreds(ctx, a.ID)
	if err != nil {
		agentDBBusy(c, err)
		return db.Agent{}, db.AgentSharedCreds{}, false
	}
	return a, creds, true
}

// sharedCheckTOTP 校验动态码；不通过时已写好响应。通过后调用方要 release（defer）并在成功后 ok()。
func sharedCheckTOTP(c *gin.Context, a db.Agent, code string) (*agentTOTPSlot, bool) {
	slot, ok := agentTOTPAllowed(c, a)
	if !ok {
		return nil, false
	}
	switch verifyAgentTOTP(c, a, code) {
	case totpErrorSent:
		slot.release()
		return nil, false
	case totpRejected:
		slot.fail()
		agentLoginFailed(agentRateKey(c, a.Username))
		slot.release()
		agentError(c, http.StatusBadRequest, "totp_invalid", agentMsgBadTOTP)
		return nil, false
	}
	return slot, true
}

// sharedStatus 对接状态：管理员关闭优先，其次有商户 ID 即 active，否则 inactive。
func sharedStatus(creds db.AgentSharedCreds) string {
	switch {
	case creds.AdminDisabled:
		return "admin_disabled"
	case creds.AppID != "":
		return "active"
	}
	return "inactive"
}

// sharedFailureView 取码失败记录的 JSON 视图，代理商端和管理员端共用。
func sharedFailureView(f db.AgentSharedFailure) gin.H {
	var resolved any
	if f.ResolvedOrderID != nil {
		resolved = *f.ResolvedOrderID
	}
	return gin.H{
		"id": f.ID, "request_no": f.RequestNo, "product_code": f.ProductCode, "quantity": f.Quantity, "reason": f.Reason,
		"message": f.Message, "attempts": f.Attempts, "first_at": f.FirstAt, "last_at": f.LastAt, "resolved_order_id": resolved,
	}
}

// AgentSharedGet GET /shared
func AgentSharedGet(c *gin.Context) {
	_, creds, ok := sharedAgentCreds(c)
	if !ok {
		return
	}
	status := sharedStatus(creds)
	keyInvalid := false
	if creds.KeySealed != "" {
		mk, err := secretbox.LoadKey()
		if err != nil {
			keyInvalid = true
		} else if _, err := secretbox.Open(mk, sharedKeyName(creds.AgentID, creds.AppID), creds.KeySealed); err != nil {
			keyInvalid = true
		}
	}
	allow := creds.IPAllowlist
	if allow == nil {
		allow = []string{}
	}
	c.JSON(http.StatusOK, gin.H{
		"status": status, "app_id": creds.AppID, "key_created_at": creds.KeyCreatedAt, "last_used_at": creds.LastUsedAt,
		"key_invalid": keyInvalid, "ip_allowlist": allow, "low_balance_cents": creds.LowBalanceCents, "min_acg_version": sharedMinACGVersion,
	})
}

// AgentSharedCreateKey POST /shared/key：开通或重置。重置保留商户 ID、换密钥，旧密钥立刻失效。
func AgentSharedCreateKey(c *gin.Context) {
	var req struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		agentOrderInvalid(c, agentBodyFailText)
		return
	}
	a, creds, ok := sharedAgentCreds(c)
	if !ok {
		return
	}
	if creds.AdminDisabled { // 先拒，不消耗动态码
		agentError(c, http.StatusForbidden, "shared_admin_disabled", sharedMsgSetupAdminOff)
		return
	}
	mk, err := secretbox.LoadKey()
	if err != nil {
		agentOrderNoKey(c, secretErrKind(err), sharedMsgKeyMaint)
		return
	}
	slot, ok := sharedCheckTOTP(c, a, req.Code)
	if !ok {
		return
	}
	defer slot.release()

	ctx := c.Request.Context()
	reset := creds.AppID != ""
	appID := creds.AppID
	var key string
	for try := 0; ; try++ {
		if !reset {
			suffix, err := sharedRandom(14)
			if err != nil {
				agentDBBusy(c, err)
				return
			}
			appID = "ag" + suffix
		}
		if key, err = sharedRandom(32); err != nil {
			agentDBBusy(c, err)
			return
		}
		sealed, err := secretbox.Seal(mk, sharedKeyName(a.ID, appID), key)
		if err != nil {
			log.Printf("[acg-shared] 加密商户密钥失败: %s", secretErrKind(err))
			agentError(c, http.StatusServiceUnavailable, "maintenance", sharedMsgKeyMaint)
			return
		}
		err = db.SetAgentSharedKey(ctx, a.ID, appID, sealed, agentNow())
		if err == nil {
			break
		}
		if errors.Is(err, db.ErrAgentSharedAdminDisabled) {
			agentError(c, http.StatusForbidden, "shared_admin_disabled", sharedMsgSetupAdminOff)
			return
		}
		if errors.Is(err, db.ErrAgentSharedAppIDTaken) && !reset && try+1 < sharedAppIDAttempts {
			continue
		}
		agentDBBusy(c, err)
		return
	}
	slot.ok()
	action := "agent_shared_key_create"
	if reset {
		action = "agent_shared_key_reset"
	}
	auditAgent(c, action, "app_id="+appID)
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"app_id": appID, "app_key": key})
}

// AgentSharedDisable POST /shared/disable
func AgentSharedDisable(c *gin.Context) {
	var req struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		agentOrderInvalid(c, agentBodyFailText)
		return
	}
	a, creds, ok := sharedAgentCreds(c)
	if !ok {
		return
	}
	slot, ok := sharedCheckTOTP(c, a, req.Code)
	if !ok {
		return
	}
	defer slot.release()
	if err := db.DisableAgentShared(c.Request.Context(), a.ID, agentNow()); err != nil {
		agentDBBusy(c, err)
		return
	}
	slot.ok()
	auditAgent(c, "agent_shared_disable", "app_id="+creds.AppID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// sharedNormalizeIP 把白名单的一项规整成存库的写法；不合法返回 false。
// 单个地址去掉 IPv4 映射和 zone；网段先取网络地址；::ffff:a.b.c.d/N（N >= 96）转成 IPv4 网段，N < 96 按非法处理。
func sharedNormalizeIP(item string) (string, bool) {
	if strings.Contains(item, "/") {
		p, err := netip.ParsePrefix(item)
		if err != nil {
			return "", false
		}
		if p.Addr().Is4In6() {
			if p.Bits() < 96 {
				return "", false
			}
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		return p.Masked().String(), true
	}
	addr, err := netip.ParseAddr(item)
	if err != nil {
		return "", false
	}
	return addr.Unmap().WithZone("").String(), true
}

// AgentSharedAllowlist PUT /shared/allowlist：先校验再要动态码。
func AgentSharedAllowlist(c *gin.Context) {
	var req struct {
		Code string   `json:"code"`
		IPs  []string `json:"ips"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		agentOrderInvalid(c, agentBodyFailText)
		return
	}
	tooMany := fmt.Sprintf("IP 白名单最多 %d 项", sharedAllowlistMax)
	if len(req.IPs) > 100 { // 防超大请求，不逐项解析
		agentOrderInvalid(c, tooMany)
		return
	}
	list := make([]string, 0, len(req.IPs))
	seen := map[string]bool{}
	for _, raw := range req.IPs {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		norm, ok := sharedNormalizeIP(item)
		if !ok {
			agentOrderInvalid(c, "「"+item+"」不是合法的 IP 或网段")
			return
		}
		if !seen[norm] {
			seen[norm] = true
			list = append(list, norm)
		}
	}
	if len(list) > sharedAllowlistMax {
		agentOrderInvalid(c, tooMany)
		return
	}
	a, _, ok := sharedAgentCreds(c)
	if !ok {
		return
	}
	slot, ok := sharedCheckTOTP(c, a, req.Code)
	if !ok {
		return
	}
	defer slot.release()
	if err := db.SetAgentSharedAllowlist(c.Request.Context(), a.ID, list, agentNow()); err != nil {
		agentDBBusy(c, err)
		return
	}
	slot.ok()
	auditAgent(c, "agent_shared_allowlist", fmt.Sprintf("count=%d", len(list)))
	c.JSON(http.StatusOK, gin.H{"ip_allowlist": list})
}

// AgentSharedLowBalance PUT /shared/low-balance：不要动态码，不审计。
func AgentSharedLowBalance(c *gin.Context) {
	var req struct {
		LowBalanceCents *int64 `json:"low_balance_cents"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.LowBalanceCents == nil {
		agentOrderInvalid(c, agentBodyFailText)
		return
	}
	v := *req.LowBalanceCents
	if v < 0 || v > sharedLowBalanceMax {
		agentOrderInvalid(c, "提醒线需为 0 到 10000 美元")
		return
	}
	if err := db.SetAgentSharedLowBalance(c.Request.Context(), agentID(c), v, agentNow()); err != nil {
		agentDBBusy(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"low_balance_cents": v})
}

// AgentSharedFailures GET /shared/failures：最近 30 天的取码失败记录，按最后一次时间从新到旧。
func AgentSharedFailures(c *gin.Context) {
	limit, offset := agentPaging(c)
	since := agentNow().Add(-sharedFailuresWindow).Unix()
	list, total, err := db.ListAgentSharedFailures(c.Request.Context(), agentID(c), since, limit, offset)
	if err != nil {
		agentOrderBusy(c, "列出取码失败记录", err)
		return
	}
	items := make([]gin.H, 0)
	for _, f := range list {
		items = append(items, sharedFailureView(f))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total})
}

// pruneSharedFailures 删 30 天前最后一次失败的记录。挂在 runOpsMaintenance 里（每 10 分钟一轮，last_at 有索引）。
func pruneSharedFailures(now time.Time) {
	n, err := db.PruneAgentSharedFailures(context.Background(), now.Add(-sharedFailuresWindow).Unix())
	if err != nil {
		log.Printf("[acg-shared] 清理取码失败记录失败: %v", err)
		return
	}
	if n > 0 {
		log.Printf("[acg-shared] 已清理 %d 条超过 30 天的取码失败记录", n)
	}
}
