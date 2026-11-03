package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/provider/providertest"
)

// dumpExample 在设置了 PROVIDER_API_EXAMPLES（一个目录）时，把接口响应按缩进格式写成文件，供文档引用；平时不写。
func dumpExample(t *testing.T, name string, body []byte) {
	t.Helper()
	dir := os.Getenv("PROVIDER_API_EXAMPLES")
	if dir == "" {
		return
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, body, "", "  "); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// GET /admin/providers：内置两家的列表、产品线、配置、能力矩阵；能力矩阵和就绪接口出自同一个函数。
func TestAdminListProviders(t *testing.T) {
	e := newLocalEnv(t)
	w := e.do("GET", "/admin/providers", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	m := localJSON(t, w)
	list := m["providers"].([]any)
	if len(list) != 2 {
		t.Fatalf("内置两家: %v", list)
	}
	zc, mw := list[0].(map[string]any), list[1].(map[string]any)
	if zc["name"] != "zovocard" || zc["display_name"] != "ZovoCard" || zc["kind"] != "upstream" || zc["kind_text"] != "上游发码" ||
		zc["code_list"] != "cardplatform" || localBody(t, zc["products"]) != `["gpt","x","grok","claude"]` {
		t.Fatalf("ZovoCard: %v", zc)
	}
	if _, has := zc["code_prefix"]; has {
		t.Fatalf("上游型没有 code_prefix: %v", zc)
	}
	if mw["name"] != "marswallex" || mw["display_name"] != "MarsWallex" || mw["kind"] != "card_supply" || mw["kind_text"] != "供卡·本站付款" ||
		mw["code_prefix"] != "MW-" || mw["code_list"] != "local" || localBody(t, mw["products"]) != `["gpt"]` {
		t.Fatalf("MarsWallex: %v", mw)
	}
	if localBody(t, m["products"]) != `[{"key":"gpt","label":"ChatGPT"},{"key":"x","label":"X Premium"},{"key":"grok","label":"Grok"},{"key":"claude","label":"Claude"}]` {
		t.Fatalf("产品线: %v", m["products"])
	}
	// 出厂配置：全局默认 MarsWallex，它不做的 X、Grok、Claude 单独选 ZovoCard
	if m["default"] != "marswallex" || localBody(t, m["by_product"]) != `{"claude":"zovocard","grok":"zovocard","x":"zovocard"}` || localBody(t, m["warnings"]) != "[]" ||
		localBody(t, m["effective"]) != `{"claude":"zovocard","gpt":"marswallex","grok":"zovocard","x":"zovocard"}` {
		t.Fatalf("配置: %v", m)
	}
	r := localJSON(t, e.do("GET", "/admin/providers/readiness", ""))
	if caps := localBody(t, m["capabilities"]); caps != localBody(t, r["capabilities"]) || !strings.Contains(caps, "仅限无订阅账号") {
		t.Fatalf("两个接口的能力矩阵应一模一样:\n%s\n%s", caps, localBody(t, r["capabilities"]))
	}

	// 新注册的服务商自动出现在列表里；新的上游型默认没有码列表
	providertest.RegisterBoth(t)
	m = localJSON(t, e.do("GET", "/admin/providers", ""))
	byName := map[string]map[string]any{}
	for _, it := range m["providers"].([]any) {
		p := it.(map[string]any)
		byName[p["name"].(string)] = p
	}
	if tc := byName["testcard"]; tc == nil || tc["code_prefix"] != "TC-" || tc["code_list"] != "local" || tc["kind"] != "card_supply" {
		t.Fatalf("testcard: %v", tc)
	}
	if tu := byName["testup"]; tu == nil || tu["code_list"] != "" || tu["kind"] != "upstream" || localBody(t, tu["products"]) != `["gpt","x"]` {
		t.Fatalf("testup: %v", tu)
	}
	if caps := localBody(t, m["capabilities"]); !strings.Contains(caps, `"testcard":true`) || !strings.Contains(caps, `"testup":true`) {
		t.Fatalf("能力矩阵带上新服务商: %s", caps)
	}
}

// PUT /admin/providers/config：校验、保存、审计；被选用但没就绪的照样保存，在 not_ready 里列出；只影响之后新发的码。
func TestAdminPutProviderConfig(t *testing.T) {
	e := newLocalEnv(t)
	put := func(body string) (int, map[string]any) {
		w := e.do("PUT", "/admin/providers/config", body)
		return w.Code, localJSON(t, w)
	}
	for body, want := range map[string]string{
		`{"by_product":{"gpt":"marswallex"}}`:                    "请选择全局默认服务商",
		`{"default":"nope"}`:                                     `全局默认服务商 "nope" 未注册`,
		`{"default":"zovocard","by_product":{"tv":"zovocard"}}`:  `未知产品 "tv"`,
		`{"default":"zovocard","by_product":{"gpt":"nope"}}`:     `ChatGPT 选的服务商 "nope" 未注册`,
		`{"default":"zovocard","by_product":{"x":"marswallex"}}`: "MarsWallex 不做 X Premium",
		`not json`: "请求格式无效",
	} {
		if code, m := put(body); code != http.StatusBadRequest || !strings.Contains(m["error"].(string), want) {
			t.Fatalf("%s: %d %v", body, code, m)
		}
	}
	if strings.Contains(localAudit(t), "provider_config_update") {
		t.Fatal("校验失败不写审计")
	}

	w := e.do("PUT", "/admin/providers/config", `{"default":"zovocard","by_product":{"gpt":"marswallex","x":""}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	dumpExample(t, "put_providers_config", w.Body.Bytes())
	m := localJSON(t, w)
	if m["default"] != "zovocard" || localBody(t, m["by_product"]) != `{"gpt":"marswallex"}` ||
		localBody(t, m["effective"]) != `{"claude":"zovocard","gpt":"marswallex","grok":"zovocard","x":"zovocard"}` {
		t.Fatalf("保存结果: %v", m)
	}
	// MarsWallex 还没过首笔真实付款，被选用了照样保存，只做提示
	nr := m["not_ready"].([]any)
	if len(nr) != 1 || nr[0].(map[string]any)["provider"] != "marswallex" ||
		!strings.Contains(localBody(t, nr[0]), "需要一笔系统自动确认成功的真实付款") {
		t.Fatalf("not_ready: %v", nr)
	}
	if a := localAudit(t); !strings.Contains(a, "provider_config_update default marswallex→zovocard by_product claude=zovocard,grok=zovocard,x=zovocard→gpt=marswallex") {
		t.Fatalf("审计: %s", a)
	}
	got := localJSON(t, e.do("GET", "/admin/providers", ""))
	dumpExample(t, "get_providers", e.do("GET", "/admin/providers", "").Body.Bytes())
	if localBody(t, got["by_product"]) != `{"gpt":"marswallex"}` {
		t.Fatalf("GET 读回: %v", got)
	}

	// 没带 by_product：保留现有的按产品配置
	if code, m := put(`{"default":"marswallex"}`); code != http.StatusOK || m["default"] != "marswallex" || localBody(t, m["by_product"]) != `{"gpt":"marswallex"}` {
		t.Fatalf("%d %v", code, m)
	}
	// 带了空对象：清空
	if code, m := put(`{"default":"zovocard","by_product":{}}`); code != http.StatusOK || localBody(t, m["by_product"]) != "{}" || localBody(t, m["not_ready"]) != "[]" {
		t.Fatalf("%d %v", code, m)
	}

	// 配置只影响之后新发的码：已发的 MarsWallex 码照样走 MarsWallex
	e.seedRealPayment()
	code, _ := e.issueCode("plus", "PH")
	if code, m := put(`{"default":"zovocard","by_product":{}}`); code != http.StatusOK {
		t.Fatalf("%d %v", code, m)
	}
	if m := localJSON(t, e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code}))); m["provider"] != "marswallex" {
		t.Fatalf("已发的码不受配置影响: %v", m)
	}
}

// 库里存的服务商名已经没有注册、或者按产品配置坏了：接口照常返回，按规则退回并在 warnings 里说明。
func TestAdminProvidersConfigWarnings(t *testing.T) {
	e := newLocalEnv(t)
	localSet(t, provider.SettingDefault, "gone")
	localSet(t, provider.SettingByProduct, `{"gpt":"gone2","tv":"zovocard","x":"marswallex"}`)
	m := localJSON(t, e.do("GET", "/admin/providers", ""))
	warn := localBody(t, m["warnings"])
	for _, want := range []string{`全局默认服务商 \"gone\" 已不存在`, `ChatGPT 选的服务商 \"gone2\" 已不存在`, `未知产品 \"tv\"`, "MarsWallex 不做这条产品线"} {
		if !strings.Contains(warn, want) {
			t.Fatalf("warnings 缺 %s: %s", want, warn)
		}
	}
	if m["default"] != "zovocard" || localBody(t, m["effective"]) != `{"claude":"zovocard","gpt":"zovocard","grok":"zovocard","x":"zovocard"}` {
		t.Fatalf("退回: %v", m)
	}
	if r := localJSON(t, e.do("GET", "/admin/providers/readiness", "")); r["default"] != "zovocard" {
		t.Fatalf("就绪接口同样退回: %v", r)
	}

	localSet(t, provider.SettingByProduct, `{bad json`)
	if m := localJSON(t, e.do("GET", "/admin/providers", "")); !strings.Contains(localBody(t, m["warnings"]), "无法解析") {
		t.Fatalf("%v", m)
	}
}
