package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/provider/providertest"
)

// fakeUp 是 testup 的适配器，带卡密查询能力：按码（完全相同才算命中，不替调用方规范化）返回预设的结果。
// panics 里的码查询时 panic，errs 里的码返回错误，都没有的码按「不认得」。
type fakeUp struct {
	upstreamAdapter // 预览、兑换等用不到的方法借 fakeUpstream 的

	facts  map[string]afterSaleFacts
	errs   map[string]error
	panics map[string]bool

	mu    *sync.Mutex
	asked *[]string
}

func (u fakeUp) Lookup(_ context.Context, code, _ string) (afterSaleFacts, error) {
	u.mu.Lock()
	*u.asked = append(*u.asked, code)
	u.mu.Unlock()
	if u.panics[code] {
		panic("假上游出错 alice@example.com")
	}
	if err, ok := u.errs[code]; ok {
		return afterSaleFacts{}, err
	}
	return u.facts[code], nil
}

// calls 是到目前为止被查过的码，按顺序。
func (u fakeUp) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), *u.asked...)
}

// useFakeUp 注册 testup，接上 u 当它的适配器，返回接好的 u。
func useFakeUp(t *testing.T, u fakeUp) fakeUp {
	t.Helper()
	providertest.Register(t, providertest.NewTestUp())
	u.upstreamAdapter = &fakeUpstream{}
	u.mu, u.asked = &sync.Mutex{}, &[]string{}
	withUpstreamAdapter(t, providertest.TestUp, u)
	return u
}

func afterSaleRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/lookup/cdk", LookupCDKStatus)
	r.POST("/lookup/cdk/batch", LookupCDKStatusBatch)
	return r
}

func afterSaleGet(t *testing.T, code string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/lookup/cdk?code="+code, nil)
	w := httptest.NewRecorder()
	afterSaleRouter().ServeHTTP(w, req)
	return w
}

func afterSaleBatch(t *testing.T, codes ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/lookup/cdk/batch", strings.NewReader(localBody(t, map[string]any{"codes": codes})))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	afterSaleRouter().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("批量查询应 200: %d %s", w.Code, w.Body.String())
	}
	return w
}

// batchResults 按顺序取出批量查询的每一条。
func batchResults(t *testing.T, w *httptest.ResponseRecorder) []cdkLookupResult {
	t.Helper()
	var body struct {
		Results []cdkLookupResult `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %s", w.Body.String())
	}
	return body.Results
}

func TestAfterSaleMasksEmailFromProvider(t *testing.T) {
	useFakeUp(t, fakeUp{facts: map[string]afterSaleFacts{
		"TU-AAAA": {Found: true, Status: "used", AccountEmail: "alice@example.com"},
	}})

	w := afterSaleGet(t, "TU-AAAA")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); strings.Contains(body, "alice@example.com") || !strings.Contains(body, "al***@example.com") {
		t.Fatalf("单条查询的邮箱应打码: %s", body)
	}

	w = afterSaleBatch(t, "TU-AAAA")
	if body := w.Body.String(); strings.Contains(body, "alice@example.com") || !strings.Contains(body, "al***@example.com") {
		t.Fatalf("批量查询的邮箱应打码: %s", body)
	}
}

func TestAfterSaleMasksUnicodeEmail(t *testing.T) {
	cases := []struct{ email, want string }{
		{"张三丰@x.com", "张三***@x.com"},
		{"a@x.com", "a***@x.com"},
		{"abcdef", "ab***"},
	}
	facts := map[string]afterSaleFacts{}
	for i, tc := range cases {
		facts["TU-MAIL-"+string(rune('A'+i))] = afterSaleFacts{Found: true, Status: "used", AccountEmail: tc.email}
	}
	useFakeUp(t, fakeUp{facts: facts})

	for i, tc := range cases {
		code := "TU-MAIL-" + string(rune('A'+i))
		got := lookupAfterSale(context.Background(), code, "").AccountEmail
		if !utf8.ValidString(got) {
			t.Fatalf("%q 打码后不是合法 UTF-8: %q", tc.email, got)
		}
		if got != tc.want {
			t.Fatalf("%q 打码后应为 %q，实际 %q", tc.email, tc.want, got)
		}
		w := afterSaleGet(t, code)
		if !utf8.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), "张三丰@x.com") {
			t.Fatalf("响应里不能有乱码或原文邮箱: %s", w.Body.String())
		}
	}
}

func TestAfterSaleSanitizesNote(t *testing.T) {
	head := "卡台返回：账号 bob@x.com 处理失败，令牌 abcdefghijklmnopqrstuvwxyz0123 已作废。"
	note := head + strings.Repeat("长", 300-utf8.RuneCountInString(head))
	if n := utf8.RuneCountInString(note); n != 300 {
		t.Fatalf("测试数据应为 300 个字符，实际 %d", n)
	}
	useFakeUp(t, fakeUp{facts: map[string]afterSaleFacts{
		"TU-NOTE": {Found: true, Status: "failed", ProviderNote: note, Message: "使用失败：" + note},
	}})

	r := lookupAfterSale(context.Background(), "TU-NOTE", "")
	if n := utf8.RuneCountInString(r.ProviderNote); n > 200 || n == 0 {
		t.Fatalf("provider_note 应截到 200 个字符以内，实际 %d", n)
	}
	if r.Notes != r.ProviderNote {
		t.Fatalf("notes 和 provider_note 都应是清洗后的原文: %q / %q", r.Notes, r.ProviderNote)
	}
	for _, field := range []string{r.ProviderNote, r.Message} {
		if strings.Contains(field, "bob@x.com") || strings.Contains(field, "abcdefghijklmnopqrstuvwxyz0123") {
			t.Fatalf("邮箱和长串应被替换: %q", field)
		}
		if !strings.Contains(field, "***") {
			t.Fatalf("替换后应为 ***: %q", field)
		}
	}
	if !strings.HasPrefix(r.ProviderNote, "卡台返回：账号 *** 处理失败，令牌 *** 已作废。") {
		t.Fatalf("只替换邮箱和长串，其余原文保留: %q", r.ProviderNote)
	}
}

func TestAfterSaleUnsupportedProvider(t *testing.T) {
	providertest.Register(t, providertest.NewTestUp())
	withUpstreamAdapter(t, providertest.TestUp, &fakeUpstream{}) // 没有 Lookup

	w := afterSaleGet(t, "TU-AAAA")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	m := localJSON(t, w)
	if m["status"] != "unsupported" || m["message"] != "这张卡密暂不支持在线查询，请联系售卖方" {
		t.Fatalf("%v", m)
	}
}

func TestAfterSaleUnknownGuideFallsBack(t *testing.T) {
	useFakeUp(t, fakeUp{facts: map[string]afterSaleFacts{
		"TU-RETRY": {Found: true, Status: "failed", Reusable: true, Guide: "weird"},
		"TU-WAIT":  {Found: true, Status: "failed", Reusable: false, Guide: "weird"},
		"TU-KNOWN": {Found: true, Status: "failed", Reusable: true, Guide: "session_invalid"},
		"TU-NONE":  {Found: true, Status: "unused"},
	}})

	for code, want := range map[string]string{
		"TU-RETRY": "provider_retry",
		"TU-WAIT":  "provider_wait",
		"TU-KNOWN": "session_invalid",
		"TU-NONE":  "",
	} {
		if got := lookupAfterSale(context.Background(), code, "").Guide; got != want {
			t.Fatalf("%s: guide 应为 %q，实际 %q", code, want, got)
		}
	}
	if r := lookupAfterSale(context.Background(), "TU-RETRY", ""); !r.CanResubmit {
		t.Fatalf("码已恢复时 can_resubmit 应为 true: %+v", r)
	}
}

func TestAfterSaleChainFollows(t *testing.T) {
	useFakeUp(t, fakeUp{facts: map[string]afterSaleFacts{
		"TU-AAA": {Found: true, Status: "replaced", Replacement: "TU-BBB"},
		"TU-BBB": {Found: true, Status: "replaced", Replacement: "TU-CCC"},
		"TU-CCC": {Found: true, Status: "unused"},
	}})

	w := afterSaleGet(t, "TU-AAA")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var r cdkLookupResult
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Status != "replaced" || r.Message != "该卡密已换发新码" || r.CDKCode != "TU-AAA" {
		t.Fatalf("%s", w.Body.String())
	}
	if r.Replacement == nil || *r.Replacement != (lookupReplacement{Code: "TU-CCC", Status: "unused", Depth: 2}) {
		t.Fatalf("应跟到最终那张码: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"replacement":{"code":"TU-CCC","status":"unused","depth":2}`) {
		t.Fatalf("JSON 字段名不对: %s", w.Body.String())
	}
}

func TestAfterSaleChainNormalizesCodes(t *testing.T) {
	u := useFakeUp(t, fakeUp{facts: map[string]afterSaleFacts{
		"TU-AAA": {Found: true, Status: "replaced", Replacement: " tu-bbb\u200b "},
		"TU-BBB": {Found: true, Status: "unused"},
	}})

	r := lookupAfterSale(context.Background(), "TU-AAA", "")
	if r.Replacement == nil || r.Replacement.Code != "TU-BBB" || r.Replacement.Status != "unused" || r.Replacement.Depth != 1 {
		t.Fatalf("新码应规范化后再查: %+v %v", r.Replacement, u.calls())
	}

	// 入口的码同样带空格、小写、零宽字符
	r = lookupAfterSale(context.Background(), "\u200b tu-aaa\ufeff", "")
	if r.Status != "replaced" || r.Replacement == nil || r.Replacement.Code != "TU-BBB" {
		t.Fatalf("入口的码应规范化后再查: %+v %v", r, u.calls())
	}
}

func TestAfterSaleChainCycleAndCap(t *testing.T) {
	t.Run("A 换成 B，B 又换回 A：停在 B", func(t *testing.T) {
		u := useFakeUp(t, fakeUp{facts: map[string]afterSaleFacts{
			"TU-AAA": {Found: true, Status: "replaced", Replacement: "TU-BBB"},
			"TU-BBB": {Found: true, Status: "replaced", Replacement: "tu-aaa"},
		}})
		r := lookupAfterSale(context.Background(), "TU-AAA", "")
		if r.Replacement == nil || *r.Replacement != (lookupReplacement{Code: "TU-BBB", Status: "replaced", Depth: 1}) {
			t.Fatalf("遇到走过的码应停在 B: %+v", r.Replacement)
		}
		if got := u.calls(); len(got) != 2 {
			t.Fatalf("A、B 各查一次，不应绕圈: %v", got)
		}
	})

	t.Run("7 层的链停在第 5 层", func(t *testing.T) {
		facts := map[string]afterSaleFacts{}
		layer := func(i int) string { return "TU-L" + string(rune('0'+i)) }
		for i := 0; i < 7; i++ {
			facts[layer(i)] = afterSaleFacts{Found: true, Status: "replaced", Replacement: layer(i + 1)}
		}
		facts[layer(7)] = afterSaleFacts{Found: true, Status: "unused"}
		u := useFakeUp(t, fakeUp{facts: facts})
		r := lookupAfterSale(context.Background(), layer(0), "")
		if r.Replacement == nil || *r.Replacement != (lookupReplacement{Code: layer(5), Status: "replaced", Depth: 5}) {
			t.Fatalf("最多跟 5 层: %+v", r.Replacement)
		}
		if got := u.calls(); len(got) != 1+afterSaleChainMax {
			t.Fatalf("入口加 5 层，不应多查: %v", got)
		}
	})
}

func TestAfterSaleChainStopsOnError(t *testing.T) {
	useFakeUp(t, fakeUp{
		facts: map[string]afterSaleFacts{
			"TU-AAA": {Found: true, Status: "replaced", Replacement: "TU-BBB"},
			"TU-DDD": {Found: true, Status: "replaced", Replacement: "TU-EEE"},
			"TU-EEE": {Found: true, Status: "replaced", Replacement: "TU-FFF"},
			"TU-GGG": {Found: true, Status: "replaced", Replacement: "TU-HHH"},
			"TU-JJJ": {Found: true, Status: "replaced", Replacement: "TU-KKK"},
			"TU-KKK": {Found: true, Status: "replaced", Replacement: "TU-PANIC"},
		},
		errs:   map[string]error{"TU-BBB": errors.New("超时"), "TU-FFF": errors.New("超时")},
		panics: map[string]bool{"TU-PANIC": true},
	})

	for _, tc := range []struct {
		code string
		want *lookupReplacement
	}{
		{"TU-AAA", nil}, // 第 1 层就出错：不带 replacement
		{"TU-DDD", &lookupReplacement{Code: "TU-EEE", Status: "replaced", Depth: 1}}, // 第 2 层出错：停在第 1 层
		{"TU-GGG", nil}, // 第 1 层找不到
		{"TU-JJJ", &lookupReplacement{Code: "TU-KKK", Status: "replaced", Depth: 1}}, // 第 2 层 panic
	} {
		r := lookupAfterSale(context.Background(), tc.code, "")
		if r.Status != "replaced" {
			t.Fatalf("%s 自己的状态应为 replaced: %+v", tc.code, r)
		}
		switch {
		case tc.want == nil && r.Replacement != nil:
			t.Fatalf("%s 不应带 replacement: %+v", tc.code, r.Replacement)
		case tc.want != nil && (r.Replacement == nil || *r.Replacement != *tc.want):
			t.Fatalf("%s 应停在 %+v，实际 %+v", tc.code, tc.want, r.Replacement)
		}
		if r.ReplacedUnknown {
			t.Fatalf("%s 查不到新码状态不等于库里没有新码: %+v", tc.code, r)
		}
	}
}

func TestAfterSaleReplacedUnknown(t *testing.T) {
	useFakeUp(t, fakeUp{facts: map[string]afterSaleFacts{
		"TU-AAA": {Found: true, Status: "replaced", ReplacedButUnknown: true},
	}})

	w := afterSaleGet(t, "TU-AAA")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	m := localJSON(t, w)
	if m["status"] != "replaced" || m["replaced_unknown"] != true || m["message"] != "该卡密已换发新码" {
		t.Fatalf("%v", m)
	}
	if _, ok := m["replacement"]; ok {
		t.Fatalf("不应带 replacement: %v", m)
	}
}

func TestAfterSaleExpiresOnlyWhenUsed(t *testing.T) {
	exp := time.Date(2026, 12, 4, 8, 0, 0, 0, time.FixedZone("CST", 8*3600))
	useFakeUp(t, fakeUp{facts: map[string]afterSaleFacts{
		"TU-USED":   {Found: true, Status: "used", ExpiresAt: &exp},
		"TU-UNUSED": {Found: true, Status: "unused", ExpiresAt: &exp},
	}})

	m := localJSON(t, afterSaleGet(t, "TU-USED"))
	if m["expires_at"] != "2026-12-04T00:00:00Z" {
		t.Fatalf("used 应输出 RFC3339（UTC）的到期时间: %v", m)
	}
	w := afterSaleGet(t, "TU-UNUSED")
	if strings.Contains(w.Body.String(), "expires_at") {
		t.Fatalf("没用过的码不输出到期时间: %s", w.Body.String())
	}
}

func TestAfterSaleProviderPanicIsUnknown(t *testing.T) {
	useFakeUp(t, fakeUp{
		facts:  map[string]afterSaleFacts{"TU-NOPE": {}},
		errs:   map[string]error{"TU-ERR": errors.New("卡台超时")},
		panics: map[string]bool{"TU-PANIC": true},
	})

	for _, code := range []string{"TU-PANIC", "TU-ERR"} {
		w := afterSaleGet(t, code)
		if w.Code != http.StatusOK {
			t.Fatalf("%s 查询出错应回 200: %d %s", code, w.Code, w.Body.String())
		}
		m := localJSON(t, w)
		if m["status"] != "unknown" || m["message"] != "暂时查不到，请稍后再试" || m["cdk_code"] != code {
			t.Fatalf("%s: %v", code, m)
		}
		if strings.Contains(w.Body.String(), "alice@example.com") || strings.Contains(w.Body.String(), "卡台超时") {
			t.Fatalf("不能带错误细节: %s", w.Body.String())
		}
	}

	// 真找不到：照旧 404，原文不变
	w := afterSaleGet(t, "TU-NOPE")
	if w.Code != http.StatusNotFound {
		t.Fatalf("找不到应回 404: %d %s", w.Code, w.Body.String())
	}
	m := localJSON(t, w)
	if m["error"] != "未找到该卡密记录" || m["status"] != "unknown" || m["message"] != "未找到该卡密。请确认输入完整卡密。" {
		t.Fatalf("%v", m)
	}
}

func TestAfterSaleBatchIsolatesPanic(t *testing.T) {
	useFakeUp(t, fakeUp{
		facts: map[string]afterSaleFacts{
			"TU-AAA": {Found: true, Status: "used"},
			"TU-CCC": {Found: true, Status: "unused"},
		},
		panics: map[string]bool{"TU-PANIC": true},
	})

	rs := batchResults(t, afterSaleBatch(t, "TU-AAA", "TU-PANIC", "TU-CCC"))
	if len(rs) != 3 {
		t.Fatalf("应返回 3 条: %+v", rs)
	}
	if rs[0].CDKCode != "TU-AAA" || rs[0].Status != "used" || !rs[0].Used {
		t.Fatalf("第 1 条: %+v", rs[0])
	}
	if rs[1].CDKCode != "TU-PANIC" || rs[1].Status != "unknown" || rs[1].Message != "暂时查不到，请稍后再试" {
		t.Fatalf("第 2 条: %+v", rs[1])
	}
	if rs[2].CDKCode != "TU-CCC" || rs[2].Status != "unused" {
		t.Fatalf("第 3 条: %+v", rs[2])
	}
}
