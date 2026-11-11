// Package cardpool 实现 MarsWallex 卡池：开卡、租约、转账、对账与健康度。
package cardpool

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

var (
	ErrNotFound     = errors.New("记录不存在")
	ErrNoCard       = errors.New("没有可用的卡")
	ErrLeaseInvalid = errors.New("租约无效或已过期")
	ErrCardBusy     = errors.New("卡正忙，有在途操作")
	ErrBadRequest   = errors.New("请求参数不合法")
	ErrInflight     = errors.New("已有在途单据")
	// ErrNoAccounts：平台一个账户都没返回（商户至少有主钱包），发生在建单前，请求未提交
	ErrNoAccounts = errors.New("平台未返回任何账户，本次未提交，请稍后刷新账户再试")
	// ErrHolderAccepted：平台已受理新建持卡人，但本地写入失败
	ErrHolderAccepted = errors.New("平台已受理，请勿重复提交；稍后刷新或在提交记录中查看")
	// ErrMaybeApplied：请求已经发到平台（平台可能已经生效），但本地没能记下结果。包在原错误外面，调用方据此留失败审计、提示勿重复提交
	ErrMaybeApplied = errors.New("请求已发出，但本地没能记下结果，平台可能已经生效；请勿重复提交，稍后到单据或列表里核对")
)

// badRequestError 的 Error() 即消息本身，并可被 errors.Is(err, ErrBadRequest) 识别。
type badRequestError struct{ msg string }

func (e *badRequestError) Error() string        { return e.msg }
func (e *badRequestError) Is(target error) bool { return target == ErrBadRequest }

func badRequest(msg string) error { return &badRequestError{msg: msg} }

// 卡池状态
const (
	PoolAvailable = "available"
	PoolLeased    = "leased"
	PoolReleasing = "releasing"
	PoolUsed      = "used"
	PoolRetired   = "retired"
	PoolDisabled  = "disabled"
)

// 单据类型
const (
	KindApply    = "apply"
	KindRecharge = "recharge"
	KindWithdraw = "withdraw"
	KindStatus   = "status"

	KindConversion     = "conversion"
	KindWalletTransfer = "wallet_transfer"
)

// 单据状态
const (
	StatePending     = "pending"
	StateProcessing  = "processing"
	StateUnknown     = "unknown"
	StateNeedsReview = "needs_review"
	StateSuccess     = "success"
	StateFailed      = "failed"
	StateNotSent     = "not_sent"
)

// IsTerminal 判断单据状态是否已终结。
func IsTerminal(state string) bool {
	return state == StateSuccess || state == StateFailed || state == StateNotSent
}

// Options 为 Service 的依赖。
type Options struct {
	Client func() (*marswallex.Client, error) // 必填；生产环境为 handler.MarsWallexClient
	Now    func() time.Time                   // nil → time.Now
	Alert  func(text string)                  // nil → log.Printf
	// MarsWallexSelected 判断 MarsWallex 现在是否被选用，决定自动补卡要不要暂停（见 replenishAllowed）。
	// 由启动代码注入：这个判定属于 provider 包，而 provider 依赖付款引擎，引擎又依赖卡池，卡池不能反过来 import。nil 视为放行。
	MarsWallexSelected func(ctx context.Context) (bool, error)
}

// Service 是卡池服务。
type Service struct {
	opts Options

	applyMu sync.Mutex // 手动开卡与自动补卡串行
	rr      int        // 持卡人轮转计数，受 applyMu 保护

	kickEvents      chan struct{}
	kickReplenishCh chan struct{}

	// 为 nil 时由事件消费循环回退到 handleEvent
	eventHandler func(context.Context, *queuedEvent, *marswallex.WebhookEvent) (string, error)

	loops sync.WaitGroup // Start 启动的后台协程

	syncMu        sync.Mutex    // 账户同步串行
	syncGen       atomic.Uint64 // 每次同步成功 +1，等锁的调用据此共享结果
	accountsDirty atomic.Bool   // 本地账户可能落后于平台

	backfillMu sync.Mutex // 定时补拉与手动补拉串行

	alertMu   sync.Mutex
	alertSent map[string]int64 // key → 最后发送时所在的北京日起点（Unix 秒）

	refreshFailMu sync.Mutex
	refreshFails  map[string]refreshFail // 回收中刷新卡详情连续失败的卡 → 失败计时，刷新成功即删除
}

// New 创建服务。
func New(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Alert == nil {
		opts.Alert = func(text string) { log.Printf("[cardpool] 告警: %s", text) }
	}
	return &Service{
		opts:            opts,
		kickEvents:      make(chan struct{}, 1),
		kickReplenishCh: make(chan struct{}, 1),
		alertSent:       map[string]int64{},
		refreshFails:    map[string]refreshFail{},
	}
}

func (s *Service) now() time.Time { return s.opts.Now() }

var beijing = time.FixedZone("UTC+8", 8*3600)

// beijingDayStart 返回 t 所在北京自然日的 00:00（北京时区）。
func beijingDayStart(t time.Time) time.Time {
	b := t.In(beijing)
	return time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, beijing)
}

// alert 发送告警；key 非空时同一 key 在同一北京自然日只发一次，key 为空每次都发。
func (s *Service) alert(key, text string) {
	if key != "" {
		day := beijingDayStart(s.now()).Unix()
		s.alertMu.Lock()
		if last, ok := s.alertSent[key]; ok && last == day {
			s.alertMu.Unlock()
			return
		}
		s.alertSent[key] = day
		s.alertMu.Unlock()
	}
	s.opts.Alert(text)
}
