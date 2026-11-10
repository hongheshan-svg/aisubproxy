package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

func writeCardErr(c *gin.Context, err error) {
	if ae, ok := err.(*cardplatform.APIError); ok {
		status := ae.HTTPStatus
		if status < 400 {
			status = http.StatusBadRequest
		}
		if status > 599 {
			status = http.StatusBadGateway
		}
		// 上游卡台 401/403 表示 API Key 无效/权限不足，不是 CDK 管理员会话失效。
		// 映射为 502，避免前端 authFetch 把 401 误判为登录过期并踢回登录页。
		upstreamAuth := status == http.StatusUnauthorized || status == http.StatusForbidden
		if upstreamAuth {
			status = http.StatusBadGateway
		}
		msg := ae.Msg
		if msg == "" {
			msg = "cardplatform api error"
		}
		if upstreamAuth && !strings.Contains(strings.ToLower(msg), "api key") {
			msg = msg + "（卡台 API Key 无效/未配置/无权限，与 CDK 登录无关）"
		}
		code := ae.ErrorCode
		if code == "" && upstreamAuth {
			code = "cardplatform_unauthorized"
		}
		c.JSON(status, gin.H{
			"error":      msg,
			"error_code": code,
			"code":       ae.Code,
			"upstream":   true,
		})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "upstream": true})
}

// CardPlatformPlans GET /api/v1/admin/cardplatform/plans
// 实时套餐服务费（CDK 收费价）
func CardPlatformPlans(c *gin.Context) {
	cli := cardplatform.NewFromSettings()
	plans, err := cli.GetPlans(c.Request.Context(), c.Query("product"))
	if err != nil {
		writeCardErr(c, err)
		return
	}
	// ★只下发「能发码也能兑换」的档位★——过滤在服务端，前端拿不到就渲染不出。
	// 放在前端过滤的话，每加一个界面就要记得再过滤一次；这次线上就是这么漏的：
	// 卡台透传了 ACC 的 claude_* 定价键，代理后台照单全列，还能发码。
	sellable := plans.SellablePlans()
	m := map[string]cardplatform.SellablePlan{}
	for _, p := range sellable {
		m[p.Key] = p
	}
	c.JSON(http.StatusOK, gin.H{
		"version": plans.Version,
		"plans":   m,
		"base":    cardplatform.LoadConfig().SiteBase,
		// 展示顺序/文案/性质仍以卡台注册表为准，前端不维护档位清单
		"registry": sellable,
		// 付款地区同理：卡台下发什么就能选什么，本站不写死。
		// 老版本卡台没有这个字段 → 空数组 → 界面只剩「默认(PH)」，即本功能上线前的行为。
		"payment_regions": plans.PaymentRegions,
	})
}

// CardPlatformBalance GET /api/v1/admin/cardplatform/balance
func CardPlatformBalance(c *gin.Context) {
	cli := cardplatform.NewFromSettings()
	bal, err := cli.GetBalance(c.Request.Context())
	if err != nil {
		writeCardErr(c, err)
		return
	}
	c.JSON(http.StatusOK, bal)
}

// CardPlatformSyncUpstreamCDKs POST /api/v1/admin/cardplatform/cdks/sync
// 从卡台列表拉取完整码写入本站。卡台升级后列表会带 code；旧后端只能拿到前缀。
func CardPlatformSyncUpstreamCDKs(c *gin.Context) {
	var req struct {
		Status   string `json:"status"`
		Plan     string `json:"plan"`
		MaxPages int    `json:"max_pages"`
	}
	_ = c.ShouldBindJSON(&req)
	cli := cardplatform.NewFromSettings()
	res, err := cli.SyncUpstreamFullCodes(c.Request.Context(), req.Status, req.Plan, req.MaxPages)
	if err != nil {
		writeCardErr(c, err)
		return
	}
	u, _ := c.Get("username")
	username, _ := u.(string)
	db.WriteAudit(username, "cardplatform_sync_cdk",
		fmt.Sprintf("imported=%d prefix_only=%d scanned=%d", res.Imported, res.PrefixOnly, res.Scanned), c.ClientIP())
	msg := fmt.Sprintf("从卡台同步：新入库 %d 张，扫描 %d 张", res.Imported, res.Scanned)
	if res.NeedCardplatformUpgrade {
		msg = "卡台列表仍只返回前缀。请先升级卡台后端后再同步。"
	}
	c.JSON(http.StatusOK, gin.H{
		"imported":                  res.Imported,
		"updated":                   res.Updated,
		"prefix_only":               res.PrefixOnly,
		"scanned":                   res.Scanned,
		"pages":                     res.Pages,
		"upstream_total":            res.UpstreamTotal,
		"need_cardplatform_upgrade": res.NeedCardplatformUpgrade,
		"codes":                     res.Codes,
		"full_code_in_store":        db.CountCardplatformCDKCodes(),
		"msg":                       msg,
	})
}

// CardPlatformStoreCDKCodes POST /api/v1/admin/cardplatform/cdks/store
// 把完整码写入本站 SQLite（发码时自动写；也可用本机缓存/导出回填历史码）。
// body: { items: [{ id, code, code_prefix?, plan?, fee_amount_minor? }] }
func CardPlatformStoreCDKCodes(c *gin.Context) {
	var req struct {
		Items []struct {
			ID             int64  `json:"id"`
			Code           string `json:"code"`
			CodePrefix     string `json:"code_prefix"`
			Plan           string `json:"plan"`
			FeeAmountMinor int64  `json:"fee_amount_minor"`
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "items required"})
		return
	}
	if len(req.Items) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "items max 500"})
		return
	}
	saved, skipped, failed := 0, 0, 0
	for _, it := range req.Items {
		code := strings.TrimSpace(it.Code)
		if len(code) < 20 || !strings.Contains(code, "-") {
			skipped++
			continue
		}
		prefix := strings.TrimSpace(it.CodePrefix)
		if prefix == "" && len(code) >= 14 {
			prefix = code[:14]
		}
		if err := db.SaveCardplatformCDKCode(it.ID, code, prefix, it.Plan, it.FeeAmountMinor); err != nil {
			failed++
			log.Printf("[cdk-store] save failed id=%d: %v", it.ID, err)
			continue
		}
		saved++
	}
	u, _ := c.Get("username")
	username, _ := u.(string)
	db.WriteAudit(username, "cardplatform_store_cdk",
		"saved="+strconv.Itoa(saved)+" skipped="+strconv.Itoa(skipped)+" failed="+strconv.Itoa(failed), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"ok": true, "saved": saved, "skipped": skipped, "failed": failed,
	})
}

// CardPlatformListStoredCDKs GET /api/v1/admin/cardplatform/cdks/stored
// 只读本站已存完整码（随时复制/导出；不依赖卡台列表）。
// query: plan= / q= / status= / page= / page_size= / limit= / format=json|txt
//
// 列表默认分页（page_size=20）；导出 txt / 显式大 limit 仍可一次拉多条。
// 状态：本站 SQLite + 当前页轻量向卡台核对（不再整库翻页）；Webhook completed 也会回写 consumed。
func CardPlatformListStoredCDKs(c *gin.Context) {
	plan := strings.TrimSpace(c.Query("plan"))
	q := strings.TrimSpace(c.Query("q"))
	status := strings.TrimSpace(strings.ToLower(c.Query("status")))
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "json")))
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "0"))
	skipSync := c.Query("sync") == "0" || c.Query("sync") == "false"

	// 导出：一次尽量多取（硬顶 10000），按本地 status 过滤
	if format == "txt" || format == "text" || format == "plain" {
		if limit <= 0 {
			limit = 10000
		}
		list, _, err := db.ListCardplatformStoredCDKCodesPage(plan, q, status, 1, limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		var b strings.Builder
		for _, it := range list {
			if strings.TrimSpace(it.Code) == "" {
				continue
			}
			b.WriteString(it.Code)
			b.WriteByte('\n')
		}
		c.Header("Content-Disposition", `attachment; filename="cdk-full-codes.txt"`)
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(b.String()))
		return
	}

	// JSON 列表：优先 page/page_size；兼容旧客户端只传 limit（复制全部等 bulk）
	hasPageParam := strings.TrimSpace(c.Query("page")) != "" || strings.TrimSpace(c.Query("page_size")) != ""
	bulkLegacy := !hasPageParam && limit > 0
	if pageSize <= 0 {
		if bulkLegacy {
			pageSize = limit
			page = 1
		} else {
			pageSize = 20
		}
	}
	maxPage := 200
	if bulkLegacy {
		maxPage = 10000 // 复制/导出全部仍可一次取够
	}
	if pageSize > maxPage {
		pageSize = maxPage
	}
	if page < 1 {
		page = 1
	}

	list, total, err := db.ListCardplatformStoredCDKCodesPage(plan, q, status, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 当前页向卡台核对状态（仅本页 ID；bulk 大导出跳过以免拖慢）
	metadata := map[int64]cardplatform.CDKListItem{}
	if !skipSync && !bulkLegacy && len(list) > 0 && len(list) <= 200 {
		ids := make([]int64, 0, len(list))
		for _, it := range list {
			if it.UpstreamID > 0 {
				ids = append(ids, it.UpstreamID)
			}
		}
		metadata = refreshStoredCDKMetadata(c.Request.Context(), ids)
	}

	noteIDs := make([]int64, 0, len(list))
	for _, it := range list {
		noteIDs = append(noteIDs, it.UpstreamID)
	}
	notes := db.MapCardplatformCDKNotes(noteIDs)
	out := make([]gin.H, 0, len(list))
	for _, it := range list {
		st := it.Status
		country := it.PaymentCountry
		if current, ok := metadata[it.UpstreamID]; ok {
			if current.Status != "" {
				st = current.Status
			}
			country = &current.PaymentCountry
		}
		if st == "" {
			st = "unused"
		}
		out = append(out, gin.H{
			"id": it.UpstreamID, "code": it.Code, "full_code": it.Code,
			"code_prefix": it.CodePrefix, "plan": it.Plan, "status": st,
			"fee_amount_minor": it.FeeAmountMinor, "created_at": it.CreatedAt,
			"payment_country": country,
			"has_full_code":   true, "stored": true,
			"note": notes[it.UpstreamID],
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"list":               out,
		"total":              total,
		"page":               page,
		"page_size":          pageSize,
		"full_code_in_store": db.CountCardplatformCDKCodes(),
		"server_stored":      true,
		"status_synced":      len(metadata) > 0,
	})
}

// refreshStoredCDKMetadata reuses current-page status requests to also cache
// each CDK's creation region. No extra requests or full inventory scans.
func refreshStoredCDKMetadata(ctx context.Context, ids []int64) map[int64]cardplatform.CDKListItem {
	out := make(map[int64]cardplatform.CDKListItem, len(ids))
	if len(ids) == 0 {
		return out
	}
	cli := cardplatform.NewFromSettings()
	ch := make(chan cardplatform.CDKListItem, len(ids))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for _, id := range ids {
		id := id
		wg.Add(1)
		safego.Go("刷新上游卡密信息", func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, err := cli.ListCDKsQuery(ctx, cardplatform.CDKListQuery{
				Page: 1, PageSize: 5, Query: strconv.FormatInt(id, 10),
			})
			if err != nil || res == nil {
				return
			}
			for _, it := range res.List {
				if it.ID == id {
					it.Status = strings.ToLower(strings.TrimSpace(it.Status))
					it.PaymentCountry = strings.ToUpper(strings.TrimSpace(it.PaymentCountry))
					_ = db.UpdateCardplatformCDKMetadata(id, it.Status, it.PaymentCountry)
					ch <- it
					return
				}
			}
		})
	}
	safego.Go("等待刷新上游卡密信息", func() {
		wg.Wait()
		close(ch)
	})
	for p := range ch {
		out[p.ID] = p
	}
	return out
}

// CardPlatformDisableCDK POST /api/v1/admin/cardplatform/cdks/:id/disable
func CardPlatformDisableCDK(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	cli := cardplatform.NewFromSettings()
	if err := cli.DisableCDK(c.Request.Context(), id); err != nil {
		writeCardErr(c, err)
		return
	}
	localFailed := setLocalCDKStatuses([]int64{id}, "disabled")
	u, _ := c.Get("username")
	username, _ := u.(string)
	db.WriteAudit(username, "cardplatform_disable_cdk", "id="+strconv.FormatInt(id, 10), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": id, "status": "disabled", "local_failed": localFailed})
}

// CardPlatformEnableCDK POST /api/v1/admin/cardplatform/cdks/:id/enable — 解除禁用
func CardPlatformEnableCDK(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	cli := cardplatform.NewFromSettings()
	if err := cli.EnableCDK(c.Request.Context(), id); err != nil {
		writeCardErr(c, err)
		return
	}
	localFailed := setLocalCDKStatuses([]int64{id}, "unused")
	u, _ := c.Get("username")
	username, _ := u.(string)
	db.WriteAudit(username, "cardplatform_enable_cdk", "id="+strconv.FormatInt(id, 10), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": id, "status": "unused", "local_failed": localFailed})
}

// CardPlatformBatchDisableCDKs POST /api/v1/admin/cardplatform/cdks/batch-disable
// body: { ids: [1,2,3] }
func CardPlatformBatchDisableCDKs(c *gin.Context) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids required"})
		return
	}
	if len(req.IDs) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids max 100"})
		return
	}
	cli := cardplatform.NewFromSettings()
	res, err := cli.BatchDisableCDKs(c.Request.Context(), req.IDs)
	if err != nil {
		writeCardErr(c, err)
		return
	}
	localFailed := setLocalCDKStatuses(res.Disabled, "disabled")
	u, _ := c.Get("username")
	username, _ := u.(string)
	db.WriteAudit(username, "cardplatform_batch_disable_cdk",
		"ok="+strconv.Itoa(res.DisabledCount)+" fail="+strconv.Itoa(res.FailedCount), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"ok":       true,
		"disabled": res.Disabled, "failed": res.Failed,
		"disabled_count": res.DisabledCount, "failed_count": res.FailedCount,
		"local_failed": localFailed,
	})
}

// CardPlatformBatchEnableCDKs POST /api/v1/admin/cardplatform/cdks/batch-enable
func CardPlatformBatchEnableCDKs(c *gin.Context) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids required"})
		return
	}
	if len(req.IDs) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids max 100"})
		return
	}
	cli := cardplatform.NewFromSettings()
	res, err := cli.BatchEnableCDKs(c.Request.Context(), req.IDs)
	if err != nil {
		writeCardErr(c, err)
		return
	}
	localFailed := setLocalCDKStatuses(res.Enabled, "unused")
	u, _ := c.Get("username")
	username, _ := u.(string)
	db.WriteAudit(username, "cardplatform_batch_enable_cdk",
		"ok="+strconv.Itoa(res.EnabledCount)+" fail="+strconv.Itoa(res.FailedCount), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"enabled": res.Enabled, "failed": res.Failed,
		"enabled_count": res.EnabledCount, "failed_count": res.FailedCount,
		"local_failed": localFailed,
	})
}

// CardPlatformListCDKs GET /api/v1/admin/cardplatform/cdks?page=&page_size=&q=&status=&plan=
func CardPlatformListCDKs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	ps, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	cli := cardplatform.NewFromSettings()
	res, err := cli.ListCDKsQuery(c.Request.Context(), cardplatform.CDKListQuery{
		Page: page, PageSize: ps,
		Status: c.Query("status"), Plan: c.Query("plan"), Query: c.Query("q"),
	})
	if err != nil {
		writeCardErr(c, err)
		return
	}
	// 用本站发码缓存补全 code，便于列表点击复制完整码
	type rowOut struct {
		ID             int64  `json:"id"`
		Plan           string `json:"plan"`
		CodePrefix     string `json:"code_prefix"`
		Status         string `json:"status"`
		PaymentCountry string `json:"payment_country"`
		FeeAmountMinor int64  `json:"fee_amount_minor"`
		CreatedAt      string `json:"created_at"`
		Code           string `json:"code,omitempty"`
		FullCode       string `json:"full_code,omitempty"`
		HasFullCode    bool   `json:"has_full_code"`
		Note           string `json:"note,omitempty"`
	}
	ids := make([]int64, 0, len(res.List))
	for _, it := range res.List {
		ids = append(ids, it.ID)
	}
	notes := db.MapCardplatformCDKNotes(ids)
	out := make([]rowOut, 0, len(res.List))
	withFull := 0
	for _, it := range res.List {
		// 只按上游 id 精确取完整码，不按前缀兜底：前缀可能撞上别的码，会把另一张码的完整码展示出来
		full, err := db.CardplatformCDKCodeByUpstreamID(it.ID)
		ok := err == nil
		if !ok {
			if up := it.FullCodeText(); up != "" {
				full, ok = up, true
				_ = db.SaveCardplatformCDKCodeWithStatus(it.ID, up, it.CodePrefix, it.Plan, it.FeeAmountMinor, it.Status, it.PaymentCountry)
			}
		}
		row := rowOut{
			ID: it.ID, Plan: it.Plan, CodePrefix: it.CodePrefix, Status: it.Status,
			PaymentCountry: strings.ToUpper(strings.TrimSpace(it.PaymentCountry)),
			FeeAmountMinor: it.FeeAmountMinor, CreatedAt: it.CreatedAt,
			HasFullCode: ok, Note: notes[it.ID],
		}
		if ok {
			_ = db.UpdateCardplatformCDKRegion(it.ID, row.PaymentCountry)
			row.Code = full
			row.FullCode = full
			withFull++
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{
		"list":               out,
		"total":              res.Total,
		"full_code_on_page":  withFull,
		"full_code_in_store": db.CountCardplatformCDKCodes(),
		"server_stored":      true,
	})
}

// CardPlatformListCDKOrders GET /api/v1/admin/cardplatform/cdk-orders
// 对账列表：卡台 CDK 兑换订单；若上游暂未带 code_prefix/cdk_status，则本站按 cdk_id 补齐。
// 支持 page / page_size(1–100) / status / cdk_id / order_id。
func CardPlatformListCDKOrders(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	ps, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if ps < 1 {
		ps = 20
	}
	if ps > 100 {
		ps = 100
	}
	q := cardplatform.CDKOrderListQuery{
		Page:     page,
		PageSize: ps,
		Status:   strings.TrimSpace(c.Query("status")),
		CDKID:    int64Any(c.Query("cdk_id")),
		OrderID:  int64Any(c.Query("order_id")),
	}
	cli := cardplatform.NewFromSettings()
	raw, err := cli.ListCDKOrdersQuery(c.Request.Context(), q)
	if err != nil {
		writeCardErr(c, err)
		return
	}
	if len(raw) == 0 {
		c.JSON(http.StatusOK, gin.H{"list": []any{}, "total": 0, "page": page, "page_size": ps})
		return
	}
	enriched := enrichCDKOrderList(c, cli, raw)
	enriched["page"] = page
	enriched["page_size"] = ps
	c.JSON(http.StatusOK, enriched)
}

// CardPlatformDeleteCard DELETE /api/v1/admin/cardplatform/cards/:id
// 代理卡台 DELETE /cards/{id}：永久删卡并把卡内余额退回平台余额。
func CardPlatformDeleteCard(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "card id required"})
		return
	}
	cli := cardplatform.NewFromSettings()
	raw, err := cli.DeleteCard(c.Request.Context(), id)
	if err != nil {
		writeCardErr(c, err)
		return
	}
	u, _ := c.Get("username")
	username, _ := u.(string)
	db.WriteAudit(username, "cardplatform_delete_card", "card_id="+id, c.ClientIP())
	// 上游 data 可能为空（仅 msg），统一给前端可消费结构
	out := gin.H{"ok": true, "card_id": id, "message": "删卡成功，余额已退回"}
	if len(raw) > 0 && string(raw) != "null" {
		var m any
		if json.Unmarshal(raw, &m) == nil && m != nil {
			stripOrderValue(m)
			out["data"] = m
		}
	}
	c.JSON(http.StatusOK, out)
}

// CardPlatformGetCDKOrder GET /api/v1/admin/cardplatform/cdk-orders/:id
func CardPlatformGetCDKOrder(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id required"})
		return
	}
	cli := cardplatform.NewFromSettings()
	raw, err := cli.GetCDKOrder(c.Request.Context(), id)
	if err != nil {
		writeCardErr(c, err)
		return
	}
	if len(raw) == 0 {
		c.JSON(http.StatusOK, gin.H{})
		return
	}
	writeCDKOrderRaw(c, cli, raw)
}

// writeCDKOrderRaw 解析上游单笔订单并剔除敏感字段后回写；解析不了的不透传，回 502。
func writeCDKOrderRaw(c *gin.Context, cli *cardplatform.Client, raw []byte) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "卡台返回格式不对"})
		return
	}
	enrichOneCDKOrder(c, cli, m)
	c.JSON(http.StatusOK, m)
}

// enrichCDKOrderList 解析 list/total，按需从列码接口补 code_prefix / cdk_status。
func enrichCDKOrderList(c *gin.Context, cli *cardplatform.Client, raw json.RawMessage) gin.H {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		// 无法解析时原样包一层，避免吞数据
		return gin.H{"list": []any{}, "total": 0, "parse_error": true}
	}
	listAny, _ := envelope["list"].([]any)
	total := int64Any(envelope["total"])
	// 兜底：上游 total 缺失时至少不低于本页条数，避免前端「共 0 笔」把下一页锁死
	if total <= 0 && len(listAny) > 0 {
		total = int64(len(listAny))
	}
	// 收集需要补全的 cdk_id
	needCDK := false
	ids := map[int64]bool{}
	for _, it := range listAny {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if strAny(m["code_prefix"]) == "" || strAny(m["cdk_status"]) == "" {
			needCDK = true
		}
		if id := int64Any(m["cdk_id"]); id > 0 {
			ids[id] = true
		}
	}
	cdkMap := map[int64]cardplatform.CDKListItem{}
	if needCDK && len(ids) > 0 {
		// 拉若干页 CDK 建索引（白标量级通常不大）
		for page := 1; page <= 20; page++ {
			res, err := cli.ListCDKs(c.Request.Context(), page, 100)
			if err != nil || res == nil || len(res.List) == 0 {
				break
			}
			for _, item := range res.List {
				cdkMap[item.ID] = item
			}
			if len(res.List) < 100 || len(cdkMap) >= res.Total {
				break
			}
		}
	}
	outList := make([]any, 0, len(listAny))
	for _, it := range listAny {
		m, ok := it.(map[string]any)
		if !ok {
			outList = append(outList, it)
			continue
		}
		if id := int64Any(m["cdk_id"]); id > 0 {
			if item, ok := cdkMap[id]; ok {
				if strAny(m["code_prefix"]) == "" {
					m["code_prefix"] = item.CodePrefix
				}
				if strAny(m["cdk_status"]) == "" {
					m["cdk_status"] = item.Status
				}
			}
		}
		stripOrderCardNumber(m)
		// 归一化 CDK 生命周期展示字段
		m["cdk_lifecycle"] = cdkLifecycleLabel(strAny(m["cdk_status"]), strAny(m["status"]), strAny(m["service_fee_status"]))
		outList = append(outList, m)
	}
	return gin.H{"list": outList, "total": total}
}

func enrichOneCDKOrder(c *gin.Context, cli *cardplatform.Client, m map[string]any) {
	id := int64Any(m["cdk_id"])
	if id > 0 && (strAny(m["code_prefix"]) == "" || strAny(m["cdk_status"]) == "") {
		for page := 1; page <= 20; page++ {
			res, err := cli.ListCDKs(c.Request.Context(), page, 100)
			if err != nil || res == nil {
				break
			}
			found := false
			for _, item := range res.List {
				if item.ID == id {
					if strAny(m["code_prefix"]) == "" {
						m["code_prefix"] = item.CodePrefix
					}
					if strAny(m["cdk_status"]) == "" {
						m["cdk_status"] = item.Status
					}
					found = true
					break
				}
			}
			if found || len(res.List) < 100 {
				break
			}
		}
	}
	stripOrderCardNumber(m)
	m["cdk_lifecycle"] = cdkLifecycleLabel(strAny(m["cdk_status"]), strAny(m["status"]), strAny(m["service_fee_status"]))
}

// stripOrderCardNumber 卡台订单透传给前端前递归去掉完整卡号（card_number / pan）和 CVV、有效期，
// 嵌套的 map、slice 都处理；同一层缺 card_last_four 时用卡号后 4 位补上。
func stripOrderCardNumber(m map[string]any) {
	stripOrderValue(m)
}

func stripOrderValue(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			switch {
			case db.SensitiveCardFieldDrop(k):
				delete(t, k)
			case db.SensitiveCardFieldPAN(k):
				delete(t, k)
				if strAny(t["card_last_four"]) != "" {
					continue
				}
				digits := make([]byte, 0, 19)
				for _, ch := range []byte(strAny(val)) {
					if ch >= '0' && ch <= '9' {
						digits = append(digits, ch)
					}
				}
				if len(digits) >= 4 {
					t["card_last_four"] = string(digits[len(digits)-4:])
				}
			default:
				stripOrderValue(val)
			}
		}
	case []any:
		for _, e := range t {
			stripOrderValue(e)
		}
	}
}

func cdkLifecycleLabel(cdkStatus, orderStatus, feeStatus string) string {
	switch strings.ToLower(strings.TrimSpace(cdkStatus)) {
	case "consumed":
		return "已消耗"
	case "reserved":
		return "预留中"
	case "unused":
		if strings.EqualFold(feeStatus, "released") ||
			strings.EqualFold(orderStatus, "declined") ||
			strings.EqualFold(orderStatus, "failed_precharge") ||
			strings.EqualFold(orderStatus, "cancelled") {
			return "已释放"
		}
		return "未使用"
	case "frozen":
		return "已冻结"
	case "disabled":
		return "已禁用"
	}
	if strings.EqualFold(feeStatus, "released") {
		return "服务费已释放"
	}
	if cdkStatus != "" {
		return cdkStatus
	}
	return "—"
}

func strAny(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	b, _ := json.Marshal(v)
	return strings.Trim(string(b), `"`)
}

func int64Any(v any) int64 {
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
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	default:
		return 0
	}
}

// clearSessionAfterCompleted 订单完成后清空会话。best-effort：失败（如旧明文行缺主密钥）只记日志，由定时清理兜底。
func clearSessionAfterCompleted(code, token string) {
	var err error
	if code != "" {
		err = db.ClearCDKSessionByCode(code)
	} else {
		err = db.ClearCDKSessionByToken(token)
	}
	if err != nil {
		log.Printf("[cdk-session] 订单完成后清空会话失败: %v", err)
	}
}

// PublicCDKPlans GET /api/v1/public/cdk/plans
const docsDefaultNote = "★这是文档默认兜底价，不是你的账户实时价★：配置卡台 API Key 后才会返回实时值。" +
	"档位是否开放、点数的比索付款价，一律以卡台实时返回为准。"

// docsDefaultRegistry 未配置 API Key / 卡台不可达时的参考价目表。
//
// ★点数必须带上 checkout_amount_minor★：点数的 $0.10 只是我们的服务费，
// 代理真正要垫的是那笔比索付款（₱565 … ₱56,500）。只列服务费的话，
// 代理会把「一张 ₱56,500 的码」当成一毛钱的东西发出去。
func docsDefaultRegistry() []cardplatform.SellablePlan {
	return []cardplatform.SellablePlan{
		{Key: "plus", Label: "Plus", Flow: "direct", SortOrder: 2, ServiceFeeUsdMinor: 100, ServiceFeeUSD: 1},
		{Key: "pro_5x", Label: "Pro 5x", Flow: "direct", SortOrder: 3, ServiceFeeUsdMinor: 500, ServiceFeeUSD: 5},
		{Key: "pro_50x", Label: "Pro 50x", Flow: "direct", SortOrder: 5, ServiceFeeUsdMinor: 1000, ServiceFeeUSD: 10},
		{Key: "pro_20x", Label: "Pro", Flow: "plus_upgrade", SortOrder: 4, ServiceFeeUsdMinor: 1000, ServiceFeeUSD: 10},
		{Key: "credit250", Label: "Codex 点数 250", Flow: "credit", SortOrder: 5, IsCredit: true,
			RequiresActiveSubscription: true, ServiceFeeUsdMinor: 10, ServiceFeeUSD: 0.1,
			CheckoutCurrency: "PHP", CheckoutAmountMinor: 56500},
		{Key: "credit500", Label: "Codex 点数 500", Flow: "credit", SortOrder: 6, IsCredit: true,
			RequiresActiveSubscription: true, ServiceFeeUsdMinor: 10, ServiceFeeUSD: 0.1,
			CheckoutCurrency: "PHP", CheckoutAmountMinor: 113000},
		{Key: "credit1000", Label: "Codex 点数 1000", Flow: "credit", SortOrder: 7, IsCredit: true,
			RequiresActiveSubscription: true, ServiceFeeUsdMinor: 10, ServiceFeeUSD: 0.1,
			CheckoutCurrency: "PHP", CheckoutAmountMinor: 226000},
		// 大额三档（2026-09-26 加）。菲区点数线性按件计价：₱2.26 × 数量。
		// 服务费 15 = 卡台注册表现值；上面三档写 10 是历史漂移，本次不动。
		// ★25000 档代理要垫 ₱56,500（约 $900+）★，别当成一毛钱的码发出去。
		{Key: "credit2500", Label: "Codex 点数 2500", Flow: "credit", SortOrder: 8, IsCredit: true,
			RequiresActiveSubscription: true, ServiceFeeUsdMinor: 15, ServiceFeeUSD: 0.15,
			CheckoutCurrency: "PHP", CheckoutAmountMinor: 565000},
		{Key: "credit5000", Label: "Codex 点数 5000", Flow: "credit", SortOrder: 9, IsCredit: true,
			RequiresActiveSubscription: true, ServiceFeeUsdMinor: 15, ServiceFeeUSD: 0.15,
			CheckoutCurrency: "PHP", CheckoutAmountMinor: 1130000},
		{Key: "credit25000", Label: "Codex 点数 25000", Flow: "credit", SortOrder: 10, IsCredit: true,
			RequiresActiveSubscription: true, ServiceFeeUsdMinor: 15, ServiceFeeUSD: 0.15,
			CheckoutCurrency: "PHP", CheckoutAmountMinor: 5650000},
	}
}

func docsDefaultPlans() map[string]cardplatform.SellablePlan {
	out := map[string]cardplatform.SellablePlan{}
	for _, p := range docsDefaultRegistry() {
		out[p.Key] = p
	}
	return out
}

// 公开展示服务费参考价（不暴露 API Key；若未配置 Key 则返回文档默认价）
func PublicCDKPlans(c *gin.Context) {
	if c.Query("product") == "x" {
		// X has no safe GPT fallback catalogue, including on old upstreams.
		plans, err := cardplatform.NewFromSettings().GetPlans(c.Request.Context(), "x")
		if err != nil {
			writeCardErr(c, err)
			return
		}
		rows := plans.SellablePlans()
		items := map[string]cardplatform.SellablePlan{}
		for _, p := range rows {
			items[p.Key] = p
		}
		c.JSON(http.StatusOK, gin.H{"version": plans.Version, "plans": items, "registry": rows, "payment_regions": plans.PaymentRegions})
		return
	}
	cli := cardplatform.NewFromSettings()
	cfg := cardplatform.LoadConfig()
	if cfg.APIKey == "" {
		c.JSON(http.StatusOK, gin.H{
			"version":  0,
			"source":   "docs_default",
			"plans":    docsDefaultPlans(),
			"registry": docsDefaultRegistry(),
			"note":     docsDefaultNote,
		})
		return
	}
	plans, err := cli.GetPlans(c.Request.Context(), c.Query("product"))
	if err != nil {
		// 降级文档默认
		c.JSON(http.StatusOK, gin.H{
			"version":  0,
			"source":   "docs_default_fallback",
			"error":    err.Error(),
			"plans":    docsDefaultPlans(),
			"registry": docsDefaultRegistry(),
			"note":     docsDefaultNote,
		})
		return
	}
	// 同 CardPlatformPlans：只公开真能买到的档位。
	// 公开页比后台更不能乱列——列了 Claude，客户会照着去问「怎么买不到」。
	sellable := plans.SellablePlans()
	m := map[string]any{}
	for _, p := range sellable {
		m[p.Key] = p
	}
	c.JSON(http.StatusOK, gin.H{
		"version": plans.Version,
		"source":  "cardplatform_live",
		"plans":   m,
		// 档位展示顺序/文案/性质：前端据此渲染，不再维护自己的档位清单
		"registry": sellable,
	})
}

// CardPlatformSetCDKNote PUT /api/v1/admin/cardplatform/cdks/:id/note
// 本站备注（不写卡台）；空 note 表示清空。
func CardPlatformSetCDKNote(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if err := db.SetCardplatformCDKNote(id, req.Note); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	u, _ := c.Get("username")
	username, _ := u.(string)
	action := "cardplatform_cdk_note_set"
	if strings.TrimSpace(req.Note) == "" {
		action = "cardplatform_cdk_note_clear"
	}
	db.WriteAudit(username, action, "id="+strconv.FormatInt(id, 10), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": id, "note": db.GetCardplatformCDKNote(id)})
}

// CardPlatformBatchSetCDKNote POST /api/v1/admin/cardplatform/cdks/batch-note
// body: { ids: number[], note: string }  note 为空则批量清空。
func CardPlatformBatchSetCDKNote(c *gin.Context) {
	var req struct {
		IDs  []int64 `json:"ids"`
		Note string  `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids 必填"})
		return
	}
	if len(req.IDs) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "单次最多 200 条"})
		return
	}
	okN, failed, err := db.BatchSetCardplatformCDKNotes(req.IDs, req.Note)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	u, _ := c.Get("username")
	username, _ := u.(string)
	cleared := strings.TrimSpace(req.Note) == ""
	action := "cardplatform_batch_note_set"
	if cleared {
		action = "cardplatform_batch_note_clear"
	}
	db.WriteAudit(username, action,
		"ok="+strconv.Itoa(okN)+" fail="+strconv.Itoa(len(failed)), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"ok": true, "updated_count": okN, "failed": failed, "failed_count": len(failed),
		"cleared": cleared, "note": strings.TrimSpace(req.Note),
	})
}

// CardPlatformBatchClearCDKNote POST /api/v1/admin/cardplatform/cdks/batch-clear-note
// body: { ids: number[] }
func CardPlatformBatchClearCDKNote(c *gin.Context) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids 必填"})
		return
	}
	if len(req.IDs) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "单次最多 200 条"})
		return
	}
	n, err := db.ClearCardplatformCDKNotes(req.IDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	u, _ := c.Get("username")
	username, _ := u.(string)
	db.WriteAudit(username, "cardplatform_batch_note_clear",
		"cleared="+strconv.Itoa(n)+" requested="+strconv.Itoa(len(req.IDs)), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"ok": true, "cleared_count": n, "requested": len(req.IDs)})
}

// setLocalCDKStatuses 把卡台已成功的禁用/启用同步到本地缓存（E-8）。
// 卡台那边已经生效，这里写库失败不能再报成整单失败，但要记日志并通过 local_failed 返回给调用方。
func setLocalCDKStatuses(ids []int64, status string) []int64 {
	failed := []int64{}
	for _, id := range ids {
		if err := db.UpdateCardplatformCDKStatus(id, status); err != nil {
			log.Printf("cardplatform cdk %d: 本地状态同步为 %s 失败: %v", id, status, err)
			failed = append(failed, id)
		}
	}
	return failed
}
