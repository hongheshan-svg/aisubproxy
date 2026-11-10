package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// 谁都不认领的码（比如已移除的 ZovoCard 留下的 ZC- 码）：预览、兑换、代理换码一律 404「无法识别的卡密」，不转发任何上游。
func TestUnclaimedCodeIs404(t *testing.T) {
	e := newLocalEnv(t)
	up := e.up
	if err := setAgentSwapPassword(sealedSwapPassword); err != nil {
		t.Fatal(err)
	}
	const code = "ZC-ABCD-EFGH-IJKL-MNOP"
	cases := []struct{ name, method, path, body string }{
		{"预览", "POST", "/public/cdk/preview", `{"code":"` + code + `"}`},
		{"兑换", "POST", "/public/cdk/redeem", `{"code":"` + code + `"}`},
		{"代理换码（口令正确时）", "POST", "/public/cdk/exchange", `{"password":"` + sealedSwapPassword + `","code":"` + code + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := e.do(tc.method, tc.path, tc.body)
			if w.Code != http.StatusNotFound {
				t.Fatalf("应 404: %d %s", w.Code, w.Body.String())
			}
			m := localJSON(t, w)
			if m["error"] != "无法识别的卡密" || m["error_code"] != "code_unrecognized" {
				t.Fatalf("响应不对: %s", w.Body.String())
			}
			if strings.Contains(w.Body.String(), "provider") {
				t.Fatalf("不应带服务商字段: %s", w.Body.String())
			}
		})
	}
	if up.callCount() != 0 {
		t.Fatalf("不应发出任何上游请求: %s", up.log())
	}
}

// 库里存的全局默认服务商已经不存在：后台发码回 400 和固定文案，不发码、不去别家。
func TestIssueWithGoneDefaultIs400(t *testing.T) {
	e := newLocalEnv(t)
	up := e.up
	localSet(t, provider.SettingDefault, "zovocard") // 旧库里存着已移除的 ZovoCard
	w := e.do("POST", "/admin/cdks/issue", localBody(t, map[string]any{
		"plan": "plus", "region": "PH", "count": 1, "funding_confirmed": true,
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("应 400: %d %s", w.Code, w.Body.String())
	}
	if got := localJSON(t, w)["error"]; got != "没有可用的默认服务商，请到「卡服务商」页重新选择" {
		t.Fatalf("文案不对: %v", got)
	}
	if localCDKCount(t) != 0 || up.callCount() != 0 {
		t.Fatalf("不应发出任何码或上游请求: %d %s", localCDKCount(t), up.log())
	}
}

// 全局默认是 MarsWallex 时，它不做的 X / Grok / Claude 发码回 400（UnsupportedError），不发码、不发任何上游请求。
func TestIssueXGrokClaudeRefused(t *testing.T) {
	e := newLocalEnv(t)
	up := e.up
	if err := provider.SaveConfig(provider.MarsWallex, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ product, plan, want string }{
		{"x", "premium_monthly", "MarsWallex 不支持 X Premium premium_monthly"},
		{"grok", "premium_monthly", "MarsWallex 不支持 Grok premium_monthly"},
		{"claude", "pro", "MarsWallex 不支持 Claude pro"},
	} {
		w := e.do("POST", "/admin/cdks/issue", localBody(t, map[string]any{
			"product": tc.product, "plan": tc.plan, "region": "US", "count": 1, "funding_confirmed": true,
		}))
		if w.Code != http.StatusBadRequest || !strings.Contains(localJSON(t, w)["error"].(string), tc.want) {
			t.Fatalf("%s 应 400 并说明不支持: %d %s", tc.product, w.Code, w.Body.String())
		}
	}
	if localCDKCount(t) != 0 || up.callCount() != 0 {
		t.Fatalf("不应发出任何码或上游请求: %d %s", localCDKCount(t), up.log())
	}
}

// 既没码也没令牌的请求（空对象、空串）：预览、兑换都回 404「无法识别的卡密」。行为保护，CHANGELOG 对外承诺过。
func TestEmptyBodyIsUnrecognized(t *testing.T) {
	e := newLocalEnv(t)
	for _, path := range []string{"/public/cdk/preview", "/public/cdk/redeem"} {
		for _, body := range []string{`{}`, `{"code":"","token":""}`} {
			t.Run(path+" "+body, func(t *testing.T) {
				w := e.do("POST", path, body)
				if w.Code != http.StatusNotFound {
					t.Fatalf("应 404: %d %s", w.Code, w.Body.String())
				}
				m := localJSON(t, w)
				if m["error"] != "无法识别的卡密" || m["error_code"] != "code_unrecognized" {
					t.Fatalf("响应不对: %s", w.Body.String())
				}
			})
		}
	}
}
