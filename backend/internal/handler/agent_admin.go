package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 管理员管理代理商的接口（/api/v1/admin），字段名以 .superpowers/sdd/2026-11-04-agent-core/api-contract.md 为准。

const (
	agentDisplayNameMax = 100
	agentNoteMax        = 1000
	agentAdjustMaxCents = int64(100_000_000_000) // 单次调账上限 $1,000,000,000，防止余额溢出
	agentReconcileKey   = "agent_reconcile_last"
)

// adminInvalid 400 invalid_request。
func adminInvalid(c *gin.Context, msg string) {
	agentError(c, http.StatusBadRequest, "invalid_request", msg)
}

func adminNotFound(c *gin.Context, what string) {
	agentError(c, http.StatusNotFound, "not_found", what+"不存在")
}

// adminInternal 意外错误：详情只进日志（里面不会有密码、码），响应统一 500。
func adminInternal(c *gin.Context, op string, err error) {
	log.Printf("[agent-admin] %s 失败: %v", op, err)
	agentError(c, http.StatusInternalServerError, "internal", "服务异常，请稍后再试")
}

func adminBind(c *gin.Context, v any) bool {
	if err := c.ShouldBindJSON(v); err != nil {
		adminInvalid(c, "请求内容格式不对")
		return false
	}
	return true
}

func adminPathID(c *gin.Context, what string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		adminNotFound(c, what)
		return 0, false
	}
	return id, true
}

func adminNoStore(c *gin.Context) { c.Header("Cache-Control", "no-store") }

// adminAgentFromPath 取路径里的代理商；不存在 404，读库出错 500，都已写好响应。
func adminAgentFromPath(c *gin.Context) (db.Agent, bool) {
	id, ok := adminPathID(c, "代理商")
	if !ok {
		return db.Agent{}, false
	}
	a, err := db.GetAgent(c.Request.Context(), id)
	if errors.Is(err, db.ErrAgentNotFound) {
		adminNotFound(c, "代理商")
		return db.Agent{}, false
	}
	if err != nil {
		adminInternal(c, "读取代理商", err)
		return db.Agent{}, false
	}
	return a, true
}

// agentAdminView 对应契约里的 agentAdmin。
func agentAdminView(a db.Agent, codeCount int) gin.H {
	var last any
	if a.LastLoginAt != nil {
		last = *a.LastLoginAt
	}
	return gin.H{
		"id": a.ID, "username": a.Username, "display_name": a.DisplayName, "tier_id": a.TierID, "tier_name": a.TierName,
		"balance_cents": a.BalanceCents, "status": a.Status, "must_change_password": a.MustChangePassword,
		"totp_bound": a.TOTPBound, "note": a.Note, "last_login_at": last, "created_at": a.CreatedAt, "code_count": codeCount,
	}
}

// adminAgentResponse 重新读一遍代理商再响应 {agent}。
func adminAgentResponse(c *gin.Context, id int64, extra gin.H) {
	a, err := db.GetAgent(c.Request.Context(), id)
	if err != nil {
		adminInternal(c, "读取代理商", err)
		return
	}
	counts, err := db.CountAgentCodes(c.Request.Context())
	if err != nil {
		adminInternal(c, "统计码数", err)
		return
	}
	out := gin.H{"agent": agentAdminView(a, counts[a.ID])}
	for k, v := range extra {
		out[k] = v
	}
	c.JSON(http.StatusOK, out)
}

// AdminListAgents GET /agents
func AdminListAgents(c *gin.Context) {
	list, err := db.ListAgents(c.Request.Context())
	if err != nil {
		adminInternal(c, "列出代理商", err)
		return
	}
	counts, err := db.CountAgentCodes(c.Request.Context())
	if err != nil {
		adminInternal(c, "统计码数", err)
		return
	}
	items := make([]gin.H, 0, len(list))
	for _, a := range list {
		items = append(items, agentAdminView(a, counts[a.ID]))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// validAgentText 校验显示名和备注的长度。
func validAgentText(c *gin.Context, displayName, note string) bool {
	if utf8.RuneCountInString(displayName) > agentDisplayNameMax {
		adminInvalid(c, fmt.Sprintf("显示名不能超过 %d 个字", agentDisplayNameMax))
		return false
	}
	if utf8.RuneCountInString(note) > agentNoteMax {
		adminInvalid(c, fmt.Sprintf("备注不能超过 %d 个字", agentNoteMax))
		return false
	}
	return true
}

// AdminCreateAgent POST /agents：登录名由这里校验（数据层不校验）；响应带一次初始密码，审计里不写密码。
func AdminCreateAgent(c *gin.Context) {
	var req struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		TierID      int64  `json:"tier_id"`
		Note        string `json:"note"`
	}
	if !adminBind(c, &req) {
		return
	}
	username, ok := agent.NormalizeUsername(req.Username)
	if !ok {
		adminInvalid(c, "登录名需为 3–32 位，只能用字母、数字、下划线和短横线")
		return
	}
	display := strings.TrimSpace(req.DisplayName)
	if display == "" {
		display = username
	}
	note := strings.TrimSpace(req.Note)
	if !validAgentText(c, display, note) {
		return
	}
	pw, err := agent.GeneratePassword()
	if err != nil {
		adminInternal(c, "生成初始密码", err)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		adminInternal(c, "加密初始密码", err)
		return
	}
	id, err := db.CreateAgent(c.Request.Context(), db.NewAgent{
		Username: username, DisplayName: display, PasswordHash: string(hash), TierID: req.TierID, Note: note,
	}, time.Now())
	switch {
	case errors.Is(err, db.ErrAgentUsernameTaken):
		agentError(c, http.StatusConflict, "username_taken", "登录名已被使用")
		return
	case errors.Is(err, db.ErrAgentTierNotFound):
		adminInvalid(c, "等级不存在")
		return
	case err != nil:
		adminInternal(c, "建号", err)
		return
	}
	auditAdmin(c, "agent_create", fmt.Sprintf("id=%d username=%s tier_id=%d", id, username, req.TierID))
	adminNoStore(c)
	adminAgentResponse(c, id, gin.H{"password": pw})
}

// AdminGetAgent GET /agents/:id
func AdminGetAgent(c *gin.Context) {
	a, ok := adminAgentFromPath(c)
	if !ok {
		return
	}
	adminAgentResponse(c, a.ID, nil)
}

// AdminUpdateAgent PUT /agents/:id
func AdminUpdateAgent(c *gin.Context) {
	old, ok := adminAgentFromPath(c)
	if !ok {
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
		TierID      int64  `json:"tier_id"`
		Note        string `json:"note"`
	}
	if !adminBind(c, &req) {
		return
	}
	display := strings.TrimSpace(req.DisplayName)
	if display == "" {
		display = old.Username
	}
	note := strings.TrimSpace(req.Note)
	if !validAgentText(c, display, note) {
		return
	}
	err := db.UpdateAgentProfile(c.Request.Context(), old.ID, display, req.TierID, note, time.Now())
	switch {
	case errors.Is(err, db.ErrAgentTierNotFound):
		adminInvalid(c, "等级不存在")
		return
	case errors.Is(err, db.ErrAgentNotFound):
		adminNotFound(c, "代理商")
		return
	case err != nil:
		adminInternal(c, "改资料", err)
		return
	}
	auditAdmin(c, "agent_update", fmt.Sprintf("id=%d username=%s tier_id=%d->%d", old.ID, old.Username, old.TierID, req.TierID))
	adminAgentResponse(c, old.ID, nil)
}

func adminSetAgentStatus(c *gin.Context, status, action string) {
	a, ok := adminAgentFromPath(c)
	if !ok {
		return
	}
	// 停用时数据层会让会话版本加 1，已登录的会话立刻失效
	err := db.SetAgentStatus(c.Request.Context(), a.ID, status, time.Now())
	if errors.Is(err, db.ErrAgentNotFound) {
		adminNotFound(c, "代理商")
		return
	}
	if err != nil {
		adminInternal(c, action, err)
		return
	}
	auditAdmin(c, action, fmt.Sprintf("id=%d username=%s", a.ID, a.Username))
	adminAgentResponse(c, a.ID, nil)
}

// AdminDisableAgent POST /agents/:id/disable
func AdminDisableAgent(c *gin.Context) { adminSetAgentStatus(c, db.AgentDisabled, "agent_disable") }

// AdminEnableAgent POST /agents/:id/enable
func AdminEnableAgent(c *gin.Context) { adminSetAgentStatus(c, db.AgentActive, "agent_enable") }

// AdminResetAgentPassword POST /agents/:id/reset-password：生成新的临时密码，下次登录必须改；旧会话失效。
func AdminResetAgentPassword(c *gin.Context) {
	a, ok := adminAgentFromPath(c)
	if !ok {
		return
	}
	pw, err := agent.GeneratePassword()
	if err != nil {
		adminInternal(c, "生成临时密码", err)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		adminInternal(c, "加密临时密码", err)
		return
	}
	if _, err := db.SetAgentPassword(c.Request.Context(), a.ID, string(hash), true, time.Now()); err != nil {
		if errors.Is(err, db.ErrAgentNotFound) {
			adminNotFound(c, "代理商")
			return
		}
		adminInternal(c, "重置密码", err)
		return
	}
	auditAdmin(c, "agent_reset_password", fmt.Sprintf("id=%d username=%s", a.ID, a.Username))
	adminNoStore(c)
	c.JSON(http.StatusOK, gin.H{"password": pw})
}

// AdminResetAgentTOTP POST /agents/:id/reset-totp
func AdminResetAgentTOTP(c *gin.Context) {
	a, ok := adminAgentFromPath(c)
	if !ok {
		return
	}
	if err := db.ResetAgentTOTP(c.Request.Context(), a.ID, time.Now()); err != nil {
		if errors.Is(err, db.ErrAgentNotFound) {
			adminNotFound(c, "代理商")
			return
		}
		adminInternal(c, "重置两步验证", err)
		return
	}
	auditAdmin(c, "agent_reset_totp", fmt.Sprintf("id=%d username=%s", a.ID, a.Username))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// adminLedgerRowView 对应契约里的 adminLedgerRow（= ledgerRow + operator、ext_ref）。
func adminLedgerRowView(e db.LedgerEntry) gin.H {
	var orderID int64
	if e.OrderID != nil {
		orderID = *e.OrderID
	}
	return gin.H{
		"id": e.ID, "kind": e.Kind, "amount_cents": e.AmountCents, "balance_after_cents": e.BalanceAfterCents,
		"order_id": orderID, "note": e.Note, "created_at": e.CreatedAt, "operator": e.Operator, "ext_ref": e.ExtRef,
	}
}

// AdminAdjustAgent POST /agents/:id/adjust：先自己校验参数，再调数据层，参数错 400、数据层意外错误 500。
func AdminAdjustAgent(c *gin.Context) {
	a, ok := adminAgentFromPath(c)
	if !ok {
		return
	}
	key, ok := issueIdempotencyKey(c)
	if !ok {
		return
	}
	var req struct {
		AmountCents int64  `json:"amount_cents"`
		Note        string `json:"note"`
	}
	if !adminBind(c, &req) {
		return
	}
	note := strings.TrimSpace(req.Note)
	switch {
	case req.AmountCents == 0:
		adminInvalid(c, "调账金额不能为 0")
		return
	case req.AmountCents > agentAdjustMaxCents || req.AmountCents < -agentAdjustMaxCents:
		adminInvalid(c, "调账金额超出范围")
		return
	case note == "":
		adminInvalid(c, "调账原因不能为空")
		return
	case utf8.RuneCountInString(note) > agentNoteMax:
		adminInvalid(c, fmt.Sprintf("调账原因不能超过 %d 个字", agentNoteMax))
		return
	}
	entry, replayed, err := db.AdjustAgentBalance(c.Request.Context(), a.ID, req.AmountCents, key, "admin:"+adminName(c), note, time.Now())
	var ib *db.InsufficientBalanceError
	switch {
	case errors.As(err, &ib):
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "insufficient_balance", "message": "余额不足，不能扣成负数", "balance_cents": ib.BalanceCents})
		return
	case errors.Is(err, db.ErrLedgerRefConflict):
		agentError(c, http.StatusConflict, "idempotency_conflict", "这个幂等键已用于另一个代理商或另一个金额")
		return
	case errors.Is(err, db.ErrAgentNotFound):
		adminNotFound(c, "代理商")
		return
	case err != nil:
		adminInternal(c, "调账", err)
		return
	}
	if !replayed {
		auditAdmin(c, "agent_adjust", fmt.Sprintf("agent_id=%d username=%s amount_cents=%d ledger_id=%d note=%s", a.ID, a.Username, req.AmountCents, entry.ID, note))
	}
	balance := entry.BalanceAfterCents
	if cur, err := db.GetAgent(c.Request.Context(), a.ID); err == nil {
		balance = cur.BalanceCents // 重放时返回当前余额，不是第一次之后的余额
	}
	c.JSON(http.StatusOK, gin.H{"entry": adminLedgerRowView(entry), "balance_cents": balance, "replayed": replayed})
}

// agentOrderView 对应契约里的 order。
func agentOrderView(o db.AgentOrder) gin.H {
	return gin.H{
		"id": o.ID, "product_code": o.ProductCode, "product_name": o.ProductName, "plan": o.PlanKey, "region": o.Region,
		"quantity": o.Quantity, "unit_price_cents": o.UnitPriceCents, "total_cents": o.TotalCents,
		"request_no": o.RequestNo, "note": o.Note, "created_at": o.CreatedAt, "source": o.Source, "external_ref": o.ExternalRef,
	}
}

// AdminListAgentOrders GET /agents/:id/orders
func AdminListAgentOrders(c *gin.Context) {
	a, ok := adminAgentFromPath(c)
	if !ok {
		return
	}
	limit, offset := agentPaging(c)
	list, total, err := db.ListAgentOrders(c.Request.Context(), a.ID, limit, offset)
	if err != nil {
		adminInternal(c, "列出订单", err)
		return
	}
	items := make([]gin.H, 0, len(list))
	for _, o := range list {
		items = append(items, agentOrderView(o))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total})
}

// adminOverridesResponse 响应覆盖价清单：每个商品一行，等级价和覆盖价没有的为 null。
func adminOverridesResponse(c *gin.Context, a db.Agent) {
	ctx := c.Request.Context()
	products, err := db.ListAgentProducts(ctx)
	if err != nil {
		adminInternal(c, "列出商品", err)
		return
	}
	tierPrices, err := db.ListAgentTierPrices(ctx)
	if err != nil {
		adminInternal(c, "列出等级价", err)
		return
	}
	ovs, err := db.ListAgentOverrides(ctx, a.ID)
	if err != nil {
		adminInternal(c, "列出覆盖价", err)
		return
	}
	tp := map[int64]int64{}
	for _, t := range tierPrices {
		if t.TierID == a.TierID {
			tp[t.ProductID] = t.PriceCents
		}
	}
	ov := map[int64]int64{}
	for _, o := range ovs {
		ov[o.ProductID] = o.PriceCents
	}
	items := make([]gin.H, 0, len(products))
	for _, p := range products {
		var tier, over any
		if v, ok := tp[p.ID]; ok {
			tier = v
		}
		if v, ok := ov[p.ID]; ok {
			over = v
		}
		items = append(items, gin.H{"product_id": p.ID, "product_code": p.Code, "name": p.Name, "tier_price_cents": tier, "override_price_cents": over})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// AdminGetAgentOverrides GET /agents/:id/overrides
func AdminGetAgentOverrides(c *gin.Context) {
	a, ok := adminAgentFromPath(c)
	if !ok {
		return
	}
	adminOverridesResponse(c, a)
}

// AdminPutAgentOverrides PUT /agents/:id/overrides：先确认代理商存在（数据层没有外键），价格先校验再写库。
func AdminPutAgentOverrides(c *gin.Context) {
	a, ok := adminAgentFromPath(c)
	if !ok {
		return
	}
	var req struct {
		Cells []struct {
			ProductID  int64 `json:"product_id"`
			PriceCents int64 `json:"price_cents"`
		} `json:"cells"`
	}
	if !adminBind(c, &req) {
		return
	}
	if len(req.Cells) > agentPriceCellsMax {
		adminInvalid(c, "一次提交的价格太多")
		return
	}
	cells := make([]db.AgentOverride, 0, len(req.Cells))
	for _, x := range req.Cells {
		if x.ProductID <= 0 {
			adminInvalid(c, "商品 ID 不合法")
			return
		}
		if !validPriceCents(c, x.PriceCents) {
			return
		}
		cells = append(cells, db.AgentOverride{ProductID: x.ProductID, PriceCents: x.PriceCents})
	}
	err := db.PutAgentOverrides(c.Request.Context(), a.ID, cells, time.Now())
	switch {
	case errors.Is(err, db.ErrAgentProductNotFound):
		adminInvalid(c, "商品不存在")
		return
	case err != nil:
		adminInternal(c, "写覆盖价", err)
		return
	}
	auditAdmin(c, "agent_override_update", fmt.Sprintf("agent_id=%d username=%s cells=%d", a.ID, a.Username, len(cells)))
	adminOverridesResponse(c, a)
}

// agentReconcileView 是 site_settings.agent_reconcile_last 的内容，也是 /agent-summary 里 reconcile 的形状。
type agentReconcileView struct {
	At         int64                    `json:"at"`
	OK         bool                     `json:"ok"`
	Mismatches []agentReconcileMismatch `json:"mismatches"`
}

// agentReconcileMismatch 是一个对不上的代理商和差额（余额减流水）。
type agentReconcileMismatch struct {
	AgentID   int64 `json:"agent_id"`
	DiffCents int64 `json:"diff_cents"`
}

// AdminAgentSummary GET /agent-summary：余额合计、今天和本月的下单合计（按北京时间）、最近一次对账结果。
func AdminAgentSummary(c *gin.Context) {
	now := agentNow().In(beijingZone)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, beijingZone)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, beijingZone)
	t, err := db.AgentTotalsSince(c.Request.Context(), dayStart.Unix(), monthStart.Unix())
	if err != nil {
		adminInternal(c, "统计", err)
		return
	}
	var rec any
	raw, err := db.GetSetting(agentReconcileKey)
	if err != nil {
		adminInternal(c, "读取对账结果", err)
		return
	}
	if strings.TrimSpace(raw) != "" {
		var v agentReconcileView
		if json.Unmarshal([]byte(raw), &v) == nil {
			if v.At > 0 { // {}、null 等没有时间的内容按还没对过账处理
				if v.Mismatches == nil {
					v.Mismatches = []agentReconcileMismatch{}
				}
				rec = v
			}
		} else {
			log.Printf("[agent-admin] %s 内容无法解析，按没有处理", agentReconcileKey)
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"total_balance_cents": t.BalanceCents, "today_order_cents": t.TodayOrderCents, "month_order_cents": t.MonthOrderCents,
		"reconcile": rec,
	})
}
