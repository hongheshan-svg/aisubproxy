package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// N-2：同一张码先后预览拿到 T1、T2，批量查结果带 [T1, T2] 时两个令牌都转发给上游，都不回本地 404。
func TestS7ResultsBatchForwardsBothTokensOfSameCode(t *testing.T) {
	e := newLocalEnv(t)
	code := "ZC-S7-REPEAT-CODE-0001"
	for _, tok := range []string{"zc-s7-t1", "zc-s7-t2"} {
		if err := db.BindCDKRedemptionToken(string(provider.ZovoCard), code, tok); err != nil {
			t.Fatal(err)
		}
	}
	before := e.zovo.hits("/api/v1/cdk/result")
	w := e.do("POST", "/lookup/cdk/results", localBody(t, map[string]any{"tokens": []string{"zc-s7-t1", "zc-s7-t2"}}))
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	if n := e.zovo.hits("/api/v1/cdk/result") - before; n != 2 {
		t.Fatalf("两个令牌都应转发卡台，实际 %d 次", n)
	}
	bodies := e.zovo.requestBodies() + e.zovo.requestQueries()
	for _, tok := range []string{"zc-s7-t1", "zc-s7-t2"} {
		if !strings.Contains(bodies, tok) {
			t.Fatalf("假上游没收到令牌 %s: %s", tok, bodies)
		}
	}
	for _, it := range s4Decode(t, w.Body.Bytes()) {
		if it.Status == http.StatusNotFound {
			t.Fatalf("令牌 %s 不应回本地 404", it.Token)
		}
	}
}
