package payengine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

// sessionAAD 是会话密文的 AAD。
const sessionAAD = "pay_order_session"

// Pool 是引擎用到的卡池能力，*cardpool.Service 直接满足。每家供卡型服务商一个，数据结构沿用 cardpool 包的类型。
type Pool interface {
	Acquire(context.Context, cardpool.AcquireRequest) (*cardpool.Lease, error)
	Fund(context.Context, *cardpool.Lease, int64) (*cardpool.Order, error)
	WaitOrder(context.Context, string) (*cardpool.Order, error)
	Sensitive(context.Context, *cardpool.Lease) (*cardpool.CardSensitive, error)
	WaitOTP(context.Context, string, time.Time) (string, error)
	Release(context.Context, *cardpool.Lease, cardpool.Outcome) error
	CardInfo(context.Context, string) (cardpool.CardInfo, error)
	ListTransactions(context.Context, cardpool.TxnFilter) ([]cardpool.Transaction, int, error)
	BackfillRecent(context.Context, int) (cardpool.BackfillState, error)
}

// ChatGPT 是引擎用到的 ChatGPT 接口，*chatgpt.Client 直接满足。
// 写操作只有 CreateCheckout、CreateCreditCheckout（建结账会话，不花钱）和 CancelRenewal（取消下期续费），其余只读。
type ChatGPT interface {
	Refresh(context.Context, *chatgpt.Session) error
	Account(context.Context, chatgpt.Session) (chatgpt.Account, error)
	CreateCheckout(context.Context, chatgpt.Session, string, string, string) (chatgpt.Checkout, error)
	CreateCreditCheckout(ctx context.Context, s chatgpt.Session, accountID string, quantity int) (chatgpt.CreditCheckoutResult, error)
	CancelRenewal(ctx context.Context, s chatgpt.Session, accountID string) error
	SubscriptionStatus(ctx context.Context, s chatgpt.Session, accountID string) (chatgpt.RenewalInfo, error)
	SavedCards(ctx context.Context, s chatgpt.Session, accountID string) ([]chatgpt.SavedCard, error)
	CreditBalance(ctx context.Context, s chatgpt.Session, accountID string) (chatgpt.CreditBalanceInfo, error)
}

// FormBrowser 是填表用到的浏览器能力，没有 Submit：只拿到它的代码点不了支付。
type FormBrowser interface {
	Open(context.Context, string, []paybrowser.Cookie) error
	ReadAmount(context.Context, string) (int64, string, error)
	Fill(context.Context, paybrowser.Card, paybrowser.Billing) error
	FillReport() paybrowser.FillReport     // 最近一次 Fill 的字段命中与核对结果，不含值
	PayButtonState(context.Context) string // 只读支付按钮的状态，绝不点击
	Diagnose(context.Context) []string     // 定位页面问题的几行文字，已脱敏
	// EnsureNoSavePayment 确认「保存付款信息」类勾选框没有勾选（勾着就取消），返回处理结果；确认不了报错。只动勾选框，绝不点支付。
	EnsureNoSavePayment(context.Context) (string, error)
	Shot(context.Context, string, paybrowser.ShotScope) (string, error)
	Close()
}

// Browser 是引擎用到的支付页浏览器，*paybrowser.Browser 直接满足。
type Browser interface {
	FormBrowser
	Submit(context.Context) error
	LockSubmit() bool // 锁定提交，返回此前是否开始过点击；返回 false 才能证明没点击
	WaitOutcome(context.Context, time.Duration) (paybrowser.Outcome, string, error)
	Notices() []string // 取走等结果期间记下的未识别提示（已脱敏、去重）
	EnterOTP(context.Context, string) error
}

// Timeouts 是各步骤的超时；测试可缩短。VerifyTick 是确认协程扫描到期单据的间隔；
// OutcomeMargin 是等页面结果时外层时限比 SubmitResult 多出的余量；Backfill 是定论失败前补拉一次交易的时限。
// RenewalTick 是续费协程的扫描间隔；SessionKeep 是单据结束后会话最长保留多久（关闭续费要用）。
type Timeouts struct {
	PageLoad, Fill, SubmitResult, OTP, Fund, Verify, VerifyEvery, DeclineGrace, VerifyTick, OutcomeMargin, Backfill time.Duration
	RenewalTick, SessionKeep                                                                                        time.Duration
}

// DefaultTimeouts 返回引擎默认超时：页面加载 60 秒、填表 2 分钟、提交后等待页面结果 2 分钟、
// 验证码 5 分钟、注资 3 分钟、确认 10 分钟（每 15 秒查一次）、被拒后等待授权推送 2 分钟；确认协程每 5 秒扫描一次；
// 等页面结果的外层时限多给 10 秒；定论失败前补拉交易 30 秒。
func DefaultTimeouts() Timeouts {
	return Timeouts{
		PageLoad:     60 * time.Second,
		Fill:         2 * time.Minute,
		SubmitResult: 2 * time.Minute,
		OTP:          5 * time.Minute,
		Fund:         3 * time.Minute,
		Verify:       10 * time.Minute,
		VerifyEvery:  15 * time.Second,
		DeclineGrace: 2 * time.Minute,
		VerifyTick:   5 * time.Second,

		OutcomeMargin: 10 * time.Second,
		Backfill:      30 * time.Second,

		RenewalTick: defaultRenewalTick,
		SessionKeep: defaultSessionKeep,
	}
}

// MaxPayDuration 是一单付款从取卡租约到定论最长可能占用卡的时间，卡池租约须不短于它：
// 注资 + 填表 + 点支付后等页面结果 + 3DS 页面加载 + 验证码 + 填验证码并等结果 + 被拒宽限 + 确认 + 确认间隔 + 扫描间隔 + 5 秒关闭浏览器
// + 两次定论前补拉（期限到达之前最后一轮，加上到期那一轮，都可能等满补拉时限）。
func MaxPayDuration(t Timeouts) time.Duration {
	return t.Fund + t.Fill + t.SubmitResult + t.SubmitResult + t.PageLoad + t.OTP +
		t.SubmitResult + t.SubmitResult + t.DeclineGrace + t.Verify + t.VerifyEvery + t.VerifyTick + 5*time.Second + 2*t.Backfill
}

// Options 为 Service 的依赖。
type Options struct {
	// Pools 按供卡型服务商名取卡池，单据的 provider 决定用哪个。取不到时绝不改用别家的卡池：
	// 还没取卡就以 provider_unavailable 放弃，已经取卡或注资的转待人工。建好后不再改动。
	Pools   map[string]Pool
	ChatGPT func(proxyURL string) (ChatGPT, error)                               // nil → chatgpt.New
	Browser func(ctx context.Context, proxyURL, shotDir string) (Browser, error) // nil → paybrowser.Launch
	Now     func() time.Time                                                     // nil → time.Now；提交时间、确认时限与截图清理都用它
	Alert   func(string)                                                         // nil → log.Printf
	// ShotDir 是截图根目录，每单一个子目录；空则为 DB_PATH 所在目录下的 pay-shots。
	ShotDir  string
	Timeouts *Timeouts // nil → DefaultTimeouts()
}

// Service 是付款引擎：建单入队，后台工作协程按顺序处理。
type Service struct {
	opts Options
	t    Timeouts
	// realBrowser 表示用的是默认的真实浏览器工厂。只有这时 Start 才清理 Chrome 临时目录：
	// 测试替身不用真实浏览器，go test ./... 并行时别的包的测试可能正在用这些目录。
	realBrowser bool

	kick    chan struct{}
	loops   sync.WaitGroup // 工作协程与各单据协程
	mu      sync.Mutex
	running map[string]bool // 正在处理的单号
	// verifyErrs 记各单上次写入时间线的确认错误（单号 → 来源 → 脱敏后的文本），文本没变就不再写。
	// 只在内存里，重启后多写一条可以接受；单据定论时删除。
	verifyErrs map[string]map[string]string

	breakerAlerted string // 熔断告警的去重键，见 breakerAlert；受 mu 保护

	recoveryFailed bool            // 重启恢复失败过（已告警）；只在 Start 和其后的工作协程里读写，二者不并发
	graceRestored  map[string]bool // 本次启动已补过宽限期的被拒单，恢复重试时不再补；读写范围同上
	recovered      atomic.Bool     // 重启恢复已完成：此前不派发，确认协程不接管、不定论被拒单
	renewing       map[string]bool // 正在关闭续费的单号（续费协程与手动重试互斥）；受 mu 保护

	// checkURL 校验支付链接，New 里固定为 checkCheckoutURL。不开放给 Options：只有包内测试（用本机仿造页跑真实 Chrome）能换掉它。
	checkURL func(string) error
}

// New 创建引擎，未提供的依赖取默认实现。
func New(o Options) *Service {
	if o.ChatGPT == nil {
		o.ChatGPT = newChatGPT
	}
	realBrowser := o.Browser == nil
	if realBrowser {
		o.Browser = launchBrowser
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Alert == nil {
		o.Alert = func(text string) { log.Printf("[payengine] 告警: %s", text) }
	}
	if o.ShotDir == "" {
		o.ShotDir = defaultShotDir()
	}
	t := DefaultTimeouts()
	if o.Timeouts != nil {
		t = *o.Timeouts
	}
	if t.VerifyTick <= 0 {
		t.VerifyTick = DefaultTimeouts().VerifyTick
	}
	if t.OutcomeMargin <= 0 {
		t.OutcomeMargin = DefaultTimeouts().OutcomeMargin
	}
	if t.Backfill <= 0 {
		t.Backfill = DefaultTimeouts().Backfill
	}
	if t.RenewalTick <= 0 {
		t.RenewalTick = defaultRenewalTick
	}
	// 会话保留不能比默认更长：到点一律清空（设计 §3.1 最长 1 小时）
	if t.SessionKeep <= 0 || t.SessionKeep > defaultSessionKeep {
		t.SessionKeep = defaultSessionKeep
	}
	return &Service{opts: o, t: t, realBrowser: realBrowser, kick: make(chan struct{}, 1), running: map[string]bool{}, verifyErrs: map[string]map[string]string{},
		renewing: map[string]bool{}, checkURL: checkCheckoutURL}
}

func (s *Service) now() time.Time { return s.opts.Now() }

// HasPool 判断引擎里有没有这家服务商的卡池。
func (s *Service) HasPool(provider string) bool {
	_, ok := s.poolFor(provider)
	return ok
}

// poolFor 取这家服务商的卡池。名字为空或没有卡池时返回 false：调用方按 provider_unavailable 处理，绝不改用别家的卡池。
func (s *Service) poolFor(provider string) (Pool, bool) {
	if provider == "" {
		return nil, false
	}
	p, ok := s.opts.Pools[provider]
	return p, ok && p != nil
}

// ShotDir 返回截图根目录（每单一个子目录），供后台接口读取截图。
func (s *Service) ShotDir() string { return s.opts.ShotDir }

// Timeouts 返回引擎实际使用的各步骤超时。
func (s *Service) Timeouts() Timeouts { return s.t }

func newChatGPT(proxyURL string) (ChatGPT, error) {
	c, err := chatgpt.New(proxyURL)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func launchBrowser(ctx context.Context, proxyURL, shotDir string) (Browser, error) {
	b, err := paybrowser.Launch(ctx, paybrowser.LaunchOptions{ProxyURL: proxyURL, ShotDir: shotDir})
	if err != nil {
		return nil, err
	}
	return b, nil
}

// defaultShotDir 与 db.Init 取同一个数据库路径（DB_PATH，默认 ../data/cdk_recharge.db），放在其目录下的 pay-shots。
func defaultShotDir() string {
	dbPath := "../data/cdk_recharge.db"
	if p := strings.TrimSpace(os.Getenv("DB_PATH")); p != "" {
		dbPath = p
	}
	return filepath.Join(filepath.Dir(dbPath), "pay-shots")
}

// Request 是一次付款请求。DryRun 试运行到读金额；DryRunFill 试运行到填表（用公开测试卡走完 Fill，绝不点支付、不碰卡池），隐含 DryRun。
// Provider 是供卡型服务商名，必填，且引擎里要有它的卡池。
type Request struct {
	Provider, PlanKey, Region, Session, Source, SourceRef string
	DryRun, DryRunFill                                    bool
}

// badSessionError 保留 chatgpt 的错误说明，同时可被 errors.Is(err, ErrBadSession) 识别。
type badSessionError struct{ err error }

func (e *badSessionError) Error() string        { return e.err.Error() }
func (e *badSessionError) Unwrap() error        { return e.err }
func (e *badSessionError) Is(target error) bool { return target == ErrBadSession }

// Submit 校验并建单入队，立即返回。错误：ErrPlanUnavailable（套餐 × 地区不可用）、
// ErrBadSession（会话无法解析、缺 sessionToken 或邮箱）、ErrInflight（同账号已有进行中单据）、
// ErrProviderUnavailable（该服务商没有卡池）；主密钥缺失时也报错。出错时不建单。
func (s *Service) Submit(ctx context.Context, r Request) (*Order, error) {
	if strings.TrimSpace(r.Source) == "" {
		return nil, fmt.Errorf("%w: 缺少来源", ErrBadRequest)
	}
	prov := strings.TrimSpace(r.Provider)
	if prov == "" {
		return nil, fmt.Errorf("%w: 缺少服务商", ErrBadRequest)
	}
	if !s.HasPool(prov) {
		return nil, fmt.Errorf("%w: %s", ErrProviderUnavailable, prov)
	}
	dryRun := r.DryRun || r.DryRunFill
	_, rc, err := ResolvePlan(r.PlanKey, r.Region, dryRun)
	if err != nil {
		return nil, err
	}
	sess, err := chatgpt.ParseSession(r.Session)
	if err != nil {
		return nil, &badSessionError{err: err}
	}
	key, err := secretbox.LoadKey()
	if err != nil {
		return nil, fmt.Errorf("无法加密会话: %w", err)
	}
	sealed, err := secretbox.Seal(key, sessionAAD, r.Session)
	if err != nil {
		return nil, fmt.Errorf("加密会话失败: %w", err)
	}
	o := &Order{
		Provider:       prov,
		Kind:           PlanKind(r.PlanKey),
		CreditQuantity: CreditQuantity(r.PlanKey),
		Source:         r.Source,
		SourceRef:      r.SourceRef,
		DryRun:         dryRun,
		DryRunFill:     r.DryRunFill,
		PlanKey:        r.PlanKey,
		Region:         r.Region,
		Currency:       rc.Currency,
		CapMinor:       rc.CapMinor,
		FundCents:      rc.FundCents,
		AccountEmail:   sess.Email,
		SessionSealed:  sealed,
	}
	if err := insertOrder(ctx, o); err != nil {
		return nil, err
	}
	s.Kick()
	return o, nil
}

// openSession 解开单据里的会话密文并解析。
func openSession(sealed string) (chatgpt.Session, error) {
	key, err := secretbox.LoadKey()
	if err != nil {
		return chatgpt.Session{}, err
	}
	raw, err := secretbox.Open(key, sessionAAD, sealed)
	if err != nil {
		return chatgpt.Session{}, err
	}
	return chatgpt.ParseSession(raw)
}

// Get 返回单据与时间线；不存在返回 ErrNotFound。
func (s *Service) Get(ctx context.Context, no string) (*Order, []Event, error) {
	o, err := getOrder(ctx, no)
	if err != nil {
		return nil, nil, err
	}
	ev, err := listEvents(ctx, no)
	if err != nil {
		return nil, nil, err
	}
	return o, ev, nil
}

// List 按 id 倒序分页列出单据，state 为空不筛选；返回总数。
func (s *Service) List(ctx context.Context, state string, page, size int) ([]Order, int, error) {
	return listOrders(ctx, state, page, size)
}

// ListBy 按筛选条件（状态、续费进度、种类）分页列出单据，id 倒序；返回总数。
func (s *Service) ListBy(ctx context.Context, f OrderFilter, page, size int) ([]Order, int, error) {
	return listOrdersBy(ctx, f, page, size)
}

// Resolve 人工定论一张待人工的单据：result 为 succeeded 或 failed，note 去空白后不能为空，否则返回 ErrBadRequest。
// 只改本单结果，不释放租约（转待人工时已释放）、不触发任何付款。needs_review 已写过一次终态字段，这里不重写 finished_at。
//
// 标记失败时按有没有提交过区分：提交过（submitted_at 非空）记 declined，绝不记成 not_submitted；
// 没提交过的（例如重启时已经取卡、但所属服务商没有卡池而转人工的单）记 not_submitted，保留原错误码。会话随之清空。
// 标记成功时，真实开通单还要关闭续费：会话还在就交给续费协程（pending），会话已清空记 skipped 并告警。
func (s *Service) Resolve(ctx context.Context, no, result, note, by string) error {
	note, by = strings.TrimSpace(note), strings.TrimSpace(by)
	if result != StateSucceeded && result != StateFailed {
		return fmt.Errorf("%w: 结果只能是 succeeded 或 failed", ErrBadRequest)
	}
	if note == "" {
		return fmt.Errorf("%w: 备注不能为空", ErrBadRequest)
	}
	if by == "" {
		return fmt.Errorf("%w: 缺少操作人", ErrBadRequest)
	}
	// 只读建单后不再变的字段（种类、试运行、提交时间）和会话；状态只靠下面的条件更新判断：
	// 先读状态再判断的话，两人同时定论时后者走的是另一条分支
	o, err := getOrder(ctx, no)
	if err != nil {
		return err
	}
	set := map[string]any{"resolved_by": by, "resolve_note": note}
	label, mode := "成功", moveStep
	renewalSkipped := false
	switch result {
	case StateSucceeded:
		// 与确认阶段的成功定论一致：成功的单不留错误信息；标记失败时保留，那是定论的依据
		set["error_code"], set["error_message"] = "", ""
		if wantsRenewalCancel(o) {
			if o.SessionSealed != "" {
				set["renewal_state"], set["renewal_next_at"] = RenewalPending, s.now().Unix()
			} else {
				set["renewal_state"], renewalSkipped = RenewalSkipped, true
			}
		}
	case StateFailed:
		label, mode = "失败", moveStepClear
		set["failure"] = FailureDeclined
		if o.SubmittedAt == nil {
			set["failure"] = FailureNotSubmitted
		}
	}
	msg := fmt.Sprintf("%s 人工定论为%s：%s", by, label, note)
	err = move(ctx, no, StateNeedsReview, result, "info", msg, set, mode)
	if errors.Is(err, ErrStateChanged) {
		return fmt.Errorf("%w: 只有待人工的单据才能定论（%w）", ErrBadRequest, err)
	}
	if err == nil && renewalSkipped {
		const why = "会话已清空，无法关闭续费，请让用户自行在 ChatGPT 设置里关闭续费"
		if eerr := addEvent(ctx, no, "warn", why, ""); eerr != nil {
			log.Printf("[payengine] 单据 %s 写时间线失败: %v", no, eerr)
		}
		s.opts.Alert(fmt.Sprintf("付款单 %s 人工定论为成功，但%s。", no, why))
	}
	return err
}

// Evidence 返回该单的卡在授权窗口内（submitted_at 减去时钟容差，见 authWindowStart）的交易，新的在前；
// 未提交或没有卡时返回空。翻完所有页，查询出错如实返回。
func (s *Service) Evidence(ctx context.Context, no string) ([]cardpool.Transaction, error) {
	o, err := getOrder(ctx, no)
	if err != nil {
		return nil, err
	}
	if o.SubmittedAt == nil || o.CardID == "" {
		return []cardpool.Transaction{}, nil
	}
	pool, ok := s.poolFor(o.Provider)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrProviderUnavailable, o.Provider)
	}
	return s.txnsSince(ctx, pool, o.CardID, authWindowStart(*o.SubmittedAt))
}
