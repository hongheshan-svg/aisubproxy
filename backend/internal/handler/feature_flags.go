package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 功能开关。代理商功能（代理商后台、异次元对接、管理员的代理商菜单）默认关闭，
// 在后台「系统 → 功能开关」打开。老的代理换码（/partner/swap）不归这个开关管。

// SettingAgentEnabled 是设置表里的键：值为 "1" 表示打开，其他（包括没有这一项）都算关闭。
const SettingAgentEnabled = "agent_enabled"

// agentFeature 是开关的内存副本：启动时由 LoadFeatureFlags 从设置表读，后台改开关时同步更新，所以不用重启。
var agentFeature atomic.Bool

// LoadFeatureFlags 从设置表读开关（服务启动时调用）。
func LoadFeatureFlags() error {
	v, err := db.GetSetting(SettingAgentEnabled)
	if err != nil {
		return fmt.Errorf("读取功能开关: %w", err)
	}
	agentFeature.Store(strings.TrimSpace(v) == "1")
	return nil
}

// AgentFeatureEnabled 代理商功能是否打开。
func AgentFeatureEnabled() bool { return agentFeature.Load() }

// SetAgentFeatureEnabled 只改内存里的开关，不写设置表（后台接口先写表再调它）。
func SetAgentFeatureEnabled(on bool) { agentFeature.Store(on) }

// AgentFeatureGate 代理商功能关闭时，受控接口一律返回 404 agent_disabled。
func AgentFeatureGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !AgentFeatureEnabled() {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "agent_disabled", "message": "代理商功能未开放"})
			return
		}
		c.Next()
	}
}

// AgentStatus GET /api/v1/agent/status：不受开关限制，代理商登录页用它判断要不要显示登录框。
func AgentStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"enabled": AgentFeatureEnabled()})
}

// AdminGetFeatures GET /api/v1/admin/features
func AdminGetFeatures(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"agent_enabled": AgentFeatureEnabled()})
}

type featuresBody struct {
	AgentEnabled *bool `json:"agent_enabled"`
}

// AdminPutFeatures PUT /api/v1/admin/features：{"agent_enabled": true|false}，先写设置表再改内存，记审计。
func AdminPutFeatures(c *gin.Context) {
	var body featuresBody
	dec := json.NewDecoder(c.Request.Body)
	if err := dec.Decode(&body); err != nil || body.AgentEnabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对：需要 agent_enabled（true 或 false）"})
		return
	}
	on := *body.AgentEnabled
	v := "0"
	if on {
		v = "1"
	}
	if err := db.SetSetting(SettingAgentEnabled, v); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败，请稍后再试"})
		return
	}
	SetAgentFeatureEnabled(on)
	auditAdmin(c, "admin_feature_toggle", "agent_enabled="+v)
	AdminGetFeatures(c)
}
