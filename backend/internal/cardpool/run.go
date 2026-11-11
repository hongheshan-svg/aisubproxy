package cardpool

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/safego"
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

// Start 启动五个后台协程，ctx 结束时全部退出：
//   - 每 5 秒：释放到期租约 → 清除过期验证码 → 处理有效期不足的可用卡 → 每日待人工汇总（北京时间 9 点后每天告警一次）→
//     对账 → 检查是否长期收不到 Webhook → 同步账户 → 回收；
//   - 每 60 秒或收到补卡通知：补卡；
//   - 收到 Kick 或每 10 秒：处理 Webhook 事件（启动时先处理一轮积压）；
//   - 每 15 分钟：补拉授权与结算交易（与手动补拉串行）；
//   - 每 10 分钟：定时查询卡状态与持卡人状态（没配 Webhook 时补上外部冻结、注销与审核结果的告警）。
//
// 各步骤出错只记日志、不中断循环；取不到 MarsWallex 客户端（未配置）时，本轮跳过需要调接口的步骤。
func (s *Service) Start(ctx context.Context) {
	poll := cardStatusPollInterval
	s.loops.Add(5)
	safego.Go("卡池维护循环", func() { s.runLoop(ctx, "卡池维护", maintainInterval, nil, s.maintainRound) })
	safego.Go("补卡循环", func() { s.runLoop(ctx, "补卡", replenishInterval, s.kickReplenishCh, s.replenishRound) })
	safego.Go("Webhook 事件循环", func() { s.runLoop(ctx, "Webhook 事件处理", eventScanInterval, s.kickEvents, s.eventsRound) })
	safego.Go("交易补拉循环", func() { s.runLoop(ctx, "交易补拉", backfillInterval, nil, s.backfillRound) })
	safego.Go("定时查询循环", func() { s.runLoop(ctx, "定时查询卡与持卡人状态", poll, nil, s.statusPollRound) })
	s.Kick()
}

// Wait 阻塞到 Start 启动的后台协程全部退出；调用前须先取消传给 Start 的 ctx。
func (s *Service) Wait() { s.loops.Wait() }

// runLoop 在定时器到点或收到通知时执行一轮 round，直到 ctx 结束。kick 为 nil 时只按定时器执行。
// 每一轮单独兜住 panic（记日志、告警，name 是告警里的任务名）：一轮出错，下一轮照常，循环本身不退出。
func (s *Service) runLoop(ctx context.Context, name string, every time.Duration, kick <-chan struct{}, round func(context.Context, *loopLog)) {
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
		safego.Run(name, func() { round(ctx, lg) })
	}
}

// maintainRound：先做只读写本地库的步骤（释放到期租约、清除过期验证码、处理有效期不足的可用卡、每日待人工汇总），平台变慢也不会拖延它们；
// 再对账（取不到客户端时只做本地的超时升级并告警，见 reconcileOrdersOnce），然后检查是否长期收不到 Webhook（只读本地库）、按需同步账户，
// 最后回收（要调接口，取不到客户端时跳过；Webhook 检查与同步账户也只在 MarsWallex 已就绪时做）。
func (s *Service) maintainRound(ctx context.Context, lg *loopLog) {
	local := []struct {
		name string
		run  func(context.Context) error
	}{
		{"释放到期租约", s.expireLeasesOnce},
		{"清除过期验证码", s.purgeOTPsOnce},
		{"处理有效期不足的可用卡", s.retireExpiringOnce},
		{"每日待人工汇总", s.dailyReviewSummary},
	}
	for _, st := range local {
		if ctx.Err() != nil {
			return
		}
		lg.check(st.name, st.run(ctx))
	}
	if ctx.Err() != nil {
		return
	}
	_, cerr := s.opts.Client()
	lg.readiness("查单与回收", cerr)
	lg.check("对账", s.reconcileOrdersOnce(ctx))
	if cerr != nil || ctx.Err() != nil {
		return
	}
	lg.check("检查是否收到 Webhook", s.checkNoWebhook(ctx))
	lg.check("同步账户", s.syncAccountsIfDirty(ctx))
	if ctx.Err() != nil {
		return
	}
	lg.check("回收", s.advanceCardsOnce(ctx))
}

// replenishRound 补卡一轮；未配置时跳过。
func (s *Service) replenishRound(ctx context.Context, lg *loopLog) {
	_, err := s.opts.Client()
	lg.readiness("补卡", err)
	if err != nil {
		return
	}
	lg.check("补卡", s.replenishOnce(ctx))
}

// backfillRound 补拉一轮交易；未配置时跳过，不改补拉状态。
func (s *Service) backfillRound(ctx context.Context, lg *loopLog) {
	_, err := s.opts.Client()
	lg.readiness("交易补拉", err)
	if err != nil {
		return
	}
	lg.check("交易补拉", s.backfillOnce(ctx))
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
	now      func() time.Time
	last     map[string]time.Time
	notReady bool // 上一次检查时 MarsWallex 未就绪；初始视为就绪
}

// readiness 只在就绪状态变化时记日志：就绪 → 未就绪记一次（附原因），未就绪 → 就绪记一次。skipped 为未就绪时跳过的步骤。
func (l *loopLog) readiness(skipped string, err error) {
	switch {
	case err != nil && !l.notReady:
		l.notReady = true
		log.Printf("[cardpool] MarsWallex 未就绪，跳过%s: %v", skipped, err)
	case err == nil && l.notReady:
		l.notReady = false
		log.Printf("[cardpool] MarsWallex 已就绪，恢复%s", skipped)
	}
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
