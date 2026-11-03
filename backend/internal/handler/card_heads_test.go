package handler

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// fakeHeads 是测试用的卡头数据源。
type fakeHeads struct {
	items []db.CardHeadProduct
	err   error
}

func (f fakeHeads) CardHeads(context.Context) ([]db.CardHeadProduct, error) { return f.items, f.err }

func TestParseCardHead(t *testing.T) {
	cases := map[string]string{
		"美卡-泛场景-512998-充值卡":            "512998",
		"美卡-泛场景-428820-充值卡":            "428820",
		"美卡-泛场景-537100-充值卡-支持APPLEPAY": "537100",
		"美卡投流AI-537872712-充值卡":         "537872712",
		"普通卡":                          "",
		"卡-12345-x":                    "",
		"卡-1234567890-x":               "",
		"A-411111-B-522222":            "411111",
		"":                             "",
	}
	for in, want := range cases {
		if got := parseCardHead(in); got != want {
			t.Errorf("parseCardHead(%q) = %q, want %q", in, got, want)
		}
	}
}

// 数据源出错或返回空列表时，表里什么都不改，记下错误。
func TestSyncCardHeadsEmptyIsError(t *testing.T) {
	dbtest.Open(t)
	const p provider.Name = "heads-empty"
	ctx := context.Background()
	if err := db.SyncCardHeads(string(p), []db.CardHeadProduct{{ProductID: "A", Name: "卡 A"}}, 10); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateCardHeadNotes(string(p), "A", "yes", "no", "备注", "admin", 11); err != nil {
		t.Fatal(err)
	}
	for _, src := range []fakeHeads{{}, {err: errors.New("接口超时")}} {
		if err := syncCardHeads(ctx, p, src); err == nil {
			t.Fatalf("%+v 应报错", src)
		}
		a, err := db.GetCardHead(string(p), "A")
		if err != nil || !a.Listed || a.ThreeDS != "yes" || a.ApplePay != "no" || a.Note != "备注" || a.SyncedAt != 10 {
			t.Fatalf("A 被改动: %+v err=%v", a, err)
		}
		if _, lastErr := cardHeadSyncState(p); lastErr == "" {
			t.Fatal("应记下同步错误")
		}
	}

	ok := fakeHeads{items: []db.CardHeadProduct{{ProductID: "A", Name: "卡 A2"}}}
	if err := syncCardHeads(ctx, p, ok); err != nil {
		t.Fatal(err)
	}
	if at, lastErr := cardHeadSyncState(p); at == 0 || lastErr != "" {
		t.Fatalf("成功后状态 at=%d err=%q", at, lastErr)
	}
	if a, _ := db.GetCardHead(string(p), "A"); a.Name != "卡 A2" || a.ThreeDS != "yes" {
		t.Fatalf("A = %+v", a)
	}
}

func TestMarsWallexCardHeads(t *testing.T) {
	_, mw, _ := cpEnv(t, false)
	mw.On("GET", "/v1/card/product", mwtest.OK(`[
{"product_id":"2100050582268325889","product_name":"美卡-泛场景-512998-充值卡","card_label":"MASTER","card_country":"US","card_mode":"PREPAID","card_currency":"USD"},
{"product_id":"2099791451576242177","product_name":"美卡-泛场景-428820-充值卡","card_label":"VISA","card_country":"US","card_mode":"PREPAID","card_currency":"USD"},
{"product_id":"2099527430266204162","product_name":"美卡-泛场景-537100-充值卡-支持APPLEPAY","card_label":"MASTER","card_country":"US","card_mode":"PREPAID","card_currency":"USD"},
{"product_id":"2077290094140084226","product_name":"美卡投流AI-537872712-充值卡","card_label":"MASTER","card_country":"US","card_mode":"PREPAID","card_currency":"USD"},
{"product_id":"9","product_name":"无卡头产品","card_label":"VISA","card_country":"HK","card_mode":"PREPAID","card_currency":"HKD"}]`))
	items, err := marsWallexAdapter{}.CardHeads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("len=%d", len(items))
	}
	want := []string{"512998", "428820", "537100", "537872712"}
	for i, w := range want {
		if !reflect.DeepEqual(items[i].CardHeads, []string{w}) || items[i].Currency != "USD" || items[i].Country != "US" {
			t.Fatalf("items[%d] = %+v", i, items[i])
		}
	}
	if items[0].ProductID != "2100050582268325889" || items[0].Name != "美卡-泛场景-512998-充值卡" || items[0].Network != "MASTER" {
		t.Fatalf("items[0] = %+v", items[0])
	}
	if items[4].CardHeads == nil || len(items[4].CardHeads) != 0 || items[4].Currency != "HKD" {
		t.Fatalf("没有卡头的产品: %+v", items[4])
	}
}

func TestMarsWallexCardHeadsNoPool(t *testing.T) {
	SetCardPool(nil)
	if _, err := (marsWallexAdapter{}).CardHeads(context.Background()); err == nil {
		t.Fatal("卡池未启动时应报错")
	}
}

func TestZovoCardCardHeadsNeedsCache(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	if _, err := (zovoCardAdapter{}).CardHeads(ctx); err == nil {
		t.Fatal("产品缓存为空时应报错")
	}
	for _, p := range []db.CardProductCache{
		{ProductCode: "P-HEADS", BIN: "411111", Network: "VISA", IssuingArea: "US", Description: "多卡头", BinHeads: []string{"411111", "422222"}, Enabled: true},
		{ProductCode: "P-BIN", BIN: "533333", Network: "MASTER", IssuingArea: "HK", Enabled: true},
		{ProductCode: "P-NONE", Network: "VISA", Enabled: true},
	} {
		if err := db.UpsertCardProduct(p); err != nil {
			t.Fatal(err)
		}
	}
	items, err := zovoCardAdapter{}.CardHeads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]db.CardHeadProduct{}
	for _, it := range items {
		by[it.ProductID] = it
	}
	if h := by["P-HEADS"]; h.Name != "多卡头" || !reflect.DeepEqual(h.CardHeads, []string{"411111", "422222"}) || h.Network != "VISA" || h.Country != "US" || h.Currency != "" {
		t.Fatalf("P-HEADS = %+v", h)
	}
	if h := by["P-BIN"]; h.Name != "P-BIN" || !reflect.DeepEqual(h.CardHeads, []string{"533333"}) || h.Country != "HK" {
		t.Fatalf("P-BIN = %+v", h)
	}
	if h := by["P-NONE"]; h.CardHeads == nil || len(h.CardHeads) != 0 {
		t.Fatalf("P-NONE = %+v", h)
	}
}

// 两家内置服务商都能自动读取卡头；测试用的上游服务商不能。
func TestCardHeadSourceFor(t *testing.T) {
	if _, ok := cardHeadSourceFor(provider.MarsWallex); !ok {
		t.Fatal("MarsWallex 应能读取卡头")
	}
	if _, ok := cardHeadSourceFor(provider.ZovoCard); !ok {
		t.Fatal("ZovoCard 应能读取卡头")
	}
	if _, ok := cardHeadSourceFor("nope"); ok {
		t.Fatal("未注册的服务商不能读取卡头")
	}
}
