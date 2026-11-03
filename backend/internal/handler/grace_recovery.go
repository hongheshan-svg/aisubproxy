package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"net/http"
	"strings"
)

// graceRecoveryRequest 是宽限期恢复的请求体，原样转给上游。
type graceRecoveryRequest struct {
	RedemptionToken string `json:"redemption_token"`
	PreflightToken  string `json:"preflight_token"`
	Confirmed       bool   `json:"confirmed"`
}

// PublicCDKGraceRecovery POST /api/v1/public/cdk/recover-subscription
// Uses the holder's redemption/preflight tokens, never the site's privileged API key.
func PublicCDKGraceRecovery(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 80<<10)
	var body graceRecoveryRequest
	if c.ShouldBindJSON(&body) != nil || !body.Confirmed || strings.TrimSpace(body.RedemptionToken) == "" ||
		len(body.RedemptionToken) > 512 || strings.TrimSpace(body.PreflightToken) == "" || len(body.PreflightToken) > 65536 {
		c.JSON(400, gin.H{"error": "请先检测账号并确认取消宽限期原订阅"})
		return
	}
	p, ok := routeToken(c, body.RedemptionToken)
	if !ok {
		return
	}
	// 供卡型只做无订阅账号的新开通，没有宽限期流程（子项目 C）。令牌绝不发往上游
	if cs, ok := provider.AsCardSupply(p); ok {
		withCardSupply(c, cs)
		localReply(c, 400, gin.H{"error": "该卡密暂不支持宽限期处理"})
		return
	}
	a, ok := upstreamOrFail(c, p)
	if !ok {
		return
	}
	gr, ok := a.(upstreamGraceRecoverer)
	if !ok {
		c.JSON(400, gin.H{"error": "该服务商不支持宽限期处理"})
		return
	}
	gr.GraceRecovery(c, body)
}

// zovoCardGraceRecovery 是 ZovoCard 的宽限期恢复：转发卡台。
func zovoCardGraceRecovery(c *gin.Context, body graceRecoveryRequest) {
	status, raw, err := cardplatform.NewFromSettings().RecoverSubscription(c.Request.Context(), body, deviceFrom(c))
	if err != nil {
		c.JSON(502, gin.H{"error": "暂时无法确认处理结果，请重新检测账号，不要重复取消"})
		return
	}
	proxyPublicJSON(c, status, raw)
}
