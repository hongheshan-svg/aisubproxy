package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// 代理换码的每一处报错都带 error_code，error 文字、状态码、其他字段与原来一致（旧页面靠 error 文字显示）。

// swapPost 发一份原始请求体，password 和 code 之外的内容也能测。
func swapPost(body, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/exchange", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	w := httptest.NewRecorder()
	exchangeRouter().ServeHTTP(w, req)
	return w
}

func swapBody(password, code string) string {
	b, _ := json.Marshal(map[string]string{"password": password, "code": code})
	return string(b)
}

func decodeSwap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("响应不是 JSON: %v: %s", err, w.Body.String())
	}
	return d
}

// ordersUpstream 是只回订单列表的假卡台，其余路径 404；订单详情按 detail 回。
func ordersUpstream(t *testing.T, list, detail string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/openapi/v1/gpt-direct/cdk-orders":
			w.Write([]byte(list))
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/openapi/v1/gpt-direct/cdk-orders/"):
			w.Write([]byte(detail))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"code":404,"msg":"unexpected"}`))
		}
	}))
	t.Cleanup(srv.Close)
	if err := db.SetSetting("card_api_base", srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting("card_api_key", "fixture-key"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentExchangeErrorCodes(t *testing.T) {
	const ip = "203.0.113.9"
	type tc struct {
		name   string
		run    func(t *testing.T) *httptest.ResponseRecorder
		status int
		code   string
		errMsg string // 空串表示不比对（带数字的文字另行断言）
		extra  map[string]any
	}
	failedDetail := `{"code":0,"data":{"order":{"status":"failed","stage":"declined","final_amount_minor":0},"events":[]}}`
	seed := func(t *testing.T) {
		dbtest.Open(t)
		seedExchange(t, exchangeOldCode)
		captureOpsAlerts(t)
	}
	cases := []tc{
		{
			name: "限流", status: http.StatusTooManyRequests, code: "rate_limited", errMsg: "请求过于频繁，请稍后再试",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				var w *httptest.ResponseRecorder
				for i := 0; i < 21; i++ {
					w = swapPost(`bad`, ip)
				}
				return w
			},
		},
		{
			name: "功能未启用", status: http.StatusServiceUnavailable, code: "disabled", errMsg: "管理员尚未启用代理换码功能",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				dbtest.Open(t)
				resetLocalRate()
				return swapPost(swapBody(exchangePassword, exchangeOldCode), ip)
			},
		},
		{
			name: "请求体不是 JSON", status: http.StatusBadRequest, code: "invalid_request", errMsg: "invalid request",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				return swapPost(`not json`, ip)
			},
		},
		{
			name: "口令锁定", status: http.StatusTooManyRequests, code: "locked",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				t.Cleanup(func() { swapPasswordFails.succeeded(swapPasswordFailKey) })
				for i := 0; i < swapPasswordMaxFailures; i++ {
					swapPost(swapBody("wrong-password-x", exchangeOldCode), fmt.Sprintf("198.51.100.%d", i+1))
				}
				return swapPost(swapBody(exchangePassword, exchangeOldCode), "203.0.113.200")
			},
		},
		{
			name: "口令错误", status: http.StatusUnauthorized, code: "wrong_password", errMsg: "密码错误",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				return swapPost(swapBody("wrong-password-x", exchangeOldCode), ip)
			},
		},
		{
			name: "卡密太短", status: http.StatusBadRequest, code: "code_required", errMsg: "请输入完整卡密",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				return swapPost(swapBody(exchangePassword, "short"), ip)
			},
		},
		{
			name: "服务商不支持换码", status: http.StatusBadRequest, code: "provider_unsupported", errMsg: "该服务商不支持代理换码",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				adapterMu.Lock()
				old := upstreamAdapters["zovocard"]
				upstreamAdapters["zovocard"] = struct{ upstreamAdapter }{old}
				adapterMu.Unlock()
				t.Cleanup(func() {
					adapterMu.Lock()
					upstreamAdapters["zovocard"] = old
					adapterMu.Unlock()
				})
				return swapPost(swapBody(exchangePassword, exchangeOldCode), ip)
			},
		},
		{
			name: "本站没有这张码", status: http.StatusNotFound, code: "code_not_found", errMsg: "本站未找到该完整卡密（仅支持本站发过的码）",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				newExchangeUpstream(t)
				return swapPost(swapBody(exchangePassword, "ZC-NEVER-SEEN-CODE-0001"), ip)
			},
		},
		{
			name: "没有失败记录", status: http.StatusBadRequest, code: "no_failed_record", errMsg: "该卡密尚无兑换失败记录，不能换新码",
			extra: map[string]any{"hint": "仅限充值失败且未扣款的卡密"},
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				ordersUpstream(t, `{"code":0,"data":{"total":0,"list":[]}}`, failedDetail)
				return swapPost(swapBody(exchangePassword, exchangeOldCode), ip)
			},
		},
		{
			name: "无法确认扣款", status: http.StatusServiceUnavailable, code: "charge_unconfirmed", errMsg: "暂时无法确认订单扣款情况，请稍后重试",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				newExchangeUpstream(t).detailFail.Store(true)
				return swapPost(swapBody(exchangePassword, exchangeOldCode), ip)
			},
		},
		{
			name: "已有成功记录", status: http.StatusBadRequest, code: "already_succeeded", errMsg: "该卡密已有成功充值记录，不能换新码",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				ordersUpstream(t, `{"code":0,"data":{"total":1,"list":[{"order_id":5,"status":"completed","stage":"done","final_amount_minor":100}]}}`,
					`{"code":0,"data":{"order":{"status":"completed","stage":"done","final_amount_minor":100},"events":[]}}`)
				return swapPost(swapBody(exchangePassword, exchangeOldCode), ip)
			},
		},
		{
			name: "不符合换码条件", status: http.StatusBadRequest, code: "not_eligible", errMsg: "不符合换码条件（需充值失败且未扣款）",
			extra: map[string]any{"order_status": "running"},
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				ordersUpstream(t, `{"code":0,"data":{"total":1,"list":[{"order_id":5,"status":"running","stage":"x","final_amount_minor":0}]}}`,
					`{"code":0,"data":{"order":{"status":"running","stage":"x","final_amount_minor":0},"events":[]}}`)
				return swapPost(swapBody(exchangePassword, exchangeOldCode), ip)
			},
		},
		{
			name: "套餐不明", status: http.StatusServiceUnavailable, code: "plan_unknown", errMsg: "无法确认原卡密的套餐，暂不能补发，请联系管理员",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				dbtest.Open(t)
				seedExchange(t)
				captureOpsAlerts(t)
				if err := db.SaveCardplatformCDKCode(77, exchangeOldCode, exchangeOldCode[:14], "", 0, "PH"); err != nil {
					t.Fatal(err)
				}
				newExchangeUpstream(t)
				return swapPost(swapBody(exchangePassword, exchangeOldCode), ip)
			},
		},
		{
			name: "付款地区不明", status: http.StatusServiceUnavailable, code: "region_unknown", errMsg: "无法确认原卡密的付款地区，暂不能补发，请联系管理员",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				if _, err := db.DB.Exec(`UPDATE cardplatform_cdk_codes SET payment_country = NULL WHERE upstream_id = 77`); err != nil {
					t.Fatal(err)
				}
				newExchangeUpstream(t)
				return swapPost(swapBody(exchangePassword, exchangeOldCode), ip)
			},
		},
		{
			name: "已换过新码", status: http.StatusConflict, code: "conflict", errMsg: "该卡密已换过新码，不可重复兑换",
			extra: map[string]any{"state": db.AgentExchangeDone},
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				newExchangeUpstream(t)
				if w := swapPost(swapBody(exchangePassword, exchangeOldCode), ip); w.Code != 200 {
					t.Fatalf("第一次应成功: %d %s", w.Code, w.Body.String())
				}
				return swapPost(swapBody(exchangePassword, exchangeOldCode), ip)
			},
		},
		{
			name: "今日额度用完", status: http.StatusTooManyRequests, code: "daily_limit", errMsg: "代理换码已达今日上限（1 张），请联系管理员",
			extra: map[string]any{"limit": float64(1)},
			run: func(t *testing.T) *httptest.ResponseRecorder {
				dbtest.Open(t)
				seedExchange(t, exchangeOldCode, "ZC-EXCHANGE-OLD-CODE-0002")
				captureOpsAlerts(t)
				newExchangeUpstream(t)
				if err := db.SetSetting(agentSwapDailyLimitKey, "1"); err != nil {
					t.Fatal(err)
				}
				if w := swapPost(swapBody(exchangePassword, exchangeOldCode), ip); w.Code != 200 {
					t.Fatalf("额度内应成功: %d %s", w.Code, w.Body.String())
				}
				return swapPost(swapBody(exchangePassword, "ZC-EXCHANGE-OLD-CODE-0002"), ip)
			},
		},
		{
			name: "占位失败", status: http.StatusInternalServerError, code: "unavailable", errMsg: "换码暂时不可用，请稍后再试",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				newExchangeUpstream(t)
				// 占位表被删时 Reserve 返回普通错误（既不是已存在也不是额度用完）
				if _, err := db.DB.Exec(`DROP TABLE agent_cdk_exchanges`); err != nil {
					t.Fatal(err)
				}
				return swapPost(swapBody(exchangePassword, exchangeOldCode), ip)
			},
		},
		{
			name: "发码结果不明", status: http.StatusBadGateway, code: "outcome_unknown", errMsg: "发码结果不明，已转人工核对，请勿重试",
			extra: map[string]any{"outcome": "unknown"},
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				u := newExchangeUpstream(t)
				u.onIssue = func(_ int32, w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(500)
					w.Write([]byte(`{"code":500,"msg":"boom"}`))
				}
				return swapPost(swapBody(exchangePassword, exchangeOldCode), ip)
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := c.run(t)
			if w.Code != c.status {
				t.Fatalf("状态码应为 %d，实际 %d: %s", c.status, w.Code, w.Body.String())
			}
			d := decodeSwap(t, w)
			if d["error_code"] != c.code {
				t.Fatalf("error_code 应为 %q，实际 %v: %s", c.code, d["error_code"], w.Body.String())
			}
			if c.errMsg != "" && d["error"] != c.errMsg {
				t.Fatalf("error 文字应不变 %q，实际 %v", c.errMsg, d["error"])
			}
			for k, v := range c.extra {
				if d[k] != v {
					t.Fatalf("字段 %s 应为 %v，实际 %v: %s", k, v, d[k], w.Body.String())
				}
			}
			if c.code == "locked" {
				mins, ok := d["retry_after_minutes"].(float64)
				if !ok || mins < 1 || mins > 60 {
					t.Fatalf("locked 应带 1~60 的 retry_after_minutes: %s", w.Body.String())
				}
				if want := fmt.Sprintf("换码口令错误次数过多，已暂停换码，请 %d 分钟后再试", int(mins)); d["error"] != want {
					t.Fatalf("locked 的 error 应为 %q，实际 %v", want, d["error"])
				}
			}
		})
	}
}

// conflict 带的 state 要和占位状态一致，前端靠它选 pending / unknown 的文案。
func TestAgentExchangeConflictCarriesState(t *testing.T) {
	dbtest.Open(t)
	seedExchange(t, exchangeOldCode)
	captureOpsAlerts(t)
	u := newExchangeUpstream(t)
	u.onIssue = func(_ int32, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"code":500,"msg":"boom"}`))
	}
	swapPost(swapBody(exchangePassword, exchangeOldCode), "203.0.113.9") // 结果不明，占位保留
	w := swapPost(swapBody(exchangePassword, exchangeOldCode), "203.0.113.9")
	d := decodeSwap(t, w)
	if w.Code != http.StatusConflict || d["error_code"] != "conflict" || d["state"] != db.AgentExchangeUnknown ||
		d["error"] != "该卡密上次换码结果不明，已转人工核对，请勿重试" {
		t.Fatalf("冲突响应不符: %d %s", w.Code, w.Body.String())
	}
}
