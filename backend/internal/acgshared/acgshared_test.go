package acgshared

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
)

func TestSign_Vectors(t *testing.T) {
	// 规范串依次是：
	// 1. app_id=ag0123456789abcd&app_key=Key32&code=mw-plus-ph&num=2&key=Key32
	// 2. app_id=ag0123456789abcd&app_key=Key32&card_id=0&num=1&shared_code=mw-plus-ph&key=Key32
	// 3. app_id=ag0123456789abcd&app_key=Key32&code=x&sku[颜色]=红&sku[尺寸]=&sku[A]=1&key=Key32
	// 4. app_id=ag0123456789abcd&app_key=Key32&code=a&b=c+d 中文&key=Key32
	cases := []struct{ body, want string }{
		{"app_id=ag0123456789abcd&app_key=Key32&code=mw-plus-ph&num=2&race=&sign=whatever", "54437c21b855c09186c62ce29780343e"},
		{"shared_code=mw-plus-ph&num=1&card_id=0&password=&race=&app_id=ag0123456789abcd&app_key=Key32", "d2681f16ecd11d47540603c66bdcd975"},
		{"app_id=ag0123456789abcd&app_key=Key32&code=x&sku%5B%E9%A2%9C%E8%89%B2%5D=%E7%BA%A2&sku%5B%E5%B0%BA%E5%AF%B8%5D=&sku%5BA%5D=1", "c9e1f9e9bb51e70a1e07331b10bd6fd1"},
		{"code=a%26b%3Dc%2Bd+%E4%B8%AD%E6%96%87&app_key=Key32&app_id=ag0123456789abcd", "b99c1131176af2d4c1ba1d46dc1f6e4f"},
	}
	for i, c := range cases {
		f, err := ParseForm([]byte(c.body))
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if got := Sign(f, "Key32"); got != c.want {
			t.Errorf("case %d: sign=%s want %s", i, got, c.want)
		}
	}
}

func TestVerify(t *testing.T) {
	base := "app_id=ag0123456789abcd&app_key=Key32&code=mw-plus-ph&num=2&race="
	f := mustParse(t, base)
	good := Sign(f, "Key32")
	if !Verify(mustParse(t, base+"&sign="+good), "Key32") {
		t.Error("正确签名应通过")
	}
	if Verify(mustParse(t, base+"&sign="+strings.ToUpper(good)), "Key32") {
		t.Error("大写签名应失败")
	}
	if Verify(mustParse(t, base), "Key32") {
		t.Error("缺 sign 应失败")
	}
	if Verify(mustParse(t, base+"&sign%5Ba%5D=x"), "Key32") {
		t.Error("嵌套 sign 应失败")
	}
}

func mustParse(t *testing.T, s string) Form {
	t.Helper()
	f, err := ParseForm([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseForm_Order(t *testing.T) {
	f := mustParse(t, "b=2&a=1&sku%5Bx%5D=1&sku%5By%5D=2&sku%5Bx%5D=3&a=9&k")
	var keys []string
	for _, fd := range f.Fields {
		keys = append(keys, fd.Key)
	}
	if !reflect.DeepEqual(keys, []string{"b", "a", "sku", "k"}) {
		t.Fatalf("顺序 %v", keys)
	}
	if v, ok := f.Get("a"); !ok || v != "9" {
		t.Errorf("a=%q %v", v, ok)
	}
	var sku []SubField
	for _, fd := range f.Fields {
		if fd.Key == "sku" {
			sku = fd.Sub
		}
	}
	if !reflect.DeepEqual(sku, []SubField{{"x", "3"}, {"y", "2"}}) {
		t.Errorf("sku=%v", sku)
	}
	if _, ok := f.Get("sku"); ok {
		t.Error("Get(sku) 应为 false")
	}
	if !f.Has("sku") {
		t.Error("Has(sku) 应为 true")
	}
	if v, ok := f.Get("k"); !ok || v != "" {
		t.Errorf("k=%q %v", v, ok)
	}
	if _, ok := f.Get("none"); ok || f.Has("none") {
		t.Error("不存在的键")
	}
}

func TestParseForm_Invalid(t *testing.T) {
	for _, body := range []string{"%zz=1", "a=%E4%B", "a%5B%5D=1", "a%5Bb%5D%5Bc%5D=1", "a%5B=1", "sku=1&sku%5Ba%5D=2", "a%5D=1", "sku%5Ba%5D=2&sku=1"} {
		if _, err := ParseForm([]byte(body)); err != ErrBadForm {
			t.Errorf("%q: err=%v", body, err)
		}
	}
	f, err := ParseForm(nil)
	if err != nil || len(f.Fields) != 0 {
		t.Errorf("空请求体: %v %v", f, err)
	}
}

func TestMoney(t *testing.T) {
	for _, c := range []struct {
		m    Money
		want string
	}{{325, `{"P":3.25}`}, {5, `{"P":0.05}`}, {0, `{"P":0.00}`}, {100000, `{"P":1000.00}`}} {
		b, err := json.Marshal(struct{ P Money }{c.m})
		if err != nil || string(b) != c.want {
			t.Errorf("%d: %s %v want %s", c.m, b, err, c.want)
		}
	}
}

func TestValidRequestNo(t *testing.T) {
	for s, want := range map[string]bool{
		"202611050000000001":    true,
		strings.Repeat("a", 64): true,
		strings.Repeat("a", 65): false,
		"":                      false,
		"a-b":                   false,
		"中":                     false,
		"abc\n":                 false,
	} {
		if got := ValidRequestNo(s); got != want {
			t.Errorf("%q: %v", s, got)
		}
	}
}

func TestParseQuantity(t *testing.T) {
	if MaxQuantity != cardplatform.MaxIssueCount {
		t.Fatal("MaxQuantity 与 MaxIssueCount 不一致")
	}
	for s, want := range map[string]bool{"1": true, "200": true, "0": false, "201": false, "1.5": false, "abc": false, "": false, "+1": false, "-1": false} {
		n, ok := ParseQuantity(s)
		if ok != want {
			t.Errorf("%q: ok=%v", s, ok)
		}
		if ok && strconvAtoi(s) != n {
			t.Errorf("%q: n=%d", s, n)
		}
	}
}

func strconvAtoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestNewItem(t *testing.T) {
	b, err := json.Marshal(NewItem(ItemInput{ID: 7, Code: "mw-plus-ph", Name: "Plus 菲律宾", Sort: 3, PriceCents: 325, Stock: 200}))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id": 7.0, "code": "mw-plus-ph", "name": "Plus 菲律宾", "description": ItemDescription, "cover": "",
		"price": 3.25, "factory_price": 3.25, "user_price": 0.0, "stock": 200.0, "config": "", "widget": "[]",
		"sort": 3.0, "status": 1.0, "category_id": 1.0,
	}
	for _, k := range []string{"delivery_way", "contact_type", "password_status", "minimum", "maximum", "only_user", "purchase_count", "draft_status", "draft_premium", "inventory_hidden", "seckill_status"} {
		want[k] = 0.0
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("got %v\nwant %v", m, want)
	}
	if got := NewItem(ItemInput{Code: "c1"}).Name; got != "c1" {
		t.Errorf("空名应用 code: %q", got)
	}
	if got := NewItem(ItemInput{Code: "c", Name: strings.Repeat("汉", 300)}).Name; len([]rune(got)) != 255 {
		t.Errorf("截断后 %d 个字", len([]rune(got)))
	}
}

func TestAllCategory(t *testing.T) {
	b, _ := json.Marshal(AllCategory(nil))
	want := `[{"id":1,"name":"全部商品","sort":0,"icon":"","status":1,"pid":0,"children":[]}]`
	if string(b) != want {
		t.Errorf("%s", b)
	}
}

func TestSecretText(t *testing.T) {
	got := SecretText("shop.example", []string{"AB+C", "XYZ"})
	want := "AB+C  兑换：https://shop.example/recharge?code=AB%2BC\nXYZ  兑换：https://shop.example/recharge?code=XYZ"
	if got != want {
		t.Errorf("%q", got)
	}
}
