package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/provider/providertest"
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

// fakeCardSupplyWithHeads 是能自动读取卡头的供卡型适配器。
type fakeCardSupplyWithHeads struct{ fakeHeads }

func (fakeCardSupplyWithHeads) Check(context.Context, string, string, bool) providerStatus {
	return providerStatus{}
}

func headsRoute(h gin.HandlerFunc, method, route, path, body string) *httptest.ResponseRecorder {
	return cpDo(h, method, route, path, body)
}

const (
	headsListRoute = "/providers/:name/card-heads"
	headsItemRoute = "/providers/:name/card-heads/:product_id"
	headsSyncRoute = "/providers/:name/card-heads/sync"
)

func decodeHeadList(t *testing.T, w *httptest.ResponseRecorder) cardHeadListResponse {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	var out cardHeadListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCardHeadsUnknownProvider(t *testing.T) {
	dbtest.Open(t)
	cases := []struct {
		h                   gin.HandlerFunc
		method, route, path string
	}{
		{AdminListCardHeads, "GET", headsListRoute, "/providers/nope/card-heads"},
		{AdminSyncCardHeads, "POST", headsSyncRoute, "/providers/nope/card-heads/sync"},
		{AdminAddCardHead, "POST", headsListRoute, "/providers/nope/card-heads"},
		{AdminUpdateCardHead, "PUT", headsItemRoute, "/providers/nope/card-heads/A"},
		{AdminDeleteCardHead, "DELETE", headsItemRoute, "/providers/nope/card-heads/A"},
	}
	for _, tc := range cases {
		w := headsRoute(tc.h, tc.method, tc.route, tc.path, `{"product_id":"A","name":"x"}`)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s: status %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	if list, _ := db.ListCardHeads("nope"); len(list) != 0 {
		t.Fatalf("未知服务商不应写库: %+v", list)
	}
}

// 不能自动读取卡头的服务商：可以手动增删改，不能同步。
func TestCardHeadsManualProvider(t *testing.T) {
	dbtest.Open(t)
	providertest.Register(t, providertest.NewTestUp())
	base := "/providers/" + string(providertest.TestUp) + "/card-heads"

	if w := headsRoute(AdminSyncCardHeads, "POST", headsSyncRoute, base+"/sync", ``); w.Code != http.StatusBadRequest {
		t.Fatalf("手动维护的服务商同步应返回 400，得到 %d", w.Code)
	}
	w := headsRoute(AdminAddCardHead, "POST", headsListRoute, base, `{"product_id":"P1","name":"测试卡","card_heads":["411111"],"three_ds":"yes"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("添加: %d %s", w.Code, w.Body.String())
	}
	list := decodeHeadList(t, headsRoute(AdminListCardHeads, "GET", headsListRoute, base, ``))
	if list.Auto || list.Provider != string(providertest.TestUp) || len(list.Items) != 1 || list.Items[0].ProductID != "P1" ||
		list.Items[0].ThreeDS != "yes" || list.Items[0].Source != db.CardHeadSourceManual {
		t.Fatalf("列表 = %+v", list)
	}
	if w := headsRoute(AdminAddCardHead, "POST", headsListRoute, base, `{"product_id":"P1","name":"重复"}`); w.Code != http.StatusConflict {
		t.Fatalf("重复添加应返回 409，得到 %d %s", w.Code, w.Body.String())
	}

	w = headsRoute(AdminUpdateCardHead, "PUT", headsItemRoute, base+"/P1", `{"three_ds":"no","apple_pay":"yes","note":"只在美国用","name":"测试卡2","card_heads":["411111","422222"],"network":"VISA","country":"US","currency":"USD"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("修改: %d %s", w.Code, w.Body.String())
	}
	got, _ := db.GetCardHead(string(providertest.TestUp), "P1")
	if got.Note != "只在美国用" || got.ThreeDS != "no" || got.ApplePay != "yes" || got.Name != "测试卡2" || len(got.CardHeads) != 2 || got.Currency != "USD" || got.UpdatedBy != "alice" {
		t.Fatalf("修改后 = %+v", got)
	}
	if w := headsRoute(AdminUpdateCardHead, "PUT", headsItemRoute, base+"/NOPE", `{"note":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("修改不存在的应返回 404，得到 %d", w.Code)
	}

	if w := headsRoute(AdminDeleteCardHead, "DELETE", headsItemRoute, base+"/P1", ``); w.Code != http.StatusOK {
		t.Fatalf("删除: %d %s", w.Code, w.Body.String())
	}
	if list := decodeHeadList(t, headsRoute(AdminListCardHeads, "GET", headsListRoute, base, ``)); len(list.Items) != 0 {
		t.Fatalf("删除后列表 = %+v", list.Items)
	}
	audit := cpAudit(t, db.DB)
	for _, a := range []string{"card_head_add", "card_head_update", "card_head_delete"} {
		if !strings.Contains(audit, a) {
			t.Fatalf("审计缺少 %s:\n%s", a, audit)
		}
	}
}

// 能自动读取卡头的服务商：不能手动添加、删除；同步后标注保留。
func TestCardHeadsAutoProvider(t *testing.T) {
	dbtest.Open(t)
	providertest.Register(t, providertest.NewTestCard())
	src := fakeCardSupplyWithHeads{fakeHeads{items: []db.CardHeadProduct{{ProductID: "A", Name: "接口卡", CardHeads: []string{"512998"}, Currency: "USD"}}}}
	withCardSupplyAdapter(t, providertest.TestCard, src)
	base := "/providers/" + string(providertest.TestCard) + "/card-heads"

	if w := headsRoute(AdminAddCardHead, "POST", headsListRoute, base, `{"product_id":"M","name":"手动"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("能自动读取的服务商手动添加应返回 400，得到 %d", w.Code)
	}
	list := decodeHeadList(t, headsRoute(AdminSyncCardHeads, "POST", headsSyncRoute, base+"/sync", ``))
	if !list.Auto || len(list.Items) != 1 || list.LastSyncAt == 0 || list.LastSyncError != "" {
		t.Fatalf("同步后 = %+v", list)
	}
	w := headsRoute(AdminUpdateCardHead, "PUT", headsItemRoute, base+"/A", `{"three_ds":"yes","note":"支持 3DS","name":"手改名称"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("标注: %d %s", w.Code, w.Body.String())
	}
	if w := headsRoute(AdminDeleteCardHead, "DELETE", headsItemRoute, base+"/A", ``); w.Code != http.StatusBadRequest {
		t.Fatalf("删除接口来源的应返回 400，得到 %d", w.Code)
	}
	list = decodeHeadList(t, headsRoute(AdminSyncCardHeads, "POST", headsSyncRoute, base+"/sync", ``))
	a := list.Items[0]
	if a.ThreeDS != "yes" || a.Note != "支持 3DS" || a.Name != "接口卡" {
		t.Fatalf("再同步后标注应保留、接口来源的名称不能手改: %+v", a)
	}
	if !strings.Contains(cpAudit(t, db.DB), "card_heads_sync") {
		t.Fatal("审计缺少 card_heads_sync")
	}

	withCardSupplyAdapter(t, providertest.TestCard, fakeCardSupplyWithHeads{fakeHeads{err: errors.New("接口超时")}})
	w = headsRoute(AdminSyncCardHeads, "POST", headsSyncRoute, base+"/sync", ``)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "接口超时") {
		t.Fatalf("同步失败应返回 502: %d %s", w.Code, w.Body.String())
	}
	list = decodeHeadList(t, headsRoute(AdminListCardHeads, "GET", headsListRoute, base, ``))
	if list.LastSyncError == "" || len(list.Items) != 1 || !list.Items[0].Listed {
		t.Fatalf("同步失败后 = %+v", list)
	}
}

func TestCardHeadsValidation(t *testing.T) {
	dbtest.Open(t)
	providertest.Register(t, providertest.NewTestUp())
	base := "/providers/" + string(providertest.TestUp) + "/card-heads"
	heads11 := `["4111","4112","4113","4114","4115","4116","4117","4118","4119","4120","4121"]`
	bad := []string{
		`{"product_id":"P1","name":"x","three_ds":"maybe"}`,
		`{"product_id":"P1","name":"x","apple_pay":"1"}`,
		`{"product_id":"P1","name":"x","note":"` + strings.Repeat("字", 201) + `"}`,
		`{"product_id":"P1","name":"` + strings.Repeat("名", 101) + `"}`,
		`{"product_id":"P1","name":"x","card_heads":["12ab"]}`,
		`{"product_id":"P1","name":"x","card_heads":["123"]}`,
		`{"product_id":"P1","name":"x","card_heads":["1234567890123"]}`,
		`{"product_id":"P1","name":"x","card_heads":` + heads11 + `}`,
		`{"product_id":"P1","name":"x","network":"` + strings.Repeat("V", 17) + `"}`,
		`{"product_id":"","name":"x"}`,
		`{"product_id":"a b","name":"x"}`,
		`{"product_id":"` + strings.Repeat("a", 65) + `","name":"x"}`,
		`{"product_id":"P1"`,
	}
	for _, body := range bad {
		if w := headsRoute(AdminAddCardHead, "POST", headsListRoute, base, body); w.Code != http.StatusBadRequest {
			t.Fatalf("%.80s: 应返回 400，得到 %d %s", body, w.Code, w.Body.String())
		}
	}
	if list, _ := db.ListCardHeads(string(providertest.TestUp)); len(list) != 0 {
		t.Fatalf("校验不通过不应写库: %+v", list)
	}
	for _, id := range []string{"a/b", "a b", "", strings.Repeat("a", 65), "中文"} {
		if err := validateCardHead(db.CardHead{ProductID: id, Name: "x"}); err == nil {
			t.Fatalf("product_id %q 应校验不通过", id)
		}
	}
	if err := validateCardHead(db.CardHead{ProductID: "2100050582268325889", Name: "美卡", CardHeads: []string{"537872712"}, ThreeDS: "yes", ApplePay: "no"}); err != nil {
		t.Fatalf("合法输入: %v", err)
	}

	if w := headsRoute(AdminAddCardHead, "POST", headsListRoute, base, `{"product_id":"P1","name":"x"}`); w.Code != http.StatusOK {
		t.Fatalf("添加: %d", w.Code)
	}
	if w := headsRoute(AdminUpdateCardHead, "PUT", headsItemRoute, base+"/P1", `{"three_ds":"maybe"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("修改时也要校验，得到 %d", w.Code)
	}
	if got, _ := db.GetCardHead(string(providertest.TestUp), "P1"); got.ThreeDS != "" {
		t.Fatalf("校验不通过不应写库: %+v", got)
	}
}
