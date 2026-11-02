package server

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// 钱包、持卡人、补拉与卡明细接口都注册在 /api/v1/admin 下，且与已有卡池路由不冲突（冲突时 gin 注册即 panic）。
func TestCardPoolWalletRoutesRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("WEB_DIR", "")
	r := gin.New()
	setupRoutes(r)
	have := map[string]bool{}
	for _, rt := range r.Routes() {
		have[rt.Method+" "+rt.Path] = true
	}
	const p = "/api/v1/admin/cardpool"
	for _, want := range []string{
		"GET " + p + "/wallet/accounts",
		"POST " + p + "/wallet/accounts",
		"POST " + p + "/wallet/accounts/:no/status",
		"GET " + p + "/wallet/receiving-addresses",
		"GET " + p + "/wallet/serials",
		"POST " + p + "/wallet/conversions",
		"POST " + p + "/wallet/transfers",
		"POST " + p + "/cardholders/photos",
		"POST " + p + "/cardholders",
		"GET " + p + "/cardholders/:id/profile",
		"PUT " + p + "/cardholders/:id",
		"POST " + p + "/cardholders/:id/status",
		"GET " + p + "/cardholder-submissions",
		"POST " + p + "/cardholder-submissions/:out_id/abandon",
		"GET " + p + "/backfill",
		"POST " + p + "/backfill",
		"GET " + p + "/cards/:id/balance-transactions",
		// 原有路由仍在
		"POST " + p + "/cardholders/import",
		"POST " + p + "/cardholders/:id/refresh",
		"GET " + p + "/cards/:id",
	} {
		if !have[want] {
			t.Errorf("route missing: %s", want)
		}
	}
}

func TestMarsWallexKeypairRouteRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("WEB_DIR", "")
	r := gin.New()
	setupRoutes(r)
	for _, rt := range r.Routes() {
		if rt.Method == "POST" && rt.Path == "/api/v1/admin/marswallex/keypair" {
			return
		}
	}
	t.Fatal("route missing: POST /api/v1/admin/marswallex/keypair")
}

// 付款后台接口的 10 条路由都注册在 /api/v1/admin/pay 下。
func TestPayRoutesRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("WEB_DIR", "")
	r := gin.New()
	setupRoutes(r)
	have := map[string]bool{}
	for _, rt := range r.Routes() {
		have[rt.Method+" "+rt.Path] = true
	}
	const p = "/api/v1/admin/pay"
	for _, want := range []string{
		"POST " + p + "/orders",
		"GET " + p + "/orders",
		"GET " + p + "/orders/:no",
		"POST " + p + "/orders/:no/resolve",
		"GET " + p + "/orders/:no/shots/:name",
		"GET " + p + "/config",
		"PUT " + p + "/config",
		"GET " + p + "/proxies",
		"PUT " + p + "/proxies",
		"POST " + p + "/browser-check",
	} {
		if !have[want] {
			t.Errorf("route missing: %s", want)
		}
	}
}
