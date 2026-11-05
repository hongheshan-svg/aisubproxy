package handler

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/tuzi/cdk-recharge-system/internal/acgshared"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

// ---- 夹具 ----

const (
	sharedFixAppID  = "agFixture0000001"                 // ag + 14 位
	sharedFixKey    = "Kf8pQ2xZ7vN3mB6tR1wY4uE9sA5dG0hJ" // 32 位
	sharedFixRemote = "203.0.113.7:5000"
)

// sharedFix 是 /shared/* 测试的现成环境：在 orderFix 之上给 alice 开通异次元对接，商户 ID 是 appID、密钥是 key。
// TRUSTED_PROXIES 设成空：谁的 X-Forwarded-Proto 都不信。
type sharedFix struct {
	*orderFix
	appID, key string
}

func newSharedFix(t *testing.T, e *agentEnv, ready bool) *sharedFix {
	t.Helper()
	t.Setenv("TRUSTED_PROXIES", "")
	f := &sharedFix{orderFix: newOrderFix(t, e, ready), appID: sharedFixAppID, key: sharedFixKey}
	mk, err := secretbox.LoadKey()
	if err != nil {
		t.Fatal(err)
	}
	f.storeKey(mk, f.key)
	return f
}

// storeKey 用主密钥 masterKey 把 plain 加密后存成 alice 的商户密钥（商户 ID 不变）。
// 名字按规格写死成 acg-shared:<agent_id>:<app_id>，格式变了这里会先发现。
func (f *sharedFix) storeKey(masterKey []byte, plain string) {
	f.t.Helper()
	sealed, err := secretbox.Seal(masterKey, fmt.Sprintf("acg-shared:%d:%s", f.aid, f.appID), plain)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := db.SetAgentSharedKey(context.Background(), f.aid, f.appID, sealed, f.clock); err != nil {
		f.t.Fatal(err)
	}
}

// sharedReq 是 post 的选项。
type sharedReq struct {
	tls     bool
	remote  string
	headers [][2]string
	raw     *string
	key     string
	appID   string
}

type sharedOpt func(*sharedReq)

// noTLS 不带 TLS（默认带）。
func noTLS() sharedOpt { return func(r *sharedReq) { r.tls = false } }

// from 改对端地址（默认 203.0.113.7:5000）。
func from(addr string) sharedOpt { return func(r *sharedReq) { r.remote = addr } }

// header 设请求头，可以覆盖默认的 Content-Type；名字是 Host 时改 req.Host。
func header(k, v string) sharedOpt {
	return func(r *sharedReq) { r.headers = append(r.headers, [2]string{k, v}) }
}

// rawBody 原样发这个请求体，不补 app_id、app_key、sign。
func rawBody(s string) sharedOpt { return func(r *sharedReq) { r.raw = &s } }

// signWith 用这个密钥签名，app_key 也填它（和配错密钥的下游一样）。
func signWith(key string) sharedOpt { return func(r *sharedReq) { r.key = key } }

// appID 改商户 ID。
func appID(id string) sharedOpt { return func(r *sharedReq) { r.appID = id } }

// sharedBody 按给定顺序编码 fields，末尾补 app_id、app_key（等于签名用的密钥，和下游一样），再按协议补 sign。
// 编出来的串解析不了时（测试故意构造的坏字段）不补 sign。
func sharedBody(fields [][2]string, id, key string) string {
	parts := make([]string, 0, len(fields)+3)
	for _, kv := range fields {
		parts = append(parts, url.QueryEscape(kv[0])+"="+url.QueryEscape(kv[1]))
	}
	parts = append(parts, "app_id="+url.QueryEscape(id), "app_key="+url.QueryEscape(key))
	body := strings.Join(parts, "&")
	if form, err := acgshared.ParseForm([]byte(body)); err == nil {
		body += "&sign=" + acgshared.Sign(form, key)
	}
	return body
}

// post 发 POST /shared/<path>。默认带 TLS、对端 203.0.113.7:5000、Content-Type 是 form-urlencoded。
// 不调 t.Fatal，可以在 goroutine 里用。
func (f *sharedFix) post(path string, fields [][2]string, opts ...sharedOpt) *httptest.ResponseRecorder {
	o := sharedReq{tls: true, remote: sharedFixRemote, key: f.key, appID: f.appID}
	for _, opt := range opts {
		opt(&o)
	}
	body := ""
	if o.raw != nil {
		body = *o.raw
	} else {
		body = sharedBody(fields, o.appID, o.key)
	}
	req := httptest.NewRequest("POST", "/shared/"+path, strings.NewReader(body))
	req.RemoteAddr = o.remote
	req.TLS = nil
	if o.tls {
		req.TLS = &tls.ConnectionState{}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, h := range o.headers {
		if strings.EqualFold(h[0], "Host") {
			req.Host = h[1]
			continue
		}
		req.Header.Set(h[0], h[1])
	}
	w := httptest.NewRecorder()
	f.r.ServeHTTP(w, req)
	return w
}

// decodeShared 解开共享协议的响应，同时要求 HTTP 200 和 Cache-Control: no-store。
func decodeShared(t *testing.T, w *httptest.ResponseRecorder) (code int, msg string, data json.RawMessage) {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("HTTP 状态应为 200: %d %s", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("应带 Cache-Control: no-store，实际 %q", cc)
	}
	var r struct {
		Code *int            `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || r.Code == nil {
		t.Fatalf("响应不是共享协议的格式: %s", w.Body.String())
	}
	return *r.Code, r.Msg, r.Data
}

// mustShared 要求成功（code 200、msg success），返回 data。
func (f *sharedFix) mustShared(path string, fields [][2]string, opts ...sharedOpt) json.RawMessage {
	f.t.Helper()
	code, msg, data := decodeShared(f.t, f.post(path, fields, opts...))
	if code != 200 || msg != "success" {
		f.t.Fatalf("%s 应成功: code=%d msg=%q", path, code, msg)
	}
	return data
}

// sharedRefused 要求失败：code 0、msg 等于 want、没有 data。
func (f *sharedFix) sharedRefused(path string, fields [][2]string, want string, opts ...sharedOpt) {
	f.t.Helper()
	code, msg, data := decodeShared(f.t, f.post(path, fields, opts...))
	if code != 0 || msg != want || data != nil {
		f.t.Fatalf("%s 应失败「%s」: code=%d msg=%q data=%s", path, want, code, msg, data)
	}
}

func rawFields(t *testing.T, data json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("data 不是对象: %s", data)
	}
	return m
}

func jsonString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("不是字符串: %s", raw)
	}
	return s
}

// signOf 取请求体里的 sign。
func signOf(t *testing.T, body string) string {
	t.Helper()
	form, err := acgshared.ParseForm([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := form.Get("sign")
	if !ok || s == "" {
		t.Fatalf("没有 sign: %s", body)
	}
	return s
}

// slowReadyCheck 把就绪检查换成睡 1 秒再说能出码；它不碰库和全局变量，测试结束后还会睡完剩下的时间。
func slowReadyCheck(t *testing.T) {
	old := agentReadyCheck
	agentReadyCheck = func(context.Context, db.AgentProduct) (provider.CardSupply, bool) {
		time.Sleep(time.Second)
		return nil, true
	}
	t.Cleanup(func() { agentReadyCheck = old })
}

// ---- connect ----

func TestShared_Connect(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), false)
	m := rawFields(t, f.mustShared("authentication/connect", nil))
	if got := jsonString(t, m["shopName"]); got != "Recharge Portal（美元报价）" {
		t.Fatalf("shopName=%q", got)
	}
	if string(m["balance"]) != "10.00" {
		t.Fatalf("balance 原始 JSON 应为 10.00: %s", m["balance"])
	}
	if len(m) != 2 {
		t.Fatalf("connect 只应有 shopName、balance: %v", m)
	}
}

func TestShared_ConnectShopNameLimits(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), false)
	// 控制字符放在最前面：先去掉再截断，才能刚好 128 个字
	localSet(t, "brand_name", "\x07"+strings.Repeat("店", 200))
	name := jsonString(t, rawFields(t, f.mustShared("authentication/connect", nil))["shopName"])
	if n := len([]rune(name)); n != 128 {
		t.Fatalf("shopName 应为 128 个字: %d", n)
	}
	if !strings.HasSuffix(name, "（美元报价）") {
		t.Fatalf("应以「（美元报价）」结尾: %q", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			t.Fatalf("不应含控制字符: %q", name)
		}
	}
	if name != strings.Repeat("店", 122)+"（美元报价）" {
		t.Fatalf("shopName=%q", name)
	}
}

// ---- items、item ----

type sharedCategory struct {
	ID       int                          `json:"id"`
	Name     string                       `json:"name"`
	Sort     int                          `json:"sort"`
	Icon     string                       `json:"icon"`
	Status   int                          `json:"status"`
	PID      int                          `json:"pid"`
	Children []map[string]json.RawMessage `json:"children"`
}

func sharedItems(t *testing.T, f *sharedFix) []sharedCategory {
	t.Helper()
	var cats []sharedCategory
	data := f.mustShared("commodity/items", nil)
	if err := json.Unmarshal(data, &cats); err != nil {
		t.Fatalf("items 的 data 不是分类数组: %s", data)
	}
	return cats
}

func TestShared_Items(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	f.seedProduct("mw-plus-us", "plus", "US") // 没定价
	off := f.seedProduct("mw-pro-20x-us", "pro_20x", "US")
	f.setPrice(off, 800)
	f.setEnabled(off, false) // 定了价但下架

	cats := sharedItems(t, f)
	if len(cats) != 1 || cats[0].ID != 1 || cats[0].Name != "全部商品" || cats[0].Status != 1 || cats[0].PID != 0 {
		t.Fatalf("应只有一个「全部商品」分类: %+v", cats)
	}
	ch := cats[0].Children
	if len(ch) != 1 {
		t.Fatalf("只应列出能买的商品: %v", ch)
	}
	it := ch[0]
	if jsonString(t, it["code"]) != "mw-plus-ph" || string(it["id"]) != fmt.Sprint(f.pid) {
		t.Fatalf("商品不对: %v", it)
	}
	if string(it["price"]) != "5.00" || string(it["factory_price"]) != "5.00" || string(it["stock"]) != "200" {
		t.Fatalf("price=%s factory_price=%s stock=%s", it["price"], it["factory_price"], it["stock"])
	}

	t.Run("未就绪", func(t *testing.T) {
		f := newSharedFix(t, newAgentEnv(t), false)
		ch := sharedItems(t, f)[0].Children
		if len(ch) != 1 || string(ch[0]["stock"]) != "0" {
			t.Fatalf("未就绪时 stock 应为 0: %v", ch)
		}
	})
	t.Run("没有能买的商品", func(t *testing.T) {
		f := newSharedFix(t, newAgentEnv(t), false)
		f.setEnabled(f.pid, false)
		data := f.mustShared("commodity/items", nil)
		if !strings.Contains(string(data), `"children":[]`) {
			t.Fatalf("children 应为空数组: %s", data)
		}
	})
}

func TestShared_Item(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	for _, fields := range [][][2]string{
		{{"code", "mw-plus-ph"}},
		{{"sharedCode", "mw-plus-ph"}},
		{{"code", "mw-plus-ph"}, {"sharedCode", "mw-plus-ph"}}, // 3.8.5 两个都发
	} {
		m := rawFields(t, f.mustShared("commodity/item", fields))
		if jsonString(t, m["code"]) != "mw-plus-ph" || string(m["id"]) != fmt.Sprint(f.pid) {
			t.Fatalf("%v: 商品不对: %v", fields, m)
		}
		if string(m["price"]) != "5.00" || string(m["stock"]) != "200" || jsonString(t, m["description"]) != acgshared.ItemDescription {
			t.Fatalf("%v: price=%s stock=%s", fields, m["price"], m["stock"])
		}
	}
	f.sharedRefused("commodity/item", [][2]string{{"code", "no-such-product"}}, "商品不存在")
	f.sharedRefused("commodity/item", nil, "商品不存在")
	f.seedProduct("mw-plus-us", "plus", "US") // 没定价
	f.sharedRefused("commodity/item", [][2]string{{"code", "mw-plus-us"}}, "当前商品已停售")
	f.setEnabled(f.pid, false)
	f.sharedRefused("commodity/item", [][2]string{{"sharedCode", "mw-plus-ph"}}, "当前商品已停售")

	t.Run("未就绪", func(t *testing.T) {
		f := newSharedFix(t, newAgentEnv(t), false)
		m := rawFields(t, f.mustShared("commodity/item", [][2]string{{"code", "mw-plus-ph"}}))
		if string(m["stock"]) != "0" {
			t.Fatalf("未就绪时 stock 应为 0: %s", m["stock"])
		}
	})
}

// ---- stock、valuation、inventory、inventoryState ----

func TestShared_StockAndSpec(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	for _, fields := range [][][2]string{
		{{"code", "mw-plus-ph"}},
		{{"code", "mw-plus-ph"}, {"race", ""}}, // 没有种类的商品下游发空串
	} {
		if data := f.mustShared("commodity/stock", fields); string(data) != `{"stock":200}` {
			t.Fatalf("%v: 就绪时应为 200: %s", fields, data)
		}
	}
	f.sharedRefused("commodity/stock", [][2]string{{"code", "mw-plus-ph"}, {"race", "x"}}, "该商品没有可选规格")
	f.sharedRefused("commodity/stock", [][2]string{{"code", "mw-plus-ph"}, {"sku[a]", "b"}}, "该商品没有可选规格")
	f.sharedRefused("commodity/stock", [][2]string{{"code", "mw-plus-ph"}, {"race[a]", ""}}, "该商品没有可选规格")
	f.sharedRefused("commodity/stock", [][2]string{{"code", "no-such-product"}}, "商品不存在")

	t.Run("未就绪", func(t *testing.T) {
		f := newSharedFix(t, newAgentEnv(t), false)
		if data := f.mustShared("commodity/stock", [][2]string{{"code", "mw-plus-ph"}}); string(data) != `{"stock":0}` {
			t.Fatalf("未就绪时应为 0: %s", data)
		}
	})
}

func TestShared_ReadyTimeout(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	old := sharedReadyTimeout
	sharedReadyTimeout = 20 * time.Millisecond
	t.Cleanup(func() { sharedReadyTimeout = old })
	slowReadyCheck(t)

	timed := func(name string, fn func()) {
		t.Helper()
		start := time.Now()
		fn()
		if d := time.Since(start); d >= 500*time.Millisecond {
			t.Fatalf("%s 应在超时后立即返回，不等慢的检查: %v", name, d)
		}
	}
	timed("stock", func() {
		if data := f.mustShared("commodity/stock", [][2]string{{"code", "mw-plus-ph"}}); string(data) != `{"stock":0}` {
			t.Fatalf("超时应按未就绪: %s", data)
		}
	})
	timed("inventoryState", func() {
		f.sharedRefused("commodity/inventoryState", [][2]string{{"shared_code", "mw-plus-ph"}, {"num", "1"}}, "库存不足")
	})
	timed("items", func() {
		if ch := sharedItems(t, f)[0].Children; len(ch) != 1 || string(ch[0]["stock"]) != "0" {
			t.Fatalf("超时应按未就绪: %v", ch)
		}
	})
}

func TestShared_Valuation(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	m := rawFields(t, f.mustShared("commodity/valuation", [][2]string{{"code", "mw-plus-ph"}, {"num", "3"}, {"race", ""}, {"card_id", "0"}}))
	if string(m["price"]) != "15.00" || jsonString(t, m["currency_code"]) != "USD" {
		t.Fatalf("price=%s currency_code=%s", m["price"], m["currency_code"])
	}
	for _, num := range []string{"0", "201", "abc", ""} {
		f.sharedRefused("commodity/valuation", [][2]string{{"code", "mw-plus-ph"}, {"num", num}}, "购买数量需为 1–200")
	}
	f.sharedRefused("commodity/valuation", [][2]string{{"code", "mw-plus-ph"}}, "购买数量需为 1–200")
	f.sharedRefused("commodity/valuation", [][2]string{{"code", "mw-plus-ph"}, {"num", "1"}, {"race", "x"}}, "该商品没有可选规格")
	f.sharedRefused("commodity/valuation", [][2]string{{"code", "no-such-product"}, {"num", "1"}}, "商品不存在")
}

func TestShared_Inventory(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	data := f.mustShared("commodity/inventory", [][2]string{{"sharedCode", "mw-plus-ph"}, {"race", ""}})
	want := `{"config":"","count":200,"delivery_way":0,"draft_status":0,"factory_price":5.00,"is_category":0,"price":5.00,"user_price":0}`
	if string(data) != want {
		t.Fatalf("inventory:\n got %s\nwant %s", data, want)
	}
	f.sharedRefused("commodity/inventory", [][2]string{{"sharedCode", "mw-plus-ph"}, {"race", "x"}}, "该商品没有可选规格")
	f.sharedRefused("commodity/inventory", [][2]string{{"sharedCode", "no-such-product"}}, "商品不存在")

	t.Run("未就绪", func(t *testing.T) {
		f := newSharedFix(t, newAgentEnv(t), false)
		m := rawFields(t, f.mustShared("commodity/inventory", [][2]string{{"sharedCode", "mw-plus-ph"}}))
		if string(m["count"]) != "0" {
			t.Fatalf("未就绪时 count 应为 0: %s", m["count"])
		}
	})
}

func TestShared_InventoryState(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	for _, num := range []string{"1", "200"} {
		if data := f.mustShared("commodity/inventoryState", [][2]string{{"shared_code", "mw-plus-ph"}, {"num", num}, {"card_id", "0"}, {"race", ""}}); string(data) != `{}` {
			t.Fatalf("num=%s: data 应为空对象: %s", num, data)
		}
	}
	for _, num := range []string{"201", "0", "abc"} {
		f.sharedRefused("commodity/inventoryState", [][2]string{{"shared_code", "mw-plus-ph"}, {"num", num}}, "库存不足")
	}
	f.sharedRefused("commodity/inventoryState", [][2]string{{"shared_code", "mw-plus-ph"}, {"num", "1"}, {"race", "x"}}, "该商品没有可选规格")
	f.sharedRefused("commodity/inventoryState", [][2]string{{"shared_code", "no-such-product"}, {"num", "1"}}, "商品不存在")

	t.Run("未就绪", func(t *testing.T) {
		f := newSharedFix(t, newAgentEnv(t), false)
		f.sharedRefused("commodity/inventoryState", [][2]string{{"shared_code", "mw-plus-ph"}, {"num", "1"}}, "库存不足")
	})
}

// ---- 公共校验 ----

func TestShared_Errors(t *testing.T) {
	otherMaster := []byte("another-master-key-of-32-bytes!!")
	cases := []struct {
		name  string
		setup func(t *testing.T, f *sharedFix)
		opts  []sharedOpt
		want  string // 空串表示应成功
	}{
		{name: "无 TLS", opts: []sharedOpt{noTLS()}, want: "请使用 https 地址"},
		{name: "无 TLS、X-Forwarded-Proto 来自不可信对端", opts: []sharedOpt{noTLS(), header("X-Forwarded-Proto", "https")}, want: "请使用 https 地址"},
		{name: "无 TLS、伪造 X-Forwarded-For 回环", opts: []sharedOpt{noTLS(), header("X-Forwarded-For", "127.0.0.1")}, want: "请使用 https 地址"},
		{name: "无 TLS、来自 127.0.0.1", opts: []sharedOpt{noTLS(), from("127.0.0.1:5000")}},
		{name: "无 TLS、来自 ::1", opts: []sharedOpt{noTLS(), from("[::1]:5000")}},
		// 同机反代把明文 HTTP 转进来：对端是回环，但带了转发头，不能豁免
		{name: "无 TLS、来自 127.0.0.1、X-Forwarded-Proto: http", opts: []sharedOpt{noTLS(), from("127.0.0.1:5000"), header("X-Forwarded-Proto", "http")}, want: "请使用 https 地址"},
		{name: "无 TLS、来自 127.0.0.1、带 X-Forwarded-For、反代不可信", opts: []sharedOpt{noTLS(), from("127.0.0.1:5000"), header("X-Forwarded-For", "203.0.113.7")}, want: "请使用 https 地址"},
		{name: "无 TLS、来自 127.0.0.1、带 Forwarded", opts: []sharedOpt{noTLS(), from("127.0.0.1:5000"), header("Forwarded", "for=203.0.113.7;proto=http")}, want: "请使用 https 地址"},
		{name: "无 TLS、来自 127.0.0.1、带 X-Real-IP", opts: []sharedOpt{noTLS(), from("127.0.0.1:5000"), header("X-Real-IP", "203.0.113.7")}, want: "请使用 https 地址"},
		{name: "无 TLS、来自 127.0.0.1、X-Forwarded-Proto 为空值", opts: []sharedOpt{noTLS(), from("127.0.0.1:5000"), header("X-Forwarded-Proto", "")}, want: "请使用 https 地址"},
		{name: "无 TLS、来自 127.0.0.1、可信反代标了 https",
			setup: func(t *testing.T, f *sharedFix) { t.Setenv("TRUSTED_PROXIES", "127.0.0.1") },
			opts:  []sharedOpt{noTLS(), from("127.0.0.1:5000"), header("X-Forwarded-Proto", "https"), header("X-Forwarded-For", "203.0.113.7")}},
		{name: "无 TLS、可信反代标了 https",
			setup: func(t *testing.T, f *sharedFix) { t.Setenv("TRUSTED_PROXIES", "203.0.113.0/24") },
			opts:  []sharedOpt{noTLS(), header("X-Forwarded-Proto", "https")}},
		{name: "未知商户 ID", opts: []sharedOpt{appID("agNoSuchApp00001")}, want: "商户ID不存在"},
		{name: "商户 ID 是数组", opts: []sharedOpt{rawBody("app_id%5Bx%5D=1")}, want: "商户ID不存在"},
		{name: "签名用错密钥", opts: []sharedOpt{signWith("wrong-key-0123456789")}, want: "密钥错误"},
		{name: "缺 sign", opts: []sharedOpt{rawBody("app_id=" + sharedFixAppID + "&app_key=" + sharedFixKey)}, want: "密钥错误"},
		{name: "Content-Type 是 JSON", opts: []sharedOpt{header("Content-Type", "application/json")}, want: "请求格式不正确"},
		{name: "没有 Content-Type", opts: []sharedOpt{header("Content-Type", "")}, want: "请求格式不正确"},
		{name: "Content-Type 带 charset", opts: []sharedOpt{header("Content-Type", "Application/X-WWW-Form-Urlencoded; charset=UTF-8")}},
		{name: "请求体 65 KB", opts: []sharedOpt{rawBody("pad=" + strings.Repeat("a", 65<<10) + "&app_id=" + sharedFixAppID)}, want: "请求格式不正确"},
		{name: "表单格式不对", opts: []sharedOpt{rawBody("a%5B%5D=1&app_id=" + sharedFixAppID)}, want: "请求格式不正确"},
		{name: "白名单外",
			setup: func(t *testing.T, f *sharedFix) { f.allow("198.51.100.0/24") },
			want:  "来源 IP 不在白名单"},
		{name: "白名单内",
			setup: func(t *testing.T, f *sharedFix) { f.allow("198.51.100.0/24") },
			opts:  []sharedOpt{from("198.51.100.9:5000")}},
		{name: "白名单内（IPv4 映射成 IPv6）",
			setup: func(t *testing.T, f *sharedFix) { f.allow("198.51.100.0/24") },
			opts:  []sharedOpt{header("X-Forwarded-For", "::ffff:198.51.100.9")}}, // 测试引擎信任所有代理头
		{name: "白名单单个地址、不相等",
			setup: func(t *testing.T, f *sharedFix) { f.allow("2001:db8::1", "198.51.100.9") },
			opts:  []sharedOpt{from("198.51.100.10:5000")}, want: "来源 IP 不在白名单"},
		{name: "白名单单个地址、相等",
			setup: func(t *testing.T, f *sharedFix) { f.allow("2001:db8::1", "198.51.100.9") },
			opts:  []sharedOpt{from("198.51.100.9:5000")}},
		{name: "管理员关闭",
			setup: func(t *testing.T, f *sharedFix) {
				if err := db.SetAgentSharedAdminDisabled(context.Background(), f.aid, true, f.clock); err != nil {
					t.Fatal(err)
				}
			},
			want: "对接已被管理员关闭"},
		{name: "代理商被停用",
			setup: func(t *testing.T, f *sharedFix) {
				if err := db.SetAgentStatus(context.Background(), f.aid, db.AgentDisabled, f.clock); err != nil {
					t.Fatal(err)
				}
			},
			want: "代理商账号已停用"},
		{name: "代理商自己停用了对接",
			setup: func(t *testing.T, f *sharedFix) {
				if err := db.DisableAgentShared(context.Background(), f.aid, f.clock); err != nil {
					t.Fatal(err)
				}
			},
			want: "商户ID不存在"},
		{name: "主密钥清空",
			setup: func(t *testing.T, f *sharedFix) { t.Setenv("APP_MASTER_KEY", "") },
			want:  "系统维护中"},
		{name: "密钥用另一把主密钥加密",
			setup: func(t *testing.T, f *sharedFix) { f.storeKey(otherMaster, f.key) },
			want:  "商户ID不存在"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSharedFix(t, newAgentEnv(t), false)
			if c.setup != nil {
				c.setup(t, f)
			}
			if c.want == "" {
				f.mustShared("authentication/connect", nil, c.opts...)
				return
			}
			f.sharedRefused("authentication/connect", nil, c.want, c.opts...)
		})
	}
}

// allow 设 alice 的 IP 白名单。
func (f *sharedFix) allow(entries ...string) {
	f.t.Helper()
	if err := db.SetAgentSharedAllowlist(context.Background(), f.aid, entries, f.clock); err != nil {
		f.t.Fatal(err)
	}
}

// 主密钥不可用每天告警一次，文字和后台下单的一样。
func TestShared_NoMasterKeyAlert(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), false)
	t.Setenv("APP_MASTER_KEY", "")
	before := len(f.alertList())
	f.sharedRefused("authentication/connect", nil, "系统维护中")
	f.sharedRefused("commodity/items", nil, "系统维护中")
	alerts := f.alertList()[before:]
	if len(alerts) != 1 || !strings.Contains(alerts[0], "APP_MASTER_KEY 缺失或不合法") {
		t.Fatalf("应有 1 条主密钥告警: %v", alerts)
	}
}

func TestShared_SignLockout(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), false)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	old := sharedSignFails.now
	sharedSignFails.now = func() time.Time { return now }
	t.Cleanup(func() { sharedSignFails.now = old })

	// 9 次错签后一次成功会清零，再错 9 次也不锁
	for i := 0; i < 9; i++ {
		f.sharedRefused("authentication/connect", nil, "密钥错误", signWith("wrong-key-0123456789"))
	}
	f.mustShared("authentication/connect", nil)
	for i := 0; i < 9; i++ {
		f.sharedRefused("authentication/connect", nil, "密钥错误", signWith("wrong-key-0123456789"))
	}
	f.mustShared("authentication/connect", nil)

	for i := 0; i < 10; i++ {
		f.sharedRefused("authentication/connect", nil, "密钥错误", signWith("wrong-key-0123456789"))
	}
	f.sharedRefused("authentication/connect", nil, "密钥错误") // 签名对了也不行
	f.mustShared("authentication/connect", nil, from("198.51.100.20:5000"))
	now = now.Add(15*time.Minute + time.Second)
	f.mustShared("authentication/connect", nil)
}

func TestShared_UnknownAppIDLocksIP(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), false)
	for i := 0; i < 10; i++ {
		f.sharedRefused("authentication/connect", nil, "商户ID不存在", appID(fmt.Sprintf("agUnknown%07d", i)))
	}
	f.sharedRefused("authentication/connect", nil, "密钥错误")
	f.mustShared("authentication/connect", nil, from("198.51.100.20:5000"))
}

func TestShared_IPRateLimit(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), false)
	for i := 0; i < 120; i++ {
		if code, msg, _ := decodeShared(t, f.post("authentication/connect", nil)); code != 200 {
			t.Fatalf("第 %d 次应成功: %d %q", i+1, code, msg)
		}
	}
	f.sharedRefused("authentication/connect", nil, "请求太频繁，请稍后再试")
	f.mustShared("authentication/connect", nil, from("198.51.100.20:5000"))
	f.clock = f.clock.Add(time.Minute)
	f.mustShared("authentication/connect", nil)
}

func sharedRateKeys() int {
	sharedRateMu.Lock()
	defer sharedRateMu.Unlock()
	return len(sharedRateHits)
}

func TestSharedRateAllow_Bounded(t *testing.T) {
	resetSharedState()
	t.Cleanup(resetSharedState)
	old := sharedRateMaxKeys
	sharedRateMaxKeys = 3
	t.Cleanup(func() { sharedRateMaxKeys = old })
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if !sharedRateAllow("a", 1, now) || sharedRateAllow("a", 1, now) {
		t.Fatal("a 的额度是 1")
	}
	for i, k := range []string{"b", "c", "d"} {
		if !sharedRateAllow(k, 1, now.Add(time.Duration(i+1)*time.Second)) {
			t.Fatalf("%s 第一次应放行", k)
		}
	}
	if n := sharedRateKeys(); n != 3 {
		t.Fatalf("表里应最多 3 个键: %d", n)
	}
	if !sharedRateAllow("a", 1, now.Add(5*time.Second)) {
		t.Fatal("满了应淘汰最早的 a，a 重新计数")
	}
	// 一分钟没动静的键会被顺手清掉
	sharedRateAllow("e", 1, now.Add(2*time.Minute))
	if n := sharedRateKeys(); n != 1 {
		t.Fatalf("过期的键应被清掉: %d", n)
	}
}

func TestShared_Unimplemented404(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), false)
	for _, p := range []string{"commodity/draft", "commodity/draftCard", "commodity/query"} {
		if w := f.post(p, nil); w.Code != 404 {
			t.Fatalf("%s 应为 404: %d %s", p, w.Code, w.Body.String())
		}
	}
}

func TestShared_LastUsed(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), false)
	lastUsed := func() int64 {
		t.Helper()
		c, err := db.GetAgentSharedCreds(context.Background(), f.aid)
		if err != nil {
			t.Fatal(err)
		}
		return c.LastUsedAt
	}
	f.sharedRefused("authentication/connect", nil, "密钥错误", signWith("wrong-key-0123456789"))
	if got := lastUsed(); got != 0 {
		t.Fatalf("验签没通过不应更新: %d", got)
	}
	first := f.clock.Unix()
	f.mustShared("authentication/connect", nil)
	if got := lastUsed(); got != first {
		t.Fatalf("第一次成功后应为 %d: %d", first, got)
	}
	f.clock = f.clock.Add(30 * time.Second)
	f.mustShared("authentication/connect", nil)
	if got := lastUsed(); got != first {
		t.Fatalf("30 秒内不应再写: %d", got)
	}
	f.clock = f.clock.Add(31 * time.Second)
	f.mustShared("authentication/connect", nil)
	if got := lastUsed(); got != f.clock.Unix() {
		t.Fatalf("过了 60 秒应更新: %d", got)
	}
}

func TestShared_LogNoSecrets(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), false)
	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) })

	const wrong = "wrong-key-0123456789"
	f.sharedRefused("authentication/connect", nil, "密钥错误", signWith(wrong))
	f.mustShared("authentication/connect", nil)

	out := buf.String()
	for _, s := range []string{f.key, wrong, signOf(t, sharedBody(nil, f.appID, f.key)), signOf(t, sharedBody(nil, f.appID, wrong)), "app_key="} {
		if strings.Contains(out, s) {
			t.Fatalf("日志不应含 %q:\n%s", s, out)
		}
	}
	if !strings.Contains(out, f.appID) {
		t.Fatalf("验签失败应记下商户 ID:\n%s", out)
	}
}
