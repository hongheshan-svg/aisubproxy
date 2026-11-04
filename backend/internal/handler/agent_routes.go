package handler

import "github.com/gin-gonic/gin"

// RegisterAgentRoutes 挂代理商接口（/api/v1/agent）。后续任务往 sess 组里追加需要登录的接口。
func RegisterAgentRoutes(api *gin.RouterGroup) {
	g := api.Group("/agent", LookupGuard())

	auth := g.Group("/auth")
	auth.POST("/login", AgentLogin)
	auth.POST("/password/initial", AgentPasswordInitial)
	auth.GET("/totp/setup", AgentTOTPSetup)
	auth.POST("/totp/confirm", AgentTOTPConfirm)
	auth.POST("/totp", AgentTOTPLogin)
	auth.POST("/logout", AgentLogout)

	sess := g.Group("", AgentAuthMiddleware())
	sess.GET("/me", AgentMe)
	sess.POST("/me/password", AgentChangePassword)
}

// RegisterAgentAdminRoutes 挂管理员管理代理商的接口（调用方已套好管理员中间件），任务 6 填内容。
func RegisterAgentAdminRoutes(admin *gin.RouterGroup) {}
