package handler

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"testing"
)

// 批量查结果：认不出的令牌本地回 404，不发往任何上游。
// 整批仍只按一次查询计限流（用户决定 U2，S4 的测试钉着）。

func o3RandomToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "unk-" + hex.EncodeToString(b)
}

// 100 个随机令牌：一个上游请求都不发，每个都回 404。
func TestO3ResultsBatchUnknownTokensNeverHitUpstream(t *testing.T) {
	e := newLocalEnv(t)
	toks := make([]string, lookupResultsMax)
	for i := range toks {
		toks[i] = o3RandomToken(t)
	}
	before := e.up.callCount()
	w := e.do("POST", "/lookup/cdk/results", localBody(t, map[string]any{"tokens": toks}))
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	if n := e.up.callCount() - before; n != 0 {
		t.Fatalf("未知令牌不能发往上游，实际发了 %d 个请求", n)
	}
	res := s4Decode(t, w.Body.Bytes())
	if len(res) != len(toks) {
		t.Fatalf("应逐个返回 %d 条，实际 %d", len(toks), len(res))
	}
	for i, it := range res {
		if it.Token != toks[i] || it.Status != http.StatusNotFound {
			t.Fatalf("第 %d 条应为 404 且顺序不变: %+v", i, it)
		}
		if it.Data["error"] == nil {
			t.Fatalf("第 %d 条应带错误说明: %+v", i, it)
		}
	}
}

// 本站发的码（供卡型令牌）只查本库：未知的 mwt_ 令牌仍走本地，结果和单查一致。
func TestO3ResultsBatchCardSupplyTokensStayLocal(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, _ := e.issueCode("plus", "PH")
	tokA := mwToken(code)
	before := e.up.callCount()
	w := e.do("POST", "/lookup/cdk/results", localBody(t, map[string]any{"tokens": []string{tokA, "mwt_no_such"}}))
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	if n := e.up.callCount() - before; n != 0 {
		t.Fatalf("本站码不应发往上游，实际 %d", n)
	}
	res := s4Decode(t, w.Body.Bytes())
	if single := e.result(tokA); res[0].Status != single.Code {
		t.Fatalf("本站码批量结果 %d 应与单查 %d 一致", res[0].Status, single.Code)
	}
	if single := e.result("mwt_no_such"); res[1].Status != single.Code {
		t.Fatalf("未知本站令牌批量结果 %d 应与单查 %d 一致", res[1].Status, single.Code)
	}
}
