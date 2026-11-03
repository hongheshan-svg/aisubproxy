package handler

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
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
	r.SetLink("api_key", provider.Link{To: "/ops/integration", Label: "去配置卡台"})
	r.SetLink("balance", provider.Link{To: "/ops/integration", Label: "去卡台页"})
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
	r.SetLink("config", provider.Link{To: "/ops/marswallex", Label: "去配置 MarsWallex"})
	r.SetLink("breaker", provider.Link{To: "/ops/marswallex-pay", Label: "去自动付款查看"})
	r.SetLink("card_pool", provider.Link{To: "/ops/cardpool", Label: "去卡池"})
	r.SetLink("wallet_sync", provider.Link{To: "/ops/marswallex-wallet", Label: "去 MarsWallex 钱包"})
	r.SetLink("plan", provider.Link{To: "/ops/marswallex-pay", Label: "去付款配置启用套餐"})
	return providerStatus{Readiness: r, Combos: combos, Extras: []providerExtra{}, Warnings: []string{}}
}

// CardHeads 读取卡台产品缓存（plansync 每 3 分钟从卡台同步）。卡头用 bin_heads，为空时用 BIN。
// 缓存为空时报错：卡台还没配置或还没同步，不能把已有卡头都标成「接口已不再返回」。
func (zovoCardAdapter) CardHeads(context.Context) ([]db.CardHeadProduct, error) {
	ps, err := db.GetCardProducts()
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, errors.New("卡台产品还没同步")
	}
	out := make([]db.CardHeadProduct, 0, len(ps))
	for _, p := range ps {
		heads := p.BinHeads
		if len(heads) == 0 {
			heads = []string{}
			if p.BIN != "" {
				heads = []string{p.BIN}
			}
		}
		name := p.Description
		if name == "" {
			name = p.ProductCode
		}
		out = append(out, db.CardHeadProduct{ProductID: p.ProductCode, Name: name, CardHeads: heads, Network: p.Network, Country: p.IssuingArea})
	}
	return out, nil
}

// PruneMissingCardHeads：卡台下架的卡段直接删除，不保留它的 BIN（与产品缓存的规则一致）。
func (zovoCardAdapter) PruneMissingCardHeads() bool { return true }

// CardHeads 调用 MarsWallex 卡产品接口。接口没有卡头字段，卡头从产品名里识别。
func (marsWallexAdapter) CardHeads(ctx context.Context) ([]db.CardHeadProduct, error) {
	s := cardPool
	if s == nil {
		return nil, errors.New("卡池未启动")
	}
	ps, err := s.ListProducts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]db.CardHeadProduct, 0, len(ps))
	for _, p := range ps {
		heads := []string{}
		if h := parseCardHead(p.ProductName); h != "" {
			heads = []string{h}
		}
		out = append(out, db.CardHeadProduct{ProductID: p.ProductID, Name: p.ProductName, CardHeads: heads,
			Network: p.CardLabel, Country: p.CardCountry, Currency: p.CardCurrency})
	}
	return out, nil
}
