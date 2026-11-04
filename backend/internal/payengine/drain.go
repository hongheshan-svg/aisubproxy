package payengine

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

const (
	// DrainMax 是停止服务、在线更新重载前最多等在途单多久。到点还没走完就照旧退出，重启后由恢复逻辑接管。
	// systemd 的 TimeoutStopSec、docker 的 stop_grace_period 都按它留了余量（240 秒）。
	DrainMax = 3 * time.Minute
	// DrainLogEvery 是等待期间每隔多久记一行日志，写明还剩几张在途单。
	DrainLogEvery = 30 * time.Second

	drainPoll = 20 * time.Millisecond
)

// processingStates 是在途状态：离开排队之后、进入确认之前。这时单据正在查账号、开支付页、注资、填表，或刚点完支付在等页面结果，
// 替换程序或停止服务会把它打断。排队的（还没开始）和确认中的（重启后确认协程接着查）不算。
var processingStates = []string{StateChecking, StateLinkCreated, StateOpening, StateFunding, StateFilling, StateSubmitted, StateRequires3DS}

// CountProcessing 返回处于在途状态的单据张数（以数据库为准，含试运行单）。在线更新替换文件之前用它挡住更新。
func CountProcessing(ctx context.Context) (int, error) {
	args := make([]any, len(processingStates))
	for i, st := range processingStates {
		args[i] = st
	}
	var n int
	err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM pay_orders WHERE state IN (?`+
		strings.Repeat(", ?", len(processingStates)-1)+`)`, args...).Scan(&n)
	return n, err
}

// dispatchHeld 判断派发是否暂停；调用方须持有 s.mu。
func (s *Service) dispatchHeld() bool { return s.holds > 0 }

// holdDispatch 暂停派发新单，返回恢复派发的函数（重复调用只生效一次）。可以叠加：全部恢复之后才重新派发。
// 已经在处理的单不受影响，照常走完。
func (s *Service) holdDispatch() (resume func()) {
	s.mu.Lock()
	s.holds++
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.holds--
			s.mu.Unlock()
			s.Kick()
		})
	}
}

// inFlight 返回正在处理的单据协程数。单据走到确认（verifying）或终态后协程就结束，名额归还。
func (s *Service) inFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running)
}

// Drain 停止派发新单，等在途单走到确认或终态，最多等 max；等待期间每隔 every 记一行日志，写明还剩几张。
// 返回结束时还剩的在途单张数，以及恢复派发的函数：停止服务时不用调；在线更新重载失败、进程接着跑时调用它恢复派发。
// 到点没走完的单不打断，由调用方决定是否照旧退出（重启后由恢复逻辑接管）。
func (s *Service) Drain(max, every time.Duration) (left int, resume func()) {
	resume = s.holdDispatch()
	n := s.inFlight()
	if n == 0 {
		return 0, resume
	}
	log.Printf("[payengine] 已停止派发新付款单，等 %d 张在途单处理完（最多 %v）", n, max)
	deadline := time.Now().Add(max)
	nextLog := time.Now().Add(every)
	poll := time.NewTicker(drainPoll)
	defer poll.Stop()
	for range poll.C {
		n = s.inFlight()
		if n == 0 {
			log.Printf("[payengine] 在途付款单已全部处理完（进入确认或已结束）")
			return 0, resume
		}
		now := time.Now()
		if !now.Before(deadline) {
			log.Printf("[payengine] 等了 %v 还有 %d 张在途单没处理完，不再等待；重启后由恢复逻辑接管", max, n)
			return n, resume
		}
		if every > 0 && !now.Before(nextLog) {
			log.Printf("[payengine] 等待在途付款单：还剩 %d 张", n)
			nextLog = now.Add(every)
		}
	}
	return n, resume
}
