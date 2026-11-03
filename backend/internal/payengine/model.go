// Package payengine 驱动 ChatGPT 订阅付款单据：建单、状态迁移、时间线与查询。
package payengine

import "errors"

// 单据状态。
const (
	StateQueued      = "queued"
	StateChecking    = "checking"
	StateLinkCreated = "link_created"
	StateOpening     = "opening"
	StateFunding     = "funding"
	StateFilling     = "filling"
	StateSubmitted   = "submitted"
	StateRequires3DS = "requires_3ds"
	StateVerifying   = "verifying"
	StateSucceeded   = "succeeded"
	StateFailed      = "failed"
	StateNeedsReview = "needs_review"
	StatePreviewed   = "previewed"
)

// 失败类别。
const (
	FailureNotSubmitted = "not_submitted"
	FailureDeclined     = "declined"
)

// IsTerminal 判断状态是否为终态（与在途唯一索引的排除集合一致）。
func IsTerminal(state string) bool {
	switch state {
	case StateSucceeded, StateFailed, StateNeedsReview, StatePreviewed:
		return true
	}
	return false
}

var (
	ErrNotFound        = errors.New("付款单据不存在")
	ErrInflight        = errors.New("该账号已有在途的付款单据")
	ErrStateChanged    = errors.New("单据状态已变化")
	ErrPlanUnavailable = errors.New("套餐不可用")
	ErrBadSession      = errors.New("会话无效")
	ErrBadRequest      = errors.New("请求参数无效")
	// ErrProviderUnavailable 表示单据所属的供卡型服务商在本引擎里没有卡池。引擎绝不改用别家的卡池。
	ErrProviderUnavailable = errors.New("该服务商没有可用的卡池")
)

// Order 是一张付款单据，字段与 pay_orders 列一一对应。DryRun、DryRunFill 共用 dry_run 列，见 dryRunColumn。
type Order struct {
	ID                int64  `json:"id"`
	OrderNo           string `json:"order_no"`
	Provider          string `json:"provider"` // 供卡型服务商，引擎按它取卡池；建单后不变
	Source            string `json:"source"`
	SourceRef         string `json:"source_ref"`
	DryRun            bool   `json:"dry_run"`
	DryRunFill        bool   `json:"dry_run_fill"` // 试运行到填表：DryRun 也为 true
	PlanKey           string `json:"plan_key"`
	Region            string `json:"region"`
	Currency          string `json:"currency"`
	CapMinor          int64  `json:"cap_minor"`
	FundCents         int64  `json:"fund_cents"`
	AccountEmail      string `json:"account_email"`
	AccountID         string `json:"account_id"`
	State             string `json:"state"`
	SessionSealed     string `json:"-"`
	CheckoutSessionID string `json:"checkout_session_id"`
	CheckoutURL       string `json:"-"`
	AmountMinor       int64  `json:"amount_minor"`
	AmountText        string `json:"amount_text"`
	CardID            string `json:"card_id"`
	CardLast4         string `json:"card_last4"`
	LeaseToken        string `json:"-"`
	FundOrderNo       string `json:"fund_order_no"`
	BillingState      string `json:"billing_state"`
	PageOutcome       string `json:"page_outcome"`
	SubmittedAt       *int64 `json:"submitted_at"`
	Failure           string `json:"failure"`
	ErrorCode         string `json:"error_code"`
	ErrorMessage      string `json:"error_message"`
	VerifyUntil       *int64 `json:"verify_until"`
	NextCheckAt       *int64 `json:"next_check_at"`
	ResolvedBy        string `json:"resolved_by"`
	ResolveNote       string `json:"resolve_note"`
	CreatedAt         int64  `json:"created_at"`
	UpdatedAt         int64  `json:"updated_at"`
	FinishedAt        *int64 `json:"finished_at"`
}

// Event 是单据时间线上的一条记录。
type Event struct {
	ID         int64  `json:"id"`
	OrderNo    string `json:"order_no"`
	At         int64  `json:"at"`
	State      string `json:"state"`
	Level      string `json:"level"`
	Message    string `json:"message"`
	Screenshot string `json:"screenshot"`
}
