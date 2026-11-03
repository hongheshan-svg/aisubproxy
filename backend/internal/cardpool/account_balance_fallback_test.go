package cardpool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// noBalanceAcct 是分页结果里没有 balance_list 字段的账户。
func noBalanceAcct(no, typ string) string {
	return `{"account_no":"` + no + `","account_name":"` + no + `","account_status":"ACTIVE","account_type":"` + typ + `"}`
}

func detailPath2(no string) string { return "/v1/accounts/detail/" + no }

// API-05：分页接口没带余额时，用详情接口兜底，兑换的余额校验才不会因为拿不到余额而误拒。
func TestSyncAccountsFallsBackToDetailBalance(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	mw.On("GET", pathAccounts, acctPage(noBalanceAcct("M1", "MAIN"), acctJSON("S1", "SUB_ACCOUNT", "ACTIVE", "USD", "20.00")))
	mw.On("GET", detailPath2("M1"), mwtest.OK(acctJSON("M1", "MAIN", "ACTIVE", "USDT", "100.00", "USD", "50.00")))
	mw.On("POST", pathConversion, mwtest.OK(`{"transaction_id":"CV1"}`))

	o, err := s.SubmitConversion(ctx, ConversionParams{SellCurrency: "USDT", BuyCurrency: "USD", SellCents: 10000, By: "admin"})
	if err != nil || o.State != StateProcessing {
		t.Fatalf("详情里有余额，兑换应通过校验: %+v %v", o, err)
	}
	if n := len(mw.Requests("GET", detailPath2("M1"))); n != 1 {
		t.Fatalf("应只给缺余额的账户取一次详情: %d", n)
	}
	if n := len(mw.Requests("GET", detailPath2("S1"))); n != 0 {
		t.Fatalf("分页里已带余额的账户不应再取详情: %d", n)
	}
	if a := accountRow(t, "M1"); len(a.Balances) != 2 {
		t.Fatalf("应写入详情里的余额: %+v", a.Balances)
	}
}

// 兜底也失败：返回错误，本地不变，兑换不发出——绝不把「没拿到余额」当成余额 0。
func TestSyncAccountsDetailFailureIsAnError(t *testing.T) {
	for name, reply := range map[string]mwtest.Reply{
		"被拒":   mwtest.Rejected(400, "X"),
		"结果不明": mwtest.Unknown(),
	} {
		t.Run(name, func(t *testing.T) {
			s, mw, _, _ := newTestService(t)
			ctx := context.Background()
			// 先有一份本地账户（余额充足），后面同步失败后它必须保持原样
			mw.On("GET", pathAccounts, acctPage(acctJSON("M1", "MAIN", "ACTIVE", "USDT", "100.00")))
			if err := s.SyncAccounts(ctx); err != nil {
				t.Fatal(err)
			}
			mw.On("GET", pathAccounts, acctPage(noBalanceAcct("M1", "MAIN")))
			mw.On("GET", detailPath2("M1"), reply)

			err := s.SyncAccounts(ctx)
			if err == nil || !strings.Contains(err.Error(), "M1") {
				t.Fatalf("应返回带账户号的错误: %v", err)
			}
			if a := accountRow(t, "M1"); len(a.Balances) != 1 || a.Balances[0].AvailableBalance != "100.00" {
				t.Fatalf("同步失败不应改动本地余额: %+v", a.Balances)
			}
			if _, err := s.SubmitConversion(ctx, ConversionParams{SellCurrency: "USDT", BuyCurrency: "USD", SellCents: 100, By: "admin"}); err == nil || errors.Is(err, ErrBadRequest) {
				// 同步失败是平台/网络错误，不是「余额不足」类 400
				t.Fatalf("兑换应因同步失败而报错（非 400 余额不足）: %v", err)
			}
			if n := len(mw.Requests("POST", pathConversion)); n != 0 {
				t.Fatalf("兑换不应发出: %d", n)
			}
		})
	}
}

// 分页里明确给了空列表（账户确实没有余额）不需要兜底；详情也说没有余额则写成空，由余额校验拒绝。
func TestSyncAccountsEmptyBalanceListNeedsNoFallback(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	mw.On("GET", pathAccounts, acctPage(acctJSON("M1", "MAIN", "ACTIVE")))
	if err := s.SyncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(mw.Requests("GET", detailPath2("M1"))); n != 0 {
		t.Fatalf("balance_list 是空数组不应兜底: %d", n)
	}
}
