package handler

import (
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// mwToken 是 MarsWallex 码的兑换令牌。
func mwToken(code string) string {
	p, _ := provider.Get(provider.MarsWallex)
	cs, _ := provider.AsCardSupply(p)
	return provider.TokenFor(cs, code)
}

// withCardSupplyAdapter 测试期间给一家供卡型服务商接上就绪检查，结束时恢复。
func withCardSupplyAdapter(t *testing.T, n provider.Name, a cardSupplyAdapter) {
	t.Helper()
	adapterMu.Lock()
	prev, had := cardSupplyAdapters[n]
	cardSupplyAdapters[n] = a
	adapterMu.Unlock()
	t.Cleanup(func() {
		adapterMu.Lock()
		defer adapterMu.Unlock()
		if had {
			cardSupplyAdapters[n] = prev
		} else {
			delete(cardSupplyAdapters, n)
		}
	})
}

// withUpstreamAdapter 测试期间给一家上游型服务商接上适配器，结束时恢复。
func withUpstreamAdapter(t *testing.T, n provider.Name, a upstreamAdapter) {
	t.Helper()
	adapterMu.Lock()
	prev, had := upstreamAdapters[n]
	upstreamAdapters[n] = a
	adapterMu.Unlock()
	t.Cleanup(func() {
		adapterMu.Lock()
		defer adapterMu.Unlock()
		if had {
			upstreamAdapters[n] = prev
		} else {
			delete(upstreamAdapters, n)
		}
	})
}
