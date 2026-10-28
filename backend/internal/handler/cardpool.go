package handler

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
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

// poolFail 把服务错误映射成 HTTP 响应。错误信息不含卡号、CVV2、验证码。
func poolFail(c *gin.Context, err error) {
	var me *marswallex.Error
	switch {
	case errors.As(err, &me):
		if me.Reason == marswallex.ReasonConfig {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "outcome": me.Outcome.String()})
	case errors.Is(err, cardpool.ErrBadRequest):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, cardpool.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, cardpool.ErrCardBusy), errors.Is(err, cardpool.ErrLeaseInvalid), errors.Is(err, cardpool.ErrNoCard):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		log.Printf("cardpool api %s %s: %v", c.Request.Method, c.FullPath(), err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
	}
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
		cents, err := marswallex.ParseAmount(strings.TrimSpace(req.Amount))
		if err != nil || cents <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "金额无效，须为大于 0 且最多两位小数的数字"})
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

func AdminCardPoolPutPolicy(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		p := cardpool.DefaultPolicy()
		if !bindPoolJSON(c, &p) {
			return
		}
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
		poolAudit(c, "policy", "updated")
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
