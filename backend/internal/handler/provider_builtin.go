package handler

import (
	"context"
	"errors"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// 内置服务商（MarsWallex）在 handler 层的接入。

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
