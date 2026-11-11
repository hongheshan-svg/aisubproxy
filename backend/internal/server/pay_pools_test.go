package server

import (
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/handler"
	"github.com/tuzi/cdk-recharge-system/internal/provider/providertest"
)

// 每家注册了的供卡型服务商都要有卡池，漏接就拒绝启动；上游型不需要卡池。
func TestPayPoolsCoverEveryCardSupplyProvider(t *testing.T) {
	mw := cardpool.New(cardpool.Options{})
	pools, err := payPools(mw)
	if err != nil || len(pools) != 1 || pools["marswallex"] != mw {
		t.Fatalf("内置只有 MarsWallex 一家供卡: %v %v", pools, err)
	}

	providertest.Register(t, providertest.NewTestUp())
	if _, err := payPools(mw); err != nil {
		t.Fatalf("上游型不需要卡池: %v", err)
	}

	providertest.Register(t, providertest.NewTestCard())
	if _, err := payPools(mw); err == nil || !strings.Contains(err.Error(), "testcard") {
		t.Fatalf("没接卡池的供卡型服务商应拒绝启动: %v", err)
	}
}

// 每家注册了的服务商都要接上 handler 层的适配器，漏接就拒绝启动。
func TestEveryProviderHasHandlerAdapter(t *testing.T) {
	if err := handler.CheckProviderAdapters(); err != nil {
		t.Fatalf("内置服务商都应接上: %v", err)
	}
	providertest.Register(t, providertest.NewTestCard())
	if err := handler.CheckProviderAdapters(); err == nil || !strings.Contains(err.Error(), "testcard") {
		t.Fatalf("没接就绪检查的供卡型服务商应拒绝启动: %v", err)
	}
}

func TestUpstreamWithoutAdapterRefused(t *testing.T) {
	providertest.Register(t, providertest.NewTestUp())
	if err := handler.CheckProviderAdapters(); err == nil || !strings.Contains(err.Error(), "testup") {
		t.Fatalf("没接适配器的上游应拒绝启动: %v", err)
	}
}
