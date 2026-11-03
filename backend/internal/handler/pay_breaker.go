package handler

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
)

// AdminPayBreakerStatus GET /api/v1/admin/pay/breaker 查询付款熔断状态。
func AdminPayBreakerStatus(c *gin.Context) {
	payRoute(func(c *gin.Context, s *payengine.Service) {
		st, err := s.BreakerStatus(c.Request.Context())
		if err != nil {
			// 状态查不出来时引擎按暂停处理，这里如实告诉前端，同时让管理员还能点恢复
			log.Printf("payengine api %s %s: %v", c.Request.Method, c.FullPath(), err)
			c.JSON(http.StatusOK, gin.H{
				"paused": true, "reason": payengine.BreakerReasonCheck, "reason_text": "熔断状态查询失败，按暂停处理",
				"order_nos": []string{},
			})
			return
		}
		c.JSON(http.StatusOK, st)
	})(c)
}

// AdminPayBreakerResume POST /api/v1/admin/pay/breaker/resume  body: { note } 管理员恢复派发，计数从此刻重新开始。
func AdminPayBreakerResume(c *gin.Context) {
	payRoute(func(c *gin.Context, s *payengine.Service) {
		var req struct {
			Note string `json:"note"`
		}
		if !bindPayJSON(c, &req) {
			return
		}
		note := strings.TrimSpace(req.Note)
		if note == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请填写恢复说明（note）"})
			return
		}
		if r := []rune(note); len(r) > payNoteAuditMax {
			note = string(r[:payNoteAuditMax])
		}
		st, err := s.ResumeBreaker(c.Request.Context(), adminName(c), note)
		if errors.Is(err, payengine.ErrBreakerNotPaused) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		if err != nil {
			payFail(c, err)
			return
		}
		auditAdmin(c, "pay_breaker_resume", fmt.Sprintf("note=%s", note))
		c.JSON(http.StatusOK, st)
	})(c)
}
