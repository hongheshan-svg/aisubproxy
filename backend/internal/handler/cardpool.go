package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// cardPool 为运行中的卡池服务；未启动时为 nil。
var cardPool *cardpool.Service

// SetCardPool 注入卡池服务（服务启动时调用；测试可传 nil）。
func SetCardPool(s *cardpool.Service) { cardPool = s }

// MarsWallexClient 供卡池服务按当前设置构造 MarsWallex 客户端。
func MarsWallexClient() (*marswallex.Client, error) { return marsWallexClientFromSettings() }

// poolRoute 包装卡池接口：服务未启动时统一返回 503。
func poolRoute(fn func(c *gin.Context, s *cardpool.Service)) gin.HandlerFunc {
	return func(c *gin.Context) {
		s := cardPool
		if s == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "卡池未启动"})
			return
		}
		fn(c, s)
	}
}

// poolErrorStatus 把服务错误映射成 HTTP 状态码与响应体，不写日志。未识别的错误为 500「操作失败」。
func poolErrorStatus(err error) (int, gin.H) {
	var me *marswallex.Error
	switch {
	case errors.As(err, &me):
		if me.Reason == marswallex.ReasonConfig {
			return http.StatusServiceUnavailable, gin.H{"error": err.Error()}
		}
		return http.StatusBadGateway, gin.H{"error": err.Error(), "outcome": me.Outcome.String()}
	case errors.Is(err, cardpool.ErrBadRequest):
		return http.StatusBadRequest, gin.H{"error": err.Error()}
	case errors.Is(err, cardpool.ErrNotFound):
		return http.StatusNotFound, gin.H{"error": err.Error()}
	case errors.Is(err, cardpool.ErrCardBusy), errors.Is(err, cardpool.ErrLeaseInvalid), errors.Is(err, cardpool.ErrNoCard),
		errors.Is(err, cardpool.ErrInflight):
		return http.StatusConflict, gin.H{"error": err.Error()}
	case errors.Is(err, cardpool.ErrNoAccounts): // 建单前失败：不带 outcome，前端显示「未提交」
		return http.StatusBadGateway, gin.H{"error": err.Error()}
	case errors.Is(err, cardpool.ErrHolderAccepted): // 文案不含资料值，原样给前端，免得管理员重复提交
		return http.StatusInternalServerError, gin.H{"error": err.Error()}
	case errors.Is(err, cardpool.ErrMaybeApplied): // 只给固定提示，免得管理员重复提交；内部错误另记日志
		return http.StatusInternalServerError, gin.H{"error": cardpool.ErrMaybeApplied.Error()}
	}
	return http.StatusInternalServerError, gin.H{"error": "操作失败"}
}

// poolErrorBody 把服务错误映射成 HTTP 状态码与响应体，内部错误写日志。错误信息不含卡号、CVV2、验证码。
func poolErrorBody(c *gin.Context, err error) (int, gin.H) {
	status, body := poolErrorStatus(err)
	if status == http.StatusInternalServerError {
		log.Printf("cardpool api %s %s: %v", c.Request.Method, c.FullPath(), err)
	}
	return status, body
}

// poolFail 把服务错误映射成 HTTP 响应。
func poolFail(c *gin.Context, err error) {
	status, body := poolErrorBody(c, err)
	c.JSON(status, body)
}

func poolAudit(c *gin.Context, action, detail string) {
	db.WriteAudit(adminName(c), "cardpool."+action, detail, c.ClientIP())
}

// 卡池操作的审计口径：成功记 <action>；出错时只要平台可能已经生效就记 <action>_failed，明确没生效的
// （参数错、卡不存在、卡正忙、平台明确拒绝、请求没发出）不记。备注进审计统一截到 200 字（同付款侧 payNoteAuditMax）。

// poolMaybeApplied 判断直接调平台的操作出错时，平台那边是否可能已经生效：平台结果不明（超时、5xx 等），
// 平台已受理但本地写入失败（ErrHolderAccepted），或请求已发出、本地没记下结果（ErrMaybeApplied）。
func poolMaybeApplied(err error) bool {
	if errors.Is(err, cardpool.ErrMaybeApplied) || errors.Is(err, cardpool.ErrHolderAccepted) {
		return true
	}
	var me *marswallex.Error
	return errors.As(err, &me) && me.Outcome == marswallex.OutcomeUnknown
}

// poolResultText 是失败审计里的结果摘要，不带平台返回的 message（可能回显卡号或持卡人资料）。
func poolResultText(err error) string {
	var me *marswallex.Error
	switch {
	case errors.Is(err, cardpool.ErrHolderAccepted):
		return "平台已受理，本地写入失败"
	case errors.Is(err, cardpool.ErrMaybeApplied):
		return "已发出，本地没记下结果"
	case errors.As(err, &me):
		return fmt.Sprintf("结果不明（%s HTTP %d）", me.Reason, me.HTTPStatus)
	}
	return "结果不明"
}

// poolAuditFailed 用于直接调平台的操作（限额、子账户、持卡人）：平台可能已生效时记 <action>_failed，detail 后附结果摘要。
func poolAuditFailed(c *gin.Context, action, detail string, err error) {
	if poolMaybeApplied(err) {
		poolAudit(c, action+"_failed", detail+" result="+poolResultText(err))
	}
}

// poolAuditOrderFailed 用于单据类操作（开卡、注资、冻结、解冻、注销、清零、兑换、钱包转账）出错时：平台的结果记在单据里、
// 不以错误返回（结果不明时照常返回 unknown 的单据，审计见 orderAuditAction），返回的平台错误都来自发单前的查询
// （刷新卡、同步账户），所以出错时只有 ErrMaybeApplied（已发出、本地没记下）才记 <action>_failed。
func poolAuditOrderFailed(c *gin.Context, action, detail string, err error) {
	if errors.Is(err, cardpool.ErrMaybeApplied) {
		poolAudit(c, action+"_failed", detail+" result="+poolResultText(err))
	}
}

// orderAuditAction 是单据类操作提交成功后审计用的动作名：单据结果不明（state=unknown，平台可能已生效）记 <action>_failed，
// 和直接调平台的操作同一口径；其余（处理中、成功、平台明确失败、未发出）记 <action>。只记一条，detail 里都带单号和状态。
func orderAuditAction(action string, o *cardpool.Order) string {
	if o.State == cardpool.StateUnknown {
		return action + "_failed"
	}
	return action
}

// orderAuditSuffix 是单据类操作审计的后缀：单号和提交后的单据状态（平台结果不明时为 unknown）。
func orderAuditSuffix(o *cardpool.Order) string {
	return fmt.Sprintf(" order=%s state=%s", o.OutOrderNo, o.State)
}

// applyCardsAudit 决定批量开卡记哪条审计，返回动作名和 detail 后缀（动作名为空表示不记）：
// 返回的单据里有结果不明的，或最后一张已发出、本地没记下（ErrMaybeApplied），记 apply_cards_failed，后缀写结果不明的张数
// （算上没记下的那张）；否则建了单（或没出错）记 apply_cards；一张没建、也不可能已生效的不记。
func applyCardsAudit(orders []cardpool.Order, err error) (string, string) {
	unknown := 0
	for i := range orders {
		if orders[i].State == cardpool.StateUnknown {
			unknown++
		}
	}
	maybe := errors.Is(err, cardpool.ErrMaybeApplied)
	switch {
	case unknown > 0 || maybe:
		tail := ""
		if maybe {
			unknown++
			tail = " last_result=" + poolResultText(err)
		}
		return "apply_cards_failed", fmt.Sprintf(" unknown=%d", unknown) + tail
	case len(orders) > 0 || err == nil:
		return "apply_cards", ""
	}
	return "", ""
}

// poolNote 把进审计的备注去首尾空白并截到 200 字。
func poolNote(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > payNoteAuditMax {
		r = r[:payNoteAuditMax]
	}
	return string(r)
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func poolList[T any](c *gin.Context, items []T, total int, err error) {
	if err != nil {
		poolFail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": nonNil(items), "total": total})
}

func pageParams(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.Query("page"))
	size, _ := strconv.Atoi(c.Query("page_size"))
	return page, size
}

// parsePoolAmount 把字符串金额（^\d{1,12}(\.\d{1,2})?$，见 marswallex.ParseAmount）换算为分，须大于 0；
// 不合法时回 400 并返回 false。
func parsePoolAmount(c *gin.Context, s string) (int64, bool) {
	cents, err := marswallex.ParseAmount(strings.TrimSpace(s))
	if err != nil || cents <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "金额无效，须为大于 0 且最多两位小数的数字"})
		return 0, false
	}
	return cents, true
}

// bindPoolJSON 解析请求体；失败回 400。
func bindPoolJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体无效"})
		return false
	}
	return true
}

// ---------- 概览、持卡人、产品 ----------

func AdminCardPoolOverview(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		ov, err := s.Overview(c.Request.Context())
		if err != nil {
			poolFail(c, err)
			return
		}
		ov.Products = nonNil(ov.Products)
		c.JSON(http.StatusOK, ov)
	})(c)
}

func AdminCardPoolProducts(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		items, err := s.ListProducts(c.Request.Context())
		poolList(c, items, len(items), err)
	})(c)
}

func AdminCardPoolCardholders(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		items, err := s.ListCardholders(c.Request.Context())
		poolList(c, items, len(items), err)
	})(c)
}

func AdminCardPoolImportCardholder(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			CardHolderID string `json:"card_holder_id"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		h, err := s.ImportCardholder(c.Request.Context(), req.CardHolderID, adminName(c))
		if err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, "import_cardholder", "card_holder_id="+h.CardHolderID)
		c.JSON(http.StatusOK, h)
	})(c)
}

func AdminCardPoolRefreshCardholder(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		h, err := s.RefreshCardholder(c.Request.Context(), c.Param("id"))
		if err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, "refresh_cardholder", "card_holder_id="+h.CardHolderID)
		c.JSON(http.StatusOK, h)
	})(c)
}

// ---------- 卡 ----------

func AdminCardPoolCards(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		page, size := pageParams(c)
		items, total, err := s.ListCards(c.Request.Context(), cardpool.CardFilter{
			State: c.Query("state"), ProductID: c.Query("product_id"), Page: page, PageSize: size})
		poolList(c, items, total, err)
	})(c)
}

func AdminCardPoolCardDetail(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		d, err := s.CardDetail(c.Request.Context(), c.Param("id"))
		if err != nil {
			poolFail(c, err)
			return
		}
		d.Orders = nonNil(d.Orders)
		d.Transactions = nonNil(d.Transactions)
		c.JSON(http.StatusOK, d)
	})(c)
}

func AdminCardPoolApplyCards(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			ProductID    string `json:"product_id"`
			CardHolderID string `json:"card_holder_id"`
			Count        int    `json:"count"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		orders, err := s.ApplyCards(c.Request.Context(), req.ProductID, req.CardHolderID, req.Count, adminName(c))
		if action, tail := applyCardsAudit(orders, err); action != "" {
			poolAudit(c, action, fmt.Sprintf("product_id=%s card_holder_id=%s count=%d created=%d",
				req.ProductID, req.CardHolderID, req.Count, len(orders))+tail)
		}
		if err != nil {
			if len(orders) > 0 {
				// 已创建的单据必须让管理员看到，否则会误以为一张都没开而重试。
				status, body := poolErrorBody(c, err)
				body["orders"] = orders
				body["created"] = len(orders)
				c.JSON(status, body)
				return
			}
			poolFail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"orders": nonNil(orders)})
	})(c)
}

func AdminCardPoolImportCard(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			CardID       string `json:"card_id"`
			ProductID    string `json:"product_id"`
			CardHolderID string `json:"card_holder_id"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		card, err := s.ImportCard(c.Request.Context(), req.CardID, req.ProductID, req.CardHolderID, adminName(c))
		if err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, "import_card", fmt.Sprintf("card_id=%s product_id=%s", card.CardID, card.ProductID))
		c.JSON(http.StatusOK, card)
	})(c)
}

// AdminCardPoolPreviewCard 只读：导入前按卡 ID 查平台，返回卡信息和可选产品。只有前 6 位与后 4 位卡号，不写库、不审计。
func AdminCardPoolPreviewCard(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			CardID string `json:"card_id"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		p, err := s.PreviewCard(c.Request.Context(), req.CardID)
		if err != nil {
			poolFail(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, p)
	})(c)
}

// cardAction 生成「对一张卡做动作」的 POST 处理器；fn 返回的 payload 为空时回 {"ok":true}。
func cardAction(action string, fn func(c *gin.Context, s *cardpool.Service, id string) (any, error)) gin.HandlerFunc {
	return poolRoute(func(c *gin.Context, s *cardpool.Service) {
		id := c.Param("id")
		payload, err := fn(c, s, id)
		if err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, action, "card_id="+id)
		if payload == nil {
			payload = gin.H{"ok": true}
		}
		c.JSON(http.StatusOK, payload)
	})
}

// orderAction 生成「对一张卡提交一张单据」的 POST 处理器（冻结、解冻、注销、清零）：审计带单号和单据状态，
// 结果不明记 <action>_failed（见 orderAuditAction）；出错时按 poolAuditOrderFailed 记失败审计。
func orderAction(action string, fn func(c *gin.Context, s *cardpool.Service, id string) (*cardpool.Order, error)) gin.HandlerFunc {
	return poolRoute(func(c *gin.Context, s *cardpool.Service) {
		id := c.Param("id")
		o, err := fn(c, s, id)
		if err != nil {
			poolAuditOrderFailed(c, action, "card_id="+id, err)
			poolFail(c, err)
			return
		}
		poolAudit(c, orderAuditAction(action, o), "card_id="+id+orderAuditSuffix(o))
		c.JSON(http.StatusOK, o)
	})
}

var (
	AdminCardPoolRefreshCard = cardAction("refresh_card", func(c *gin.Context, s *cardpool.Service, id string) (any, error) {
		card, err := s.RefreshCard(c.Request.Context(), id)
		if err != nil {
			return nil, err
		}
		return card, nil
	})
	AdminCardPoolEnableCard = cardAction("enable_card", func(c *gin.Context, s *cardpool.Service, id string) (any, error) {
		return nil, s.EnableCard(c.Request.Context(), id, adminName(c))
	})
	AdminCardPoolFreezeCard = orderAction("freeze_card", func(c *gin.Context, s *cardpool.Service, id string) (*cardpool.Order, error) {
		return s.FreezeCard(c.Request.Context(), id, adminName(c))
	})
	AdminCardPoolUnfreezeCard = orderAction("unfreeze_card", func(c *gin.Context, s *cardpool.Service, id string) (*cardpool.Order, error) {
		return s.UnfreezeCard(c.Request.Context(), id, adminName(c))
	})
	AdminCardPoolCancelCard = orderAction("cancel_card", func(c *gin.Context, s *cardpool.Service, id string) (*cardpool.Order, error) {
		return s.CancelCard(c.Request.Context(), id, adminName(c))
	})
	AdminCardPoolSweepCard = orderAction("sweep_card", func(c *gin.Context, s *cardpool.Service, id string) (*cardpool.Order, error) {
		return s.SweepCard(c.Request.Context(), id, adminName(c))
	})
	AdminCardPoolLeaseCard = cardAction("lease_card", func(c *gin.Context, s *cardpool.Service, id string) (any, error) {
		l, err := s.AdminLease(c.Request.Context(), id, adminName(c))
		if err != nil {
			return nil, err
		}
		return gin.H{"card_id": l.CardID, "expires_at": l.ExpiresAt.Unix()}, nil
	})
	AdminCardPoolReleaseCard = cardAction("release_card", func(c *gin.Context, s *cardpool.Service, id string) (any, error) {
		return nil, s.AdminRelease(c.Request.Context(), id, adminName(c))
	})
)

func AdminCardPoolDisableCard(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			Reason string `json:"reason"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		id := c.Param("id")
		if err := s.DisableCard(c.Request.Context(), id, req.Reason, adminName(c)); err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, "disable_card", "card_id="+id)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})(c)
}

func AdminCardPoolFundCard(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			Amount string `json:"amount"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		cents, ok := parsePoolAmount(c, req.Amount)
		if !ok {
			return
		}
		id := c.Param("id")
		o, err := s.AdminFund(c.Request.Context(), id, cents, adminName(c))
		if err != nil {
			poolAuditOrderFailed(c, "fund_card", fmt.Sprintf("card_id=%s amount_cents=%d", id, cents), err)
			poolFail(c, err)
			return
		}
		poolAudit(c, orderAuditAction("fund_card", o), fmt.Sprintf("card_id=%s order=%s amount_cents=%d state=%s", id, o.OutOrderNo, cents, o.State))
		c.JSON(http.StatusOK, o)
	})(c)
}

func AdminCardPoolOTP(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		c.Header("Cache-Control", "no-store")
		id := c.Param("id")
		v, err := s.ViewOTP(c.Request.Context(), id, adminName(c))
		if err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, "view_otp", "card_id="+id)
		c.JSON(http.StatusOK, v)
	})(c)
}

// ---------- 单据、交易 ----------

func AdminCardPoolOrders(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		page, size := pageParams(c)
		items, total, err := s.ListOrders(c.Request.Context(), cardpool.OrderFilter{
			State: c.Query("state"), Kind: c.Query("kind"), CardID: c.Query("card_id"), Page: page, PageSize: size})
		poolList(c, items, total, err)
	})(c)
}

func AdminCardPoolRecheck(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		no := c.Param("no")
		o, err := s.Recheck(c.Request.Context(), no)
		if err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, "recheck_order", "order="+no)
		c.JSON(http.StatusOK, o)
	})(c)
}

func AdminCardPoolConfirmFailed(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			Note string `json:"note"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		note := strings.TrimSpace(req.Note)
		if note == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请填写备注"})
			return
		}
		no := c.Param("no")
		o, err := s.ConfirmFailed(c.Request.Context(), no, note, adminName(c))
		if err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, "confirm_failed", fmt.Sprintf("order=%s note=%s", no, poolNote(note)))
		c.JSON(http.StatusOK, o)
	})(c)
}

func AdminCardPoolTransactions(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		page, size := pageParams(c)
		items, total, err := s.ListTransactions(c.Request.Context(), cardpool.TxnFilter{
			CardID: c.Query("card_id"), Page: page, PageSize: size})
		poolList(c, items, total, err)
	})(c)
}

// ---------- 策略、健康 ----------

func AdminCardPoolGetPolicy(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		p, err := cardpool.LoadPolicy()
		if err != nil {
			poolFail(c, err)
			return
		}
		c.JSON(http.StatusOK, p)
	})(c)
}

// policyWriteMu 串行化卡池策略的两个保存接口（PUT /policy、PUT /selection）。
// 两者都是「读当前策略 → 合并 → 写整份 JSON」，不串行时会互相冲掉对方刚保存的字段。
var policyWriteMu sync.Mutex

func AdminCardPoolPutPolicy(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		// 选卡字段由「MarsWallex → 选卡规则」页单独保存（PUT /cardpool/selection），新的卡池页不再提交。
		// 请求体里没出现的选卡字段沿用当前值；出现了照常使用（升级前还开着的旧卡池页会整份提交）。
		// 按「键有没有出现」判断，不按零值判断。
		// 先读完请求体再加锁、读当前策略：读当前值不能早于另一页的保存，上传慢的请求也不占着锁。
		var raw map[string]json.RawMessage
		if c.ShouldBindBodyWith(&raw, binding.JSON) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求体无效"})
			return
		}
		var absent []string
		for _, k := range cardpool.SelectionKeys {
			if _, ok := raw[k]; !ok {
				absent = append(absent, k)
			}
		}
		policyWriteMu.Lock()
		defer policyWriteMu.Unlock()
		cur, curErr := cardpool.LoadPolicy()
		// 读不出当前策略时，没带的选卡字段无从沿用：拒绝保存，否则会被默认值顶掉
		// （plan_products 清空，MarsWallex 就停止供卡）。整份提交照旧，见下面资金上限的说明。
		if curErr != nil && len(absent) > 0 {
			poolFail(c, curErr)
			return
		}
		p := cardpool.DefaultPolicy()
		// 旧前端不认识资金上限字段，保存时不传：沿用当前值，不能被默认值悄悄改掉。
		// 其它字段不能这样打底（map 会被合并）。读不出来时 LoadPolicy 返回默认值，正好是安全的上限。
		p.MaxFundCents, p.MaxWalletOpCents, p.DailyFundCapCents = cur.MaxFundCents, cur.MaxWalletOpCents, cur.DailyFundCapCents
		p.DailyFundCountCap = cur.DailyFundCountCap
		p.ReplenishWhenUnselected = cur.ReplenishWhenUnselected
		if c.ShouldBindBodyWith(&p, binding.JSON) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求体无效"})
			return
		}
		p.CopySelection(cur, absent...)
		if p.PlanProducts == nil {
			p.PlanProducts = map[string][]string{}
		}
		if p.ProductTargets == nil {
			p.ProductTargets = map[string]cardpool.ProductTarget{}
		}
		if p.HolderIDs == nil {
			p.HolderIDs = []string{}
		}
		if err := cardpool.SavePolicy(p); err != nil {
			poolFail(c, err)
			return
		}
		// 审计记保存后的完整策略（只有产品、持卡人 ID 与数值参数，没有密钥）
		detail, err := json.Marshal(p)
		if err != nil {
			detail = []byte("updated")
		}
		poolAudit(c, "policy", string(detail))
		c.JSON(http.StatusOK, p)
	})(c)
}

func AdminCardPoolHealth(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		items, err := s.HealthReport(c.Request.Context())
		poolList(c, items, len(items), err)
	})(c)
}

func AdminCardPoolResume(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			ProductID string `json:"product_id"`
			PlanKey   string `json:"plan_key"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		if err := s.ResumeProduct(c.Request.Context(), req.ProductID, req.PlanKey, adminName(c)); err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, "resume_product", fmt.Sprintf("product_id=%s plan_key=%s", req.ProductID, req.PlanKey))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})(c)
}

// AdminReprocessMarsWallexWebhookEvent 让一条 Webhook 事件重新处理。
func AdminReprocessMarsWallexWebhookEvent(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "事件 ID 无效"})
			return
		}
		if err := s.Reprocess(c.Request.Context(), id, adminName(c)); err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, "reprocess_event", "event_id="+strconv.FormatInt(id, 10))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})(c)
}

// AdminCardPoolGetSelection GET /api/v1/admin/cardpool/selection：只读选卡字段。
func AdminCardPoolGetSelection(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		p, err := cardpool.LoadPolicy()
		if err != nil {
			poolFail(c, err)
			return
		}
		c.JSON(http.StatusOK, p.Selection())
	})(c)
}

// selectionSaved 是保存选卡规则的返回：选卡字段，加上调低单卡最大使用次数时将不再可用的卡数（见 cardpool.CountUsedUp）。
type selectionSaved struct {
	cardpool.Selection
	UnusableCards int `json:"unusable_cards,omitempty"`
}

// AdminCardPoolPutSelection PUT /api/v1/admin/cardpool/selection：只改选卡字段，其余策略字段保持当前值。
// 请求体里没带的选卡字段也保持当前值。调低单卡最大使用次数时，返回里带上将不再可用的卡数，由前端提示管理员手动处理。
func AdminCardPoolPutSelection(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		// 先读完请求体再加锁、读当前策略（同 PUT /policy）
		var body json.RawMessage
		if c.ShouldBindBodyWith(&body, binding.JSON) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求体无效"})
			return
		}
		policyWriteMu.Lock()
		defer policyWriteMu.Unlock()
		// 读不出当前策略时拒绝保存：否则其余字段会被默认值覆盖
		cur, err := cardpool.LoadPolicy()
		if err != nil {
			poolFail(c, err)
			return
		}
		sel := cur.Selection()
		// plan_products 不能绑定到已有的 map 上（会和原来的合并，删掉的套餐删不掉）
		sel.PlanProducts = nil
		if c.ShouldBindBodyWith(&sel, binding.JSON) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求体无效"})
			return
		}
		if sel.PlanProducts == nil {
			sel.PlanProducts = cur.PlanProducts
		}
		oldMaxUses := cur.MaxUsesPerCard
		cur.ApplySelection(sel)
		if err := cardpool.SavePolicy(cur); err != nil {
			poolFail(c, err)
			return
		}
		// 审计记保存后的选卡字段（只有产品 ID 与数值参数，没有密钥）
		detail, err := json.Marshal(sel)
		if err != nil {
			detail = []byte("updated")
		}
		poolAudit(c, "selection", string(detail))
		out := selectionSaved{Selection: sel}
		if sel.MaxUsesPerCard < oldMaxUses {
			// 已经保存成功：统计失败只少一条提示，不影响保存结果
			if n, err := s.CountUsedUp(c.Request.Context(), sel.MaxUsesPerCard); err != nil {
				log.Printf("[cardpool] 统计用满的卡失败: %v", err)
			} else {
				out.UnusableCards = n
			}
		}
		c.JSON(http.StatusOK, out)
	})(c)
}
