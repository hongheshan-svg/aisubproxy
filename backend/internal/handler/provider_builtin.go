package handler

import (
	"context"

	"github.com/gin-gonic/gin"
)

// 内置两家服务商在 handler 层的接入。

// zovoCardAdapter 是 ZovoCard 的上游适配器：原有的转发卡台逻辑原样搬进来，行为不变。
type zovoCardAdapter struct{}

func (zovoCardAdapter) Preview(c *gin.Context, body map[string]any)   { zovoCardPreview(c, body) }
func (zovoCardAdapter) Preflight(c *gin.Context, body map[string]any) { zovoCardPreflight(c, body) }
func (zovoCardAdapter) Redeem(c *gin.Context, body map[string]any)    { zovoCardRedeem(c, body) }
func (zovoCardAdapter) Result(c *gin.Context, token string)           { zovoCardResult(c, token) }
func (zovoCardAdapter) ResultByCode(c *gin.Context, code string)      { zovoCardResultByCode(c, code) }
func (zovoCardAdapter) Issue(c *gin.Context, req cdkIssueRequest)     { zovoCardIssueCDKs(c, req) }

// Exchange 代理换码：在卡台上查单、发新码、禁用旧码。
func (zovoCardAdapter) Exchange(c *gin.Context, code, ip string) { zovoCardExchange(c, code, ip) }

// GraceRecovery 宽限期恢复：转发卡台。
func (zovoCardAdapter) GraceRecovery(c *gin.Context, body graceRecoveryRequest) {
	zovoCardGraceRecovery(c, body)
}

// Lookup 卡密查询：本站的码表、绑定和卡台订单。
func (zovoCardAdapter) Lookup(ctx context.Context, code, device string) cdkLookupResult {
	return zovoCardLookup(ctx, code, device)
}

// Check 是卡台的就绪检查，附带卡台余额。卡台余额接口不带币种，这里原样展示数字。
func (zovoCardAdapter) Check(ctx context.Context) providerStatus {
	r, bal := checkZovoCard(ctx)
	st := providerStatus{Readiness: r, Extras: []providerExtra{}, Warnings: []string{}}
	if bal != nil {
		st.Extras = append(st.Extras,
			providerExtra{Label: "卡台余额", Value: bal.Balance.String()},
			providerExtra{Label: "可用余额", Value: bal.SpendableBalance.String()})
	}
	return st
}

// marsWallexAdapter 是 MarsWallex 的就绪检查。兑换、发码、换码走通用的本地流程。
type marsWallexAdapter struct{}

func (marsWallexAdapter) Check(ctx context.Context, plan, region string, public bool) providerStatus {
	sync := syncWalletNow
	if public {
		sync = syncWalletThrottled
	}
	r, combos := checkMarsWallex(ctx, plan, region, sync)
	return providerStatus{Readiness: r, Combos: combos, Extras: []providerExtra{}, Warnings: []string{}}
}
