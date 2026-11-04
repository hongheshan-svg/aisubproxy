package handler

import (
	"context"
	"testing"
)

// E-9：本站码连换两次（A→B→C），售后查旧码 A 应直接得到链尾 C，而不是已作废的 B。
func TestAfterSaleLookupFollowsExchangeChainToEnd(t *testing.T) {
	e := newSealedExchangeEnv(t)
	codeA, _ := exchangeableCode(t, e)
	_, codeB, idB := doSealedExchange(t, e, codeA)
	e.insertFundedFailure(idB, "a@example.com")
	_, codeC, _ := doSealedExchange(t, e, codeB)

	r := lookupAfterSale(context.Background(), codeA, "")
	if r.Status != "replaced" || r.Replacement == nil {
		t.Fatalf("%+v", r)
	}
	if *r.Replacement != (lookupReplacement{Code: codeC, Status: "unused", Depth: 2}) {
		t.Fatalf("链尾应为 C（深度 2），实际 %+v（B=%s）", *r.Replacement, codeB)
	}
}
