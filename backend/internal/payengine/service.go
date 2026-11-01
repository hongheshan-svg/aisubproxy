package payengine

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

// sessionAAD 是会话密文的 AAD。
const sessionAAD = "pay_order_session"

// Pool 是引擎用到的卡池能力，*cardpool.Service 直接满足。
type Pool interface {
	Acquire(context.Context, cardpool.AcquireRequest) (*cardpool.Lease, error)
	Fund(context.Context, *cardpool.Lease, int64) (*cardpool.Order, error)
	WaitOrder(context.Context, string) (*cardpool.Order, error)
	Sensitive(context.Context, *cardpool.Lease) (*marswallex.CardSensitive, error)
	WaitOTP(context.Context, string, time.Time) (string, error)
	Release(context.Context, *cardpool.Lease, cardpool.Outcome) error
	CardInfo(context.Context, string) (cardpool.CardInfo, error)
	ListTransactions(context.Context, cardpool.TxnFilter) ([]cardpool.Transaction, int, error)
}

// ChatGPT 是引擎用到的 ChatGPT 接口，*chatgpt.Client 直接满足。
type ChatGPT interface {
	Refresh(context.Context, *chatgpt.Session) error
	Account(context.Context, chatgpt.Session) (chatgpt.Account, error)
	CreateCheckout(context.Context, chatgpt.Session, string, string, string) (chatgpt.Checkout, error)
}

// Browser 是引擎用到的支付页浏览器，*paybrowser.Browser 直接满足。
type Browser interface {
	Open(context.Context, string, []paybrowser.Cookie) error
	ReadAmount(context.Context, string) (int64, string, error)
	Fill(context.Context, paybrowser.Card, paybrowser.Billing) error
	Submit(context.Context) error
	WaitOutcome(context.Context, time.Duration) (paybrowser.Outcome, string, error)
	EnterOTP(context.Context, string) error
	Shot(context.Context, string, paybrowser.ShotScope) (string, error)
	Close()
}

// Timeouts 是各步骤的超时；测试可缩短。
type Timeouts struct {
	PageLoad, Fill, SubmitResult, OTP, Fund, Verify, VerifyEvery, DeclineGrace time.Duration
}

// DefaultTimeouts 返回引擎默认超时：页面加载 60 秒、填表 2 分钟、提交后等待页面结果 2 分钟、
// 验证码 5 分钟、注资 3 分钟、确认 10 分钟（每 15 秒查一次）、被拒后等待授权推送 2 分钟。
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
	}
}

// Options 为 Service 的依赖。
type Options struct {
	Pool    Pool
	ChatGPT func(proxyURL string) (ChatGPT, error)                                // nil → chatgpt.New
	Browser func(ctx context.Context, proxyURL, shotDir string) (Browser, error) // nil → paybrowser.Launch
	Now     func() time.Time                                                      // nil → time.Now
	Alert   func(string)                                                          // nil → log.Printf
	// ShotDir 是截图根目录，每单一个子目录；空则为 DB_PATH 所在目录下的 pay-shots。
	ShotDir  string
	Timeouts *Timeouts // nil → DefaultTimeouts()
}

// Service 是付款引擎：建单入队，后台工作协程按顺序处理。
type Service struct {
	opts Options
	t    Timeouts

	kick    chan struct{}
	loops   sync.WaitGroup // 工作协程与各单据协程
	mu      sync.Mutex
	running map[string]bool // 正在处理的单号
}

// New 创建引擎，未提供的依赖取默认实现。
func New(o Options) *Service {
	if o.ChatGPT == nil {
		o.ChatGPT = newChatGPT
	}
	if o.Browser == nil {
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
	return &Service{opts: o, t: t, kick: make(chan struct{}, 1), running: map[string]bool{}}
}

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

// Request 是一次付款请求。
type Request struct {
	PlanKey, Region, Session, Source, SourceRef string
	DryRun                                      bool
}

// badSessionError 保留 chatgpt 的错误说明，同时可被 errors.Is(err, ErrBadSession) 识别。
type badSessionError struct{ err error }

func (e *badSessionError) Error() string        { return e.err.Error() }
func (e *badSessionError) Unwrap() error        { return e.err }
func (e *badSessionError) Is(target error) bool { return target == ErrBadSession }

// Submit 校验并建单入队，立即返回。错误：ErrPlanUnavailable（套餐 × 地区不可用）、
// ErrBadSession（会话无法解析、缺 sessionToken 或邮箱）、ErrInflight（同账号已有进行中单据）；
// 主密钥缺失时也报错。出错时不建单。
func (s *Service) Submit(ctx context.Context, r Request) (*Order, error) {
	if strings.TrimSpace(r.Source) == "" {
		return nil, fmt.Errorf("%w: 缺少来源", ErrBadRequest)
	}
	_, rc, err := ResolvePlan(r.PlanKey, r.Region, r.DryRun)
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
		Source:        r.Source,
		SourceRef:     r.SourceRef,
		DryRun:        r.DryRun,
		PlanKey:       r.PlanKey,
		Region:        r.Region,
		Currency:      rc.Currency,
		CapMinor:      rc.CapMinor,
		FundCents:     rc.FundCents,
		AccountEmail:  sess.Email,
		SessionSealed: sealed,
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
