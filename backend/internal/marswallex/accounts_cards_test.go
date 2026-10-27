package marswallex

import (
	"context"
	"net/http"
	"testing"
)

func TestAccountAndCardRequests(t *testing.T) {
	ptr := func(n int64) *int64 { return &n }
	const acct = int64(2000519969952288769)

	cases := []struct {
		name     string
		call     func(c *Client) (any, error)
		method   string
		escPath  string
		rawQuery string
		body     string // 为空表示请求体必须为空
		reply    string // 为空用默认 {"data":{}}
		check    func(t *testing.T, got any)
	}{
		{
			name: "CreateAccount",
			call: func(c *Client) (any, error) {
				return c.CreateAccount(context.Background(), CreateAccountRequest{AccountName: "SubA", OutAccountNo: "SUB1"})
			},
			method: "POST", escPath: "/portal/api/v1/accounts",
			body:  `{"data":{"account_name":"SubA","out_account_no":"SUB1"}}`,
			reply: `{"data":{"account_no":"9","account_name":"SubA","out_account_no":"SUB1","status":"ACTIVE","create_time":1766123535}}`,
			check: func(t *testing.T, got any) {
				r := got.(*SubAccount)
				if r.AccountNo != "9" || r.OutAccountNo != "SUB1" || r.Status != "ACTIVE" || r.CreateTime.Unix() != 1766123535 {
					t.Errorf("got %+v", r)
				}
			},
		},
		{
			name: "CreateAccountShared",
			call: func(c *Client) (any, error) {
				p := "SHARED_ACCOUNT"
				return c.CreateAccount(context.Background(), CreateAccountRequest{AccountName: "S", OutAccountNo: "O", Purpose: &p})
			},
			method: "POST", escPath: "/portal/api/v1/accounts",
			body: `{"data":{"account_name":"S","out_account_no":"O","purpose":"SHARED_ACCOUNT"}}`,
		},
		{
			name: "ListAccounts",
			call: func(c *Client) (any, error) {
				return c.ListAccounts(context.Background(), ListAccountsQuery{PageNo: 1, PageSize: 10})
			},
			method: "GET", escPath: "/portal/api/v1/accounts/page", rawQuery: "page_no=1&page_size=10",
			reply: `{"count":"2","data":[{"account_no":"2000519969952288769","create_time":1766123535,
				"balance_list":[{"currency":"USD","available_balance":"89246.14","freeze_balance":"0.00"}]}]}`,
			check: func(t *testing.T, got any) {
				p := got.(*Page[Account])
				if p.Count != 2 || p.Data[0].AccountNo != "2000519969952288769" || p.Data[0].BalanceList[0].AvailableBalance != "89246.14" {
					t.Errorf("got %+v", p)
				}
			},
		},
		{
			name: "ListAccountsAllFilters",
			call: func(c *Client) (any, error) {
				return c.ListAccounts(context.Background(), ListAccountsQuery{PageNo: 2, PageSize: 5, AccountType: "MAIN", AccountNo: "A1"})
			},
			method: "GET", escPath: "/portal/api/v1/accounts/page", rawQuery: "page_no=2&page_size=5&account_type=MAIN&account_no=A1",
			reply: `{"count":0,"data":[]}`,
		},
		{
			name: "ListAccountSerials",
			call: func(c *Client) (any, error) {
				return c.ListAccountSerials(context.Background(), AccountSerialsQuery{PageNo: 1, PageSize: 20, AccountNo: "A1", Currency: "USD", StartTime: 100, EndTime: 200})
			},
			method: "GET", escPath: "/portal/api/v1/accounts/serials",
			rawQuery: "page_no=1&page_size=20&account_no=A1&currency=USD&start_time=100&end_time=200",
			reply:    `{"count":"1","data":[{"serial_id":"s1","currency":"USD","amount":"1.50","flag_dc":"CREDIT","transaction_type":"CHARGE","create_time":1766123535}]}`,
			check: func(t *testing.T, got any) {
				p := got.(*Page[AccountSerial])
				if p.Count != 1 || p.Data[0].SerialID != "s1" || p.Data[0].Amount != "1.50" || p.Data[0].FlagDC != "CREDIT" {
					t.Errorf("got %+v", p)
				}
			},
		},
		{
			name:   "GetAccount",
			call:   func(c *Client) (any, error) { return c.GetAccount(context.Background(), "a/b") },
			method: "GET", escPath: "/portal/api/v1/accounts/detail/a%2Fb",
			reply: `{"data":{"account_no":"a/b","account_name":"N","account_status":"ACTIVE","account_type":"MAIN"}}`,
			check: func(t *testing.T, got any) {
				if r := got.(*Account); r.AccountNo != "a/b" || r.AccountType != "MAIN" {
					t.Errorf("got %+v", r)
				}
			},
		},
		{
			name: "UpdateAccountStatus",
			call: func(c *Client) (any, error) {
				return c.UpdateAccountStatus(context.Background(), "A1", UpdateAccountStatusRequest{AccountStatus: "DISABLED", Remark: "停用原因"})
			},
			method: "PUT", escPath: "/portal/api/v1/accounts/A1/status",
			body:  `{"account_status":"DISABLED","remark":"停用原因"}`,
			reply: `{"data":{"account_no":"A1","out_account_no":"O1"}}`,
			check: func(t *testing.T, got any) {
				if r := got.(*AccountRef); r.AccountNo != "A1" || r.OutAccountNo != "O1" {
					t.Errorf("got %+v", r)
				}
			},
		},
		{
			name:   "ListCardProducts",
			call:   func(c *Client) (any, error) { return c.ListCardProducts(context.Background()) },
			method: "GET", escPath: "/portal/api/v1/card/product",
			reply: `{"data":[{"product_id":"p1","product_name":"N","card_label":"VISA","card_country":"US","card_mode":"NORMAL","card_currency":"USD","supported_fee_deduct_types":["WALLET"]}]}`,
			check: func(t *testing.T, got any) {
				r := got.([]CardProduct)
				if len(r) != 1 || r[0].ProductID != "p1" || r[0].SupportedFeeDeductTypes[0] != "WALLET" {
					t.Errorf("got %+v", r)
				}
			},
		},
		{
			name: "ApplyCardRequiredOnly",
			call: func(c *Client) (any, error) {
				return c.ApplyCard(context.Background(), ApplyCardRequest{OutOrderNo: "O1", ProductID: "p1", Amount: "10.00", Currency: "USD", CardHolderID: "h1", CardName: "N", ExpiryDate: "1230"})
			},
			method: "POST", escPath: "/portal/api/v2/card",
			body:  `{"out_order_no":"O1","product_id":"p1","amount":"10.00","currency":"USD","card_holder_id":"h1","card_name":"N","expiry_date":"1230"}`,
			reply: `{"data":{"out_order_no":"O1","transaction_id":"T1"}}`,
			check: func(t *testing.T, got any) {
				if r := got.(*ApplyCardResult); r.OutOrderNo != "O1" || r.TransactionID != "T1" {
					t.Errorf("got %+v", r)
				}
			},
		},
		{
			name: "ApplyCardAllFields",
			call: func(c *Client) (any, error) {
				return c.ApplyCard(context.Background(), ApplyCardRequest{OutOrderNo: "O1", ProductID: "p1", Amount: "10.00", Currency: "USD", CardHolderID: "h1", CardName: "N", ExpiryDate: "1230",
					AccountNo: "A1", Remark: "r", FeeDeductType: "WALLET", Limit: &CardLimit{SingleLimit: "100.00"}})
			},
			method: "POST", escPath: "/portal/api/v2/card",
			body: `{"out_order_no":"O1","product_id":"p1","amount":"10.00","currency":"USD","card_holder_id":"h1","card_name":"N","expiry_date":"1230","account_no":"A1","remark":"r","fee_deduct_type":"WALLET","limit":{"single_limit":"100.00"}}`,
		},
		{
			name: "GetCardApplyOrder",
			call: func(c *Client) (any, error) {
				return c.GetCardApplyOrder(context.Background(), OrderRef{TransactionID: "T1"})
			},
			method: "GET", escPath: "/portal/api/v1/card/apply-order", rawQuery: "transaction_id=T1",
			reply: `{"data":{"card_id":"c1","status":"SUCCESS"}}`,
			check: func(t *testing.T, got any) {
				if r := got.(*CardApplyOrder); r.CardID != "c1" || r.Status != "SUCCESS" {
					t.Errorf("got %+v", r)
				}
			},
		},
		{
			name: "GetCardApplyOrderBoth",
			call: func(c *Client) (any, error) {
				return c.GetCardApplyOrder(context.Background(), OrderRef{OutOrderNo: "O1", TransactionID: "T1"})
			},
			method: "GET", escPath: "/portal/api/v1/card/apply-order", rawQuery: "out_order_no=O1&transaction_id=T1",
		},
		{
			name: "RechargeCard",
			call: func(c *Client) (any, error) {
				return c.RechargeCard(context.Background(), CardTransferRequest{OutOrderNo: "O1", CardID: "c1", Currency: "USD", Amount: "5.00"})
			},
			method: "POST", escPath: "/portal/api/v1/card/recharge-transaction",
			body:  `{"out_order_no":"O1","card_id":"c1","currency":"USD","amount":"5.00"}`,
			reply: `{"data":{"out_order_no":"O1","transaction_id":"T1","card_id":"c1","currency":"USD","amount":"5.00","type":"CHARGE","status":"APPLY","create_time":1766123535}}`,
			check: func(t *testing.T, got any) {
				r := got.(*CardBalanceTransaction)
				if r.TransactionID != "T1" || r.Amount != "5.00" || r.Type != "CHARGE" || r.Status != "APPLY" || r.CreateTime.Unix() != 1766123535 {
					t.Errorf("got %+v", r)
				}
			},
		},
		{
			name: "WithdrawCard",
			call: func(c *Client) (any, error) {
				return c.WithdrawCard(context.Background(), CardTransferRequest{OutOrderNo: "O2", CardID: "c1", Currency: "USD", Amount: "1.00"})
			},
			method: "POST", escPath: "/portal/api/v1/card/withdraw-transaction",
			body: `{"out_order_no":"O2","card_id":"c1","currency":"USD","amount":"1.00"}`,
		},
		{
			name: "UpdateCardStatusWithAccountNo",
			call: func(c *Client) (any, error) {
				return c.UpdateCardStatus(context.Background(), UpdateCardStatusRequest{CardID: "c1", CardStatus: "CANCELED", AccountNo: ptr(acct)})
			},
			method: "PUT", escPath: "/portal/api/v1/card/status",
			body:  `{"card_id":"c1","card_status":"CANCELED","account_no":2000519969952288769}`,
			reply: `{"data":{"card_id":"c1","card_status":"CANCELED"}}`,
			check: func(t *testing.T, got any) {
				if r := got.(*CardStatusResult); r.CardID != "c1" || r.CardStatus != "CANCELED" {
					t.Errorf("got %+v", r)
				}
			},
		},
		{
			name: "UpdateCardStatusNoAccountNo",
			call: func(c *Client) (any, error) {
				return c.UpdateCardStatus(context.Background(), UpdateCardStatusRequest{CardID: "c1", CardStatus: "REVOKED"})
			},
			method: "PUT", escPath: "/portal/api/v1/card/status",
			body: `{"card_id":"c1","card_status":"REVOKED"}`,
		},
		{
			name: "GetCardTransferOrder",
			call: func(c *Client) (any, error) {
				return c.GetCardTransferOrder(context.Background(), OrderRef{OutOrderNo: "O1"})
			},
			method: "GET", escPath: "/portal/api/v1/card/transfer-order", rawQuery: "out_order_no=O1",
			reply: `{"data":{"out_order_no":"O1","transaction_id":"T1","card_id":"c1","status":"SUCCESS"}}`,
			check: func(t *testing.T, got any) {
				if r := got.(*CardBalanceTransaction); r.Status != "SUCCESS" || r.OutOrderNo != "O1" {
					t.Errorf("got %+v", r)
				}
			},
		},
		{
			name: "ListCardBalanceTransactions",
			call: func(c *Client) (any, error) {
				return c.ListCardBalanceTransactions(context.Background(), CardBalanceTransactionsQuery{PageNo: 1, PageSize: 10, CardID: "c1", StartTime: 100, EndTime: 200, OutOrderNo: "O1"})
			},
			method: "GET", escPath: "/portal/api/v1/card/balance-transactions",
			rawQuery: "page_no=1&page_size=10&card_id=c1&start_time=100&end_time=200&out_order_no=O1",
			reply:    `{"count":"1","data":[{"out_order_no":"O1","transaction_id":"T1","card_id":"c1","status":"FAIL"}]}`,
			check: func(t *testing.T, got any) {
				if p := got.(*Page[CardBalanceTransaction]); p.Count != 1 || p.Data[0].Status != "FAIL" {
					t.Errorf("got %+v", p)
				}
			},
		},
		{
			name:   "GetCardSensitive",
			call:   func(c *Client) (any, error) { return c.GetCardSensitive(context.Background(), "c 1") },
			method: "GET", escPath: "/portal/api/v1/card/sensitive/c%201",
			reply: `{"data":{"card_id":"c 1","card_no":"4111111111111111","expiry_date":"1230","cvv2":"123"}}`,
			check: func(t *testing.T, got any) {
				if r := got.(*CardSensitive); r.CardNo != "4111111111111111" || r.CVV2 != "123" || r.ExpiryDate != "1230" {
					t.Errorf("got %+v", r)
				}
			},
		},
		{
			name:   "GetCard",
			call:   func(c *Client) (any, error) { return c.GetCard(context.Background(), "c1") },
			method: "GET", escPath: "/portal/api/v1/card/detail/c1",
			reply: `{"data":{"card_id":"c1","card_no":"4111","card_name":"N","status":"ACTIVE","available_balance":"9.99","create_time":1766123535,"limit":{"single_limit":"100.00"}}}`,
			check: func(t *testing.T, got any) {
				r := got.(*CardDetail)
				if r.Status != "ACTIVE" || r.AvailableBalance != "9.99" || r.Limit == nil || r.Limit.SingleLimit != "100.00" || r.CreateTime.Unix() != 1766123535 {
					t.Errorf("got %+v", r)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, c := newMockPlatform(t)
			if tc.reply != "" {
				m.Reply = func(w http.ResponseWriter, r *http.Request) { m.signedJSON(w, 200, tc.reply) }
			}
			got, err := tc.call(c)
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if m.Last == nil {
				t.Fatal("no request received")
			}
			if m.Last.Method != tc.method {
				t.Errorf("method = %s, want %s", m.Last.Method, tc.method)
			}
			if p := m.Last.URL.EscapedPath(); p != tc.escPath {
				t.Errorf("escaped path = %s, want %s", p, tc.escPath)
			}
			if m.Last.URL.RawQuery != tc.rawQuery {
				t.Errorf("rawQuery = %q, want %q", m.Last.URL.RawQuery, tc.rawQuery)
			}
			if string(m.LastBody) != tc.body {
				t.Errorf("body = %s, want %s", m.LastBody, tc.body)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}
