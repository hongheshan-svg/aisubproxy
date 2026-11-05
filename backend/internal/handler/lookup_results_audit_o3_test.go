package handler

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// 审计 O3 组 money N-1：批量查结果只转发本站有绑定记录的上游令牌，未知令牌本地回「未找到」，不打卡台。
// 整批仍只按一次查询计限流（用户决定 U2，S4 的测试钉着）。

func o3RandomToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "zc-" + hex.EncodeToString(b)
}

// 100 个随机令牌：一个上游请求都不发，每个都回 404。
func TestO3ResultsBatchUnknownTokensNeverHitUpstream(t *testing.T) {
	e := newLocalEnv(t)
	toks := make([]string, lookupResultsMax)
	for i := range toks {
		toks[i] = o3RandomToken(t)
	}
	before := e.zovo.count()
	w := e.do("POST", "/lookup/cdk/results", localBody(t, map[string]any{"tokens": toks}))
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	if n := e.zovo.count() - before; n != 0 {
		t.Fatalf("未知令牌不能打卡台，实际发了 %d 个请求", n)
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

// 本站兑换时写过绑定的令牌照常转发卡台；和未知令牌混在一起时只转发有绑定的那个。
func TestO3ResultsBatchForwardsBoundTokensOnly(t *testing.T) {
	e := newLocalEnv(t)
	if err := db.BindCDKRedemptionToken(string(provider.ZovoCard), "ZC-O3-BOUND-CODE-0001", "zc-tok-bound"); err != nil {
		t.Fatal(err)
	}
	unknown := o3RandomToken(t)
	before := e.zovo.hits("/api/v1/cdk/result")
	w := e.do("POST", "/lookup/cdk/results", localBody(t, map[string]any{"tokens": []string{unknown, "zc-tok-bound"}}))
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	if n := e.zovo.hits("/api/v1/cdk/result") - before; n != 1 {
		t.Fatalf("只应为有绑定的令牌查 1 次卡台，实际 %d", n)
	}
	res := s4Decode(t, w.Body.Bytes())
	if len(res) != 2 || res[0].Token != unknown || res[0].Status != http.StatusNotFound {
		t.Fatalf("未知令牌应 404: %+v", res)
	}
	if res[1].Token != "zc-tok-bound" || res[1].Status != http.StatusOK || res[1].Data["status"] != "running" {
		t.Fatalf("有绑定的令牌应原样返回卡台结果: %+v", res[1])
	}
	// 和单查接口一致
	if single := e.result("zc-tok-bound"); single.Code != res[1].Status {
		t.Fatalf("有绑定的令牌批量结果 %d 应与单查 %d 一致", res[1].Status, single.Code)
	}
}

// 本站发的码（供卡型令牌）只查本库，不受绑定过滤影响：未知的 mwt_ 令牌仍走本地，结果和单查一致。
func TestO3ResultsBatchCardSupplyTokensStayLocal(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, _ := e.issueCode("plus", "PH")
	tokA := mwToken(code)
	before := e.zovo.count()
	w := e.do("POST", "/lookup/cdk/results", localBody(t, map[string]any{"tokens": []string{tokA, "mwt_no_such"}}))
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	if n := e.zovo.count() - before; n != 0 {
		t.Fatalf("本站码不应打卡台，实际 %d", n)
	}
	res := s4Decode(t, w.Body.Bytes())
	if single := e.result(tokA); res[0].Status != single.Code {
		t.Fatalf("本站码批量结果 %d 应与单查 %d 一致", res[0].Status, single.Code)
	}
	if single := e.result("mwt_no_such"); res[1].Status != single.Code {
		t.Fatalf("未知本站令牌批量结果 %d 应与单查 %d 一致", res[1].Status, single.Code)
	}
}
