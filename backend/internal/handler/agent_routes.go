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
	sess.GET("/products", AgentListProducts)
	sess.POST("/orders", AgentPlaceOrder)
	sess.GET("/orders", AgentListOrders)
	sess.GET("/orders/:id", AgentGetOrder)
	sess.POST("/orders/:id/export", AgentExportOrder)
	sess.GET("/codes", AgentListCodes)
	sess.POST("/codes/export", AgentExportCodes)
	sess.GET("/overview", AgentOverview)
	sess.GET("/ledger", AgentLedger)
	sess.GET("/ledger/export", AgentLedgerExport)
	sess.GET("/shared", AgentSharedGet)
	sess.POST("/shared/key", AgentSharedCreateKey)
	sess.POST("/shared/disable", AgentSharedDisable)
	sess.PUT("/shared/allowlist", AgentSharedAllowlist)
	sess.PUT("/shared/low-balance", AgentSharedLowBalance)
	sess.GET("/shared/failures", AgentSharedFailures)
}

// RegisterAgentAdminRoutes 挂管理员管理代理商的接口（调用方已套好管理员中间件）。
func RegisterAgentAdminRoutes(admin *gin.RouterGroup) {
	admin.GET("/agents", AdminListAgents)
	admin.POST("/agents", AdminCreateAgent)
	admin.GET("/agents/:id", AdminGetAgent)
	admin.PUT("/agents/:id", AdminUpdateAgent)
	admin.POST("/agents/:id/disable", AdminDisableAgent)
	admin.POST("/agents/:id/enable", AdminEnableAgent)
	admin.POST("/agents/:id/reset-password", AdminResetAgentPassword)
	admin.POST("/agents/:id/reset-totp", AdminResetAgentTOTP)
	admin.POST("/agents/:id/adjust", AdminAdjustAgent)
	admin.GET("/agents/:id/orders", AdminListAgentOrders)
	admin.GET("/agents/:id/ledger", AdminAgentLedger)
	admin.GET("/agents/:id/ledger/export", AdminAgentLedgerExport)
	admin.GET("/agents/:id/overrides", AdminGetAgentOverrides)
	admin.PUT("/agents/:id/overrides", AdminPutAgentOverrides)
	admin.GET("/agents/:id/shared", AdminAgentSharedGet)
	admin.POST("/agents/:id/shared/disable", AdminAgentSharedDisable)
	admin.POST("/agents/:id/shared/enable", AdminAgentSharedEnable)
	admin.GET("/agent-summary", AdminAgentSummary)

	admin.GET("/agent-tiers", AdminListAgentTiers)
	admin.POST("/agent-tiers", AdminCreateAgentTier)
	admin.PUT("/agent-tiers/:id", AdminUpdateAgentTier)
	admin.DELETE("/agent-tiers/:id", AdminDeleteAgentTier)

	admin.GET("/agent-products", AdminListAgentProducts)
	admin.GET("/agent-products/options", AdminAgentProductOptions)
	admin.POST("/agent-products", AdminCreateAgentProduct)
	admin.PUT("/agent-products/:id", AdminUpdateAgentProduct)

	admin.GET("/agent-prices", AdminGetAgentPrices)
	admin.PUT("/agent-prices", AdminPutAgentPrices)
}
