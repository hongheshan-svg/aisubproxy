package db_test

import (
	"reflect"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// C-6：新接口 /gpt-direct/card-products 不返回卡组织、发卡地区、BIN 头等字段，
// 同步时不能把已有的这些字段覆盖成空。
func TestUpsertDirectCardProductKeepsMetadata(t *testing.T) {
	dbtest.Open(t)
	full := db.CardProductCache{
		ProductCode: "P1", Issuer: "bank-a", BIN: "512998", Network: "MASTERCARD",
		IssuingArea: "US", Scene: "gpt", CardGroup: "g1", Description: "美卡",
		BinHeads: []string{"512998", "428820"}, Enabled: true,
	}
	if err := db.UpsertCardProduct(full); err != nil {
		t.Fatal(err)
	}
	direct := db.CardProductCache{
		ProductCode: "P1", Issuer: "bank-b", BIN: "512998", Description: "美卡-新名",
		Enabled: false, SuspendedAt: "maintenance",
	}
	if err := db.UpsertDirectCardProduct(direct); err != nil {
		t.Fatal(err)
	}
	got := cardProductByCode(t, "P1")
	if got.Network != "MASTERCARD" || got.IssuingArea != "US" || got.Scene != "gpt" || got.CardGroup != "g1" {
		t.Fatalf("元数据被覆盖: %+v", got)
	}
	if !reflect.DeepEqual(got.BinHeads, []string{"512998", "428820"}) {
		t.Fatalf("bin_heads 被覆盖: %v", got.BinHeads)
	}
	// 直接接口给出的字段要照常更新
	if got.Issuer != "bank-b" || got.Description != "美卡-新名" || got.Enabled || got.SuspendedAt != "maintenance" {
		t.Fatalf("直接接口字段未更新: %+v", got)
	}
}

func cardProductByCode(t *testing.T, code string) db.CardProductCache {
	t.Helper()
	list, err := db.GetCardProducts()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.ProductCode == code {
			return p
		}
	}
	t.Fatalf("产品 %s 不存在", code)
	return db.CardProductCache{}
}
