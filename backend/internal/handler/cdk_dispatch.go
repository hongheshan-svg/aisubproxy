package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// 公开兑换的入口（/api/v1/public/cdk/*）。浏览器只打本站，本站按码或令牌分派：
// 供卡型走本地流程（local_cdk.go），上游型交给它的适配器（provider_adapter.go）。

func bindPublicBody(c *gin.Context) (map[string]any, bool) {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return nil, false
	}
	return body, true
}

// PublicCDKPreview POST /api/v1/public/cdk/preview
// 码的归属只看码本身。供卡型的码绝不发往上游，也不走 BindCDKRedemptionToken（它会把完整码明文写库）。
func PublicCDKPreview(c *gin.Context) {
	body, ok := bindPublicBody(c)
	if !ok {
		return
	}
	code := str(body["code"])
	p, ok := routeCode(c, code)
	if !ok {
		return
	}
	if cs, ok := provider.AsCardSupply(p); ok {
		withCardSupply(c, cs)
		localCDKPreview(c, cs, code)
		return
	}
	if a, ok := upstreamOrFail(c, p); ok {
		a.Preview(c, body)
	}
}

// PublicCDKPreflight POST /api/v1/public/cdk/preflight
func PublicCDKPreflight(c *gin.Context) {
	body, ok := bindPublicBody(c)
	if !ok {
		return
	}
	p, ok := routeBody(c, body)
	if !ok {
		return
	}
	if cs, ok := provider.AsCardSupply(p); ok {
		withCardSupply(c, cs)
		localCDKPreflight(c, cs, body)
		return
	}
	a, ok := upstreamOrFail(c, p)
	if !ok {
		return
	}
	release, ok := holdUpstreamAccount(c, p.Name(), body)
	if !ok {
		return
	}
	defer release()
	a.Preflight(c, body)
}

// PublicCDKRedeem POST /api/v1/public/cdk/redeem
func PublicCDKRedeem(c *gin.Context) {
	body, ok := bindPublicBody(c)
	if !ok {
		return
	}
	p, ok := routeBody(c, body)
	if !ok {
		return
	}
	if cs, ok := provider.AsCardSupply(p); ok {
		withCardSupply(c, cs)
		localCDKRedeem(c, cs, body)
		return
	}
	a, ok := upstreamOrFail(c, p)
	if !ok {
		return
	}
	release, ok := holdUpstreamAccount(c, p.Name(), body)
	if !ok {
		return
	}
	defer release()
	a.Redeem(c, body)
}

// PublicCDKResult GET /api/v1/public/cdk/result?token=
func PublicCDKResult(c *gin.Context) {
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token required"})
		return
	}
	p, ok := routeToken(c, token)
	if !ok {
		return
	}
	if cs, ok := provider.AsCardSupply(p); ok {
		withCardSupply(c, cs)
		localCDKResult(c, cs, token)
		return
	}
	if a, ok := upstreamOrFail(c, p); ok {
		a.Result(c, token)
	}
}

// PublicCDKResultByCode GET /api/v1/public/cdk/result-by-code?code=
func PublicCDKResultByCode(c *gin.Context) {
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		code = strings.TrimSpace(c.Query("cdk_code"))
	}
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code required"})
		return
	}
	p, ok := routeCode(c, code)
	if !ok {
		return
	}
	if cs, ok := provider.AsCardSupply(p); ok {
		withCardSupply(c, cs)
		localCDKResultByCode(c, cs, code)
		return
	}
	if a, ok := upstreamOrFail(c, p); ok {
		a.ResultByCode(c, code)
	}
}
