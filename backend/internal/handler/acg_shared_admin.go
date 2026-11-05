package handler

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 管理员端查看和开关代理商的「对接异次元」。响应里没有密钥原文和密文。

const sharedAdminFailuresLimit = 50

// AdminAgentSharedGet GET /agents/:id/shared
func AdminAgentSharedGet(c *gin.Context) {
	a, ok := adminAgentFromPath(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	creds, err := db.GetAgentSharedCreds(ctx, a.ID)
	if err != nil {
		adminInternal(c, "读取对接设置", err)
		return
	}
	since := agentNow().Add(-sharedFailuresWindow).Unix()
	list, _, err := db.ListAgentSharedFailures(ctx, a.ID, since, sharedAdminFailuresLimit, 0)
	if err != nil {
		adminInternal(c, "列出取码失败记录", err)
		return
	}
	failures := make([]gin.H, 0, len(list))
	for _, f := range list {
		failures = append(failures, sharedFailureView(f))
	}
	allow := creds.IPAllowlist
	if allow == nil {
		allow = []string{}
	}
	adminNoStore(c)
	c.JSON(http.StatusOK, gin.H{
		"status": sharedStatus(creds), "app_id": creds.AppID, "key_created_at": creds.KeyCreatedAt, "last_used_at": creds.LastUsedAt,
		"ip_allowlist": allow, "low_balance_cents": creds.LowBalanceCents, "failures": failures,
	})
}

func adminAgentSharedToggle(c *gin.Context, disable bool) {
	a, ok := adminAgentFromPath(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := db.SetAgentSharedAdminDisabled(ctx, a.ID, disable, agentNow()); err != nil {
		adminInternal(c, "切换对接开关", err)
		return
	}
	action := "admin_agent_shared_enable"
	if disable {
		action = "admin_agent_shared_disable"
	}
	auditAdmin(c, action, fmt.Sprintf("id=%d username=%s", a.ID, a.Username))
	creds, err := db.GetAgentSharedCreds(ctx, a.ID)
	if err != nil {
		adminInternal(c, "读取对接设置", err)
		return
	}
	adminNoStore(c)
	c.JSON(http.StatusOK, gin.H{"status": sharedStatus(creds)})
}

// AdminAgentSharedDisable POST /agents/:id/shared/disable
func AdminAgentSharedDisable(c *gin.Context) { adminAgentSharedToggle(c, true) }

// AdminAgentSharedEnable POST /agents/:id/shared/enable
func AdminAgentSharedEnable(c *gin.Context) { adminAgentSharedToggle(c, false) }
