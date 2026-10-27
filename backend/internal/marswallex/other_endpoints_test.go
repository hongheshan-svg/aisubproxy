package marswallex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

func TestOtherEndpointRequests(t *testing.T) {
	ctx := context.Background()
	profile := CardholderProfile{
		FirstName: "San", LastName: "Zhang", DateOfBirth: "1990-01-02", ZoneNumber: "86", Mobile: "13800000000",
		Email: "a@b.com", CountryCode: "CN", IDType: "PASSPORT", IDNo: "E123", IDPeriodBegin: "2020-01-01",
		IDPeriodEnd: "2030-01-01", IDPhotoFileIDs: []string{"f1"},
		ResidentialAddress: Address{Country: "CN", State: "S", City: "C", Address: "A", PostCode: "1"},
		BillingAddress:     Address{Country: "CN", State: "S", City: "C", Address: "A", PostCode: "1"},
	}

	cases := []struct {
		name     string
		call     func(c *Client) (any, error)
		method   string
		escPath  string
		rawQuery string
		body     string
		reply    string
		check    func(t *testing.T, got any, m *mockPlatform)
	}{
		{
			name: "CreateCardholder",
			call: func(c *Client) (any, error) {
				return c.CreateCardholder(ctx, CreateCardholderRequest{OutCardHolderID: "OCH1", CardholderProfile: profile})
			},
			method: "POST", escPath: "/portal/api/v1/cardholder",
			reply: `{"data":{"card_holder_id":"CH1"}}`,
			check: func(t *testing.T, got any, m *mockPlatform) {
				if got.(*CardholderRef).CardHolderID != "CH1" {
					t.Errorf("got %+v", got)
				}
				var mp map[string]any
				if err := json.Unmarshal(m.LastBody, &mp); err != nil {
					t.Fatal(err)
				}
				if mp["out_card_holder_id"] != "OCH1" || mp["first_name"] != "San" || mp["residential_address"].(map[string]any)["post_code"] != "1" {
					t.Errorf("body not flattened: %s", m.LastBody)
				}
				if _, ok := mp["ssn"]; ok {
					t.Errorf("empty ssn should be omitted: %s", m.LastBody)
				}
				if _, ok := mp["chinese_first_name"]; ok {
					t.Errorf("empty chinese name should be omitted: %s", m.LastBody)
				}
			},
		},
		{
			name:   "GetCardholder",
			call:   func(c *Client) (any, error) { return c.GetCardholder(ctx, "CH 1") },
			method: "GET", escPath: "/portal/api/v1/cardholder/CH%201",
			reply: `{"data":{"card_holder_id":"CH 1","out_card_holder_id":"O","first_name":"San","status":"APPROVED","id_photo_file_ids":["a","b"],"residential_address":{"country":"CN"}}}`,
			check: func(t *testing.T, got any, m *mockPlatform) {
				d := got.(*CardholderDetail)
				if d.CardHolderID != "CH 1" || d.Status != "APPROVED" || d.FirstName != "San" || len(d.IDPhotoFileIDs) != 2 || d.ResidentialAddress.Country != "CN" {
					t.Errorf("got %+v", d)
				}
			},
		},
		{
			name:   "UpdateCardholder",
			call:   func(c *Client) (any, error) { return c.UpdateCardholder(ctx, "CH1", profile) },
			method: "PUT", escPath: "/portal/api/v1/cardholder/CH1",
			reply: `{"data":{"card_holder_id":"CH1"}}`,
			check: func(t *testing.T, got any, m *mockPlatform) {
				if strings.Contains(string(m.LastBody), "out_card_holder_id") {
					t.Errorf("body must not contain out_card_holder_id: %s", m.LastBody)
				}
				if !strings.Contains(string(m.LastBody), `"first_name":"San"`) {
					t.Errorf("body = %s", m.LastBody)
				}
			},
		},
		{
			name:   "UpdateCardholderStatus",
			call:   func(c *Client) (any, error) { return c.UpdateCardholderStatus(ctx, "CH1", "DISABLED") },
			method: "PUT", escPath: "/portal/api/v1/cardholder/status/CH1",
			body:  `{"status":"DISABLED"}`,
			reply: `{"data":{"card_holder_id":"CH1","out_card_holder_id":"O1","status":"DISABLED"}}`,
			check: func(t *testing.T, got any, m *mockPlatform) {
				r := got.(*CardholderStatusResult)
				if r.CardHolderID != "CH1" || r.OutCardHolderID != "O1" || r.Status != "DISABLED" {
					t.Errorf("got %+v", r)
				}
			},
		},
		{
			name: "ListAuthTransactions",
			call: func(c *Client) (any, error) {
				return c.ListAuthTransactions(ctx, CardTransactionsQuery{PageNo: 1, PageSize: 20, CardID: "c1", StartTime: 1, EndTime: 2})
			},
			method: "GET", escPath: "/portal/api/v1/card/transactions/authorization",
			rawQuery: "page_no=1&page_size=20&card_id=c1&start_time=1&end_time=2",
			reply: `{"count":"1","data":[{"transaction_id":"t1","transaction_type":"AUTH","card_id":"c1","status":"approved",
				"transaction_amount":"1.00","transaction_currency":"USD","billing_amount":"1.00","billing_currency":"USD",
				"merchant":{"merchant_name":"M","mcc":"5411"},"transaction_time":1766123535,
				"fee_details":[{"amount":"0.10","currency":"USD","type":"transactionFee"}]}]}`,
			check: func(t *testing.T, got any, m *mockPlatform) {
				p := got.(*Page[AuthTransaction])
				if p.Count != 1 || p.Data[0].TransactionID != "t1" || p.Data[0].Merchant.MCC != "5411" ||
					p.Data[0].TransactionTime.Unix() != 1766123535 || p.Data[0].FeeDetails[0].Type != "transactionFee" {
					t.Errorf("got %+v", p)
				}
			},
		},
		{
			name: "ListSettlementTransactions",
			call: func(c *Client) (any, error) {
				return c.ListSettlementTransactions(ctx, CardTransactionsQuery{PageNo: 1, PageSize: 20, TransactionID: "t9", StartTime: 1, EndTime: 2})
			},
			method: "GET", escPath: "/portal/api/v1/card/transactions/settlement",
			rawQuery: "page_no=1&page_size=20&transaction_id=t9&start_time=1&end_time=2",
			reply:    `{"count":0,"data":[{"transaction_id":"t9","transaction_type":"SETTLE","arn":"ARN1","transaction_time":1766123535}]}`,
			check: func(t *testing.T, got any, m *mockPlatform) {
				p := got.(*Page[SettlementTransaction])
				if p.Data[0].ARN != "ARN1" || p.Data[0].TransactionType != "SETTLE" {
					t.Errorf("got %+v", p)
				}
			},
		},
		{
			name:   "ListReceivingAddresses",
			call:   func(c *Client) (any, error) { return c.ListReceivingAddresses(ctx) },
			method: "GET", escPath: "/portal/api/v1/receiving/receiving-address",
			reply: `{"data":[{"currencies":["USDT","USDC"],"address":"0xabc","chain_type":"ETH"}]}`,
			check: func(t *testing.T, got any, m *mockPlatform) {
				r := got.([]ReceivingAddress)
				if len(r) != 1 || r[0].Address != "0xabc" || r[0].ChainType != "ETH" || len(r[0].Currencies) != 2 {
					t.Errorf("got %+v", r)
				}
			},
		},
		{
			name: "CreateConversion",
			call: func(c *Client) (any, error) {
				return c.CreateConversion(ctx, ConversionRequest{OutOrderNo: "O1", SellCurrency: "USDT", BuyCurrency: "USD", SellAmount: "10.00"})
			},
			method: "POST", escPath: "/portal/api/v1/conversion",
			body:  `{"out_order_no":"O1","sell_currency":"USDT","sell_amount":"10.00","buy_currency":"USD"}`,
			reply: `{"data":{"transaction_id":"T1"}}`,
			check: func(t *testing.T, got any, m *mockPlatform) {
				if got.(*ConversionResult).TransactionID != "T1" {
					t.Errorf("got %+v", got)
				}
			},
		},
		{
			name: "CreateConversionNoAmount",
			call: func(c *Client) (any, error) {
				return c.CreateConversion(ctx, ConversionRequest{OutOrderNo: "O1", SellCurrency: "USDT", BuyCurrency: "USD"})
			},
			method: "POST", escPath: "/portal/api/v1/conversion",
			body: `{"out_order_no":"O1","sell_currency":"USDT","buy_currency":"USD"}`,
		},
		{
			name:   "GetConversion",
			call:   func(c *Client) (any, error) { return c.GetConversion(ctx, "O/1") },
			method: "GET", escPath: "/portal/api/v1/conversion/O%2F1",
			reply: `{"data":{"out_order_no":"O/1","status":"SUCCESS","buy_amount":"9.9","success_time":1766123535,"create_time":1766123500}}`,
			check: func(t *testing.T, got any, m *mockPlatform) {
				d := got.(*ConversionDetail)
				if d.Status != "SUCCESS" || d.BuyAmount != "9.9" || d.SuccessTime.Unix() != 1766123535 {
					t.Errorf("got %+v", d)
				}
			},
		},
		{
			name: "CreateTransfer",
			call: func(c *Client) (any, error) {
				return c.CreateTransfer(ctx, TransferRequest{OutOrderNo: "M1", PayerAccountNo: "1", PayeeAccountNo: "2", Amount: "100.50", Currency: "USD"})
			},
			method: "POST", escPath: "/portal/api/v1/transfer",
			body:  `{"out_order_no":"M1","payer_account_no":"1","payee_account_no":"2","amount":"100.50","currency":"USD"}`,
			reply: `{"count":null,"data":{"out_order_no":"M1","order_no":"P1","status":"PROCESSING"}}`,
			check: func(t *testing.T, got any, m *mockPlatform) {
				r := got.(*TransferResult)
				if r.Status != "PROCESSING" || r.OrderNo != "P1" || r.OutOrderNo != "M1" {
					t.Errorf("got %+v", r)
				}
			},
		},
		{
			name:   "GetTransfer",
			call:   func(c *Client) (any, error) { return c.GetTransfer(ctx, TransferRef{OutOrderNo: "M1", OrderNo: "P1"}) },
			method: "GET", escPath: "/portal/api/v1/transfer/order", rawQuery: "out_order_no=M1&order_no=P1",
			reply: `{"count":null,"data":{"out_order_no":"M1","order_no":"P1","status":"SUCCESS","amount":"1.00","currency":"USD","create_time":1766123822742,"success_time":1766123822742}}`,
			check: func(t *testing.T, got any, m *mockPlatform) {
				d := got.(*TransferDetail)
				if d.CreateTime.UnixMilli() != 1766123822742 || d.SuccessTime.UnixMilli() != 1766123822742 || d.Status != "SUCCESS" {
					t.Errorf("got %+v", d)
				}
			},
		},
		{
			name:   "GetTransferByOrderNo",
			call:   func(c *Client) (any, error) { return c.GetTransfer(ctx, TransferRef{OrderNo: "P1"}) },
			method: "GET", escPath: "/portal/api/v1/transfer/order", rawQuery: "order_no=P1",
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
			if tc.body != "" && string(m.LastBody) != tc.body {
				t.Errorf("body = %s, want %s", m.LastBody, tc.body)
			}
			if tc.body == "" && tc.method == "GET" && len(m.LastBody) != 0 {
				t.Errorf("GET body should be empty: %s", m.LastBody)
			}
			if tc.check != nil {
				tc.check(t, got, m)
			}
		})
	}
}

func TestUploadFileMultipartAndSigning(t *testing.T) {
	m, c := newMockPlatform(t)
	m.Reply = func(w http.ResponseWriter, r *http.Request) { m.signedJSON(w, 200, `{"data":{"file_id":"F1"}}`) }
	content := []byte("\x89PNG-binary\x00data")
	res, err := c.UploadFile(context.Background(), "CARDHOLDER_ID_PHOTO", "id.png", content)
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if res.FileID != "F1" {
		t.Errorf("FileID = %q", res.FileID)
	}
	r := m.Last
	if r.Method != "POST" || r.URL.EscapedPath() != "/portal/api/v1/file/upload" {
		t.Errorf("request = %s %s", r.Method, r.URL.EscapedPath())
	}
	if r.URL.RawQuery != "biz_type=CARDHOLDER_ID_PHOTO" {
		t.Errorf("rawQuery = %q", r.URL.RawQuery)
	}
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/form-data; boundary=") {
		t.Fatalf("Content-Type = %q", ct)
	}
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		t.Fatal(err)
	}
	mr := multipart.NewReader(bytes.NewReader(m.LastBody), params["boundary"])
	n := 0
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		n++
		b, _ := io.ReadAll(p)
		if p.FormName() != "file" || p.FileName() != "id.png" || !bytes.Equal(b, content) {
			t.Errorf("part = %q %q %q", p.FormName(), p.FileName(), b)
		}
	}
	if n != 1 {
		t.Errorf("parts = %d, want 1", n)
	}
}

func TestUploadFileTooLargeIsNotSent(t *testing.T) {
	m, c := newMockPlatform(t)
	_, err := c.UploadFile(context.Background(), "CARDHOLDER_ID_PHOTO", "big.png", make([]byte, 7<<20+1))
	if err == nil {
		t.Fatal("expected error")
	}
	if OutcomeOf(err) != OutcomeNotSent {
		t.Errorf("outcome = %v, want NotSent", OutcomeOf(err))
	}
	if m.Last != nil {
		t.Error("request must not be sent")
	}
	// 恰好 7MB 允许发送。
	if _, err := c.UploadFile(context.Background(), "CARDHOLDER_ID_PHOTO", "ok.png", make([]byte, 7<<20)); err != nil {
		t.Errorf("7MB exactly should pass: %v", err)
	}
}
