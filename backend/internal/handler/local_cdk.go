package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

// 本地码（供卡型服务商，如 MarsWallex）的公开接口。入口是现有的 /public/cdk/*，按码或令牌分派到这里（见 cdk_dispatch.go），
// 前端不用改协议。哪家服务商由前缀给出候选，以库里码行的 provider 为准；响应都带服务商字段（见 localReply）。
//
// 这些接口不要登录，又会触发真实注资，所以：
//   - 响应全部从白名单字段生成，绝不返回卡号、卡尾号、代理、内部错误和时间线原文。
//   - 套餐和地区只取自码本身，请求里传什么都不看。
//   - 会话不落库：预检把它封进 secretbox 密文交回给前端，兑换时带回来；建单后由引擎加密保存并在终态清空。
const (
	localCDKUnavailableText = "兑换通道暂时不可用，请稍后再试"
	localCDKUsedText        = "该卡密已使用，无法再次兑换"
	localCDKBusyText        = "该卡密正在处理中，请勿重复提交"
	localCDKNotFoundText    = "卡密不存在"

	localPreflightPrefix = "mwp_"
	localPreflightAAD    = "local_cdk_preflight"
	localPreflightTTL    = 30 * time.Minute

	localSessionMaxLen   = 64 << 10
	localPreflightMaxLen = 128 << 10

	localSubmitLimit = 30 * time.Second
)

// 公开接口限流：每 IP 每小时。兑换页一张码要调 3 次变更接口；结果接口 3 秒轮询一次，批量页还是多张一起轮询，所以额度差得多。
// 现有公开接口里只有换码和安装向导有限流，预览、预检、兑换、结果、查询原来都没有，这里只给本地码分支加，ZovoCard 不变。
const (
	localRateMutate = "mutate"
	localRateQuery  = "query"
)

var localRateLimits = map[string]int{localRateMutate: 300, localRateQuery: 6000}

type localRateWindow struct {
	start time.Time
	count int
}

var (
	localRateMu   sync.Mutex
	localRateHits = map[string]*localRateWindow{}
)

// localRateAllow 固定窗口计数。结果接口的额度大，存时间戳太占内存。
func localRateAllow(kind, ip string) bool {
	now := time.Now()
	localRateMu.Lock()
	defer localRateMu.Unlock()
	if len(localRateHits) > 20000 {
		for k, w := range localRateHits {
			if now.Sub(w.start) >= time.Hour {
				delete(localRateHits, k)
			}
		}
	}
	key := kind + "|" + ip
	w := localRateHits[key]
	if w == nil || now.Sub(w.start) >= time.Hour {
		localRateHits[key] = &localRateWindow{start: now, count: 1}
		return true
	}
	if w.count >= localRateLimits[kind] {
		return false
	}
	w.count++
	return true
}

func resetLocalRate() {
	localRateMu.Lock()
	localRateHits = map[string]*localRateWindow{}
	localRateMu.Unlock()
}

func localRateCheck(c *gin.Context, kind string) bool {
	if localRateAllow(kind, c.ClientIP()) {
		return true
	}
	localReply(c, http.StatusTooManyRequests, gin.H{"error": "请求过于频繁，请稍后再试"})
	return false
}

// 同一张码同一时刻只有一个请求能往下走：状态检查和建单之间不能插进别的兑换或换码。
// 库里的唯一索引是最后一道防线，但它管不到干净失败的次数上限，也管不到换码。
var (
	localCDKLockMu sync.Mutex
	localCDKLocks  = map[int64]bool{}
)

func lockLocalCDK(id int64) (func(), bool) {
	localCDKLockMu.Lock()
	defer localCDKLockMu.Unlock()
	if localCDKLocks[id] {
		return nil, false
	}
	localCDKLocks[id] = true
	return func() {
		localCDKLockMu.Lock()
		delete(localCDKLocks, id)
		localCDKLockMu.Unlock()
	}, true
}

// ownsToken、ownsCode 判断令牌、码的前缀是不是这家供卡型服务商的。
func ownsToken(cs provider.CardSupply, token string) bool {
	p, ok := provider.CardSupplyForToken(token)
	return ok && p.Name() == cs.Name()
}

func ownsCode(cs provider.CardSupply, code string) bool {
	p, ok := provider.CardSupplyForCode(code)
	return ok && p.Name() == cs.Name()
}

// localCDKFromBody 按令牌或码找到这家的这张码。两个都给时必须是同一张。出错时已写好响应，第二个返回值为 false。
// 前缀只是候选：库里码行的 provider 必须就是这一家，对不上和查不到一样按「码不存在」处理，绝不转给上游或别家。
func localCDKFromBody(c *gin.Context, cs provider.CardSupply, body map[string]any) (db.LocalCDK, bool) {
	ctx := c.Request.Context()
	tok, code := str(body["redemption_token"]), str(body["code"])
	var row db.LocalCDK
	var err error
	switch {
	case ownsToken(cs, tok):
		row, err = db.GetLocalCDKByTokenHash(ctx, provider.HashToken(tok))
		if err == nil && code != "" {
			if other, cerr := db.GetLocalCDKByCode(ctx, provider.NormalizeCode(code)); cerr != nil || other.ID != row.ID {
				err = db.ErrLocalCDKNotFound
			}
		}
	case ownsCode(cs, code):
		row, err = db.GetLocalCDKByCode(ctx, provider.NormalizeCode(code))
	default:
		err = db.ErrLocalCDKNotFound
	}
	if err == nil && row.Provider != string(cs.Name()) {
		err = db.ErrLocalCDKNotFound
	}
	if err == nil {
		return row, true
	}
	// 码不存在时不带服务商字段：前缀只是候选，不能说这张码属于哪家
	if errors.Is(err, db.ErrLocalCDKNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": localCDKNotFoundText})
		return row, false
	}
	log.Printf("[local-cdk] 查询码失败: %v", err)
	localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
	return row, false
}

// localCDKRefuse 码现在不能兑换时的响应。文字里带「已使用」的，前端会转去查进度。
func localCDKRefuse(c *gin.Context, row db.LocalCDK, v db.LocalCDKView) {
	switch v.Status {
	case db.LocalCDKReserved:
		localReply(c, http.StatusConflict, gin.H{"error": localCDKBusyText + "（已使用，可查询进度）", "status": v.Status})
	case db.LocalCDKConsumed:
		localReply(c, http.StatusConflict, gin.H{"error": localCDKUsedText, "status": v.Status})
	case db.LocalCDKFailed:
		alertLocalCDKExhausted(row, v)
		localReply(c, http.StatusConflict, gin.H{"error": localCDKUsedText + "，已转人工处理", "status": v.Status})
	default:
		if v.Reason == db.LocalReasonExchanged {
			localReply(c, http.StatusBadRequest, gin.H{"error": "该卡密已换发新码，请使用新码", "status": v.Status})
			return
		}
		localReply(c, http.StatusBadRequest, gin.H{"error": "卡密已禁用", "status": v.Status})
	}
}

// alertLocalCDKExhausted 重兑次数用完时告警，转人工。每张码每天一条。不含完整码。
func alertLocalCDKExhausted(row db.LocalCDK, v db.LocalCDKView) {
	if v.Reason != db.LocalReasonAttemptsExhausted {
		return
	}
	opsAlertDaily("local-cdk-exhausted-"+strconv.FormatInt(row.ID, 10),
		fmt.Sprintf("%s 码 %d（%s，%s/%s）已 %d 次走到注资后失败，不再自动重试，请人工处理。", provider.DisplayName(provider.Name(row.Provider)), row.ID, row.CodePrefix, row.PlanKey, row.Region, v.FundedFailures), time.Now())
}

// localCDKPreview 预览：只读，不写任何东西，令牌由码算出。
func localCDKPreview(c *gin.Context, cs provider.CardSupply, code string) {
	if !localRateCheck(c, localRateMutate) {
		return
	}
	row, ok := localCDKFromBody(c, cs, map[string]any{"code": code})
	if !ok {
		return
	}
	v, err := db.LocalCDKViewOf(c.Request.Context(), row)
	if err != nil {
		log.Printf("[local-cdk] 码 %d 派生状态失败: %v", row.ID, err)
		localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
		return
	}
	if !v.Redeemable() {
		localCDKRefuse(c, row, v)
		return
	}
	resp := gin.H{
		"provider":         row.Provider,
		"status":           v.Status,
		"product":          row.Product,
		"plan":             row.PlanKey,
		"plan_type":        row.PlanKey,
		"plan_flow":        "direct",
		"region":           row.Region,
		"payment_country":  row.Region,
		"redemption_token": provider.TokenFor(cs, code),
		"attempts_left":    v.AttemptsLeft,
	}
	for k, val := range localKindFields(row.PlanKey) {
		resp[k] = val
	}
	if note := cs.PlanNote(row.PlanKey); note != "" {
		resp["plan_note"] = note
	}
	if _, rc, err := payengine.ResolvePlan(row.PlanKey, row.Region, false); err == nil {
		resp["currency"] = rc.Currency
	}
	c.Header("Cache-Control", "no-store")
	localReply(c, http.StatusOK, resp)
}

// localPreflightPayload 是预检令牌封装的内容。整个令牌是 secretbox 密文，只有本站能解开，篡改、跨码、过期都用不了。
type localPreflightPayload struct {
	CDKID   int64  `json:"cdk_id"`
	Session string `json:"session"`
	Exp     int64  `json:"exp"`
}

func sealLocalPreflight(id int64, session string, now time.Time) (string, error) {
	key, err := secretbox.LoadKey()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(localPreflightPayload{CDKID: id, Session: session, Exp: now.Add(localPreflightTTL).Unix()})
	if err != nil {
		return "", err
	}
	sealed, err := secretbox.Seal(key, localPreflightAAD, string(raw))
	if err != nil {
		return "", err
	}
	return localPreflightPrefix + sealed, nil
}

var errLocalPreflightInvalid = errors.New("预检已失效")

func openLocalPreflight(id int64, token string, now time.Time) (string, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, localPreflightPrefix) || len(token) > localPreflightMaxLen {
		return "", errLocalPreflightInvalid
	}
	key, err := secretbox.LoadKey()
	if err != nil {
		return "", err
	}
	raw, err := secretbox.Open(key, localPreflightAAD, strings.TrimPrefix(token, localPreflightPrefix))
	if err != nil {
		return "", errLocalPreflightInvalid
	}
	var p localPreflightPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil || p.CDKID != id || p.Session == "" || now.Unix() >= p.Exp {
		return "", errLocalPreflightInvalid
	}
	return p.Session, nil
}

// localCredentialSession 取请求自带的会话。邮箱密码模式这条路走不了：引擎只认 session。
func localCredentialSession(c *gin.Context, body map[string]any) (string, bool) {
	cred, _ := body["credential"].(map[string]any)
	if cred != nil && strings.EqualFold(str(cred["mode"]), "mailbox") {
		localReply(c, http.StatusBadRequest, gin.H{"error": "该卡密只支持粘贴 session 兑换"})
		return "", false
	}
	return str(cred["session"]), true
}

func parseLocalSession(c *gin.Context, raw string) (chatgpt.Session, bool) {
	if raw == "" {
		localReply(c, http.StatusBadRequest, gin.H{"error": "请先粘贴完整的 session JSON"})
		return chatgpt.Session{}, false
	}
	if len(raw) > localSessionMaxLen {
		localReply(c, http.StatusBadRequest, gin.H{"error": "session 内容过长"})
		return chatgpt.Session{}, false
	}
	sess, err := chatgpt.ParseSession(raw)
	if err != nil {
		localReply(c, http.StatusBadRequest, gin.H{"error": "会话无效，请粘贴完整的 session JSON"})
		return chatgpt.Session{}, false
	}
	return sess, true
}

// localCDKReady 是预检和兑换共用的检查，按这家服务商计算。公开接口只给固定文案，原因留给后台的就绪接口。
func localCDKReady(c *gin.Context, cs provider.CardSupply, row db.LocalCDK) bool {
	if st := checkCardSupply(c.Request.Context(), cs, row.PlanKey, row.Region, true); st.Readiness.OK {
		return true
	}
	localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
	return false
}

// localCDKPreflight 预检：不调上游、不落库，通过后把会话封进预检令牌。
func localCDKPreflight(c *gin.Context, cs provider.CardSupply, body map[string]any) {
	if !localRateCheck(c, localRateMutate) {
		return
	}
	c.Header("Cache-Control", "no-store")
	row, ok := localCDKFromBody(c, cs, body)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	v, err := db.LocalCDKViewOf(ctx, row)
	if err != nil {
		log.Printf("[local-cdk] 码 %d 派生状态失败: %v", row.ID, err)
		localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
		return
	}
	if !v.Redeemable() {
		localCDKRefuse(c, row, v)
		return
	}
	raw, ok := localCredentialSession(c, body)
	if !ok {
		return
	}
	sess, ok := parseLocalSession(c, raw)
	if !ok {
		return
	}
	if !localCDKReady(c, cs, row) {
		return
	}
	blocked, err := crossProviderBlocked(ctx, sess.Email, cs.Name())
	if err != nil {
		log.Printf("[local-cdk] 码 %d 互斥检查失败: %v", row.ID, err)
		localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
		return
	}
	if blocked {
		localReply(c, http.StatusConflict, gin.H{"error": crossProviderBusyText})
		return
	}
	token, err := sealLocalPreflight(row.ID, raw, time.Now())
	if err != nil {
		log.Printf("[local-cdk] 码 %d 封装预检令牌失败: %v", row.ID, err)
		localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
		return
	}
	// 不返回 plan：前端把预检里的 plan 当成账号当前套餐。这里没查账号，当前套餐要等引擎 check 才知道
	resp := gin.H{
		"provider":        row.Provider,
		"preflight_token": token,
		"email":           sess.Email,
		"account_email":   sess.Email,
		"expires_in":      int(localPreflightTTL / time.Second),
	}
	for k, val := range localKindFields(row.PlanKey) {
		resp[k] = val
	}
	localReply(c, http.StatusOK, resp)
}

// localKindFields 是预览、预检里按码的种类给出的字段：订阅开通码只给无订阅账号、到期不自动续费；
// 点数码只给已订阅 Plus 或 Pro 的账号，兑换前要勾选同意（见 localCDKRedeem 的 agree_credit_terms）。
func localKindFields(planKey string) gin.H {
	if payengine.PlanKind(planKey) == payengine.KindCredit {
		return gin.H{
			"kind":                     payengine.KindCredit,
			"credit_quantity":          payengine.CreditQuantity(planKey),
			"requires_subscription":    true,
			"requires_no_subscription": false,
			"requires_credit_terms":    true,
		}
	}
	return gin.H{
		"kind":                     payengine.KindSubscription,
		"requires_subscription":    false,
		"requires_no_subscription": true,
		"auto_renew":               false,
	}
}

// localCreditTermsText 是点数码没勾同意就兑换时的提示。
const localCreditTermsText = "请先阅读并勾选点数兑换须知：仅限已订阅 Plus 或 Pro 的账号，按账号计费币种付款、卡密地区须与之一致，点数 12 个月过期且不退款"

// localCDKRedeem 兑换（含干净失败后的重兑）。熔断、未就绪、跨服务商冲突都在建单之前拒绝，不占用码。
func localCDKRedeem(c *gin.Context, cs provider.CardSupply, body map[string]any) {
	if !localRateCheck(c, localRateMutate) {
		return
	}
	c.Header("Cache-Control", "no-store")
	row, ok := localCDKFromBody(c, cs, body)
	if !ok {
		return
	}
	unlock, ok := lockLocalCDK(row.ID)
	if !ok {
		localReply(c, http.StatusConflict, gin.H{"error": localCDKBusyText})
		return
	}
	defer unlock()

	// 拿到锁之后重读：等锁之前的那份可能已经过期（刚被禁用、刚换码）
	ctx := c.Request.Context()
	row, err := db.GetLocalCDK(ctx, row.ID)
	if err != nil {
		log.Printf("[local-cdk] 码重读失败: %v", err)
		localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
		return
	}
	v, err := db.LocalCDKViewOf(ctx, row)
	if err != nil {
		log.Printf("[local-cdk] 码 %d 派生状态失败: %v", row.ID, err)
		localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
		return
	}
	if !v.Redeemable() {
		localCDKRefuse(c, row, v)
		return
	}
	// 点数码：用户必须勾选同意兑换须知（设计 §5.4），没勾就不建单、不占用码
	if payengine.PlanKind(row.PlanKey) == payengine.KindCredit && body["agree_credit_terms"] != true {
		localReply(c, http.StatusBadRequest, gin.H{"error": localCreditTermsText})
		return
	}
	if !localCDKReady(c, cs, row) {
		return
	}
	raw, ok := localRedeemSession(c, row, body)
	if !ok {
		return
	}
	sess, ok := parseLocalSession(c, raw)
	if !ok {
		return
	}
	release, ok := holdAccountExclusive(normEmail(sess.Email))
	if !ok {
		localReply(c, http.StatusConflict, gin.H{"error": crossProviderBusyText})
		return
	}
	defer release()
	blocked, err := crossProviderBlocked(ctx, sess.Email, cs.Name())
	if err != nil {
		log.Printf("[local-cdk] 码 %d 互斥检查失败: %v", row.ID, err)
		localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
		return
	}
	if blocked {
		localReply(c, http.StatusConflict, gin.H{"error": crossProviderBusyText})
		return
	}

	// 建单不跟随客户端连接：写到一半被取消，用户看到失败、实际可能已经有单
	submitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), localSubmitLimit)
	defer cancel()
	engine := payEngine
	if engine == nil { // 就绪检查之后被摘掉
		localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
		return
	}
	o, err := engine.Submit(submitCtx, payengine.Request{
		Provider: row.Provider, PlanKey: row.PlanKey, Region: row.Region, Session: raw,
		Source: "cdk", SourceRef: db.LocalCDKSourceRef(row.ID),
	})
	if err != nil {
		localCDKSubmitFailed(c, row, err)
		return
	}
	db.WriteAudit("public", "local_cdk_redeem", fmt.Sprintf("cdk=%d order=%s", row.ID, o.OrderNo), c.ClientIP())

	token := str(body["redemption_token"])
	if !ownsToken(cs, token) {
		token = provider.TokenFor(cs, str(body["code"]))
	}
	public := localOrderPublic(db.LocalCDKView{Status: db.LocalCDKReserved, Latest: &db.LocalCDKOrder{State: o.State, AccountEmail: sess.Email}}, row)
	localReply(c, http.StatusOK, gin.H{
		"provider":         row.Provider,
		"redemption_token": token,
		"status":           public["status"],
		"message":          public["message"],
		"order":            public,
	})
}

// localRedeemSession 取兑换用的会话：请求自带的优先，其次预检令牌。
func localRedeemSession(c *gin.Context, row db.LocalCDK, body map[string]any) (string, bool) {
	raw, ok := localCredentialSession(c, body)
	if !ok {
		return "", false
	}
	if raw != "" {
		return raw, true
	}
	token := str(body["preflight_token"])
	if token == "" {
		localReply(c, http.StatusBadRequest, gin.H{"error": "请先完成账号预检"})
		return "", false
	}
	raw, err := openLocalPreflight(row.ID, token, time.Now())
	if errors.Is(err, errLocalPreflightInvalid) {
		localReply(c, http.StatusBadRequest, gin.H{"error": "预检已失效，请重新检测账号"})
		return "", false
	}
	if err != nil {
		log.Printf("[local-cdk] 码 %d 解开预检令牌失败: %v", row.ID, err)
		localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
		return "", false
	}
	return raw, true
}

// localCDKSubmitFailed 把建单错误映射成固定文案，内部错误只进日志。
func localCDKSubmitFailed(c *gin.Context, row db.LocalCDK, err error) {
	switch {
	case db.IsLocalCDKOrderConflict(err):
		localReply(c, http.StatusConflict, gin.H{"error": localCDKBusyText})
	case errors.Is(err, payengine.ErrInflight):
		localReply(c, http.StatusConflict, gin.H{"error": crossProviderBusyText})
	case errors.Is(err, payengine.ErrBadSession):
		localReply(c, http.StatusBadRequest, gin.H{"error": "会话无效，请粘贴完整的 session JSON"})
	case errors.Is(err, payengine.ErrPlanUnavailable):
		log.Printf("[local-cdk] 码 %d 建单时套餐不可用: %v", row.ID, err)
		localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
	default:
		log.Printf("[local-cdk] 码 %d 建单失败: %v", row.ID, err)
		localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
	}
}

// localStage 把单据状态映射成给用户看的阶段。只有这张表里的值能出去。
var localStage = map[string]string{
	payengine.StateQueued:      "queued",
	payengine.StateChecking:    "credential_check",
	payengine.StateLinkCreated: "checkout",
	payengine.StateOpening:     "checkout",
	payengine.StateFunding:     "card_funding",
	payengine.StateFilling:     "card_funding",
	payengine.StateSubmitted:   "payment",
	payengine.StateRequires3DS: "payment",
	payengine.StateVerifying:   "subscription",
}

// localFailureText 是干净失败时给用户看的原因，按错误码白名单映射；其余都用通用文字，绝不用 error_message。
var localFailureText = map[string]string{
	"session_invalid":    "账号登录已失效，请重新获取 session 后再试",
	"already_subscribed": "该账号已有订阅，无法开通",
	"plan_unavailable":   "该套餐暂不可用，请稍后再试",
	// 点数码的账号检查（都在付款之前，没有扣款）
	"credit_no_subscription":   "该账号没有生效中的 Plus 或 Pro 订阅，不能购买点数，也没有发起支付",
	"credit_plan_ineligible":   "该账号的套餐不能购买点数（仅限已订阅 Plus 或 Pro 的账号），没有发起支付",
	"credit_not_personal":      "该账号不是个人账号，不能购买点数，也没有发起支付",
	"credit_delinquent":        "该账号有欠费，暂不能购买点数，也没有发起支付",
	"credit_currency_mismatch": "账号计费币种与卡密地区不一致，不能用这张卡密购买点数，也没有发起支付",
}

const localFailureDefaultText = "本次未能开通，也没有发起支付，可以稍后重新提交"

// localPublicState 由派生状态生成给用户看的状态、阶段和文字。
func localPublicState(v db.LocalCDKView) (status, stage, message string) {
	switch v.Status {
	case db.LocalCDKConsumed:
		return "completed", "subscription", "开通完成"
	case db.LocalCDKReserved:
		if v.Reason == db.LocalReasonNeedsReview {
			return "review", "subscription", "结果确认中，已转人工核对，请勿重复提交"
		}
		state := ""
		if v.Latest != nil {
			state = v.Latest.State
		}
		if state == payengine.StateQueued || state == "" {
			return "queued", "queued", "已提交，正在排队"
		}
		return "running", localStage[state], "正在处理，请稍候"
	case db.LocalCDKFailed:
		switch v.Reason {
		case db.LocalReasonDeclined:
			return "declined", "", "支付未成功，已转人工处理，请勿重复提交"
		case db.LocalReasonAttemptsExhausted:
			return "failed", "", "多次未能开通，已转人工处理"
		}
		return "failed", "", "处理异常，已转人工处理"
	case db.LocalCDKDisabled:
		if v.Reason == db.LocalReasonExchanged {
			return "cancelled", "", "该卡密已换发新码"
		}
		return "cancelled", "", "卡密已禁用"
	}
	text := localFailureDefaultText
	if v.Latest != nil {
		if t, ok := localFailureText[v.Latest.ErrorCode]; ok {
			text = t
		}
	}
	return "failed_precharge", "", text
}

// localOrderPublic 是公开结果里的 order。字段是白名单，新增字段要想清楚能不能给用户看。
func localOrderPublic(v db.LocalCDKView, row db.LocalCDK) gin.H {
	status, stage, message := localPublicState(v)
	email := ""
	if v.Latest != nil {
		email = maskEmailForAdmin(v.Latest.AccountEmail)
	}
	out := gin.H{
		"status":  status,
		"stage":   stage,
		"message": message,
		"plan":    row.PlanKey,
		"region":  row.Region,
	}
	if email != "" {
		out["account_email"] = email
	}
	if v.Status == db.LocalCDKUnused {
		out["can_retry"] = true
		out["attempts_left"] = v.AttemptsLeft
	}
	return out
}

// writeLocalResult 写公开结果。没有单时 404：码还没兑换过，没有结果可查。
func writeLocalResult(c *gin.Context, row db.LocalCDK, extra gin.H) {
	v, err := db.LocalCDKViewOf(c.Request.Context(), row)
	if err != nil {
		log.Printf("[local-cdk] 码 %d 派生状态失败: %v", row.ID, err)
		localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
		return
	}
	if v.Latest == nil {
		localReply(c, http.StatusNotFound, gin.H{"error": "未找到该卡密的兑换记录"})
		return
	}
	alertLocalCDKExhausted(row, v)
	public := localOrderPublic(v, row)
	resp := gin.H{
		"provider": row.Provider,
		"status":   public["status"],
		"stage":    public["stage"],
		"message":  public["message"],
		"order":    public,
		"events":   []any{},
	}
	for k, val := range extra {
		resp[k] = val
	}
	c.Header("Cache-Control", "no-store")
	localReply(c, http.StatusOK, resp)
}

// localCDKResult 按令牌查结果。
func localCDKResult(c *gin.Context, cs provider.CardSupply, token string) {
	if !localRateCheck(c, localRateQuery) {
		return
	}
	row, ok := localCDKFromBody(c, cs, map[string]any{"redemption_token": token})
	if !ok {
		return
	}
	writeLocalResult(c, row, gin.H{"redemption_token": token})
}

// localCDKResultByCode 按码查结果，顺带把令牌给前端恢复轮询。
func localCDKResultByCode(c *gin.Context, cs provider.CardSupply, code string) {
	if !localRateCheck(c, localRateQuery) {
		return
	}
	row, ok := localCDKFromBody(c, cs, map[string]any{"code": code})
	if !ok {
		return
	}
	norm := provider.NormalizeCode(code)
	writeLocalResult(c, row, gin.H{
		"cdk_code":            norm,
		"redemption_token":    provider.TokenFor(cs, norm),
		"has_session_binding": false,
	})
}

// localCDKLookup 是卡密查询页的本地分支。库里码行的服务商对不上时按未找到处理。
func localCDKLookup(ctx context.Context, cs provider.CardSupply, code string) cdkLookupResult {
	norm := provider.NormalizeCode(code)
	resp := cdkLookupResult{CDKCode: code, Status: "unknown", Message: "未找到该卡密记录"}
	row, err := db.GetLocalCDKByCode(ctx, norm)
	if err != nil {
		if !errors.Is(err, db.ErrLocalCDKNotFound) {
			log.Printf("[local-cdk] 查询码失败: %v", err)
		}
		return resp
	}
	if row.Provider != string(cs.Name()) {
		return resp
	}
	v, err := db.LocalCDKViewOf(ctx, row)
	if err != nil {
		log.Printf("[local-cdk] 码 %d 派生状态失败: %v", row.ID, err)
		return resp
	}
	resp.Plan = row.PlanKey
	_, _, resp.Message = localPublicState(v)
	if v.Latest != nil {
		resp.AccountEmail = maskEmailForAdmin(v.Latest.AccountEmail)
	}
	switch v.Status {
	case db.LocalCDKConsumed:
		resp.Status, resp.Used = "used", true
		resp.Message = "卡密使用成功"
		if v.Latest != nil && v.Latest.FinishedAt != nil {
			at := time.Unix(*v.Latest.FinishedAt, 0).UTC().Format(time.RFC3339)
			resp.UsedAt = &at
		}
	case db.LocalCDKReserved:
		resp.Status = "processing"
	case db.LocalCDKFailed:
		resp.Status = "failed"
	case db.LocalCDKDisabled:
		resp.Status = "disabled"
	default:
		resp.Status = "unused"
		if v.Latest == nil {
			resp.Message = "卡密未使用"
		} else {
			resp.CanResubmit = true
		}
	}
	return resp
}

// localCDKExchange 是代理换码的本地分支，调用前已通过限流和密码校验。新码是同一家服务商的码。
// 只有确认没点过支付的失败才能换；被拒、待人工、在途、从未兑换一律拒绝。新码继承注资后失败的次数，换码不能绕过上限。
func localCDKExchange(c *gin.Context, cs provider.CardSupply, code, ip string) {
	ctx := c.Request.Context()
	norm := provider.NormalizeCode(code)
	row, err := db.GetLocalCDKByCode(ctx, norm)
	if err == nil && row.Provider != string(cs.Name()) {
		err = db.ErrLocalCDKNotFound
	}
	if errors.Is(err, db.ErrLocalCDKNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "本站未找到该完整卡密（仅支持本站发过的码）"})
		return
	}
	if err != nil {
		log.Printf("[local-cdk] 查询码失败: %v", err)
		localReply(c, http.StatusInternalServerError, gin.H{"error": "换码暂时不可用，请稍后再试"})
		return
	}
	codeHash := db.HashCDKCode(norm)
	if st := db.AgentCDKExchangeState(codeHash); st != "" {
		agentExchangeConflict(c, st)
		return
	}
	unlock, ok := lockLocalCDK(row.ID)
	if !ok {
		localReply(c, http.StatusConflict, gin.H{"error": localCDKBusyText})
		return
	}
	defer unlock()
	if row, err = db.GetLocalCDK(ctx, row.ID); err != nil { // 拿到锁之后重读，等锁之前的那份可能已经过期
		log.Printf("[local-cdk] 码重读失败: %v", err)
		localReply(c, http.StatusInternalServerError, gin.H{"error": "换码暂时不可用，请稍后再试"})
		return
	}
	v, err := db.LocalCDKViewOf(ctx, row)
	if err != nil {
		log.Printf("[local-cdk] 码 %d 派生状态失败: %v", row.ID, err)
		localReply(c, http.StatusInternalServerError, gin.H{"error": "换码暂时不可用，请稍后再试"})
		return
	}
	if v.Status != db.LocalCDKUnused {
		localReply(c, http.StatusBadRequest, gin.H{"error": "不符合换码条件（需充值失败且未发起支付）", "status": v.Status})
		return
	}
	// 只看这张码自己的单：继承来的次数不算它失败过，从未兑换就换等于白拿新码
	if v.Latest == nil {
		localReply(c, http.StatusBadRequest, gin.H{"error": "该卡密尚无兑换失败记录，不能换新码", "hint": "仅限充值失败且未扣款的卡密"})
		return
	}
	// 新码也是这家新发的码，硬门槛照样要过（按这张码的种类：订阅开通码、点数码分别计算）
	switch passed, err := db.HasRealSucceededPayOrder(ctx, row.Provider, payengine.PlanKind(row.PlanKey)); {
	case err != nil:
		log.Printf("[local-cdk] 查询真实付款记录失败: %v", err)
		localReply(c, http.StatusInternalServerError, gin.H{"error": "换码暂时不可用，请稍后再试"})
		return
	case !passed:
		localReply(c, http.StatusServiceUnavailable, gin.H{"error": localCDKUnavailableText})
		return
	}
	fresh, err := provider.GenerateCode(cs)
	if err != nil {
		log.Printf("[local-cdk] 生成新码失败: %v", err)
		localReply(c, http.StatusInternalServerError, gin.H{"error": "换码暂时不可用，请稍后再试"})
		return
	}
	// OldID 必须是 0：这张记录的 old_upstream_id 指卡台的码 ID，人工定论时会拿它去禁用卡台的码，本地 ID 不能填进去
	exID, err := db.ReserveAgentCDKExchange(db.AgentExchangeReservation{
		OldHash: codeHash, OldID: 0, OldPrefix: row.CodePrefix, Plan: row.PlanKey,
		OrderStatus: "not_submitted", IP: ip, PasswordFP: agentSwapPasswordFP(),
	}, agentSwapDailyLimit(), time.Now())
	switch {
	case errors.Is(err, db.ErrAgentExchangeExists):
		agentExchangeConflict(c, db.AgentCDKExchangeState(codeHash))
		return
	case errors.Is(err, db.ErrAgentExchangeDailyLimit):
		limit := agentSwapDailyLimit()
		opsAlertDaily("agent-swap-daily-limit", fmt.Sprintf("代理换码已达今日上限（%d 张），后续请求已拒绝。如果不是正常业务量，请检查换码密码是否泄露。", limit), time.Now())
		localReply(c, http.StatusTooManyRequests, gin.H{"error": fmt.Sprintf("代理换码已达今日上限（%d 张），请联系管理员", limit)})
		return
	case err != nil:
		log.Printf("[local-cdk] 码 %d 换码占位失败: %v", row.ID, err)
		localReply(c, http.StatusInternalServerError, gin.H{"error": "换码暂时不可用，请稍后再试"})
		return
	}
	newID, err := db.ExchangeLocalCDK(context.WithoutCancel(ctx), row.ID, db.NewLocalCDK{
		Provider: row.Provider, CodeHash: fresh.CodeHash, TokenHash: fresh.TokenHash, CodePrefix: fresh.Prefix,
		Product: row.Product, PlanKey: row.PlanKey, Region: row.Region, IssuedBy: "agent-swap", Note: row.Note,
		ExchangedFrom: &row.ID, AttemptsCarried: v.FundedFailures,
	}, time.Now())
	if err != nil {
		// 事务回滚，什么都没发生：本地发码不存在结果不明，放掉占位就好
		if rerr := db.ReleaseAgentCDKExchange(exID); rerr != nil {
			log.Printf("[local-cdk] 换码记录 %d 释放占位失败: %v", exID, rerr)
		}
		if errors.Is(err, db.ErrLocalCDKNotExchangeable) {
			localReply(c, http.StatusBadRequest, gin.H{"error": "不符合换码条件（需充值失败且未发起支付）"})
			return
		}
		log.Printf("[local-cdk] 码 %d 换码失败: %v", row.ID, err)
		localReply(c, http.StatusInternalServerError, gin.H{"error": "换码暂时不可用，请稍后再试"})
		return
	}
	if err := db.FinishAgentCDKExchange(exID, newID, fresh.Prefix); err != nil {
		log.Printf("[local-cdk] 换码记录 %d 收尾失败 new=%d: %v", exID, newID, err)
		opsAlert(fmt.Sprintf("%s 换码已发出新码但写库失败：记录 %d，旧码 %d，新码 %d。占位已保留，请到后台定论。", cs.DisplayName(), exID, row.ID, newID))
	} else if err := db.CompleteAgentCDKExchangeDisable(exID); err != nil {
		// 旧码和新码在 ExchangeLocalCDK 的事务里一起换好，本地码不存在两张都能兑的时刻。这里只是记账，失败了维护循环会补上
		log.Printf("[local-cdk] 换码记录 %d 转完成失败: %v", exID, err)
	}
	db.WriteAudit("agent-swap", "local_cdk_exchange",
		fmt.Sprintf("old=%d new=%d plan=%s region=%s carried=%d", row.ID, newID, row.PlanKey, row.Region, v.FundedFailures), ip)
	c.Header("Cache-Control", "no-store")
	localReply(c, http.StatusOK, gin.H{
		"ok":              true,
		"provider":        row.Provider,
		"plan":            row.PlanKey,
		"old_cdk_id":      row.ID,
		"old_code_prefix": row.CodePrefix,
		"new_cdk_id":      newID,
		"new_code":        fresh.Code,
		"new_code_prefix": fresh.Prefix,
		"payment_country": row.Region,
		"order_status":    "not_submitted",
		"message":         "已换发全新卡密，请妥善保存（仅显示一次）",
	})
}
