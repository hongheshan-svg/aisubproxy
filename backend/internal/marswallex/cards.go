package marswallex

import (
	"context"
	"net/http"
	"net/url"
)

// CardProduct 为卡产品。
type CardProduct struct {
	ProductID               string   `json:"product_id"`
	ProductName             string   `json:"product_name"`
	CardLabel               string   `json:"card_label"`
	CardCountry             string   `json:"card_country"`
	CardMode                string   `json:"card_mode"`
	CardCurrency            string   `json:"card_currency"`
	SupportedFeeDeductTypes []string `json:"supported_fee_deduct_types,omitempty"`
}

// CardLimit 为卡交易限额。
type CardLimit struct {
	SingleLimit string `json:"single_limit,omitempty"`
}

// ApplyCardRequest 为申卡参数；Amount 为字符串金额（最多 2 位小数），ExpiryDate 为 MMyy。
type ApplyCardRequest struct {
	OutOrderNo    string     `json:"out_order_no"`
	ProductID     string     `json:"product_id"`
	Amount        string     `json:"amount"`
	Currency      string     `json:"currency"`
	CardHolderID  string     `json:"card_holder_id"`
	CardName      string     `json:"card_name"`
	ExpiryDate    string     `json:"expiry_date"`
	AccountNo     string     `json:"account_no,omitempty"`
	Remark        string     `json:"remark,omitempty"`
	FeeDeductType string     `json:"fee_deduct_type,omitempty"`
	Limit         *CardLimit `json:"limit,omitempty"`
}

// ApplyCardResult 为申卡响应。
type ApplyCardResult struct {
	OutOrderNo    string `json:"out_order_no"`
	TransactionID string `json:"transaction_id"`
}

// OrderRef 用商家订单号或平台订单号定位订单（二选一，平台订单号优先）。
type OrderRef struct {
	OutOrderNo    string
	TransactionID string
}

func (o OrderRef) query() string {
	return buildQuery("out_order_no", o.OutOrderNo, "transaction_id", o.TransactionID)
}

// CardApplyOrder 为卡申请单。
type CardApplyOrder struct {
	CardID        string `json:"card_id,omitempty"`
	OutOrderNo    string `json:"out_order_no,omitempty"`
	TransactionID string `json:"transaction_id,omitempty"`
	Status        string `json:"status,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
}

// CardTransferRequest 为卡转入 / 转出参数，Amount 为字符串金额（元）。
type CardTransferRequest struct {
	OutOrderNo string `json:"out_order_no"`
	CardID     string `json:"card_id"`
	Currency   string `json:"currency"`
	Amount     string `json:"amount"`
}

// CardBalanceTransaction 为卡转入 / 转出记录。Type: CHARGE 转入、WITHDRAW 转出；Status: APPLY / SUCCESS / FAIL。
type CardBalanceTransaction struct {
	OutOrderNo    string    `json:"out_order_no"`
	TransactionID string    `json:"transaction_id"`
	CardID        string    `json:"card_id"`
	Currency      string    `json:"currency,omitempty"`
	Amount        string    `json:"amount,omitempty"`
	Type          string    `json:"type,omitempty"`
	Status        string    `json:"status"`
	CreateTime    Timestamp `json:"create_time"`
}

// UpdateCardStatusRequest 为更新卡状态参数。
// AccountNo 按规范是整数（钱包 ID，注销卡时可选），故用 *int64 而非字符串。
type UpdateCardStatusRequest struct {
	CardID     string `json:"card_id"`
	CardStatus string `json:"card_status"`
	Remark     string `json:"remark,omitempty"`
	AccountNo  *int64 `json:"account_no,omitempty"`
}

// CardStatusResult 为更新卡状态的响应。
type CardStatusResult struct {
	CardID     string `json:"card_id,omitempty"`
	CardStatus string `json:"card_status,omitempty"`
}

// CardBalanceTransactionsQuery 为分页卡转入转出明细参数，时间为 Unix 秒。
type CardBalanceTransactionsQuery struct {
	PageNo, PageSize int
	CardID           string
	StartTime        int64
	EndTime          int64
	OutOrderNo       string
}

// CardSensitive 为卡敏感信息（卡号、CVV2），调用方不得落日志。
type CardSensitive struct {
	CardID        string `json:"card_id"`
	CardNo        string `json:"card_no"`
	ExpiryDate    string `json:"expiry_date,omitempty"`
	CVV2          string `json:"cvv2,omitempty"`
	FreezeBalance string `json:"freeze_balance,omitempty"`
}

// CardDetail 为卡详情。
type CardDetail struct {
	CardID           string     `json:"card_id"`
	CardNo           string     `json:"card_no"`
	CardName         string     `json:"card_name"`
	CardType         string     `json:"card_type,omitempty"`
	CardMode         string     `json:"card_mode,omitempty"`
	CardLabel        string     `json:"card_label,omitempty"`
	CardCountry      string     `json:"card_country,omitempty"`
	CardCurrency     string     `json:"card_currency,omitempty"`
	ExpiryDate       string     `json:"expiry_date,omitempty"`
	Status           string     `json:"status"`
	AvailableBalance string     `json:"available_balance,omitempty"`
	FreezeBalance    string     `json:"freeze_balance,omitempty"`
	Remark           string     `json:"remark,omitempty"`
	CreateTime       Timestamp  `json:"create_time"`
	Limit            *CardLimit `json:"limit,omitempty"`
}

// ListCardProducts 查询卡产品。
func (c *Client) ListCardProducts(ctx context.Context) ([]CardProduct, error) {
	var out []CardProduct
	if err := c.call(ctx, http.MethodGet, "/v1/card/product", "", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ApplyCard 申卡。
func (c *Client) ApplyCard(ctx context.Context, req ApplyCardRequest) (*ApplyCardResult, error) {
	out := &ApplyCardResult{}
	if err := c.call(ctx, http.MethodPost, "/v2/card", "", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetCardApplyOrder 查询卡申请单。
func (c *Client) GetCardApplyOrder(ctx context.Context, ref OrderRef) (*CardApplyOrder, error) {
	out := &CardApplyOrder{}
	if err := c.call(ctx, http.MethodGet, "/v1/card/apply-order", ref.query(), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) cardTransfer(ctx context.Context, path string, req CardTransferRequest) (*CardBalanceTransaction, error) {
	out := &CardBalanceTransaction{}
	if err := c.call(ctx, http.MethodPost, path, "", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

// RechargeCard 卡转入。
func (c *Client) RechargeCard(ctx context.Context, req CardTransferRequest) (*CardBalanceTransaction, error) {
	return c.cardTransfer(ctx, "/v1/card/recharge-transaction", req)
}

// WithdrawCard 卡转出。
func (c *Client) WithdrawCard(ctx context.Context, req CardTransferRequest) (*CardBalanceTransaction, error) {
	return c.cardTransfer(ctx, "/v1/card/withdraw-transaction", req)
}

// UpdateCardStatus 更新卡状态。
func (c *Client) UpdateCardStatus(ctx context.Context, req UpdateCardStatusRequest) (*CardStatusResult, error) {
	out := &CardStatusResult{}
	if err := c.call(ctx, http.MethodPut, "/v1/card/status", "", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetCardTransferOrder 查询转入转出结果。
func (c *Client) GetCardTransferOrder(ctx context.Context, ref OrderRef) (*CardBalanceTransaction, error) {
	out := &CardBalanceTransaction{}
	if err := c.call(ctx, http.MethodGet, "/v1/card/transfer-order", ref.query(), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListCardBalanceTransactions 分页查询卡转入转出明细。
func (c *Client) ListCardBalanceTransactions(ctx context.Context, q CardBalanceTransactionsQuery) (*Page[CardBalanceTransaction], error) {
	rq := buildQuery(
		"page_no", intQuery(int64(q.PageNo)),
		"page_size", intQuery(int64(q.PageSize)),
		"card_id", q.CardID,
		"start_time", intQuery(q.StartTime),
		"end_time", intQuery(q.EndTime),
		"out_order_no", q.OutOrderNo,
	)
	out := &Page[CardBalanceTransaction]{}
	if err := c.call(ctx, http.MethodGet, "/v1/card/balance-transactions", rq, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetCardSensitive 查询卡敏感信息。
func (c *Client) GetCardSensitive(ctx context.Context, cardID string) (*CardSensitive, error) {
	out := &CardSensitive{}
	if err := c.call(ctx, http.MethodGet, "/v1/card/sensitive/"+url.PathEscape(cardID), "", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetCard 查询卡详情。
func (c *Client) GetCard(ctx context.Context, cardID string) (*CardDetail, error) {
	out := &CardDetail{}
	if err := c.call(ctx, http.MethodGet, "/v1/card/detail/"+url.PathEscape(cardID), "", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}
