package marswallex

import (
	"context"
	"net/http"
)

// TxMerchant 为交易商家信息。
type TxMerchant struct {
	MerchantName        string `json:"merchant_name,omitempty"`
	MerchantDescription string `json:"merchant_description,omitempty"`
	CountryCode         string `json:"country_code,omitempty"`
	MCC                 string `json:"mcc,omitempty"`
}

// TxFee 为交易手续费明细。
type TxFee struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Type     string `json:"type"`
}

// AuthTransaction 为授权交易。TransactionType: AUTH / AUTH_REVERSAL；Status: approved / failed / pending。
type AuthTransaction struct {
	TransactionID       string     `json:"transaction_id"`
	OriginTransactionID string     `json:"origin_transaction_id,omitempty"`
	TransactionType     string     `json:"transaction_type"`
	CardID              string     `json:"card_id"`
	Status              string     `json:"status"`
	TransactionAmount   string     `json:"transaction_amount"`
	TransactionCurrency string     `json:"transaction_currency"`
	BillingAmount       string     `json:"billing_amount"`
	BillingCurrency     string     `json:"billing_currency"`
	AuthCode            string     `json:"auth_code,omitempty"`
	Merchant            TxMerchant `json:"merchant"`
	OTP                 string     `json:"otp,omitempty"`
	DeclineReason       string     `json:"decline_reason,omitempty"`
	TransactionTime     Timestamp  `json:"transaction_time"`
	FeeDetails          []TxFee    `json:"fee_details,omitempty"`
}

// SettlementTransaction 为结算交易。TransactionType: SETTLE / REFUND；Status: approved / pending。
type SettlementTransaction struct {
	TransactionID       string     `json:"transaction_id"`
	TransactionType     string     `json:"transaction_type"`
	OriginTransactionID string     `json:"origin_transaction_id,omitempty"`
	AuthTransactionID   string     `json:"auth_transaction_id,omitempty"`
	CardID              string     `json:"card_id"`
	Status              string     `json:"status"`
	TransactionAmount   string     `json:"transaction_amount"`
	TransactionCurrency string     `json:"transaction_currency"`
	BillingAmount       string     `json:"billing_amount"`
	BillingCurrency     string     `json:"billing_currency"`
	AuthCode            string     `json:"auth_code,omitempty"`
	ARN                 string     `json:"arn,omitempty"`
	Merchant            TxMerchant `json:"merchant"`
	TransactionTime     Timestamp  `json:"transaction_time"`
	FeeDetails          []TxFee    `json:"fee_details,omitempty"`
}

// CardTransactionsQuery 为卡交易查询参数；StartTime / EndTime 为 Unix 秒。
type CardTransactionsQuery struct {
	PageNo, PageSize int
	CardID           string
	TransactionID    string
	StartTime        int64
	EndTime          int64
}

func (q CardTransactionsQuery) query() string {
	return buildQuery(
		"page_no", intQuery(int64(q.PageNo)),
		"page_size", intQuery(int64(q.PageSize)),
		"card_id", q.CardID,
		"transaction_id", q.TransactionID,
		"start_time", intQuery(q.StartTime),
		"end_time", intQuery(q.EndTime),
	)
}

// ListAuthTransactions 查询卡授权交易。
func (c *Client) ListAuthTransactions(ctx context.Context, q CardTransactionsQuery) (*Page[AuthTransaction], error) {
	out := &Page[AuthTransaction]{}
	if err := c.call(ctx, http.MethodGet, "/v1/card/transactions/authorization", q.query(), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListSettlementTransactions 查询卡结算交易。
func (c *Client) ListSettlementTransactions(ctx context.Context, q CardTransactionsQuery) (*Page[SettlementTransaction], error) {
	out := &Page[SettlementTransaction]{}
	if err := c.call(ctx, http.MethodGet, "/v1/card/transactions/settlement", q.query(), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}
