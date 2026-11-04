package cardpool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

func prod(id, name, label, country, mode, cur string) marswallex.CardProduct {
	return marswallex.CardProduct{ProductID: id, ProductName: name, CardLabel: label, CardCountry: country, CardMode: mode, CardCurrency: cur}
}

func TestSuggestProduct(t *testing.T) {
	detail := marswallex.CardDetail{CardNo: "4288201234567890", CardLabel: "visa", CardCountry: "us", CardMode: "prepaid", CardCurrency: "usd"}
	cases := []struct {
		name     string
		products []marswallex.CardProduct
		want     string
		cands    []string
	}{
		{"只有一个一致的", []marswallex.CardProduct{
			prod("p1", "美卡-428820", "VISA", "US", "PREPAID", "USD"),
			prod("p2", "欧卡", "VISA", "DE", "PREPAID", "EUR"),
		}, "p1", []string{"p1"}},
		{"多个靠卡号前缀选出", []marswallex.CardProduct{
			prod("p1", "美卡-泛场景-428820-充值卡", "VISA", "US", "PREPAID", "USD"),
			prod("p2", "美卡-泛场景-537872-充值卡", "VISA", "US", "PREPAID", "USD"),
		}, "p1", []string{"p1", "p2"}},
		{"产品名数字段比 6 位长，比较前 6 位", []marswallex.CardProduct{
			prod("p1", "美卡-428820999", "VISA", "US", "PREPAID", "USD"),
			prod("p2", "美卡-537872712", "VISA", "US", "PREPAID", "USD"),
		}, "p1", []string{"p1", "p2"}},
		{"多个都匹配则留空", []marswallex.CardProduct{
			prod("p1", "美卡-428820-A", "VISA", "US", "PREPAID", "USD"),
			prod("p2", "美卡-428820-B", "VISA", "US", "PREPAID", "USD"),
		}, "", []string{"p1", "p2"}},
		{"都不匹配则留空", []marswallex.CardProduct{
			prod("p1", "美卡-111111", "VISA", "US", "PREPAID", "USD"),
			prod("p2", "美卡-222222", "VISA", "US", "PREPAID", "USD"),
		}, "", []string{"p1", "p2"}},
		{"名字里没有数字段则留空", []marswallex.CardProduct{
			prod("p1", "美卡 A", "VISA", "US", "PREPAID", "USD"),
			prod("p2", "美卡 B", "VISA", "US", "PREPAID", "USD"),
		}, "", []string{"p1", "p2"}},
		{"短数字段不算前缀", []marswallex.CardProduct{
			prod("p1", "美卡-4288-A", "VISA", "US", "PREPAID", "USD"),
			prod("p2", "美卡-4288-B", "VISA", "US", "PREPAID", "USD"),
		}, "", []string{"p1", "p2"}},
		{"没有一致的产品", []marswallex.CardProduct{prod("p1", "x-428820", "MASTER", "US", "PREPAID", "USD")}, "", []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, cands := suggestProduct(detail, c.products)
			if got != c.want {
				t.Errorf("suggested=%q want %q", got, c.want)
			}
			if strings.Join(cands, ",") != strings.Join(c.cands, ",") {
				t.Errorf("candidates=%v want %v", cands, c.cands)
			}
		})
	}
}

func TestPANParts(t *testing.T) {
	cases := []struct{ in, f6, l4 string }{
		{"4288201234567890", "428820", "7890"},
		{"4288 2012 3456 7890", "428820", "7890"},
		{"4111********1234", "", "1234"}, // 打码卡号：前面不足 6 位数字，不给前 6 位
		{"123", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		f, l := panParts(c.in)
		if f != c.f6 || l != c.l4 {
			t.Errorf("%q: %q %q want %q %q", c.in, f, l, c.f6, c.l4)
		}
	}
}

func TestPreviewCard(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	const full = "4288201234567890"
	mw.On("GET", "/v1/card/detail/c1", mwtest.OK(`{"card_id":"c1","card_no":"`+full+`","card_label":"visa","card_country":"us","card_mode":"prepaid","card_currency":"usd","expiry_date":"1228","status":"ACTIVE","available_balance":"12.34","freeze_balance":"0.50"}`))
	mw.On("GET", pathProducts, mwtest.OK(`[{"product_id":"p1","product_name":"美卡-428820","card_label":"VISA","card_country":"US","card_mode":"PREPAID","card_currency":"USD"}]`))
	p, err := s.PreviewCard(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if p.CardID != "c1" || p.First6 != "428820" || p.Last4 != "7890" || p.AvailableCents != 1234 || p.FrozenCents != 50 ||
		p.Expiry != "1228" || p.Status != "ACTIVE" || p.AlreadyInPool || p.SuggestedProductID != "p1" || len(p.Candidates) != 1 {
		t.Fatalf("%+v", p)
	}
	if strings.Contains(strings.Join([]string{p.First6, p.Last4, p.CardLabel, p.Expiry}, "|"), full) {
		t.Fatal("泄露完整卡号")
	}
	// 不写库
	if _, err := getCard(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("预览不应写库: %v", err)
	}
	// 已在池中
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	p, err = s.PreviewCard(ctx, "c1")
	if err != nil || !p.AlreadyInPool {
		t.Fatalf("%v %+v", err, p)
	}
	if _, err := s.PreviewCard(ctx, "  "); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("空卡 ID: %v", err)
	}
}
