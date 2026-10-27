package marswallex

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// intQuery 把整数查询参数转成字符串；0 返回空串（buildQuery 会跳过）。
func intQuery(n int64) string {
	if n == 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

// BalanceItem 为账户某币种的余额，金额为字符串（元）。
type BalanceItem struct {
	Currency         string `json:"currency"`
	AvailableBalance string `json:"available_balance"`
	FreezeBalance    string `json:"freeze_balance"`
}

// Account 为账户（主钱包 / 子账户 / 共享账户）。Purpose 为 null 或缺失表示普通账户。
type Account struct {
	AccountNo     string        `json:"account_no"`
	AccountName   string        `json:"account_name"`
	AccountStatus string        `json:"account_status"`
	CreateTime    Timestamp     `json:"create_time"`
	BalanceList   []BalanceItem `json:"balance_list,omitempty"`
	AccountType   string        `json:"account_type"`
	Purpose       *string       `json:"purpose,omitempty"`
}

// SubAccount 为创建账户的响应。
type SubAccount struct {
	AccountNo    string    `json:"account_no"`
	AccountName  string    `json:"account_name"`
	OutAccountNo string    `json:"out_account_no"`
	Purpose      *string   `json:"purpose,omitempty"`
	Status       string    `json:"status"`
	CreateTime   Timestamp `json:"create_time"`
}

// AccountRef 为账户状态变更的响应。
type AccountRef struct {
	AccountNo    string `json:"account_no"`
	OutAccountNo string `json:"out_account_no"`
}

// AccountSerial 为账户流水。
type AccountSerial struct {
	SerialID        string    `json:"serial_id"`
	Currency        string    `json:"currency"`
	Amount          string    `json:"amount"`
	FlagDC          string    `json:"flag_dc"`
	TransactionType string    `json:"transaction_type"`
	CreateTime      Timestamp `json:"create_time"`
}

// CreateAccountRequest 为创建账户的参数。Purpose 传 "SHARED_ACCOUNT" 创建共享账户。
type CreateAccountRequest struct {
	AccountName  string  `json:"account_name"`
	OutAccountNo string  `json:"out_account_no"`
	Purpose      *string `json:"purpose,omitempty"`
}

// ListAccountsQuery 为分页查询账户的参数。
type ListAccountsQuery struct {
	PageNo, PageSize int
	AccountType      string
	AccountNo        string
}

// AccountSerialsQuery 为查询账户流水的参数，时间为 Unix 秒。
type AccountSerialsQuery struct {
	PageNo, PageSize int
	AccountNo        string
	Currency         string
	StartTime        int64
	EndTime          int64
}

// UpdateAccountStatusRequest 为账户状态变更参数（ACTIVE / DISABLED，remark 2~100 字符）。
type UpdateAccountStatusRequest struct {
	AccountStatus string `json:"account_status"`
	Remark        string `json:"remark"`
}

// CreateAccount 创建账户。按规范，请求体需包一层 {"data": {...}}。
func (c *Client) CreateAccount(ctx context.Context, req CreateAccountRequest) (*SubAccount, error) {
	body := struct {
		Data CreateAccountRequest `json:"data"`
	}{req}
	out := &SubAccount{}
	if err := c.call(ctx, http.MethodPost, "/v1/accounts", "", body, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListAccounts 分页查询账户。
func (c *Client) ListAccounts(ctx context.Context, q ListAccountsQuery) (*Page[Account], error) {
	rq := buildQuery(
		"page_no", intQuery(int64(q.PageNo)),
		"page_size", intQuery(int64(q.PageSize)),
		"account_type", q.AccountType,
		"account_no", q.AccountNo,
	)
	out := &Page[Account]{}
	if err := c.call(ctx, http.MethodGet, "/v1/accounts/page", rq, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListAccountSerials 查询账户流水。
func (c *Client) ListAccountSerials(ctx context.Context, q AccountSerialsQuery) (*Page[AccountSerial], error) {
	rq := buildQuery(
		"page_no", intQuery(int64(q.PageNo)),
		"page_size", intQuery(int64(q.PageSize)),
		"account_no", q.AccountNo,
		"currency", q.Currency,
		"start_time", intQuery(q.StartTime),
		"end_time", intQuery(q.EndTime),
	)
	out := &Page[AccountSerial]{}
	if err := c.call(ctx, http.MethodGet, "/v1/accounts/serials", rq, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetAccount 查询账户详情。
func (c *Client) GetAccount(ctx context.Context, accountNo string) (*Account, error) {
	out := &Account{}
	if err := c.call(ctx, http.MethodGet, "/v1/accounts/detail/"+url.PathEscape(accountNo), "", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateAccountStatus 变更账户状态。
func (c *Client) UpdateAccountStatus(ctx context.Context, accountNo string, req UpdateAccountStatusRequest) (*AccountRef, error) {
	out := &AccountRef{}
	if err := c.call(ctx, http.MethodPut, "/v1/accounts/"+url.PathEscape(accountNo)+"/status", "", req, out); err != nil {
		return nil, err
	}
	return out, nil
}
