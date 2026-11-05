package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 代理商的「我的码」、码导出和概览（/api/v1/agent），字段名以 api-contract.md 为准。
// 码只按会话里的代理商 ID 查；完整码只出现在 /codes/export 的响应里（no-store），不进审计、日志、告警。

// agentExportMax 一次最多导出多少张码。是变量，测试里可以改小。
var agentExportMax = 10000

const (
	agentCStatusUnused     = "unused"
	agentCStatusProcessing = "processing"
	agentCStatusUsed       = "used"
	agentCStatusFailed     = "failed"
	agentCStatusDisabled   = "disabled"
)

var agentCodeStatuses = []string{agentCStatusUnused, agentCStatusProcessing, agentCStatusUsed, agentCStatusFailed, agentCStatusDisabled}

// agentCodeStatus 把派生状态映射成对外的 5 个值：reserved → processing，consumed → used，其余同名。
func agentCodeStatus(s string) string {
	switch s {
	case db.LocalCDKReserved:
		return agentCStatusProcessing
	case db.LocalCDKConsumed:
		return agentCStatusUsed
	}
	return s
}

func validAgentCodeStatus(s string) bool {
	for _, v := range agentCodeStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// agentCodeFilter 是 GET /codes 和 POST /codes/export 的筛选项。From、To 为空表示不限。
type agentCodeFilter struct {
	Product string `json:"product"`
	Status  string `json:"status"`
	OrderID int64  `json:"order_id"`
	From    *int64 `json:"from"`
	To      *int64 `json:"to"`
}

// check 校验筛选项；不合法时返回给用户看的原因。
func (f agentCodeFilter) check() string {
	switch {
	case f.Status != "" && !validAgentCodeStatus(f.Status):
		return "状态不正确"
	case f.OrderID < 0:
		return "订单号不正确"
	case f.From != nil && f.To != nil && *f.From > *f.To:
		return "开始时间不能晚于结束时间"
	}
	return ""
}

func (f agentCodeFilter) describe() string {
	t := func(p *int64) string {
		if p == nil {
			return "-"
		}
		return strconv.FormatInt(*p, 10)
	}
	product := f.Product
	if len(product) > 64 {
		product = product[:64]
	}
	return fmt.Sprintf("product=%q status=%s order_id=%d from=%s to=%s", product, f.Status, f.OrderID, t(f.From), t(f.To))
}

// agentCodeFilterFromQuery 读查询参数；不合法时已写好 400。
func agentCodeFilterFromQuery(c *gin.Context) (agentCodeFilter, bool) {
	f := agentCodeFilter{Product: strings.TrimSpace(c.Query("product")), Status: strings.TrimSpace(c.Query("status"))}
	num := func(key string) (*int64, bool) {
		s := strings.TrimSpace(c.Query(key))
		if s == "" {
			return nil, true
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			agentOrderInvalid(c, "时间或订单号需为整数")
			return nil, false
		}
		return &v, true
	}
	var ok bool
	var oid *int64
	if oid, ok = num("order_id"); !ok {
		return f, false
	}
	if oid != nil {
		f.OrderID = *oid
	}
	if f.From, ok = num("from"); !ok {
		return f, false
	}
	if f.To, ok = num("to"); !ok {
		return f, false
	}
	if why := f.check(); why != "" {
		agentOrderInvalid(c, why)
		return f, false
	}
	return f, true
}

// agentCodeEntry 是筛选后的一张码和它所属订单的商品编码。
type agentCodeEntry struct {
	row     db.LocalCDKRow
	product string
}

// loadAgentCodes 取这个代理商名下符合筛选的码，id 降序。读库失败时已写好 503。
func loadAgentCodes(c *gin.Context, f agentCodeFilter) ([]agentCodeEntry, bool) {
	ctx := c.Request.Context()
	aid := agentID(c)
	rows, err := db.LoadLocalCDKRowsByAgent(ctx, aid)
	if err != nil {
		agentOrderBusy(c, "列出码", err)
		return nil, false
	}
	product, err := db.AgentOrderProductCodes(ctx, aid)
	if err != nil {
		agentOrderBusy(c, "列出订单", err)
		return nil, false
	}
	out := make([]agentCodeEntry, 0, len(rows))
	for _, r := range rows {
		e := agentCodeEntry{row: r, product: product[r.AgentOrderID]}
		switch {
		case f.Product != "" && e.product != f.Product:
		case f.Status != "" && agentCodeStatus(r.View.Status) != f.Status:
		case f.OrderID > 0 && r.AgentOrderID != f.OrderID:
		case f.From != nil && r.CreatedAt < *f.From:
		case f.To != nil && r.CreatedAt >= *f.To:
		default:
			out = append(out, e)
		}
	}
	return out, true
}

// agentCodeView 对应契约里的 codeRow。email（打码）和 used_at 只在已使用时有值。
func agentCodeView(e agentCodeEntry) gin.H {
	r := e.row
	status := agentCodeStatus(r.View.Status)
	var email, usedAt any
	if status == agentCStatusUsed && r.View.Latest != nil {
		email = maskEmailForAdmin(r.View.Latest.AccountEmail)
		if r.View.Latest.FinishedAt != nil {
			usedAt = *r.View.Latest.FinishedAt
		}
	}
	return gin.H{
		"id": r.ID, "code_prefix": r.CodePrefix, "product_code": e.product, "plan": r.PlanKey, "region": r.Region,
		"status": status, "reason": r.View.Reason, "created_at": r.CreatedAt, "order_id": r.AgentOrderID, "note": r.Note,
		"email": email, "used_at": usedAt,
	}
}

// AgentListCodes GET /codes
func AgentListCodes(c *gin.Context) {
	f, ok := agentCodeFilterFromQuery(c)
	if !ok {
		return
	}
	list, ok := loadAgentCodes(c, f)
	if !ok {
		return
	}
	limit, offset := agentPaging(c)
	items := make([]gin.H, 0) // 不按 page_size 预分配容量（CodeQL #25）；空结果仍返回 []
	for i := offset; i < len(list) && i < offset+limit; i++ {
		items = append(items, agentCodeView(list[i]))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(list)})
}

// AgentExportCodes POST /codes/export：请求体 {ids:[...]} 或 {filter:{...}}，二选一。
// 每项是 codeRow 加完整码；换码后作废的码照样导出，status 如实显示 disabled。
func AgentExportCodes(c *gin.Context) {
	var req struct {
		IDs    *[]int64         `json:"ids"`
		Filter *agentCodeFilter `json:"filter"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		agentOrderInvalid(c, "请求内容格式不对")
		return
	}
	if (req.IDs == nil) == (req.Filter == nil) {
		agentOrderInvalid(c, "请在 ids 和 filter 中二选一")
		return
	}
	var f agentCodeFilter
	if req.Filter != nil {
		f = *req.Filter
		f.Product, f.Status = strings.TrimSpace(f.Product), strings.TrimSpace(f.Status)
		if why := f.check(); why != "" {
			agentOrderInvalid(c, why)
			return
		}
	}
	var want map[int64]bool
	if req.IDs != nil {
		if len(*req.IDs) == 0 {
			agentOrderInvalid(c, "请选择要导出的码")
			return
		}
		want = make(map[int64]bool, len(*req.IDs))
		for _, id := range *req.IDs {
			if id <= 0 {
				agentOrderInvalid(c, "码 ID 不合法")
				return
			}
			want[id] = true
		}
	}
	list, ok := loadAgentCodes(c, f)
	if !ok {
		return
	}
	if want != nil { // 只留自己的：别人的 ID 混进来也拿不到
		kept := list[:0]
		for _, e := range list {
			if want[e.row.ID] {
				kept = append(kept, e)
			}
		}
		list = kept
	}
	if len(list) > agentExportMax {
		agentError(c, http.StatusBadRequest, "export_too_large", fmt.Sprintf("一次最多导出 %d 张，请缩小筛选范围", agentExportMax))
		return
	}
	ids := make([]int64, len(list))
	byID := make(map[int64]agentCodeEntry, len(list))
	for i, e := range list {
		ids[i] = e.row.ID
		byID[e.row.ID] = e
	}
	codes, err := openAgentCodes(c.Request.Context(), agentID(c), ids)
	var noKey *agentCodesNoKeyError
	if errors.As(err, &noKey) {
		agentOrderNoKey(c, noKey.kind, agentMsgExportMaint)
		return
	}
	if err != nil {
		agentOrderBusy(c, "读取码的密文", err)
		return
	}
	items := make([]gin.H, 0, len(codes))
	for _, x := range codes {
		v := agentCodeView(byID[x.ID])
		v["code"] = x.Code
		if x.Unavailable {
			v["unavailable"] = true
		}
		items = append(items, v)
	}
	detail := "mode=filter " + f.describe()
	if want != nil {
		detail = "mode=ids"
	}
	auditAgent(c, "agent_codes_export", fmt.Sprintf("count=%d %s", len(items), detail))
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"codes": items})
}

// AgentOverview GET /overview：余额、本月（北京时间）下单合计、各状态的码数、最近 5 笔订单。
func AgentOverview(c *gin.Context) {
	ctx := c.Request.Context()
	aid := agentID(c)
	a, err := db.GetAgent(ctx, aid)
	if err != nil {
		if errors.Is(err, db.ErrAgentNotFound) {
			agentError(c, http.StatusUnauthorized, "unauthorized", "未登录或登录已过期")
			return
		}
		agentOrderBusy(c, "读取账号", err)
		return
	}
	now := agentNow().In(beijingZone)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, beijingZone)
	month, err := db.AgentPurchaseSince(ctx, aid, monthStart.Unix(), monthStart.AddDate(0, 1, 0).Unix())
	if err != nil {
		agentOrderBusy(c, "统计本月下单", err)
		return
	}
	rows, err := db.LoadLocalCDKRowsByAgent(ctx, aid)
	if err != nil {
		agentOrderBusy(c, "统计码", err)
		return
	}
	counts := map[string]int{}
	for _, s := range agentCodeStatuses {
		counts[s] = 0
	}
	for _, r := range rows {
		counts[agentCodeStatus(r.View.Status)]++
	}
	recent, _, err := db.ListAgentOrders(ctx, aid, 5, 0)
	if err != nil {
		agentOrderBusy(c, "列出订单", err)
		return
	}
	orders := make([]gin.H, 0, len(recent))
	for _, o := range recent {
		orders = append(orders, agentOrderView(o))
	}
	c.JSON(http.StatusOK, gin.H{"balance_cents": a.BalanceCents, "month_order_cents": month, "status_counts": counts, "recent_orders": orders})
}
