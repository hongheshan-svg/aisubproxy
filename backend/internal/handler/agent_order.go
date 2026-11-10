package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

// 代理商的商品、下单、订单和订单导出（/api/v1/agent），字段名以 .superpowers/sdd/2026-11-04-agent-core/api-contract.md 为准。
// 完整的码只出现在下单、重放和导出的响应里（都带 no-store），不进日志、审计、告警、错误信息。

const (
	agentOrderRateMax    = 30
	agentOrderRateWindow = time.Minute

	agentPageSizeDefault = 50
	agentPageSizeMax     = 200
	agentPageMax         = 1_000_000 // 页码上限，防止 (page-1)*page_size 溢出

	agentUnsealAlertIDs = 20 // 告警里最多列出多少个码 ID

	agentMsgOrderMaint   = "系统维护中，暂不能下单"
	agentMsgExportMaint  = "系统维护中，暂不能导出码"
	agentMsgOrderBusy    = "系统繁忙，请稍后重试"
	agentMsgOrderRate    = "操作太频繁，请稍后再试"
	agentMsgUnavailable  = "该商品暂不可售"
	agentMsgNotReady     = "该商品暂时无法出码，请稍后再试"
	agentMsgReqConflict  = "请求号已用于另一笔订单"
	agentMsgPriceChanged = "价格已变化，请刷新后重新下单"
	agentMsgBadPrice     = "单价参数不正确，请刷新后重新下单"
)

// ---- 分页 ----

// agentPaging 是代理商相关列表统一的分页解析（裁定 P22）：page 默认 1、小于 1 取 1；
// page_size 默认 50、小于 1 取 1、大于 200 取 200；不是整数时按默认值。返回 LIMIT 和 OFFSET。
func agentPaging(c *gin.Context) (limit, offset int) {
	page := agentQueryInt(c, "page", 1)
	size := agentQueryInt(c, "page_size", agentPageSizeDefault)
	if page < 1 {
		page = 1
	}
	if page > agentPageMax {
		page = agentPageMax
	}
	if size < 1 {
		size = 1
	}
	if size > agentPageSizeMax {
		size = agentPageSizeMax
	}
	return size, (page - 1) * size
}

func agentQueryInt(c *gin.Context, key string, def int) int {
	v, err := strconv.Atoi(strings.TrimSpace(c.Query(key)))
	if err != nil {
		return def
	}
	return v
}

// ---- 下单限流 ----

type agentOrderWindow struct {
	start time.Time
	n     int
}

var (
	agentOrderRateMu sync.Mutex
	agentOrderRate   = map[int64]*agentOrderWindow{}
)

// agentOrderAllowed 下单限流：每个代理商每分钟最多 30 次，进程内固定窗口（从这个窗口的第一次请求算起）。
// 计数在处理订单之前，被拒的请求确定没有建单，前端可以保留原请求号重试。
func agentOrderAllowed(agentID int64, now time.Time) bool {
	agentOrderRateMu.Lock()
	defer agentOrderRateMu.Unlock()
	for k, w := range agentOrderRate { // 代理商不多，顺手清掉过期的窗口
		if now.Sub(w.start) >= agentOrderRateWindow || now.Before(w.start) {
			delete(agentOrderRate, k)
		}
	}
	w := agentOrderRate[agentID]
	if w == nil {
		w = &agentOrderWindow{start: now}
		agentOrderRate[agentID] = w
	}
	if w.n >= agentOrderRateMax {
		return false
	}
	w.n++
	return true
}

// resetAgentOrderRate 清空下单限流计数，给测试用。
func resetAgentOrderRate() {
	agentOrderRateMu.Lock()
	agentOrderRate = map[int64]*agentOrderWindow{}
	agentOrderRateMu.Unlock()
}

// ---- 取回完整码 ----

// exportedCode 是交给代理商的一张完整码。解不开的码 Code 为空串、Unavailable 为 true。
type exportedCode struct {
	ID          int64  `json:"id"`
	Code        string `json:"code"`
	Unavailable bool   `json:"unavailable,omitempty"`
	// Status 和 ReplacedBy 只在下单结果、同号重放、订单导出里有值：沿用「我的码」的 5 种状态；换码换出的新码 ID，没有为 null。
	Status     string `json:"status,omitempty"`
	ReplacedBy *int64 `json:"replaced_by"`
}

// agentCodesNoKeyError 主密钥不可用，一张码也解不开：调用方返回 503 maintenance，而不是把每张码都标成取不回。
// kind 是 secretErrKind 给出的错误类型。
type agentCodesNoKeyError struct{ kind string }

func (e *agentCodesNoKeyError) Error() string { return "主密钥不可用: " + e.kind }

// openAgentCodes 取这个代理商名下这些码的完整码。只读 LocalCDKSealedForAgent（条件固定带 agent_id，别人的 ID 不会返回），
// 按 id 升序逐张解密并核对哈希，失败的标 Unavailable，其余照常；有失败时告警一次，告警只写码的 ID。
// 读库失败原样返回错误（调用方 503 busy）；主密钥不可用返回 *agentCodesNoKeyError（调用方 503 maintenance）。
func openAgentCodes(ctx context.Context, agentID int64, ids []int64) ([]exportedCode, error) {
	rows, err := db.LocalCDKSealedForAgent(ctx, agentID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]exportedCode, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	key, err := secretbox.LoadKey()
	if err != nil {
		return nil, &agentCodesNoKeyError{kind: secretErrKind(err)}
	}
	var bad []int64
	for _, r := range rows {
		code, why := openAgentCode(key, r)
		if why != "" {
			log.Printf("[agent-order] 代理商 %d 的码 %d 解不开，已标成无法取回: %s", agentID, r.ID, why)
			bad = append(bad, r.ID)
			out = append(out, exportedCode{ID: r.ID, Unavailable: true})
			continue
		}
		out = append(out, exportedCode{ID: r.ID, Code: code})
	}
	if len(bad) > 0 {
		opsAlertDaily("agent-code-unseal", fmt.Sprintf("代理商 %d 有 %d 张码解不开，取码时已标成「无法取回」：码 ID %s。请检查 APP_MASTER_KEY 是否被更换、库里的密文是否被改动。",
			agentID, len(bad), agentIDList(bad)), agentNow())
	}
	return out, nil
}

// openAgentCode 解开一张码并核对哈希。失败时 why 是能写进日志的原因（不含码和密文）。
func openAgentCode(key []byte, r db.SealedLocalCDK) (code, why string) {
	if r.Sealed == "" {
		return "", "没有密文"
	}
	plain, err := secretbox.Open(key, localCDKCodeAAD(r.CodeHash), r.Sealed)
	if err != nil {
		return "", secretErrKind(err)
	}
	if db.HashCDKCode(plain) != r.CodeHash {
		return "", "解出的码与哈希不符"
	}
	return plain, ""
}

// agentIDList 把 ID 列成「1、2、3」，太多时只列前面的。
func agentIDList(ids []int64) string {
	n := len(ids)
	if n > agentUnsealAlertIDs {
		ids = ids[:agentUnsealAlertIDs]
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	s := strings.Join(parts, "、")
	if n > len(ids) {
		s += fmt.Sprintf(" 等 %d 张", n)
	}
	return s
}

// ---- 错误响应 ----

// agentOrderBusy 读写库失败：只记错误（库错误里没有码），返回 503 busy。用同一个请求号重试是安全的。
func agentOrderBusy(c *gin.Context, op string, err error) {
	log.Printf("[agent-order] 代理商 %d %s失败: %v", agentID(c), op, err)
	agentError(c, http.StatusServiceUnavailable, "busy", agentMsgOrderBusy)
}

// agentOrderNoKey 主密钥不可用：每天告警一次，返回 503 maintenance。
// why 只写错误类型。
func agentOrderNoKey(c *gin.Context, why, msg string) {
	log.Printf("[agent-order] 主密钥不可用: %s", why)
	opsAlertDaily("agent-order-nokey", "APP_MASTER_KEY 缺失或不合法：代理商暂时不能下单和取码（接口返回「系统维护中」）。请配置回原来的主密钥，换成新密钥的话已加密的码和两步验证密钥都解不开。", agentNow())
	agentError(c, http.StatusServiceUnavailable, "maintenance", msg)
}

func agentOrderInvalid(c *gin.Context, msg string) {
	agentError(c, http.StatusBadRequest, "invalid_request", msg)
}

// agentOrderFail 把下单核心的错误写成响应（状态码、错误编号、提示和以前逐字一致）。
// 除了成功和重放，这些响应都没有扣款、没有建单：前端遇到明确失败可以换新请求号重下。
func agentOrderFail(c *gin.Context, err error) {
	var (
		noKey *agentOrderNoKeyError
		busy  *agentOrderBusyError
		pc    *priceChangedError
		short *db.InsufficientBalanceError
	)
	switch {
	case errors.As(err, &noKey):
		agentOrderNoKey(c, noKey.Kind, agentMsgOrderMaint)
	case errors.As(err, &busy):
		agentOrderBusy(c, busy.Op, busy.Err)
	case errors.Is(err, errAgentOrderSeal):
		agentError(c, http.StatusServiceUnavailable, "maintenance", agentMsgOrderMaint)
	case errors.Is(err, errAgentOrderInternal):
		agentError(c, http.StatusInternalServerError, "internal", "系统出错，请稍后再试")
	case errors.Is(err, errProductUnavailable):
		agentError(c, http.StatusNotFound, "product_unavailable", agentMsgUnavailable)
	case errors.As(err, &pc):
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "price_changed", "message": agentMsgPriceChanged, "current_unit_price_cents": pc.CurrentCents})
	case errors.Is(err, errProductNotReady):
		agentError(c, http.StatusConflict, "product_not_ready", agentMsgNotReady)
	case errors.Is(err, errRequestConflict):
		agentError(c, http.StatusConflict, "request_no_conflict", agentMsgReqConflict)
	case errors.As(err, &short):
		gap := short.NeedCents - short.BalanceCents
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{
			"error": "insufficient_balance", "message": "余额不足，还差 $" + agent.FormatCents(gap), "shortfall_cents": gap,
		})
	case errors.Is(err, db.ErrAgentDisabled), errors.Is(err, db.ErrAgentNotFound):
		agentError(c, http.StatusUnauthorized, "unauthorized", "未登录或登录已过期")
	default: // 核心只返回上面这些错误，这里兜底按下单失败处理
		agentOrderBusy(c, "下单", err)
	}
}

// agentOrderCodes 取一笔订单当时发出的码并解密（订单导出用）。出错时已写好响应，返回 false。
func agentOrderCodes(c *gin.Context, o db.AgentOrder, maintMsg string) ([]exportedCode, bool) {
	codes, err := loadAgentOrderCodes(c.Request.Context(), agentID(c), o)
	var (
		noKey *agentCodesNoKeyError
		busy  *agentOrderBusyError
	)
	switch {
	case errors.As(err, &noKey):
		agentOrderNoKey(c, noKey.kind, maintMsg)
	case errors.As(err, &busy):
		agentOrderBusy(c, busy.Op, busy.Err)
	case err != nil: // loadAgentOrderCodes 只返回上面两种错误，这里兜底
		agentOrderBusy(c, "读取订单的码", err)
	default:
		return codes, true
	}
	return nil, false
}

// ---- 商品 ----

// agentProductReady 这个商品现在能不能出码：服务商是供卡型、支持这个套餐，并且就绪检查（含发码硬门槛）全部通过。
// 和后台发码共用 checkCardSupply，但按公开接口检查（裁定 P25）：代理商在外部，商品列表和下单都不能每次实时同步
// MarsWallex 主钱包，30 秒内只同步一次、不跟请求的 ctx，和客户兑换码一样。
func agentProductReady(ctx context.Context, p db.AgentProduct) (provider.CardSupply, bool) {
	pr, ok := provider.Get(provider.Name(p.Provider))
	if !ok {
		return nil, false
	}
	cs, ok := provider.AsCardSupply(pr)
	if !ok || !cs.Supports(p.Product, p.PlanKey) {
		return nil, false
	}
	return cs, checkCardSupply(ctx, cs, p.PlanKey, p.Region, true).Readiness.OK
}

// AgentListProducts GET /products：这个代理商能买的商品（已上架、取得到价格），每项带上现在能否出码。
func AgentListProducts(c *gin.Context) {
	ctx := c.Request.Context()
	list, err := db.ListAgentPurchasable(ctx, agentID(c))
	if err != nil {
		agentOrderBusy(c, "列出商品", err)
		return
	}
	ready := map[string]bool{} // 同一个服务商 × 套餐 × 地区只查一次
	items := make([]gin.H, 0, len(list))
	for _, p := range list {
		k := p.Provider + "|" + p.Product + "|" + p.PlanKey + "|" + p.Region
		ok, seen := ready[k]
		if !seen {
			_, ok = agentProductReady(ctx, p.AgentProduct)
			ready[k] = ok
		}
		items = append(items, gin.H{
			"product_code": p.Code, "name": p.Name, "plan": p.PlanKey, "region": p.Region,
			"unit_price_cents": p.PriceCents, "ready": ok,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// ---- 下单 ----

type agentOrderRequest struct {
	ProductCode            string `json:"product_code"`
	Quantity               int    `json:"quantity"`
	Note                   string `json:"note"`
	RequestNo              string `json:"request_no"`
	ExpectedUnitPriceCents int64  `json:"expected_unit_price_cents"`
}

// agentOrderBind 读并校验下单参数（第 2 步）。不合法时已写好 400，返回 false。
func agentOrderBind(c *gin.Context) (agentOrderRequest, bool) {
	var req agentOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		var te *json.UnmarshalTypeError
		switch {
		case errors.As(err, &te) && te.Field == "quantity": // 例如 1.5、"1"、超出整数范围
			agentOrderInvalid(c, fmt.Sprintf("数量需为 1–%d 的整数", provider.MaxIssueCount))
		case errors.As(err, &te) && te.Field == "expected_unit_price_cents":
			agentOrderInvalid(c, agentMsgBadPrice)
		default:
			agentOrderInvalid(c, "请求内容格式不对")
		}
		return req, false
	}
	req.ProductCode = strings.TrimSpace(req.ProductCode)
	req.Note = strings.TrimSpace(req.Note)
	switch {
	case req.ProductCode == "":
		agentOrderInvalid(c, "请选择商品")
	case req.Quantity < 1 || req.Quantity > provider.MaxIssueCount:
		agentOrderInvalid(c, fmt.Sprintf("数量需为 1–%d 的整数", provider.MaxIssueCount))
	case utf8.RuneCountInString(req.Note) > localCDKNoteMax:
		agentOrderInvalid(c, fmt.Sprintf("备注不能超过 %d 个字", localCDKNoteMax))
	case !agent.ValidRequestNo(req.RequestNo):
		agentOrderInvalid(c, "请求号需为 1–64 位，只能用字母、数字和 _ . : -")
	case strings.HasPrefix(req.RequestNo, "acg:"): // 异次元对接的订单用 "acg:" + 对方单号，后台下单不能占用
		agentOrderInvalid(c, "请求号不能以 acg: 开头")
	case req.ExpectedUnitPriceCents <= 0:
		agentOrderInvalid(c, agentMsgBadPrice)
	default:
		return req, true
	}
	return req, false
}

// AgentPlaceOrder POST /orders。处理顺序：
//  1. 限流；2. 校验参数；3–7. 下单核心 placeAgentOrder（主密钥、查请求号重放、商品检查、生成码、建单扣款）；
//  8. 返回码，写审计。
//
// 1–6 步的拒绝都在写库之前，第 7 步失败整体回滚，所以除了成功和重放，任何响应都没有扣款、没有建单：
// 前端遇到明确失败可以换新请求号重下，遇到限流可以保留原请求号重试。
func AgentPlaceOrder(c *gin.Context) {
	aid, user := agentID(c), agentUsername(c)
	if !agentOrderAllowed(aid, agentNow()) {
		agentError(c, http.StatusTooManyRequests, "rate_limited", agentMsgOrderRate)
		return
	}
	req, ok := agentOrderBind(c)
	if !ok {
		return
	}
	out, err := placeAgentOrder(c.Request.Context(), agentOrderInput{
		AgentID: aid, Username: user, ProductCode: req.ProductCode, Quantity: req.Quantity, RequestNo: req.RequestNo,
		Note: req.Note, ExpectedUnitPriceCents: req.ExpectedUnitPriceCents, Source: db.AgentOrderSourceWeb,
	})
	if err != nil {
		agentOrderFail(c, err)
		return
	}
	o := out.Order
	if out.Replayed { // 重放又交出了一遍完整码，按导出记审计
		auditAgent(c, "agent_codes_export", fmt.Sprintf("order=%d count=%d replay=true", o.ID, len(out.Codes)))
	} else {
		auditAgent(c, "agent_order", fmt.Sprintf("product=%s quantity=%d total_cents=%d order=%d", o.ProductCode, o.Quantity, o.TotalCents, o.ID))
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"order": agentOrderView(o), "codes": out.Codes, "replayed": out.Replayed})
}

// ---- 订单 ----

// AgentListOrders GET /orders：自己的订单，id 降序，不含码。
func AgentListOrders(c *gin.Context) {
	limit, offset := agentPaging(c)
	list, total, err := db.ListAgentOrders(c.Request.Context(), agentID(c), limit, offset)
	if err != nil {
		agentOrderBusy(c, "列出订单", err)
		return
	}
	items := make([]gin.H, 0, len(list))
	for _, o := range list {
		items = append(items, agentOrderView(o))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total})
}

// agentOrderFromPath 取路径里的订单，只认自己的：别人的、不存在的、ID 不合法的一律 404。出错时已写好响应。
func agentOrderFromPath(c *gin.Context) (db.AgentOrder, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		agentError(c, http.StatusNotFound, "not_found", "订单不存在")
		return db.AgentOrder{}, false
	}
	o, err := db.GetAgentOrder(c.Request.Context(), agentID(c), id)
	if errors.Is(err, sql.ErrNoRows) {
		agentError(c, http.StatusNotFound, "not_found", "订单不存在")
		return db.AgentOrder{}, false
	}
	if err != nil {
		agentOrderBusy(c, "读取订单", err)
		return db.AgentOrder{}, false
	}
	return o, true
}

// AgentGetOrder GET /orders/:id：订单详情，不含码。
func AgentGetOrder(c *gin.Context) {
	o, ok := agentOrderFromPath(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"order": agentOrderView(o)})
}

// AgentExportOrder POST /orders/:id/export：重新导出这笔订单当时发出的码（不含换码换出的新码）。
func AgentExportOrder(c *gin.Context) {
	o, ok := agentOrderFromPath(c)
	if !ok {
		return
	}
	codes, ok := agentOrderCodes(c, o, agentMsgExportMaint)
	if !ok {
		return
	}
	auditAgent(c, "agent_codes_export", fmt.Sprintf("order=%d count=%d", o.ID, len(codes)))
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"codes": codes})
}
