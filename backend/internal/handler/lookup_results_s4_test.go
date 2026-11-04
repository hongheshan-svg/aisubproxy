package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

// S4 / F-4：批量页轮询合并成一次请求，按一次查询计限流。

type s4ResultItem struct {
	Token  string         `json:"token"`
	Status int            `json:"status"`
	Data   map[string]any `json:"data"`
}

func s4Decode(t *testing.T, body []byte) []s4ResultItem {
	t.Helper()
	var out struct {
		Max     int            `json:"max"`
		Results []s4ResultItem `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("响应不是 JSON: %v %s", err, body)
	}
	if out.Max != lookupResultsMax {
		t.Fatalf("max=%d", out.Max)
	}
	return out.Results
}

func TestS4ResultsBatchReturnsPerTokenInOrder(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, _ := e.issueCode("plus", "PH")
	tokA := mwToken(code)
	body := localBody(t, map[string]any{"tokens": []string{"no-such-token", tokA}})
	w := e.do("POST", "/lookup/cdk/results", body)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	res := s4Decode(t, w.Body.Bytes())
	if len(res) != 2 || res[0].Token != "no-such-token" || res[1].Token != tokA {
		t.Fatalf("应按请求顺序逐个返回: %+v", res)
	}
	// 和单查接口的结果一致
	single := e.result(tokA)
	if res[1].Status != single.Code {
		t.Fatalf("批量里的状态码 %d 应与单查 %d 一致", res[1].Status, single.Code)
	}
	if one := e.result("no-such-token"); res[0].Status != one.Code {
		t.Fatalf("第一个令牌的状态码 %d 应与单查 %d 一致", res[0].Status, one.Code)
	}
}

func TestS4ResultsBatchLimits(t *testing.T) {
	e := newLocalEnv(t)
	if lookupResultsMax != 100 {
		t.Fatalf("上限应为 100: %d", lookupResultsMax)
	}
	if w := e.do("POST", "/lookup/cdk/results", `{"tokens":[]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("空列表应 400: %d", w.Code)
	}
	toks := make([]string, lookupResultsMax+1)
	for i := range toks {
		toks[i] = "t" + strconv.Itoa(i)
	}
	if w := e.do("POST", "/lookup/cdk/results", localBody(t, map[string]any{"tokens": toks})); w.Code != http.StatusBadRequest {
		t.Fatalf("超 100 应 400: %d", w.Code)
	}
	if w := e.do("POST", "/lookup/cdk/results", localBody(t, map[string]any{"tokens": toks[:lookupResultsMax]})); w.Code != http.StatusOK {
		t.Fatalf("正好 100 应 200: %d %s", w.Code, w.Body.String())
	}
}

// 一次请求只计一次查询限流：额度 2 时，装 100 个令牌的两次请求都通过，第三次才 429。
func TestS4ResultsBatchCountsOneQuery(t *testing.T) {
	e := newLocalEnv(t)
	old := localRateLimits[localRateQuery]
	localRateLimits[localRateQuery] = 2
	t.Cleanup(func() { localRateLimits[localRateQuery] = old })
	toks := make([]string, lookupResultsMax)
	for i := range toks {
		toks[i] = "t" + strconv.Itoa(i)
	}
	body := localBody(t, map[string]any{"tokens": toks})
	for i := 0; i < 2; i++ {
		if w := e.do("POST", "/lookup/cdk/results", body); w.Code != http.StatusOK {
			t.Fatalf("第 %d 次应 200: %d %s", i, w.Code, w.Body.String())
		}
	}
	if w := e.do("POST", "/lookup/cdk/results", body); w.Code != http.StatusTooManyRequests {
		t.Fatalf("第 3 次应 429: %d", w.Code)
	}
}
