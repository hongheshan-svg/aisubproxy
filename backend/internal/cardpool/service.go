// Package cardpool 实现 MarsWallex 卡池：开卡、租约、转账、对账与健康度。
package cardpool

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

var (
	ErrNotFound     = errors.New("记录不存在")
	ErrNoCard       = errors.New("没有可用的卡")
	ErrLeaseInvalid = errors.New("租约无效或已过期")
	ErrCardBusy     = errors.New("卡正忙，有在途操作")
	ErrBadRequest   = errors.New("请求参数不合法")
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
}

// Service 是卡池服务。
type Service struct {
	opts Options

	applyMu sync.Mutex // 手动开卡与自动补卡串行
	rr      int        // 持卡人轮转计数，受 applyMu 保护

	kickEvents      chan struct{}
	kickReplenishCh chan struct{}

	// 为 nil 时由事件消费循环回退到 handleEvent
	eventHandler func(context.Context, *marswallex.WebhookEvent) (string, error)

	loops sync.WaitGroup // Start 启动的后台协程

	alertMu   sync.Mutex
	alertSent map[string]int64 // key → 最后发送时所在的北京日起点（Unix 秒）
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
