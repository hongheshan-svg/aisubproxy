package marswallex

import (
	"context"
	"net/http"
	"net/url"
)

// ReceivingAddress 为数币收款地址。ChainType: ETH / TRX。
type ReceivingAddress struct {
	Currencies []string `json:"currencies"`
	Address    string   `json:"address"`
	ChainType  string   `json:"chain_type"`
}

// ConversionRequest 为兑换申请（卖出数币买入法币），SellAmount 为字符串金额。
type ConversionRequest struct {
	OutOrderNo   string `json:"out_order_no"`
	SellCurrency string `json:"sell_currency"`
	SellAmount   string `json:"sell_amount,omitempty"`
	BuyCurrency  string `json:"buy_currency"`
}

// ConversionResult 为兑换申请响应。
type ConversionResult struct {
	TransactionID string `json:"transaction_id"`
}

// ConversionDetail 为兑换单详情。Status: PROCESSING / SUCCESS / FAILED。
type ConversionDetail struct {
	OutOrderNo    string    `json:"out_order_no,omitempty"`
	UserID        string    `json:"user_id,omitempty"`
	TransactionID string    `json:"transaction_id,omitempty"`
	SellCurrency  string    `json:"sell_currency,omitempty"`
	SellAmount    string    `json:"sell_amount,omitempty"`
	BuyCurrency   string    `json:"buy_currency,omitempty"`
	BuyAmount     string    `json:"buy_amount,omitempty"`
	FeeAmount     string    `json:"fee_amount,omitempty"`
	FeeCurrency   string    `json:"fee_currency,omitempty"`
	Rate          string    `json:"rate,omitempty"`
	BaseCurrency  string    `json:"base_currency,omitempty"`
	Status        string    `json:"status,omitempty"`
	FailReason    string    `json:"fail_reason,omitempty"`
	SuccessTime   Timestamp `json:"success_time"`
	CreateTime    Timestamp `json:"create_time"`
}

// TransferRequest 为主钱包与子账户间划转参数，Amount 最多 2 位小数。
type TransferRequest struct {
	OutOrderNo     string `json:"out_order_no"`
	PayerAccountNo string `json:"payer_account_no"`
	PayeeAccountNo string `json:"payee_account_no"`
	Amount         string `json:"amount"`
	Currency       string `json:"currency"`
	Remark         string `json:"remark,omitempty"`
}

// TransferResult 为转账申请响应。Status: PROCESSING / SUCCESS / FAIL。
type TransferResult struct {
	OutOrderNo string `json:"out_order_no"`
	OrderNo    string `json:"order_no"`
	Status     string `json:"status"`
}

// TransferRef 用商家订单号或平台订单号定位转账单（二选一）。
type TransferRef struct {
	OutOrderNo string
	OrderNo    string
}

// TransferDetail 为转账详情，时间为 Unix 毫秒。
type TransferDetail struct {
	OutOrderNo     string    `json:"out_order_no"`
	OrderNo        string    `json:"order_no,omitempty"`
	Status         string    `json:"status"`
	Amount         string    `json:"amount"`
	Currency       string    `json:"currency"`
	PayerAccountNo string    `json:"payer_account_no"`
	PayeeAccountNo string    `json:"payee_account_no"`
	FailReason     string    `json:"fail_reason,omitempty"`
	SuccessTime    Timestamp `json:"success_time"`
	CreateTime     Timestamp `json:"create_time"`
}

// ListReceivingAddresses 查询数币收款地址。
func (c *Client) ListReceivingAddresses(ctx context.Context) ([]ReceivingAddress, error) {
	var out []ReceivingAddress
	if err := c.call(ctx, http.MethodGet, "/v1/receiving/receiving-address", "", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateConversion 发起兑换。
func (c *Client) CreateConversion(ctx context.Context, req ConversionRequest) (*ConversionResult, error) {
	out := &ConversionResult{}
	if err := c.call(ctx, http.MethodPost, "/v1/conversion", "", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetConversion 按商户订单号查询兑换单。
func (c *Client) GetConversion(ctx context.Context, outOrderNo string) (*ConversionDetail, error) {
	out := &ConversionDetail{}
	if err := c.call(ctx, http.MethodGet, "/v1/conversion/"+url.PathEscape(outOrderNo), "", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateTransfer 发起转账（out_order_no 为幂等键）。
func (c *Client) CreateTransfer(ctx context.Context, req TransferRequest) (*TransferResult, error) {
	out := &TransferResult{}
	if err := c.call(ctx, http.MethodPost, "/v1/transfer", "", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetTransfer 查询转账单。
func (c *Client) GetTransfer(ctx context.Context, ref TransferRef) (*TransferDetail, error) {
	rq := buildQuery("out_order_no", ref.OutOrderNo, "order_no", ref.OrderNo)
	out := &TransferDetail{}
	if err := c.call(ctx, http.MethodGet, "/v1/transfer/order", rq, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}
