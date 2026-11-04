package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
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

// issueProduct 发码产品线：显式传入优先（只认 gpt / x / grok / claude），否则按档位键推断。
func issueProduct(explicit, plan string) string {
	switch p := strings.ToLower(strings.TrimSpace(explicit)); p {
	case "gpt", "x", "grok", "claude":
		return p
	}
	if cardplatform.IsXPremiumPlan(plan) {
		return "x"
	}
	if cardplatform.IsGrokPlan(plan) {
		return "grok"
	}
	if cardplatform.IsClaudePlan(plan) {
		return "claude"
	}
	return "gpt"
}

// zovoCardIssueCDKs 向卡台发码。入口和服务商判断在 CardPlatformIssueCDKs。
func zovoCardIssueCDKs(c *gin.Context, req cdkIssueRequest) {
	plan := strings.TrimSpace(req.Plan)
	if plan == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "plan is required"})
		return
	}
	// ★档位以卡台实时下发为准，不要在这里写死白名单★
	// 写死的话，卡台每开一个新档位（Codex 点数 credit250/500/1000）这里都会 400 挡下，
	// 代理明明有权限发码却发不出来，而且报错还指向一个过期的档位清单。
	//
	// 但「实时下发」≠「定价表里有就能卖」：卡台透传的是 ACC 的整张定价表，
	// 里面有 claude_*（没有 CDK 兑换流程）也有 enabled=false 的档（兑换时被 ACC 挡）。
	// 按 SellableKeys 校验，跟界面上能看到的是同一份，不会出现「看得见发不出」
	// 或者「发得出兑不掉」。
	product := issueProduct(req.Product, plan)
	if product == "grok" {
		plan = cardplatform.GrokBarePlan(plan)
	}
	if product == "claude" {
		plan = cardplatform.ClaudeBarePlan(plan)
	}
	if cli := cardplatform.NewFromSettings(); cli != nil {
		if plans, err := cli.GetPlans(c.Request.Context(), product); err == nil && plans != nil && len(plans.Plans) > 0 {
			sellable := plans.SellableKeys()
			if len(sellable) > 0 && !sellable[plan] {
				known := make([]string, 0, len(sellable))
				for k := range sellable {
					known = append(known, k)
				}
				sort.Strings(known)
				reason := "unknown plan"
				if _, exists := plans.Plans[plan]; exists {
					reason = "plan not open for CDK（卡台未开放本档位，或 ACC 定价里是停用状态）"
				}
				c.JSON(http.StatusBadRequest, gin.H{"error": reason + ": " + plan + "; available: " + strings.Join(known, " | ")})
				return
			}
		}
		// 拿不到档位表（卡台不可用/未配置）时不拦：交给卡台发码时校验，
		// 避免因为一次网络抖动就让代理发不了码。
	}
	if !req.FundingConfirmed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "funding_confirmed must be true（确认承担兑换时开卡/充值/订阅实付）"})
		return
	}
	if req.Count < 1 {
		req.Count = 1
	}
	if req.Count > cardplatform.MaxIssueCount {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("count max %d", cardplatform.MaxIssueCount)})
		return
	}

	cli := cardplatform.NewFromSettings()
	idem, ok := issueIdempotencyKey(c)
	if !ok {
		return
	}
	// 本站选卡配置 → 发码偏好（跳过未启动卡头；ch1 等历史写法会归一成 one）
	var issuePrefs []cardplatform.IssueCardPref
	pref, hasSitePref := issuePrefFromSite()
	payCountry := strings.ToUpper(strings.TrimSpace(req.PaymentCountry))
	if product == "grok" {
		payCountry = "US"
	}
	if product == "claude" {
		payCountry = "US"
	}
	if product == "x" {
		var regionErr error
		payCountry, regionErr = cardplatform.XPaymentCountry(payCountry)
		if regionErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": regionErr.Error()})
			return
		}
	}
	// ★没有本站选卡配置时也要把地区带上★：地区和选卡偏好是两件独立的事，
	// 用同一个 pref 结构只是顺路。写成「有选卡配置才传 pref」会让
	// 「没配选卡、但指定了智利」这种组合静默退回菲律宾——码发出去了，
	// 区却不对，而且一切正常无报错，直到用户兑换时按 PHP 扣了款才看得出来。
	if hasSitePref || payCountry != "" {
		pref.PaymentCountry = payCountry
		issuePrefs = append(issuePrefs, pref)
	}
	wirePlan := plan
	if product == "grok" {
		wirePlan = cardplatform.GrokIssuePlan(plan)
	}
	if product == "claude" {
		wirePlan = cardplatform.ClaudeIssuePlan(plan)
	}
	issue := func(ctx context.Context) (*cardplatform.IssueCDKResult, error) {
		if len(issuePrefs) > 0 {
			return cli.IssueCDKs(ctx, wirePlan, req.Count, idem, issuePrefs[0])
		}
		return cli.IssueCDKs(ctx, wirePlan, req.Count, idem)
	}
	res, err := issue(c.Request.Context())
	replayed := false
	if err != nil && !issueDefinitelyFailed(err) && c.Request.Context().Err() == nil {
		// 结果不明（超时、断连、5xx、同键处理中）：卡台可能已经出码。唯一能对上「这一次」的是幂等键：
		// 卡台同键只执行一次、重放原样返回首次结果，所以只用同一个键重放一次，拿回来的一定是本批。
		// ★绝不按时间窗扫卡台列表「找回」★：列表里没有幂等键，同一时间别的管理员、代理换码发出的码也在里面，
		// 那样会把别人的码当成本批交出去，一码两卖（审计 C-5 / E-1）。
		rctx, cancel := context.WithTimeout(c.Request.Context(), issueReplayTimeout)
		res, err = issue(rctx)
		cancel()
		replayed = err == nil
	}
	if err != nil {
		if issueDefinitelyFailed(err) {
			writeCardErr(c, err)
			return
		}
		// 重放也没拿到结果：不给任何码，也不入库。码若已出，仍在卡台库存里属于这个幂等键，
		// 操作者用同一个键再点一次（前端失败时不换键）就会原样拿回；不会多发。
		log.Printf("[cdk-issue] 发码结果不明 plan=%s count=%d: %v", plan, req.Count, err)
		c.JSON(http.StatusBadGateway, gin.H{
			"error":          "卡台发码结果不明（已用同一个幂等键重试一次，仍没拿到结果）：" + err.Error(),
			"result_unknown": true,
			"upstream":       true,
		})
		return
	}
	u, _ := c.Get("username")
	username, _ := u.(string)
	prefNote := ""
	if len(issuePrefs) > 0 {
		prefNote = " pref=" + issuePrefs[0].Issuer + "/" + issuePrefs[0].SegmentKey
	}
	// 地区同时记录到发码审计和本站完整码缓存。
	if payCountry != "" {
		prefNote += " region=" + payCountry
	}
	if replayed {
		prefNote += " replayed=1"
	}
	db.WriteAudit(username, "cardplatform_issue_cdk", "plan="+plan+" count="+strconv.Itoa(req.Count)+prefNote, c.ClientIP())
	// 规范化：保证前端总能拿到完整 code 字段；绝不把 code_prefix 填进 code
	issued := make([]gin.H, 0, len(res.Issued))
	stored, storeFailed := 0, 0
	for _, it := range res.Issued {
		code := strings.TrimSpace(it.Code)
		prefix := strings.TrimSpace(it.CodePrefix)
		if code == "" {
			// 防御上游异常：只回了前缀时仍原样暴露前缀字段，但不伪造 code
			issued = append(issued, gin.H{
				"id": it.ID, "code": "", "plan": it.Plan,
				"code_prefix": prefix, "fee_amount_minor": it.FeeAmountMinor,
				"payment_country": payCountry,
				"incomplete":      true, "stored": false,
			})
			continue
		}
		if prefix == "" && len(code) >= 14 {
			prefix = code[:14]
		}
		// 本站 SQLite 持久化完整码（卡台列表只回 prefix）
		storedOK := false
		if err := db.SaveCardplatformCDKCode(it.ID, code, prefix, it.Plan, it.FeeAmountMinor, payCountry); err != nil {
			storeFailed++
			log.Printf("[cdk-issue] save full code failed id=%d prefix=%s: %v", it.ID, prefix, err)
		} else {
			stored++
			storedOK = true
		}
		issued = append(issued, gin.H{
			"id": it.ID, "code": code, "plan": it.Plan,
			"code_prefix": prefix, "fee_amount_minor": it.FeeAmountMinor,
			"payment_country": payCountry,
			"code_length":     len(code),
			"full_code":       code,
			"stored":          storedOK,
			"has_full_code":   true,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"provider":              string(provider.ZovoCard),
		"provider_display_name": provider.DisplayName(provider.ZovoCard),
		"requested":             res.Requested,
		"issued":                issued,
		"count":                 len(issued),
		"stored_count":          stored,
		"store_failed":          storeFailed,
		"server_stored":         true,
		"replayed":              replayed,
	})
}

// issueReplayTimeout 是发码结果不明时用同一个幂等键重放一次的时限。卡台遇到同键请求仍在处理时最多等 30 秒再回 409，
// 这里留一点余量；超时同样按「结果不明」报给操作者。
const issueReplayTimeout = 45 * time.Second

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
			lk := strings.ToLower(k)
			switch {
			case orderDropKeys[lk]:
				delete(t, k)
			case lk == "card_number" || lk == "cardnumber" || lk == "pan":
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

var orderDropKeys = map[string]bool{
	"cvv": true, "cvv2": true, "cvc": true, "cvc2": true, "security_code": true,
	"expire": true, "expiry": true, "expiry_date": true, "expire_date": true,
	"expiration": true, "expiration_date": true,
	"exp_month": true, "exp_year": true, "expire_month": true, "expire_year": true,
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

// ---- 公开兑换 BFF（浏览器只打本站，本站转发卡台）----

func deviceFrom(c *gin.Context) string {
	if d := strings.TrimSpace(c.GetHeader("X-Redemption-Device")); d != "" {
		return d
	}
	return c.GetHeader("User-Agent")
}

func proxyPublicJSON(c *gin.Context, status int, raw json.RawMessage) {
	if len(raw) == 0 {
		c.Status(status)
		return
	}
	c.Data(status, "application/json; charset=utf-8", raw)
}

// zovoCardPreview 是 ZovoCard 的预览：转发卡台。入口和分派在 PublicCDKPreview。
func zovoCardPreview(c *gin.Context, body map[string]any) {
	code := str(body["code"])
	cli := cardplatform.NewFromSettings()
	st, raw, err := cli.Preview(c.Request.Context(), code, deviceFrom(c))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	// 成功时记下 code ↔ redemption_token，供后续绑定 session / 账单查卡密
	if st >= 200 && st < 300 {
		if tok := extractJSONString(raw, "redemption_token", "token"); tok != "" {
			_ = db.BindCDKRedemptionToken(string(provider.ZovoCard), code, tok)
		}
		// 嵌套 data
		if tok := extractJSONNestedString(raw, "data", "redemption_token"); tok != "" {
			_ = db.BindCDKRedemptionToken(string(provider.ZovoCard), code, tok)
		}
	}
	proxyPublicJSON(c, st, raw)
}

// zovoCardPreflight 是 ZovoCard 的预检：转发卡台并绑定会话。入口、分派和跨服务商互斥在 PublicCDKPreflight。
func zovoCardPreflight(c *gin.Context, body map[string]any) {
	cli := cardplatform.NewFromSettings()
	st, raw, err := cli.Preflight(c.Request.Context(), body, deviceFrom(c))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	// 预检成功：把 credential.session 绑到卡密（账单页可凭卡密查）。
	// ★只在卡台确认预检通过时写★（审计 F-2）：这份绑定同时是跨服务商互斥的依据，
	// 伪造的码和会话只要能写进来，就能让别人的邮箱 15 分钟内兑换不了别家的码，而且可以无限续。
	tok := str(body["redemption_token"])
	if tok == "" {
		tok = extractJSONString(raw, "redemption_token", "token")
	}
	code := str(body["code"])
	if code == "" {
		if found, err := db.FindCodeByRedemptionToken(tok); err == nil && found != "" {
			code = found
		}
	}
	sess := extractCredentialSession(body["credential"])
	bindable := sess != "" && !cardplatform.IsXPremiumCredential(sess) && !cardplatform.IsGrokCredential(sess) && (code != "" || tok != "")
	if !bindable {
		if st >= 200 && st < 300 {
			log.Printf("[cdk-preflight] no session to bind (mode may be mailbox) tok=%s", shortTok(tok))
		}
	} else if ok, email, why := preflightVerdict(st, raw, sess); !ok {
		log.Printf("[cdk-preflight] 不写会话绑定 code=%s tok=%s: %s", shortTok(code), shortTok(tok), why)
	} else if err := db.BindCDKSessionAs(string(provider.ZovoCard), code, tok, sess, email); err != nil {
		// 这份副本只用来记账号邮箱；存不下不影响兑换（会话已随预检发给卡台），只是之后凭卡密查不到账单
		log.Printf("[cdk-preflight] bind session failed code=%s tok=%s: %v", shortTok(code), shortTok(tok), err)
		if errors.Is(err, secretbox.ErrNoMasterKey) || errors.Is(err, secretbox.ErrBadMasterKey) {
			opsAlertDaily("cdk-session-nokey", "APP_MASTER_KEY 缺失或不合法：兑换预检的用户会话已拒绝保存（不影响兑换），这些卡密之后无法凭卡密查账单。", time.Now())
		}
	}
	proxyPublicJSON(c, st, raw)
}

// preflightVerdict 判断卡台这次预检是否通过，通过了才能把会话写进绑定；同时给出绑定该记的账号邮箱。
//   - HTTP 非 2xx、响应不是 JSON 对象、envelope 的 code 不为 0：卡台没有通过预检（码不存在、会话无效等），不写。
//     没有通过预检就拿不到预检令牌，也就建不了单，跨服务商互斥用不着这份绑定。
//   - 通过时，邮箱优先用卡台核验过的那个（data.email 等）：会话 JSON 里的 user.email 是用户自己贴的，
//     可以拿自己的 sessionToken 配上别人的邮箱。卡台没给邮箱时才用会话里写的。
//
// 返回 false 时附带原因（不含邮箱、会话），供日志使用。
func preflightVerdict(status int, raw json.RawMessage, sess string) (ok bool, email, why string) {
	if status < 200 || status >= 300 {
		return false, "", fmt.Sprintf("卡台预检未通过（HTTP %d）", status)
	}
	var env map[string]any
	if json.Unmarshal(raw, &env) != nil || env == nil {
		return false, "", "卡台预检响应不是 JSON 对象"
	}
	if code, isNum := env["code"].(float64); isNum && code != 0 {
		return false, "", fmt.Sprintf("卡台预检未通过（code=%v）", code)
	}
	scopes := []map[string]any{env}
	if data, isMap := env["data"].(map[string]any); isMap {
		scopes = append([]map[string]any{data}, scopes...)
	}
	for _, m := range scopes {
		for _, k := range []string{"email", "account_email"} {
			if v, _ := m[k].(string); strings.TrimSpace(v) != "" {
				verified := strings.TrimSpace(v)
				if claimed := db.ExtractSessionEmail(sess); claimed != "" && !strings.EqualFold(claimed, verified) {
					log.Printf("[cdk-preflight] 会话里写的邮箱与卡台核验的不一致，绑定按卡台核验的邮箱记")
				}
				return true, verified, ""
			}
		}
	}
	return true, db.ExtractSessionEmail(sess), ""
}

// zovoCardRedeem 是 ZovoCard 的兑换：转发卡台。入口、分派和跨服务商互斥在 PublicCDKRedeem。
func zovoCardRedeem(c *gin.Context, body map[string]any) {
	// 有选卡配置就向卡台声明 strict，避免被卡台自己的 537872/星链级联盖过。
	injectRedeemCardPolicy(body)
	// 本站坏卡黑名单 → 本单排除这些卡：CDK 走 CDK 自己的选卡规则。实时读黑名单、纯选卡维度
	// 排除,卡台不冻结这些卡(卡台直充用户依旧可用)。拉黑即时生效,无需固化/对账。
	if _, exists := body["exclude_card_ids"]; !exists {
		if ids, err := db.ListActiveBlockedCardIDs(); err == nil && len(ids) > 0 {
			body["exclude_card_ids"] = ids
		}
	}
	cli := cardplatform.NewFromSettings()
	st, raw, err := cli.Redeem(c.Request.Context(), body, deviceFrom(c))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	proxyPublicJSON(c, st, raw)
}

// zovoCardResult 是 ZovoCard 按令牌查结果：转发卡台。入口和分派在 PublicCDKResult。
func zovoCardResult(c *gin.Context, token string) {
	cli := cardplatform.NewFromSettings()
	st, raw, err := cli.Result(c.Request.Context(), token, deviceFrom(c))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil || payload == nil {
		proxyPublicJSON(c, st, raw)
		return
	}
	// 卡健康观察（best-effort，不阻断用户）。它要用完整邮箱归因，响应另做一份打码的副本
	if st >= 200 && st < 300 {
		cdkCode := ""
		if found, err := db.FindCodeByRedemptionToken(token); err == nil {
			cdkCode = found
		}
		if isCompletedOrderStatus(publicResultStatus(payload)) {
			clearSessionAfterCompleted("", token)
		}
		safego.Go("观察卡健康（结果查询）", func() { observeFromPublicResult(context.Background(), payload, cdkCode) })
	}
	writePublicResult(c, st, raw, payload)
}

// zovoCardResultByCode 是 ZovoCard 按码查结果：用卡密反查本站绑定的 redemption_token，再转发卡台 result（刷新进度 / 任务查询用）。
// 入口和分派在 PublicCDKResultByCode。
func zovoCardResultByCode(c *gin.Context, code string) {
	bind, err := db.GetBindingByCDK(code)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询绑定失败"})
		return
	}
	if bind == nil || strings.TrimSpace(bind.RedemptionToken) == "" {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "未找到该卡密的兑换记录。请确认卡密正确；若刚在本机兑换过，请用同一浏览器打开兑换页（进度会自动恢复）。",
		})
		return
	}
	cli := cardplatform.NewFromSettings()
	st, raw, err := cli.Result(c.Request.Context(), bind.RedemptionToken, deviceFrom(c))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	// 附带本站元信息，前端可恢复轮询 token
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil || payload == nil {
		proxyPublicJSON(c, st, raw)
		return
	}
	payload["cdk_code"] = bind.CDKCode
	payload["redemption_token"] = bind.RedemptionToken
	// 含义是「能凭卡密查账单」：会话清空后邮箱还在，账单照样能查
	payload["has_session_binding"] = bind.HasSession || bind.AccountEmail != ""

	// 卡健康观察（webhook 未配时靠轮询学）。传副本：下面还要往 payload 写 invoice_url，
	// 同一个 map 并发读写会让进程直接崩溃（fatal error: concurrent map read and map write）
	if st >= 200 && st < 300 {
		snapshot, code := maps.Clone(payload), bind.CDKCode
		safego.Go("观察卡健康（按码查结果）", func() { observeFromPublicResult(context.Background(), snapshot, code) })
	}

	orderStatus := publicResultStatus(payload)
	if st >= 200 && st < 300 && isCompletedOrderStatus(orderStatus) {
		clearSessionAfterCompleted(bind.CDKCode, bind.RedemptionToken)
	}

	// 不再附带 accounthub 的账单链接（审计 F-1）：凭卡密就能调这个接口，账单链接只在用户当场粘贴 session 查账单时给。
	// 邮箱同样打码：完整邮箱只留给上面的卡健康观察做归因。这里加过本站字段，不能透传原字节。
	writePublicResult(c, st, nil, payload)
}

// publicResultEmailKeys 是公开结果响应里要打码的邮箱字段（任意层级）。
var publicResultEmailKeys = map[string]bool{"email": true, "account_email": true, "customer_email": true}

// maskPublicResultEmails 返回 payload 的深拷贝，其中所有邮箱字段按 maskEmailForAdmin 打码（与售后查询一致）；
// 第二个返回值表示有没有字段被打码。不改 payload 本身：原件还要交给卡健康观察的协程用完整邮箱归因，
// 原地改会和它并发读写同一个 map。
func maskPublicResultEmails(payload map[string]any) (map[string]any, bool) {
	out, changed := maskEmailsDeep(payload)
	m, _ := out.(map[string]any)
	return m, changed
}

func maskEmailsDeep(v any) (any, bool) {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			if s, ok := x.(string); ok && publicResultEmailKeys[k] && strings.TrimSpace(s) != "" {
				m[k], changed = maskEmailForAdmin(s), true
				continue
			}
			var c bool
			m[k], c = maskEmailsDeep(x)
			changed = changed || c
		}
		return m, changed
	case []any:
		l := make([]any, len(t))
		for i, x := range t {
			var c bool
			l[i], c = maskEmailsDeep(x)
			changed = changed || c
		}
		return l, changed
	}
	return v, false
}

// writePublicResult 写公开结果响应：没有邮箱字段时原样透传卡台的字节，有就写打码后的副本。
func writePublicResult(c *gin.Context, st int, raw json.RawMessage, payload map[string]any) {
	masked, changed := maskPublicResultEmails(payload)
	if !changed && raw != nil {
		proxyPublicJSON(c, st, raw)
		return
	}
	c.JSON(st, masked)
}

// publicResultStatus 取公开 result 响应里的订单状态（顶层优先，其次 order.status）。
func publicResultStatus(payload map[string]any) string {
	status := strAny(payload["status"])
	if status == "" {
		if order, ok := payload["order"].(map[string]any); ok {
			status = strAny(order["status"])
		}
	}
	return status
}

// isCompletedOrderStatus 判断订单是否已兑换完成（失败的单用户可能还要重试，不算）。
func isCompletedOrderStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "success", "done", "paid":
		return true
	}
	return false
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

func shortTok(tok string) string {
	tok = strings.TrimSpace(tok)
	if len(tok) <= 12 {
		return tok
	}
	return tok[:8] + "…"
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

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(toString(v))
}

func toString(v any) string {
	b, _ := json.Marshal(v)
	return strings.Trim(string(b), `"`)
}

func extractJSONString(raw json.RawMessage, keys ...string) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range keys {
		if s := str(m[k]); s != "" {
			return s
		}
	}
	return ""
}

func extractJSONNestedString(raw json.RawMessage, nest, key string) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	inner, _ := m[nest].(map[string]any)
	if inner == nil {
		return ""
	}
	return str(inner[key])
}

// extractCredentialSession 绑定账单用：优先保留完整 session 材料（sessionToken 或整段 JSON）。
// 纯 accessToken 不再接受（无法 force-refresh）。
func extractCredentialSession(cred any) string {
	m, ok := cred.(map[string]any)
	if !ok {
		return ""
	}
	if s := str(m["session"]); s != "" {
		if looksLikeBareAccessToken(s) {
			return ""
		}
		// JSON 无 sessionToken 则拒；以 { 开头却解析不了的也拒（不是会话，存它只会污染绑定）
		if strings.HasPrefix(s, "{") {
			var o map[string]any
			if json.Unmarshal([]byte(s), &o) != nil {
				return ""
			}
			st := str(o["sessionToken"])
			if st == "" {
				st = str(o["session_token"])
			}
			if st == "" {
				return ""
			}
		}
		return s
	}
	// 不再单独接受 accessToken 字段
	return ""
}

func looksLikeBareAccessToken(raw string) bool {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "eyJ") {
		return false
	}
	return len(strings.Split(s, ".")) == 3
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
