package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"golang.org/x/crypto/bcrypt"
)

const (
	agentSwapPasswordKey       = "agent_swap_password_hash"
	agentSwapDailyLimitKey     = "agent_swap_daily_limit"
	defaultAgentSwapDailyLimit = 20
)

// 简易 IP 限流：每 IP 每小时最多 20 次换码尝试
var (
	swapRateMu   sync.Mutex
	swapRateHits = map[string][]time.Time{}
)

func swapRateAllow(ip string) bool {
	swapRateMu.Lock()
	defer swapRateMu.Unlock()
	now := time.Now()
	cut := now.Add(-time.Hour)
	hits := swapRateHits[ip]
	kept := hits[:0]
	for _, t := range hits {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= 20 {
		swapRateHits[ip] = kept
		return false
	}
	swapRateHits[ip] = append(kept, now)
	return true
}

func setAgentSwapPassword(plain string) error {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return db.DeleteSetting(agentSwapPasswordKey)
	}
	if len(plain) < 6 {
		return errPasswordTooShort
	}
	hash, err := db.HashAdminPassword(plain)
	if err != nil {
		return err
	}
	return db.SetSetting(agentSwapPasswordKey, hash)
}

var errPasswordTooShort = &simpleError{"代理换码密码至少 6 位"}

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

// issueDefinitelyFailed 能否确定卡台没有发码（只有这时才能释放占位）。
// 卡台文档：发码整批成功或整批回滚，业务拒绝和密钥错误不会发码；请求没发出去（连不上、DNS 失败）当然也没发。
// 其余一律算结果不明：超时、断连、5xx、409 同键处理中、429、响应解析不了（可能是网关页面）。
func issueDefinitelyFailed(err error) bool {
	var ae *cardplatform.APIError
	if errors.As(err, &ae) {
		switch ae.HTTPStatus {
		case http.StatusUnauthorized:
			return true
		case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound:
			return ae.Code != 0 || ae.ErrorCode != ""
		}
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// finishDisableOldCode 禁用旧码，成功后把记录从 disabling 转为 done。禁用是幂等的，重复调用安全。
// 记录不在 disabling（收尾写库失败留在 pending，或已被人工处理）时只禁用，不改状态。
func finishDisableOldCode(ctx context.Context, cli *cardplatform.Client, exID, oldID int64) error {
	if cli == nil {
		return errors.New("卡台未配置")
	}
	if err := cli.DisableCDK(ctx, oldID); err != nil {
		return err
	}
	_ = db.UpdateCardplatformCDKStatus(oldID, "disabled")
	if exID > 0 {
		if err := db.CompleteAgentCDKExchangeDisable(exID); err != nil && !errors.Is(err, db.ErrAgentExchangeState) {
			log.Printf("[agent-exchange] 记录 %d 旧码已禁用但转 done 失败（下一轮维护会再试）: %v", exID, err)
		}
	}
	return nil
}

// reportDisableFailure 换码后禁用旧码失败：旧码新码此刻都能兑换，同一次购买可能被付两次费，必须有人知道。
func reportDisableFailure(exID, oldID, newID int64, err error, ip string) {
	reason := err.Error()
	if len(reason) > 200 {
		reason = reason[:200]
	}
	log.Printf("[agent-exchange] 禁用旧码 %d 失败（新码 %d 已发出）: %v", oldID, newID, err)
	_ = db.TouchAgentCDKExchange(exID, "禁用旧码失败："+reason)
	db.WriteAudit("agent-swap", "agent_cdk_exchange_disable_failed",
		fmt.Sprintf("id=%d old=%d new=%d reason=%s", exID, oldID, newID, reason), ip)
	opsAlert(fmt.Sprintf("代理换码已发出新码，但禁用旧码失败：记录 %d，旧码 ID %d，新码 ID %d，原因：%s。旧码在卡台仍可兑换，系统会每 10 分钟自动重试禁用；若一直失败请到卡台手动禁用。", exID, oldID, newID, reason))
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
	c.JSON(http.StatusConflict, gin.H{"error": msg, "state": state})
}

// agentExchangeUnknown 发码结果不明：保留占位、告警、转人工。占位状态写库失败时行留在 pending，由维护循环升级。
func agentExchangeUnknown(c *gin.Context, exID, oldID int64, reason string) {
	if len(reason) > 300 {
		reason = reason[:300]
	}
	if err := db.MarkAgentCDKExchangeUnknown(exID, reason); err != nil {
		log.Printf("[agent-exchange] 记录 %d 标记结果不明失败: %v", exID, err)
	}
	db.WriteAudit("agent-swap", "agent_cdk_exchange_unknown",
		fmt.Sprintf("id=%d old=%d reason=%s", exID, oldID, reason), c.ClientIP())
	opsAlert(fmt.Sprintf("代理换码结果不明，已转人工：记录 %d，旧码 ID %d，原因：%s。请先到卡台核对是否已发码，再到后台定论。", exID, oldID, reason))
	c.JSON(http.StatusBadGateway, gin.H{"error": "发码结果不明，已转人工核对，请勿重试", "outcome": "unknown"})
}

// orderLooksUnpaid 订单终态失败且无扣款证据。
func orderLooksUnpaid(status, stage string, finalMinor int64, events []map[string]any) bool {
	st := strings.ToLower(strings.TrimSpace(status))
	// 成功/进行中不可换
	switch st {
	case "completed", "success", "paid":
		return false
	case "running", "queued", "dispatching", "pending", "awaiting_card", "funding_pending", "card_open_review":
		return false
	}
	// 允许失败类终态
	okFail := st == "failed_precharge" || st == "declined" || st == "cancelled" || st == "failed"
	if !okFail {
		// 有些上游用 stage 表达失败
		sg := strings.ToLower(stage)
		if strings.Contains(sg, "failed") || strings.Contains(sg, "decline") || strings.Contains(sg, "unpaid") {
			okFail = true
		}
	}
	if !okFail {
		return false
	}
	if finalMinor > 0 {
		return false
	}
	for _, ev := range events {
		pf := strings.ToLower(strAny(ev["payment_fact"]))
		if pf == "true" || pf == "1" || pf == "charged" || pf == "paid" {
			return false
		}
		cat := strings.ToLower(strAny(ev["category"]))
		if cat == "charged" || cat == "payment" {
			// 再看 message
			msg := strings.ToLower(strAny(ev["public_message"]) + " " + strAny(ev["event"]))
			if strings.Contains(msg, "paid") || strings.Contains(msg, "扣款") || strings.Contains(msg, "charged") {
				return false
			}
		}
	}
	return true
}

// PublicAgentCDKExchange POST /api/v1/public/cdk/exchange
// body: { password, code }  校验代理密码后，未扣款失败的 CDK 换一张同套餐新码。
func PublicAgentCDKExchange(c *gin.Context) {
	ip := c.ClientIP()
	if !swapRateAllow(ip) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "请求过于频繁，请稍后再试"})
		return
	}
	if !agentSwapPasswordConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "管理员尚未启用代理换码功能"})
		return
	}
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if !verifyAgentSwapPassword(req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "密码错误"})
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" || len(code) < 10 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入完整卡密"})
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
	a, ok := upstreamOrFail(c, p)
	if !ok {
		return
	}
	ex, ok := a.(upstreamExchanger)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该服务商不支持代理换码"})
		return
	}
	ex.Exchange(c, code, ip)
}

// zovoCardExchange 是 ZovoCard 的代理换码：在卡台上查单、发新码、禁用旧码。入口和分派在 PublicAgentCDKExchange。
func zovoCardExchange(c *gin.Context, code, ip string) {
	codeHash := db.HashCDKCode(code)
	// 这里只是快速拒绝，真正防并发的是下面发码前的原子占位
	if st := db.AgentCDKExchangeState(codeHash); st != "" {
		agentExchangeConflict(c, st)
		return
	}

	upstreamID, plan, prefix, localStatus, ok := db.LookupStoredCDKByCode(code)
	if !ok || upstreamID <= 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "本站未找到该完整卡密（仅支持本站发过的码）"})
		return
	}
	plan = strings.ToLower(strings.TrimSpace(plan))
	if plan == "" {
		plan = "plus"
	}

	cli := cardplatform.NewFromSettings()
	// 查该 CDK 的订单（卡台 OpenAPI）
	raw, err := cli.ListCDKOrdersQuery(c.Request.Context(), cardplatform.CDKOrderListQuery{
		Page: 1, PageSize: 20, CDKID: upstreamID,
	})
	if err != nil {
		writeCardErr(c, err)
		return
	}
	list, total := parseCDKOrderList(raw)
	if total == 0 || len(list) == 0 {
		// 从未进兑换：不允许「白嫖」新码
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "该卡密尚无兑换失败记录，不能换新码",
			"hint":  "仅限充值失败且未扣款的卡密",
		})
		return
	}

	// 取最近一单
	latest := list[0]
	orderID := int64(anyToInt64(latest["order_id"]))
	if orderID == 0 {
		orderID = int64(anyToInt64(latest["id"]))
	}
	status := strings.ToLower(strAny(latest["status"]))
	stage := strings.ToLower(strAny(latest["stage"]))
	finalMinor := anyToInt64(latest["final_amount_minor"])

	// 拉详情看 events 的 payment_fact
	var events []map[string]any
	if orderID > 0 {
		if detailRaw, err := cli.GetCDKOrder(c.Request.Context(), strconv.FormatInt(orderID, 10)); err == nil {
			if evs := extractOrderEvents(detailRaw); len(evs) > 0 {
				events = evs
			}
			// 详情里的 order 覆盖
			if m := extractOrderMap(detailRaw); m != nil {
				if s := strAny(m["status"]); s != "" {
					status = strings.ToLower(s)
				}
				if s := strAny(m["stage"]); s != "" {
					stage = strings.ToLower(s)
				}
				if n := anyToInt64(m["final_amount_minor"]); n > 0 {
					finalMinor = n
				}
			}
		}
	}

	// 任一成功单则拒
	for _, it := range list {
		st := strings.ToLower(strAny(it["status"]))
		if st == "completed" || st == "success" || st == "paid" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "该卡密已有成功充值记录，不能换新码"})
			return
		}
	}

	if !orderLooksUnpaid(status, stage, finalMinor, events) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":        "不符合换码条件（需充值失败且未扣款）",
			"order_status": status,
			"order_stage":  stage,
			"final_amount": finalMinor,
			"local_status": localStatus,
		})
		return
	}

	// ★新码必须与旧码同一付款地区★：补发原来只带套餐，智利/日本等地区码补发出来全成了默认菲律宾区，
	// 用户兑换时按 PHP 走、和原单完全不是一回事（2026-10-10 代理反馈）。地区以本站缓存为准，
	// 老数据没记过地区就去卡台查；两边都拿不到就拒绝补发——宁可让代理找管理员，也不发错区的码。
	country, known := db.LookupStoredCDKRegion(upstreamID)
	if !known {
		country, known = upstreamCDKRegion(c, cli, upstreamID)
	}
	if !known {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "无法确认原卡密的付款地区，暂不能补发，请联系管理员"})
		return
	}
	// 发一张同套餐、同地区的新码（带本站选卡偏好；跳过未启动卡头）
	sitePref, hasSitePref := issuePrefFromSite()
	issuePrefs := agentSwapIssuePrefs(sitePref, hasSitePref, country)

	// ★先占位再发码★：校验全部通过才占，校验失败不占额度。并发请求只有一个能占到，其余 409，卡台收不到第二次发码。
	exID, err := db.ReserveAgentCDKExchange(db.AgentExchangeReservation{
		OldHash: codeHash, OldID: upstreamID, OldPrefix: prefix, Plan: plan,
		OrderID: orderID, OrderStatus: status, IP: ip, PasswordFP: agentSwapPasswordFP(),
	}, agentSwapDailyLimit(), time.Now())
	switch {
	case errors.Is(err, db.ErrAgentExchangeExists):
		agentExchangeConflict(c, db.AgentCDKExchangeState(codeHash))
		return
	case errors.Is(err, db.ErrAgentExchangeDailyLimit):
		limit := agentSwapDailyLimit()
		opsAlertDaily("agent-swap-daily-limit", fmt.Sprintf("代理换码已达今日上限（%d 张），后续请求已拒绝。如果不是正常业务量，请检查换码密码是否泄露。", limit), time.Now())
		c.JSON(http.StatusTooManyRequests, gin.H{"error": fmt.Sprintf("代理换码已达今日上限（%d 张），请联系管理员", limit)})
		return
	case err != nil:
		log.Printf("[agent-exchange] 占位失败 old=%d: %v", upstreamID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "换码暂时不可用，请稍后再试"})
		return
	}

	// 发码和后续收尾不跟随客户端连接：客户端断开不应把一次正常的发码变成结果不明。两个调用都有自己的超时。
	ctx := context.WithoutCancel(c.Request.Context())
	// 幂等键按旧码固定：卡台对同键只执行一次、失败不缓存，所以重试安全，也是占位之外的第二道防线。
	idem := "agent-swap-" + codeHash[:32]
	var res *cardplatform.IssueCDKResult
	if len(issuePrefs) > 0 {
		res, err = cli.IssueCDKs(ctx, plan, 1, idem, issuePrefs[0])
	} else {
		res, err = cli.IssueCDKs(ctx, plan, 1, idem)
	}
	if err != nil {
		if !issueDefinitelyFailed(err) {
			agentExchangeUnknown(c, exID, upstreamID, err.Error())
			return
		}
		if rerr := db.ReleaseAgentCDKExchange(exID); rerr != nil {
			// 没释放掉只是代理暂时不能重试，留给维护循环升级、人工处理，不影响资金安全
			log.Printf("[agent-exchange] 记录 %d 释放占位失败: %v", exID, rerr)
		}
		writeCardErr(c, err)
		return
	}
	if res == nil || len(res.Issued) == 0 || strings.TrimSpace(res.Issued[0].Code) == "" {
		agentExchangeUnknown(c, exID, upstreamID, "卡台响应成功但未返回完整码")
		return
	}
	newIt := res.Issued[0]
	newCode := strings.TrimSpace(newIt.Code)
	newPrefix := strings.TrimSpace(newIt.CodePrefix)
	if newPrefix == "" && len(newCode) >= 14 {
		newPrefix = newCode[:14]
	}
	if got := strings.ToUpper(strings.TrimSpace(newIt.PaymentCountry)); got != "" && got != country {
		log.Printf("[agent-exchange] 新码 %d 地区 %s 与旧码 %d 地区 %q 不一致", newIt.ID, got, upstreamID, country)
	}
	_ = db.SaveCardplatformCDKCode(newIt.ID, newCode, newPrefix, plan, newIt.FeeAmountMinor, country)

	// 新码已发出：写库失败也要把码交给代理，占位留在 pending，由维护循环升级、人工定论
	if err := db.FinishAgentCDKExchange(exID, newIt.ID, newPrefix); err != nil {
		log.Printf("[agent-exchange] 记录 %d 收尾失败 new=%d: %v", exID, newIt.ID, err)
		opsAlert(fmt.Sprintf("代理换码已发出新码但写库失败：记录 %d，旧码 ID %d，新码 ID %d。占位已保留，请到后台定论。", exID, upstreamID, newIt.ID))
	}

	// 禁用旧码（防再次兑换）。失败不回滚新码，但记录停在 disabling：告警，并由维护循环重试，成功后才转 done（MO-08）
	if err := finishDisableOldCode(ctx, cli, exID, upstreamID); err != nil {
		reportDisableFailure(exID, upstreamID, newIt.ID, err, ip)
	}

	db.WriteAudit("agent-swap", "agent_cdk_exchange",
		"old="+strconv.FormatInt(upstreamID, 10)+" new="+strconv.FormatInt(newIt.ID, 10)+" plan="+plan+" region="+country+" order="+strconv.FormatInt(orderID, 10),
		ip)

	c.JSON(http.StatusOK, gin.H{
		"ok":              true,
		"plan":            plan,
		"old_cdk_id":      upstreamID,
		"old_code_prefix": prefix,
		"new_cdk_id":      newIt.ID,
		"new_code":        newCode,
		"new_code_prefix": newPrefix,
		"payment_country": country,
		"order_status":    status,
		"message":         "已换发全新卡密，请妥善保存（仅显示一次）",
	})
}

// AdminListAgentExchanges GET /api/v1/admin/cardplatform/agent-exchanges?state=unknown
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

// AdminResolveAgentExchange POST /api/v1/admin/cardplatform/agent-exchanges/:id/resolve
// 人工定论结果不明的换码：先到卡台核对。released = 确认没发码，释放占位让代理重试；
// issued = 确认已发码，记下新码 ID 并禁用旧码。只能处理 unknown，每次写审计。
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
			c.JSON(http.StatusBadRequest, gin.H{"error": "请填写卡台查到的新码 ID（new_cdk_id）"})
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
	if req.Result == "issued" {
		// 新码确认存在，旧码必须禁用，否则两张都能兑换。禁用失败不回滚定论，告诉管理员手动禁用。
		e, gerr := db.GetAgentExchange(id)
		disabled := false
		if gerr == nil && e.OldUpstreamID > 0 {
			if derr := finishDisableOldCode(context.WithoutCancel(c.Request.Context()), cardplatform.NewFromSettings(), id, e.OldUpstreamID); derr != nil {
				reportDisableFailure(id, e.OldUpstreamID, req.NewCDKID, derr, c.ClientIP())
			} else {
				disabled = true
			}
			out["old_cdk_id"] = e.OldUpstreamID
		}
		out["old_disabled"] = disabled
		detail += fmt.Sprintf(" old_disabled=%v", disabled)
	}
	auditAdmin(c, "agent_exchange_resolve", detail)
	c.JSON(http.StatusOK, out)
}

// agentSwapIssuePrefs 补发的发码偏好：地区与选卡偏好是两件独立的事，
// 没有本站选卡配置时也要把非默认地区带上（与正常发码 PublicIssue 同一规则）。
func agentSwapIssuePrefs(sitePref cardplatform.IssueCardPref, hasSitePref bool, country string) []cardplatform.IssueCardPref {
	country = strings.ToUpper(strings.TrimSpace(country))
	if !hasSitePref && country == "" {
		return nil
	}
	sitePref.PaymentCountry = country
	return []cardplatform.IssueCardPref{sitePref}
}

// upstreamCDKRegion 本站没记过地区的老码，从卡台按 id 查权威地区并回填缓存。
func upstreamCDKRegion(c *gin.Context, cli *cardplatform.Client, upstreamID int64) (string, bool) {
	res, err := cli.ListCDKsQuery(c.Request.Context(), cardplatform.CDKListQuery{Page: 1, PageSize: 50, Query: strconv.FormatInt(upstreamID, 10)})
	if err != nil || res == nil {
		return "", false
	}
	for _, it := range res.List {
		if it.ID == upstreamID {
			country := strings.ToUpper(strings.TrimSpace(it.PaymentCountry))
			_ = db.UpdateCardplatformCDKRegion(upstreamID, country)
			return country, true
		}
	}
	return "", false
}

func parseCDKOrderList(raw json.RawMessage) (list []map[string]any, total int) {
	if len(raw) == 0 {
		return nil, 0
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, 0
	}
	// data.list or list
	data := envelope
	if d, ok := envelope["data"].(map[string]any); ok {
		data = d
	}
	total = int(anyToInt64(data["total"]))
	arr, _ := data["list"].([]any)
	for _, v := range arr {
		if m, ok := v.(map[string]any); ok {
			list = append(list, m)
		}
	}
	if total == 0 {
		total = len(list)
	}
	return list, total
}

func extractOrderMap(raw json.RawMessage) map[string]any {
	var envelope map[string]any
	if json.Unmarshal(raw, &envelope) != nil {
		return nil
	}
	if d, ok := envelope["data"].(map[string]any); ok {
		if o, ok := d["order"].(map[string]any); ok {
			return o
		}
		// data itself is order
		if _, ok := d["status"]; ok {
			return d
		}
	}
	if o, ok := envelope["order"].(map[string]any); ok {
		return o
	}
	return nil
}

func extractOrderEvents(raw json.RawMessage) []map[string]any {
	var envelope map[string]any
	if json.Unmarshal(raw, &envelope) != nil {
		return nil
	}
	var arr []any
	if d, ok := envelope["data"].(map[string]any); ok {
		arr, _ = d["events"].([]any)
	}
	if arr == nil {
		arr, _ = envelope["events"].([]any)
	}
	out := make([]map[string]any, 0, len(arr))
	for _, v := range arr {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func anyToInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}
