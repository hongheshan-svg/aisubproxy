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
		if len(orders) > 0 || err == nil {
			poolAudit(c, "apply_cards", fmt.Sprintf("product_id=%s card_holder_id=%s count=%d created=%d",
				req.ProductID, req.CardHolderID, req.Count, len(orders)))
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

func orderResult(o *cardpool.Order, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return o, nil
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
	AdminCardPoolFreezeCard = cardAction("freeze_card", func(c *gin.Context, s *cardpool.Service, id string) (any, error) {
		return orderResult(s.FreezeCard(c.Request.Context(), id, adminName(c)))
	})
	AdminCardPoolUnfreezeCard = cardAction("unfreeze_card", func(c *gin.Context, s *cardpool.Service, id string) (any, error) {
		return orderResult(s.UnfreezeCard(c.Request.Context(), id, adminName(c)))
	})
	AdminCardPoolCancelCard = cardAction("cancel_card", func(c *gin.Context, s *cardpool.Service, id string) (any, error) {
		return orderResult(s.CancelCard(c.Request.Context(), id, adminName(c)))
	})
	AdminCardPoolSweepCard = cardAction("sweep_card", func(c *gin.Context, s *cardpool.Service, id string) (any, error) {
		return orderResult(s.SweepCard(c.Request.Context(), id, adminName(c)))
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
			poolFail(c, err)
			return
		}
		poolAudit(c, "fund_card", fmt.Sprintf("card_id=%s order=%s amount_cents=%d", id, o.OutOrderNo, cents))
		c.JSON(http.StatusOK, o)
	})(c)
}

// AdminCardPoolSensitive 返回完整卡号；审计只记 card_id。
func AdminCardPoolSensitive(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		c.Header("Cache-Control", "no-store")
		id := c.Param("id")
		sv, err := s.ViewSensitive(c.Request.Context(), id, adminName(c))
		if err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, "view_sensitive", "card_id="+id)
		c.JSON(http.StatusOK, gin.H{"card_no": sv.CardNo, "cvv2": sv.CVV2, "expiry_date": sv.ExpiryDate})
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
		poolAudit(c, "confirm_failed", fmt.Sprintf("order=%s note=%s", no, note))
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

// AdminCardPoolPutSelection PUT /api/v1/admin/cardpool/selection：只改选卡字段，其余策略字段保持当前值。
// 请求体里没带的选卡字段也保持当前值。
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
		c.JSON(http.StatusOK, sel)
	})(c)
}
