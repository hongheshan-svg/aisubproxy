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

// GET /admin/providers：内置 MarsWallex 加环境里的假上游 testup 的列表、产品线、配置、能力矩阵；能力矩阵和就绪接口出自同一个函数。
func TestAdminListProviders(t *testing.T) {
	e := newLocalEnv(t)
	w := e.do("GET", "/admin/providers", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	m := localJSON(t, w)
	list := m["providers"].([]any)
	if len(list) != 2 {
		t.Fatalf("MarsWallex 加假上游 testup: %v", list)
	}
	mw, tu := list[0].(map[string]any), list[1].(map[string]any)
	if tu["name"] != "testup" || tu["kind"] != "upstream" || tu["kind_text"] != "上游发码" || tu["code_list"] != "" || localBody(t, tu["products"]) != `["gpt","x"]` {
		t.Fatalf("testup: %v", tu)
	}
	if _, has := tu["code_prefix"]; has {
		t.Fatalf("上游型没有 code_prefix: %v", tu)
	}
	if mw["name"] != "marswallex" || mw["display_name"] != "MarsWallex" || mw["kind"] != "card_supply" || mw["kind_text"] != "供卡·本站付款" ||
		mw["code_prefix"] != "MW-" || mw["code_list"] != "local" || localBody(t, mw["products"]) != `["gpt"]` {
		t.Fatalf("MarsWallex: %v", mw)
	}
	if localBody(t, m["products"]) != `[{"key":"gpt","label":"ChatGPT"},{"key":"x","label":"X Premium"},{"key":"grok","label":"Grok"},{"key":"claude","label":"Claude"}]` {
		t.Fatalf("产品线: %v", m["products"])
	}
	// 出厂配置：全局默认 MarsWallex，按产品线为空
	if m["default"] != "marswallex" || localBody(t, m["by_product"]) != `{}` || localBody(t, m["warnings"]) != "[]" ||
		localBody(t, m["effective"]) != `{"claude":"marswallex","gpt":"marswallex","grok":"marswallex","x":"marswallex"}` {
		t.Fatalf("配置: %v", m)
	}
	r := localJSON(t, e.do("GET", "/admin/providers/readiness", ""))
	if caps := localBody(t, m["capabilities"]); caps != localBody(t, r["capabilities"]) || !strings.Contains(caps, "仅限无订阅账号") {
		t.Fatalf("两个接口的能力矩阵应一模一样:\n%s\n%s", caps, localBody(t, r["capabilities"]))
	}

	// 新注册的服务商自动出现在列表里；上游型默认没有码列表
	providertest.Register(t, providertest.NewTestCard()) // testup 环境里已经注册
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
		`{"by_product":{"gpt":"marswallex"}}`:                       "请选择全局默认服务商",
		`{"default":"nope"}`:                                        `全局默认服务商 "nope" 未注册`,
		`{"default":"marswallex","by_product":{"tv":"marswallex"}}`: `未知产品 "tv"`,
		`{"default":"marswallex","by_product":{"gpt":"nope"}}`:      `ChatGPT 选的服务商 "nope" 未注册`,
		`{"default":"marswallex","by_product":{"x":"marswallex"}}`:  "MarsWallex 不做 X Premium",
		`not json`: "请求格式无效",
	} {
		if code, m := put(body); code != http.StatusBadRequest || !strings.Contains(m["error"].(string), want) {
			t.Fatalf("%s: %d %v", body, code, m)
		}
	}
	if strings.Contains(localAudit(t), "provider_config_update") {
		t.Fatal("校验失败不写审计")
	}

	w := e.do("PUT", "/admin/providers/config", `{"default":"testup","by_product":{"gpt":"marswallex","x":""}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	dumpExample(t, "put_providers_config", w.Body.Bytes())
	m := localJSON(t, w)
	if m["default"] != "testup" || localBody(t, m["by_product"]) != `{"gpt":"marswallex"}` ||
		localBody(t, m["effective"]) != `{"claude":"testup","gpt":"marswallex","grok":"testup","x":"testup"}` {
		t.Fatalf("保存结果: %v", m)
	}
	// MarsWallex 还没过首笔真实付款，被选用了照样保存，只做提示
	nr := m["not_ready"].([]any)
	if len(nr) != 1 || nr[0].(map[string]any)["provider"] != "marswallex" ||
		!strings.Contains(localBody(t, nr[0]), "需要一笔系统自动确认成功的真实付款") {
		t.Fatalf("not_ready: %v", nr)
	}
	if a := localAudit(t); !strings.Contains(a, "provider_config_update default marswallex→testup by_product -→gpt=marswallex") {
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
	if code, m := put(`{"default":"testup","by_product":{}}`); code != http.StatusOK || localBody(t, m["by_product"]) != "{}" || localBody(t, m["not_ready"]) != "[]" {
		t.Fatalf("%d %v", code, m)
	}

	// 配置只影响之后新发的码：已发的 MarsWallex 码照样走 MarsWallex
	e.seedRealPayment()
	code, _ := e.issueCode("plus", "PH")
	if code, m := put(`{"default":"testup","by_product":{}}`); code != http.StatusOK {
		t.Fatalf("%d %v", code, m)
	}
	if m := localJSON(t, e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code}))); m["provider"] != "marswallex" {
		t.Fatalf("已发的码不受配置影响: %v", m)
	}
}

// 库里存的服务商名已经没有注册、或者按产品配置坏了：接口照常返回，按规则处理并在 warnings 里说明。
func TestAdminProvidersConfigWarnings(t *testing.T) {
	e := newLocalEnv(t)
	localSet(t, provider.SettingDefault, "gone")
	localSet(t, provider.SettingByProduct, `{"gpt":"gone2","tv":"gone","x":"marswallex"}`)
	m := localJSON(t, e.do("GET", "/admin/providers", ""))
	warn := localBody(t, m["warnings"])
	for _, want := range []string{`全局默认服务商 \"gone\" 已不存在，请到「卡服务商」页重新选择`, `ChatGPT 选的服务商 \"gone2\" 已不存在`, `未知产品 \"tv\"`, "MarsWallex 不做这条产品线"} {
		if !strings.Contains(warn, want) {
			t.Fatalf("warnings 缺 %s: %s", want, warn)
		}
	}
	if m["default"] != "" || localBody(t, m["effective"]) != `{"claude":"","gpt":"","grok":"","x":""}` {
		t.Fatalf("默认不存在时留空，不换成任何一家: %v", m)
	}
	if r := localJSON(t, e.do("GET", "/admin/providers/readiness", "")); r["default"] != "" {
		t.Fatalf("就绪接口同样留空: %v", r)
	}

	localSet(t, provider.SettingByProduct, `{bad json`)
	if m := localJSON(t, e.do("GET", "/admin/providers", "")); !strings.Contains(localBody(t, m["warnings"]), "无法解析") {
		t.Fatalf("%v", m)
	}
}
