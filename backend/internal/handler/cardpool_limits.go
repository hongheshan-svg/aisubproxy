package handler

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// AdminCardPoolSetCardLimit PUT /api/v1/admin/cardpool/cards/:id/limit
// body: {"single_limit":"50.00","daily_limit":"100.00","monthly_limit":"300.00"}，金额为字符串，空串或不传表示该项不限制。
// 限额是整体替换，至少填一项。只能手动调用，开卡与付款流程不会自动设限。
func AdminCardPoolSetCardLimit(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			SingleLimit  string `json:"single_limit"`
			DailyLimit   string `json:"daily_limit"`
			MonthlyLimit string `json:"monthly_limit"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		id := c.Param("id")
		l, err := s.SetCardLimit(c.Request.Context(), id, marswallex.CardLimit{
			SingleLimit: req.SingleLimit, DailyLimit: req.DailyLimit, MonthlyLimit: req.MonthlyLimit})
		if err != nil {
			// 结果不明（超时、5xx）时平台可能已改，留失败审计方便事后核对；参数错、卡不存在、平台明确拒绝的不记
			poolAuditFailed(c, "set_card_limit", fmt.Sprintf("card_id=%s single=%q daily=%q monthly=%q", id, l.SingleLimit, l.DailyLimit, l.MonthlyLimit), err)
			poolFail(c, err)
			return
		}
		poolAudit(c, "set_card_limit", fmt.Sprintf("card_id=%s single=%q daily=%q monthly=%q", id, l.SingleLimit, l.DailyLimit, l.MonthlyLimit))
		c.JSON(http.StatusOK, gin.H{"ok": true, "limit": l})
	})(c)
}

// AdminCardPoolCardLimitUsage GET /api/v1/admin/cardpool/cards/:id/limit-usage
// 返回 {"card_id","limit":{single_limit,daily_limit,monthly_limit},"limit_remaining":{daily_remaining,monthly_remaining}}。
func AdminCardPoolCardLimitUsage(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		u, err := s.CardLimitUsage(c.Request.Context(), c.Param("id"))
		if err != nil {
			poolFail(c, err)
			return
		}
		c.JSON(http.StatusOK, u)
	})(c)
}
