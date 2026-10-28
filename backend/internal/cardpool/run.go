package cardpool

import (
	"context"
	"errors"
	"log"
	"time"
)

const (
	maintainInterval  = 5 * time.Second  // 对账、租约到期、回收、清除验证码
	replenishInterval = 60 * time.Second // 补卡
	eventScanInterval = 10 * time.Second // Webhook 事件兜底扫描
	loopLogQuiet      = 10 * time.Minute // 同一循环里相同的错误 10 分钟内只记一次
	loopLogMaxKeys    = 256
)

// Kick 非阻塞地通知事件处理协程立即处理一轮；已有待处理的通知时合并。
func (s *Service) Kick() {
	select {
	case s.kickEvents <- struct{}{}:
	default:
	}
}

// Start 启动三个后台协程，ctx 结束时全部退出：
//   - 每 5 秒：对账 → 释放到期租约 → 回收 → 清除过期验证码；
//   - 每 60 秒或收到补卡通知：补卡；
//   - 收到 Kick 或每 10 秒：处理 Webhook 事件（启动时先处理一轮积压）。
//
// 各步骤出错只记日志、不中断循环；取不到 MarsWallex 客户端（未配置）时，本轮跳过需要调接口的步骤。
func (s *Service) Start(ctx context.Context) {
	s.loops.Add(3)
	go s.runLoop(ctx, maintainInterval, nil, s.maintainRound)
	go s.runLoop(ctx, replenishInterval, s.kickReplenishCh, s.replenishRound)
	go s.runLoop(ctx, eventScanInterval, s.kickEvents, s.eventsRound)
	s.Kick()
}

// Wait 阻塞到 Start 启动的后台协程全部退出；调用前须先取消传给 Start 的 ctx。
func (s *Service) Wait() { s.loops.Wait() }

// runLoop 在定时器到点或收到通知时执行一轮 round，直到 ctx 结束。kick 为 nil 时只按定时器执行。
func (s *Service) runLoop(ctx context.Context, every time.Duration, kick <-chan struct{}, round func(context.Context, *loopLog)) {
	defer s.loops.Done()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	lg := &loopLog{now: s.now, last: map[string]time.Time{}}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-kick:
		}
		if ctx.Err() != nil {
			return
		}
		round(ctx, lg)
	}
}

// maintainRound：对账 → 释放到期租约 → 回收 → 清除过期验证码。对账与回收要调接口，未配置时跳过。
func (s *Service) maintainRound(ctx context.Context, lg *loopLog) {
	_, cerr := s.opts.Client()
	if cerr != nil {
		lg.check("MarsWallex 未就绪，跳过对账与回收", cerr)
	}
	steps := []struct {
		name     string
		needsAPI bool
		run      func(context.Context) error
	}{
		{"对账", true, s.reconcileOrdersOnce},
		{"释放到期租约", false, s.expireLeasesOnce},
		{"回收", true, s.advanceCardsOnce},
		{"清除过期验证码", false, s.purgeOTPsOnce},
	}
	for _, st := range steps {
		if ctx.Err() != nil {
			return
		}
		if st.needsAPI && cerr != nil {
			continue
		}
		lg.check(st.name, st.run(ctx))
	}
}

// replenishRound 补卡一轮；未配置时跳过。
func (s *Service) replenishRound(ctx context.Context, lg *loopLog) {
	if _, err := s.opts.Client(); err != nil {
		lg.check("MarsWallex 未就绪，跳过补卡", err)
		return
	}
	lg.check("补卡", s.replenishOnce(ctx))
}

// eventsRound 处理一轮事件；取满一批说明可能还有积压，立即再来一轮。
func (s *Service) eventsRound(ctx context.Context, lg *loopLog) {
	n, err := s.processEventsOnce(ctx)
	lg.check("处理 Webhook 事件", err)
	if err == nil && n >= eventBatch {
		s.Kick()
	}
}

// loopLog 记录后台循环的错误；同一循环里相同的错误 10 分钟内只记一次，避免未配置时刷屏。只在所属协程里使用。
type loopLog struct {
	now  func() time.Time
	last map[string]time.Time
}

func (l *loopLog) check(step string, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	msg := step + ": " + err.Error()
	now := l.now()
	if t, ok := l.last[msg]; ok && now.Sub(t) < loopLogQuiet {
		return
	}
	if len(l.last) >= loopLogMaxKeys {
		for k, t := range l.last {
			if now.Sub(t) >= loopLogQuiet {
				delete(l.last, k)
			}
		}
		if len(l.last) >= loopLogMaxKeys {
			l.last = map[string]time.Time{}
		}
	}
	l.last[msg] = now
	log.Printf("[cardpool] %s", msg)
}
