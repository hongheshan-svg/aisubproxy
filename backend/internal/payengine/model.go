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

// 开通成功后关闭续费的进度（renewal_state 列，设计 2026-11-03 §3）。
const (
	RenewalNone      = ""          // 不适用：点数单、试运行单、没成功的单
	RenewalPending   = "pending"   // 待处理：续费协程会用会话去关
	RenewalCancelled = "cancelled" // 已关闭并核实不再续费
	RenewalFailed    = "failed"    // 自动重试用完或会话到点清空仍没关掉，等人工
	RenewalSkipped   = "skipped"   // 会话已清空，无法处理（含迁移前的旧成功单）
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
	// ErrRenewalUnavailable 表示这张单现在不能手动重试关闭续费（不是成功的真实开通单、已关闭，或会话已清空）。
	ErrRenewalUnavailable = errors.New("这张单不能重试关闭续费")
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

	Kind           string `json:"kind"`            // subscription 订阅开通、credit Codex 点数；建单后不变
	CreditQuantity int    `json:"credit_quantity"` // 点数单的点数，取自档位键
	// 开通成功后关闭续费，见 Renewal* 常量。RenewalNextAt 是续费协程下次处理的时刻。
	RenewalState     string `json:"renewal_state"`
	RenewalAttempts  int    `json:"renewal_attempts"`
	RenewalCheckedAt *int64 `json:"renewal_checked_at"`
	RenewalNextAt    *int64 `json:"renewal_next_at"`
	// 点数单：CardSaved 是成功后核对「我们的卡有没有被存进用户账号」的结果（空、not_saved、saved、unknown）；
	// SavedCardsBefore 是付款前已存卡的快照（id 的哈希与尾号），CreditBalanceBefore 是付款前的点数余额（取不到为空）。
	CardSaved           string `json:"card_saved"`
	SavedCardsBefore    string `json:"-"`
	CreditBalanceBefore string `json:"credit_balance_before"`
	// HasSession 表示单据里还存着用户会话的密文（不含会话本身）：关闭续费要用，界面据此决定能不能重试。
	HasSession bool `json:"has_session"`
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
