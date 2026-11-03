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

// CardLimit 为卡交易限额；金额为字符串（最小 0.01），不传则不限制。
type CardLimit struct {
	SingleLimit  string `json:"single_limit,omitempty"`
	DailyLimit   string `json:"daily_limit,omitempty"`
	MonthlyLimit string `json:"monthly_limit,omitempty"`
}

// UpdateCardLimitRequest 为卡限额修改参数。Limit 是整体替换：没传的项会变成「不限制」。
type UpdateCardLimitRequest struct {
	CardID string    `json:"card_id"`
	Limit  CardLimit `json:"limit"`
}

// CardLimitRemaining 为卡的剩余限额。
type CardLimitRemaining struct {
	DailyRemaining   string `json:"daily_remaining,omitempty"`
	MonthlyRemaining string `json:"monthly_remaining,omitempty"`
}

// CardLimitUsage 为卡限额与剩余限额。
type CardLimitUsage struct {
	CardID         string             `json:"card_id"`
	Limit          *CardLimit         `json:"limit,omitempty"`
	LimitRemaining CardLimitRemaining `json:"limit_remaining"`
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
	// FailReason 为平台给出的失败原因（Status 为 FAIL 时可能有），按平台文字处理，写入前需脱敏、截断。
	FailReason string `json:"fail_reason,omitempty"`
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

// UpdateCardLimit 修改卡限额（PUT /v1/card/limit）。不是资金接口，但会影响之后的扣款是否被拒，只能手动调用。
// 请求整体替换原限额，相同内容重复发送结果一样，所以结果不明时可以放心重试。三项金额都必须是大于 0 的规范金额，
// 且至少给一项（要清除全部限额应由调用方明确决定，这里不替它发空限额）。
func (c *Client) UpdateCardLimit(ctx context.Context, cardID string, limit CardLimit) error {
	if cardID == "" {
		return configError("卡 ID 不能为空", nil)
	}
	if limit == (CardLimit{}) {
		return configError("至少需要设置一项限额", nil)
	}
	for _, v := range []string{limit.SingleLimit, limit.DailyLimit, limit.MonthlyLimit} {
		if v == "" {
			continue
		}
		if cents, err := ParseAmount(v); err != nil || cents <= 0 {
			return configError("限额必须是大于 0 且最多两位小数的金额", err)
		}
	}
	return c.call(ctx, http.MethodPut, "/v1/card/limit", "", UpdateCardLimitRequest{CardID: cardID, Limit: limit}, nil)
}

// GetCardLimitUsage 查询卡剩余限额（GET /v1/card/limit-usage/{card_id}）。
func (c *Client) GetCardLimitUsage(ctx context.Context, cardID string) (*CardLimitUsage, error) {
	out := &CardLimitUsage{}
	if err := c.call(ctx, http.MethodGet, "/v1/card/limit-usage/"+url.PathEscape(cardID), "", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}
