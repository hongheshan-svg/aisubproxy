package handler

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

const localCDKNoteMax = 200

// AdminGetDefaultProvider GET /api/v1/admin/providers/default
func AdminGetDefaultProvider(c *gin.Context) {
	def, err := provider.LoadDefault()
	if err != nil {
		log.Printf("[provider] 读取默认服务商失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取默认服务商失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"provider": def, "options": provider.Names()})
}

// AdminPutDefaultProvider PUT /api/v1/admin/providers/default  body: { provider }
// 切换只影响之后新发的码：已发的码、在途单、已占用的卡都不动。未就绪也允许保存（发码接口会拦），响应里带上原因给界面警告。
func AdminPutDefaultProvider(c *gin.Context) {
	var req struct {
		Provider string `json:"provider"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	next, err := provider.Parse(req.Provider)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未知的卡服务商：" + strings.TrimSpace(req.Provider)})
		return
	}
	prev, err := provider.LoadDefault()
	if err != nil {
		log.Printf("[provider] 读取默认服务商失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取默认服务商失败"})
		return
	}
	if err := provider.SaveDefault(next); err != nil {
		log.Printf("[provider] 保存默认服务商失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
	auditAdmin(c, "provider_default_update", fmt.Sprintf("from=%s to=%s", prev, next))
	p, _ := provider.Get(next)
	ready := checkProvider(c.Request.Context(), p, "", "").Readiness
	c.JSON(http.StatusOK, gin.H{"provider": next, "previous": prev, "readiness": ready})
}

// AdminProviderReadiness GET /api/v1/admin/providers/readiness?plan=&region=
// 每家已注册服务商的就绪情况（含各自的 extras、warnings，供卡型另有 combos）和能力矩阵。
// plan、region 可选，都给时供卡型只检查这一个组合。某家的检查本身出错只把这家标为未就绪。
func AdminProviderReadiness(c *gin.Context) {
	ctx := c.Request.Context()
	plan := strings.TrimSpace(c.Query("plan"))
	region := strings.ToUpper(strings.TrimSpace(c.Query("region")))
	cfg, err := provider.LoadConfig()
	if err != nil {
		log.Printf("[provider] 读取服务商配置失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取服务商配置失败"})
		return
	}
	by := cfg.ByProduct
	if by == nil {
		by = map[string]provider.Name{}
	}
	providers := gin.H{}
	for _, p := range provider.List() {
		providers[string(p.Name())] = checkProvider(ctx, p, plan, region).json(p.Kind())
	}
	c.JSON(http.StatusOK, gin.H{
		"default":      cfg.Default,
		"by_product":   by,
		"providers":    providers,
		"capabilities": provider.Capabilities(),
	})
}

// cdkIssueRequest 是发码请求。Provider 留空时按产品配置和全局默认决定（见 provider.ResolveIssue）。
type cdkIssueRequest struct {
	Provider string `json:"provider"`
	Plan     string `json:"plan"`
	// Product gpt / x / grok。不传按档位键推断（老前端只传 plan）。
	Product          string `json:"product"`
	Count            int    `json:"count"`
	FundingConfirmed bool   `json:"funding_confirmed"`
	// PaymentCountry 这批码兑换时用哪个地区付款。ZovoCard 空 = 菲律宾（存量行为）。
	// 不在本站校验取值：卡台的 payment_regions 是唯一真相源，
	// 这里再校验一遍就等于多了一份会过期的清单。发了不支持的地区，
	// 卡台会在发码这一步直接拒，错误原样透回给操作者。
	// MarsWallex 必填，不设默认：地区决定金额和币种，发错了要到兑换才发现。
	PaymentCountry string `json:"payment_country"`
	Region         string `json:"region"` // payment_country 的别名
	Note           string `json:"note"`
}

// CardPlatformIssueCDKs POST /api/v1/admin/cardplatform/cdks、POST /api/v1/admin/cdks/issue
// 按服务商发码。两条路由同一个处理。
//   - 请求带了 provider：必须已注册并支持这个档位，否则 400，绝不换成别家。
//   - 没带（老前端）：先看这个产品的配置，再看全局默认；选出来的那家不支持这个档位也是 400，绝不换成别家。
//
// 供卡型走通用的本地发码，上游型交给它的适配器。
func CardPlatformIssueCDKs(c *gin.Context) {
	var req cdkIssueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if req.PaymentCountry == "" {
		req.PaymentCountry = req.Region
	}
	plan := strings.TrimSpace(req.Plan)
	if plan == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "plan is required"})
		return
	}
	product := issueProduct(req.Product, plan)
	var p provider.Provider
	if v := strings.TrimSpace(req.Provider); v != "" {
		var ok bool
		if p, ok = provider.Get(provider.Name(v)); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "未知的卡服务商：" + v})
			return
		}
		if err := provider.CheckSupports(p, product, plan); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "provider": p.Name(), "product": product, "plan": plan})
			return
		}
	} else {
		var err error
		p, err = provider.ResolveIssue(product, plan)
		var unsupported *provider.UnsupportedError
		switch {
		case errors.As(err, &unsupported):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "provider": unsupported.Provider.Name(), "product": product, "plan": plan})
			return
		case err != nil:
			// 读不出来时不能当成 ZovoCard 发码：发出去就是真金白银的承诺
			log.Printf("[provider] 读取发码服务商配置失败: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取发码服务商配置失败"})
			return
		}
	}
	if cs, ok := provider.AsCardSupply(p); ok {
		localCDKIssue(c, cs, req)
		return
	}
	a, ok := upstreamAdapterFor(p.Name())
	if !ok {
		log.Printf("[provider] 上游 %s 没有适配器", p.Name())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "这家服务商没有接入发码"})
		return
	}
	a.Issue(c, req)
}

// issueIdempotencyKey 取发码的幂等键，缺了或过长时已写好 400。键必须由客户端在点击时生成并在重试时沿用：
// 服务端随机生成的键对重试没有任何保护。两家服务商共用这条规则。
func issueIdempotencyKey(c *gin.Context) (string, bool) {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" || len(key) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Idempotency-Key 请求头必填（1-128 字符），同一次发码的重试要沿用同一个值"})
		return "", false
	}
	return key, true
}

// localCDKIssueReplay 这个幂等键已经发过码。完整码只存哈希，拿不回来，所以这里不返回码，只说明发过哪一批，让操作者决定怎么处理。
func localCDKIssueReplay(c *gin.Context, cs provider.CardSupply, b db.LocalCDKIssueBatch) {
	c.JSON(http.StatusConflict, gin.H{
		"error": fmt.Sprintf("这次发码已经完成（ID %d-%d，共 %d 张），不会重复发。完整卡密只显示一次，本站无法再次取出；没有保存到的话，请在码列表禁用这批后重新发码",
			b.FirstID, b.LastID, b.Count),
		"provider":       cs.Name(),
		"already_issued": true,
		"first_id":       b.FirstID,
		"last_id":        b.LastID,
		"count":          b.Count,
	})
}

// localCDKIssue 发供卡型服务商的码：用这家的前缀生成，写入 local_cdks.provider。完整码只在这个响应里出现一次，不进日志和审计。
// 能不能发这个档位已由 CardPlatformIssueCDKs 确认；就绪检查和发码硬门槛按这家服务商计算。
// 和 ZovoCard 一样要求幂等键，防止重复点击多发码：同一个键只发一批，重试返回 409（见 localCDKIssueReplay）。
func localCDKIssue(c *gin.Context, cs provider.CardSupply, req cdkIssueRequest) {
	plan := strings.TrimSpace(req.Plan)
	product := issueProduct(req.Product, plan)
	name := cs.Name()
	if !req.FundingConfirmed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "funding_confirmed must be true（确认承担兑换时开卡/充值/订阅实付）"})
		return
	}
	region := strings.ToUpper(strings.TrimSpace(req.PaymentCountry))
	if region == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": cs.DisplayName() + " 发码必须指定地区"})
		return
	}
	if req.Count < 1 {
		req.Count = 1
	}
	if req.Count > cardplatform.MaxIssueCount {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("count max %d", cardplatform.MaxIssueCount)})
		return
	}
	note := strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(note) > localCDKNoteMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("备注最多 %d 字", localCDKNoteMax)})
		return
	}
	key, ok := issueIdempotencyKey(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	// 先查键：已经发过的批次，就算现在未就绪，也要如实告诉操作者发过了
	switch b, found, err := db.GetLocalCDKIssueBatch(ctx, key); {
	case err != nil:
		log.Printf("[local-cdk] 查询发码幂等键失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败，没有发出任何码"})
		return
	case found:
		localCDKIssueReplay(c, cs, b)
		return
	}
	if st := checkCardSupply(ctx, cs, plan, region, false); !st.Readiness.OK {
		c.JSON(http.StatusBadRequest, gin.H{"error": cs.DisplayName() + " 未就绪，不能发码", "provider": name, "problems": st.Readiness.Problems})
		return
	}

	codes := make([]provider.LocalCode, 0, req.Count)
	rows := make([]db.NewLocalCDK, 0, req.Count)
	by := adminName(c)
	for i := 0; i < req.Count; i++ {
		lc, err := provider.GenerateCode(cs)
		if err != nil {
			log.Printf("[local-cdk] 生成本地码失败: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "生成卡密失败"})
			return
		}
		codes = append(codes, lc)
		rows = append(rows, db.NewLocalCDK{
			Provider: string(name), CodeHash: lc.CodeHash, TokenHash: lc.TokenHash, CodePrefix: lc.Prefix,
			Product: product, PlanKey: plan, Region: region, IssuedBy: by, Note: note,
		})
	}
	ids, err := db.InsertLocalCDKs(ctx, key, rows, time.Now())
	if errors.Is(err, db.ErrLocalCDKIssueKeyUsed) { // 并发的重复点击：上面查的时候另一个请求还没提交
		b, found, gerr := db.GetLocalCDKIssueBatch(ctx, key)
		if gerr != nil || !found {
			log.Printf("[local-cdk] 读取发码批次失败: %v", gerr)
			c.JSON(http.StatusConflict, gin.H{"error": "这次发码已经完成，不会重复发", "provider": name, "already_issued": true})
			return
		}
		localCDKIssueReplay(c, cs, b)
		return
	}
	if err != nil {
		log.Printf("[local-cdk] 写入本地码失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "写入卡密失败，没有发出任何码"})
		return
	}
	auditAdmin(c, "local_cdk_issue", fmt.Sprintf("provider=%s plan=%s region=%s count=%d ids=%d-%d",
		name, plan, region, len(ids), ids[0], ids[len(ids)-1]))

	planNote := cs.PlanNote(plan)
	fundCents := int64(0)
	if _, rc, err := payengine.ResolvePlan(plan, region, false); err == nil {
		fundCents = rc.FundCents
	}
	issued := make([]gin.H, 0, len(codes))
	for i, lc := range codes {
		it := gin.H{
			"id": ids[i], "code": lc.Code, "full_code": lc.Code, "code_length": len(lc.Code), "code_prefix": lc.Prefix,
			"provider": name, "product": product, "plan": plan,
			"region": region, "payment_country": region, "fund_cents": fundCents, "fee_amount_minor": 0,
			"stored": false, "has_full_code": true,
		}
		if planNote != "" {
			it["plan_note"] = planNote
		}
		issued = append(issued, it)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"provider":              name,
		"provider_display_name": cs.DisplayName(),
		"requested":             req.Count,
		"issued":                issued,
		"count":                 len(issued),
		"stored_count":          0,
		"store_failed":          0,
		"server_stored":         false,
		"msg":                   "完整卡密只显示这一次，本站只保存哈希，丢失后无法找回",
	})
}

// localCDKRowJSON 是后台列表的一行。没有完整码；单据只带对账和处理要用的字段，邮箱脱敏。
func localCDKRowJSON(r db.LocalCDKRow) gin.H {
	out := gin.H{
		"id": r.ID, "provider": r.Provider, "code_prefix": r.CodePrefix, "product": r.Product, "plan": r.PlanKey,
		"region": r.Region, "payment_country": r.Region,
		"status": r.View.Status, "status_reason": r.View.Reason, "manual_status": r.Manual,
		"needs_review": r.View.NeedsReview, "funded_failures": r.View.FundedFailures, "precharge_failures": r.View.PrechargeFailures, "attempts_left": r.View.AttemptsLeft,
		"attempts_carried": r.AttemptsCarried, "exchanged_from": r.ExchangedFrom, "replaced_by": r.ReplacedBy,
		"issued_by": r.IssuedBy, "note": r.Note, "created_at": r.CreatedAt, "updated_at": r.UpdatedAt,
		"plan_note": provider.PlanNote(provider.Name(r.Provider), r.PlanKey),
		"order_no":  "", "order_state": "", "order_failure": "", "order_error_code": "", "fund_cents": int64(0), "account_email": "",
	}
	if o := r.View.Latest; o != nil {
		out["order_no"], out["order_state"], out["order_failure"], out["order_error_code"] = o.OrderNo, o.State, o.Failure, o.ErrorCode
		out["fund_cents"], out["account_email"] = o.FundCents, maskEmailForAdmin(o.AccountEmail)
	}
	return out
}

// AdminListLocalCDKs GET /api/v1/admin/cdks?page=&page_size=&status=&plan=&provider=
func AdminListLocalCDKs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	status := strings.TrimSpace(c.Query("status"))
	plan := strings.TrimSpace(c.Query("plan"))
	prov := strings.TrimSpace(c.Query("provider"))
	// agent_id 给了就按代理商筛：0 是后台发的码，其余是代理商 ID
	agentFilter, hasAgentFilter := int64(0), false
	if raw := strings.TrimSpace(c.Query("agent_id")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "agent_id 必须是非负整数"})
			return
		}
		agentFilter, hasAgentFilter = v, true
	}
	all, err := db.LoadLocalCDKRows(c.Request.Context())
	if err != nil {
		log.Printf("[local-cdk] 列表失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	filtered := all[:0]
	for _, r := range all {
		if (status == "" || r.View.Status == status) && (plan == "" || r.PlanKey == plan) && (prov == "" || r.Provider == prov) && (!hasAgentFilter || r.AgentID == agentFilter) {
			filtered = append(filtered, r)
		}
	}
	total := len(filtered)
	list := []gin.H{}
	if start := (page - 1) * size; start < total {
		end := min(start+size, total)
		names := map[int64]string{}
		if agents, err := db.ListAgents(c.Request.Context()); err != nil {
			log.Printf("[local-cdk] 读取代理商登录名失败: %v", err)
		} else {
			for _, a := range agents {
				names[a.ID] = a.Username
			}
		}
		for _, r := range filtered[start:end] {
			row := localCDKRowJSON(r)
			row["agent_id"], row["agent_username"] = r.AgentID, names[r.AgentID]
			list = append(list, row)
		}
	}
	c.JSON(http.StatusOK, gin.H{"list": list, "total": total, "page": page, "page_size": size})
}

func localCDKAdminID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return 0, false
	}
	return id, true
}

// AdminDisableLocalCDK POST /api/v1/admin/cdks/:id/disable
// 已结束的码（成功或已转人工）禁用没有意义，直接拒绝。在途单拦不住，禁用只保证它结束后不能重兑。
func AdminDisableLocalCDK(c *gin.Context) {
	id, ok := localCDKAdminID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	row, err := db.GetLocalCDK(ctx, id)
	if errors.Is(err, db.ErrLocalCDKNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
		return
	}
	if err != nil {
		log.Printf("[local-cdk] 读取码 %d 失败: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	v, err := db.LocalCDKViewOf(ctx, row)
	if err != nil {
		log.Printf("[local-cdk] 码 %d 派生状态失败: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if v.Status == db.LocalCDKConsumed || v.Status == db.LocalCDKFailed {
		c.JSON(http.StatusConflict, gin.H{"error": "该码已结束，不能禁用", "status": v.Status})
		return
	}
	changed, err := db.DisableLocalCDK(ctx, id, time.Now())
	if err != nil {
		log.Printf("[local-cdk] 禁用码 %d 失败: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
	if changed {
		auditAdmin(c, "local_cdk_disable", fmt.Sprintf("id=%d prefix=%s status=%s", id, row.CodePrefix, v.Status))
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": id, "changed": changed})
}

// AdminEnableLocalCDK POST /api/v1/admin/cdks/:id/enable
func AdminEnableLocalCDK(c *gin.Context) {
	id, ok := localCDKAdminID(c)
	if !ok {
		return
	}
	changed, err := db.EnableLocalCDK(c.Request.Context(), id, time.Now())
	switch {
	case errors.Is(err, db.ErrLocalCDKNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
		return
	case errors.Is(err, db.ErrLocalCDKReplaced):
		c.JSON(http.StatusConflict, gin.H{"error": "该码已换发新码，不能启用"})
		return
	case err != nil:
		log.Printf("[local-cdk] 启用码 %d 失败: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
	if changed {
		auditAdmin(c, "local_cdk_enable", fmt.Sprintf("id=%d", id))
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": id, "changed": changed})
}
