package payengine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// 开通成功后关闭续费（设计 2026-11-03 §3）。
//
// 触发：真实付款（dry_run = 0）的订阅开通单进入 succeeded，系统确认成功或人工定论为成功。点数单、试运行单一律不做。
// 关闭续费不花钱，只改账号设置：POST /subscriptions/cancel 取消下期续费，本期权益不受影响；之后用只读接口核实 will_renew = false，
// 核实不了按失败处理。关闭续费的成败不影响单据的成功状态和资金结论。
//
// 会话：单据成功时会话不清空，留给续费协程用；关掉了就清空。自动重试最多 3 次（间隔 1、3、10 分钟），用完转 failed 并告警，
// 会话再留到 SessionKeep（1 小时）给管理员手动重试，到点一律清空。
// 待人工的开通单在人工定论之前一直留着会话（审计 D-2）：管理员什么时候定论成功都还能关续费，否则用户账号留着自动续费、
// 下期扣的可能是我们复用中的卡。定论成功之后从定论时刻（resolved_at）起再按 SessionKeep 计时（审计 D-9）；定论失败当场清空。
const (
	renewalAutoRetries = 3
	defaultRenewalTick = 15 * time.Second
	defaultSessionKeep = time.Hour
	// reviewSessionKeep 是待人工单的会话最长保留时间（审计 D-2 后续）：管理员再怎么拖，30 天后也清掉，
	// 不让用户会话在库里无限期留着。到点后定论成功会记 skipped 并告警，由用户自己去关续费。
	reviewSessionKeep = 30 * 24 * time.Hour
)

// renewalRetryDelays 是第 n 次自动尝试失败后到下一次的间隔（n 从 1 起）。
var renewalRetryDelays = [renewalAutoRetries]time.Duration{time.Minute, 3 * time.Minute, 10 * time.Minute}

// wantsRenewalCancel 判断单据成功后要不要关闭续费：只有真实付款的订阅开通单。
func wantsRenewalCancel(o *Order) bool {
	return !o.DryRun && (o.Kind == KindSubscription || o.Kind == "")
}

// renewalLoop 是续费协程：每隔 RenewalTick 处理到期的关闭续费，并清空保留到点的会话。不占浏览器名额，不碰卡池。
func (s *Service) renewalLoop(ctx context.Context) {
	defer s.loops.Done()
	ticker := time.NewTicker(s.t.RenewalTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		// 先清到点的会话，再关续费：停机超过保留时限后重启，不能拿超期的会话再去调用一次
		safego.Run("关闭续费", func() {
			s.sweepSessions(ctx)
			s.renewalDue(ctx)
		})
	}
}

// renewalDue 逐张处理到期的待关闭续费单。
func (s *Service) renewalDue(ctx context.Context) {
	list, err := dueRenewals(ctx, s.now().Unix())
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[payengine] 读取待关闭续费的单据失败: %v", err)
		}
		return
	}
	for i := range list {
		if ctx.Err() != nil {
			return
		}
		s.renewOne(ctx, list[i].OrderNo)
	}
}

// renewOne 自动关闭一张单的续费。关闭时 panic：续费记 failed 并告警（recoverRenewal），下一轮就不会再拿到它反复出错。
func (s *Service) renewOne(ctx context.Context, no string) {
	defer func() {
		if r := recover(); r != nil {
			s.recoverRenewal(ctx, no, r)
		}
	}()
	if _, err := s.renewOnce(ctx, no, false); err != nil && ctx.Err() == nil &&
		!errors.Is(err, errRenewalBusy) && !errors.Is(err, ErrStateChanged) {
		log.Printf("[payengine] 单据 %s 关闭续费出错，下一轮再试: %v", no, err)
	}
}

// recoverRenewal 处理关闭续费时 panic 的单：续费从 pending 转 failed，停止自动重试，告警请人工重试或让用户自己关。
// 单据仍是成功，会话照常留到 SessionKeep 给手动重试。panic 的值可能带会话，只记类型和位置。
func (s *Service) recoverRenewal(ctx context.Context, no string, r any) {
	desc := safego.Describe(r)
	defer safego.RecoverNoAlert("付款单 " + no + " 关闭续费时内部错误后的收尾")
	log.Printf("[payengine] 单据 %s 关闭续费时内部错误（panic）：%s", no, desc)
	if ctx.Err() != nil {
		return
	}
	const msg = "关闭续费时内部错误，已停止自动重试"
	if err := moveRenewal(ctx, no, RenewalPending, RenewalFailed, map[string]any{"renewal_next_at": nil}, false, "error", msg); err != nil {
		log.Printf("[payengine] 单据 %s 关闭续费时内部错误，记失败也没成功，下一轮再试: %v", no, err)
		return
	}
	s.opts.Alert(fmt.Sprintf("付款单 %s 开通成功，但%s。单据仍是成功；请在 1 小时内到后台点「重试关闭续费」，或让用户自行在 ChatGPT 设置里关闭续费。", no, msg))
}

// errRenewalBusy 表示这张单正在关闭续费（续费协程和手动重试不同时做）。
var errRenewalBusy = errors.New("这张单正在关闭续费，请稍后再看结果")

func (s *Service) lockRenewal(no string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.renewing[no] {
		return false
	}
	s.renewing[no] = true
	return true
}

func (s *Service) unlockRenewal(no string) {
	s.mu.Lock()
	delete(s.renewing, no)
	s.mu.Unlock()
}

func (s *Service) renewalBusy(no string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renewing[no]
}

// RetryRenewal 是后台「重试关闭续费」：只对成功的真实开通单、续费还没关掉（待处理或失败）、会话还在的单可用。
// 同步做一次关闭并核实，返回处理后的单据；不能重试时返回 ErrRenewalUnavailable（说明原因）。不花钱，不碰卡池。
func (s *Service) RetryRenewal(ctx context.Context, no, by string) (*Order, error) {
	o, err := getOrder(ctx, no)
	if err != nil {
		return nil, err
	}
	switch {
	case o.State != StateSucceeded || !wantsRenewalCancel(o):
		return nil, fmt.Errorf("%w：只有成功的真实开通单才需要关闭续费", ErrRenewalUnavailable)
	case o.RenewalState == RenewalCancelled:
		return nil, fmt.Errorf("%w：续费已经关闭并核实过", ErrRenewalUnavailable)
	case o.RenewalState != RenewalPending && o.RenewalState != RenewalFailed:
		return nil, fmt.Errorf("%w：会话已清空，请让用户自行在 ChatGPT 设置里关闭续费", ErrRenewalUnavailable)
	case o.SessionSealed == "":
		return nil, fmt.Errorf("%w：会话已清空，请让用户自行在 ChatGPT 设置里关闭续费", ErrRenewalUnavailable)
	}
	if err := addEvent(ctx, no, "info", by+" 手动重试关闭续费", ""); err != nil {
		return nil, err
	}
	if _, err := s.renewOnce(ctx, no, true); err != nil {
		if errors.Is(err, errRenewalBusy) {
			return nil, fmt.Errorf("%w：%v", ErrRenewalUnavailable, err)
		}
		return nil, err
	}
	return getOrder(ctx, no)
}

// renewOnce 对一张单做一次关闭续费并核实，返回处理后的续费状态。manual 为 true 是管理员手动重试：失败直接记 failed，不排自动重试。
func (s *Service) renewOnce(ctx context.Context, no string, manual bool) (string, error) {
	if !s.lockRenewal(no) {
		return "", errRenewalBusy
	}
	defer s.unlockRenewal(no)
	o, err := getOrder(ctx, no) // 拿到锁之后重读：等锁之前那份可能已经过期
	if err != nil {
		return "", err
	}
	if o.State != StateSucceeded || !wantsRenewalCancel(o) {
		return o.RenewalState, nil
	}
	if o.RenewalState != RenewalPending && (!manual || o.RenewalState != RenewalFailed) {
		return o.RenewalState, nil
	}
	if o.SessionSealed == "" {
		// 会话已经清空（例如人工定论成功与会话到点清空同时发生）：关不了，记 skipped 并告警
		msg := "会话已清空，无法关闭续费，请让用户自行在 ChatGPT 设置里关闭续费"
		if err := moveRenewal(ctx, no, o.RenewalState, RenewalSkipped, map[string]any{"renewal_next_at": nil}, false, "warn", msg); err != nil {
			return "", err
		}
		s.opts.Alert(fmt.Sprintf("付款单 %s 开通成功，但%s。", no, msg))
		return RenewalSkipped, nil
	}

	j := &job{o: o}
	confirmed, detail := s.cancelRenewal(ctx, j)
	if ctx.Err() != nil {
		return "", ctx.Err() // 停机：不记这次，下次启动接着做
	}
	now := s.now().Unix()
	attempts := o.RenewalAttempts + 1
	set := map[string]any{"renewal_attempts": attempts, "renewal_checked_at": now, "renewal_next_at": nil}
	if confirmed {
		if j.accessUntil != nil {
			set["access_until"] = j.accessUntil.Unix()
		}
		// 关掉了：会话不再需要，立即清空
		if err := moveRenewal(ctx, no, o.RenewalState, RenewalCancelled, set, true, "info", "已关闭续费并核实：下期不再自动续费，本期权益不受影响"); err != nil {
			return "", err
		}
		return RenewalCancelled, nil
	}
	detail = truncate(j.scrub(detail), errorMessageLimit)
	if !manual && attempts <= renewalAutoRetries {
		delay := renewalRetryDelays[attempts-1]
		set["renewal_next_at"] = s.now().Add(delay).Unix()
		msg := fmt.Sprintf("关闭续费没有成功（第 %d 次），%d 分钟后自动重试：%s", attempts, int(delay/time.Minute), detail)
		if err := moveRenewal(ctx, no, o.RenewalState, RenewalPending, set, false, "warn", msg); err != nil {
			return "", err
		}
		return RenewalPending, nil
	}
	// 自动重试用完或手动重试失败：转 failed 等人工。会话留到 SessionKeep 给手动重试，到点由 sweepSessions 清空
	msg := "关闭续费失败：" + detail
	if !manual {
		msg = fmt.Sprintf("关闭续费失败，已自动重试 %d 次：%s", renewalAutoRetries, detail)
	}
	if err := moveRenewal(ctx, no, o.RenewalState, RenewalFailed, set, false, "error", msg); err != nil {
		return "", err
	}
	if !manual {
		s.opts.Alert(fmt.Sprintf("付款单 %s 开通成功，但%s。单据仍是成功；请在 1 小时内到后台点「重试关闭续费」，或让用户自行在 ChatGPT 设置里关闭续费。", no, msg))
	}
	return RenewalFailed, nil
}

// cancelRenewal 用单据里的会话关闭续费并核实。返回是否已核实不再续费；没核实时 detail 说明原因（未脱敏，调用方负责）。
// 重试和手动重试先读一次状态：上一次可能其实已经关掉了，那就不再发关闭请求。
func (s *Service) cancelRenewal(ctx context.Context, j *job) (bool, string) {
	sess, err := openSession(j.o.SessionSealed)
	if err != nil {
		return false, "会话密文无法解开"
	}
	j.useSession(sess)
	proxies, err := LoadProxies()
	if err != nil {
		return false, "读取地区代理失败：" + err.Error()
	}
	j.proxy = proxies[j.o.Region]
	gpt, err := s.opts.ChatGPT(j.proxy)
	if err != nil {
		return false, "创建 ChatGPT 客户端失败：" + err.Error()
	}
	if err := gpt.Refresh(ctx, &j.sess); err != nil {
		return false, "刷新会话失败：" + err.Error()
	}
	accountID := j.o.AccountID // 检查账号时记下的；成功的真实单一定走过检查
	if accountID == "" {
		return false, "单据没有记录账号 ID，无法关闭续费"
	}
	if j.o.RenewalAttempts > 0 || j.o.RenewalState == RenewalFailed {
		if off, _, until := s.renewalOff(ctx, gpt, j.sess, accountID); off {
			j.accessUntil = until
			return true, ""
		}
	}
	cancelErr := gpt.CancelRenewal(ctx, j.sess, accountID)
	// 请求出错也去核实：请求可能已经生效，只是响应没回来
	off, why, until := s.renewalOff(ctx, gpt, j.sess, accountID)
	if off {
		j.accessUntil = until
		return true, ""
	}
	if cancelErr != nil {
		return false, "关闭续费请求失败：" + cancelErr.Error()
	}
	return false, why
}

// renewalOff 核实账号是否已经不再续费。优先读 GET /subscriptions 的 will_renew；没有这个字段（或查询出错）时读 accounts/check 里的 will_renew。
// 只有明确读到 false 才算关掉；读到 true、读不到都不算。until 只在走订阅接口判定关闭时带出（active_until），走账号接口时为 nil。
func (s *Service) renewalOff(ctx context.Context, gpt ChatGPT, sess chatgpt.Session, accountID string) (off bool, why string, until *time.Time) {
	info, err := gpt.SubscriptionStatus(ctx, sess, accountID)
	if err == nil && info.WillRenew != nil {
		if !*info.WillRenew {
			return true, "", info.ActiveUntil
		}
		return false, "核实时订阅仍会续费（will_renew = true）", nil
	}
	why = "核实不了续费状态：订阅接口没有返回 will_renew"
	if err != nil {
		why = "核实不了续费状态：订阅接口出错：" + err.Error()
	}
	acc, aerr := gpt.Account(ctx, sess)
	if aerr != nil || acc.WillRenew == nil {
		return false, why, nil
	}
	if !*acc.WillRenew {
		return true, "", nil // 账号接口没有到期时间
	}
	return false, "核实时订阅仍会续费（账号接口 will_renew = true）", nil
}

// sweepSessions 清空保留到点的会话：终态单据结束满 SessionKeep 还留着会话的，一律清空。
// 人工定论过的单从定论时刻算，待人工的单不清（定论之前不知道要不要关续费）。
// 续费还在待处理的单转 failed 并告警：关闭续费到点还没结论，只能让用户自己关。
func (s *Service) sweepSessions(ctx context.Context) {
	s.sweepReviewSessions(ctx)
	cutoff := s.now().Add(-s.t.SessionKeep).Unix()
	list, err := expiredSessions(ctx, cutoff)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[payengine] 读取到点的会话失败: %v", err)
		}
		return
	}
	for i := range list {
		o := &list[i]
		if ctx.Err() != nil {
			return
		}
		if s.renewalBusy(o.OrderNo) {
			continue // 正在关闭续费：下一轮再清
		}
		if o.State == StateSucceeded && o.RenewalState == RenewalPending {
			msg := "会话保留已满 1 小时，关闭续费仍没有结论，会话已清空；请让用户自行在 ChatGPT 设置里关闭续费"
			if err := moveRenewal(ctx, o.OrderNo, RenewalPending, RenewalFailed, map[string]any{"renewal_next_at": nil}, true, "error", msg); err != nil {
				if ctx.Err() == nil && !errors.Is(err, ErrStateChanged) {
					log.Printf("[payengine] 单据 %s 清空会话失败: %v", o.OrderNo, err)
				}
				continue
			}
			s.opts.Alert(fmt.Sprintf("付款单 %s 开通成功，但%s。", o.OrderNo, msg))
			continue
		}
		if err := clearExpiredSession(ctx, o.OrderNo, cutoff); err != nil && ctx.Err() == nil {
			log.Printf("[payengine] 单据 %s 清空会话失败: %v", o.OrderNo, err)
		}
	}
}

// sweepReviewSessions 清掉待人工满 reviewSessionKeep（30 天）的单据的会话，单据本身仍待人工。
// 每张单只记一行日志（单号，不含会话和邮箱），再追加一条时间线事件。已人工定论的单不在这里处理，仍按 SessionKeep 从定论时刻计时。
func (s *Service) sweepReviewSessions(ctx context.Context) {
	cutoff := s.now().Add(-reviewSessionKeep).Unix()
	rows, err := db.DB.QueryContext(ctx, `SELECT order_no FROM pay_orders
		WHERE state = ? AND session_sealed != '' AND resolved_at IS NULL AND COALESCE(finished_at, created_at) <= ? ORDER BY id`,
		StateNeedsReview, cutoff)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[payengine] 读取待人工超期的会话失败: %v", err)
		}
		return
	}
	var nos []string
	for rows.Next() {
		var no string
		if rows.Scan(&no) == nil {
			nos = append(nos, no)
		}
	}
	rows.Close()
	for _, no := range nos {
		if ctx.Err() != nil {
			return
		}
		now := time.Now().UTC().Unix()
		tx, err := db.DB.BeginTx(ctx, nil)
		if err != nil {
			continue
		}
		res, err := tx.ExecContext(ctx, `UPDATE pay_orders SET session_sealed = '', updated_at = ?
			WHERE order_no = ? AND state = ? AND session_sealed != '' AND resolved_at IS NULL AND COALESCE(finished_at, created_at) <= ?`,
			now, no, StateNeedsReview, cutoff)
		if err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				_, err = tx.ExecContext(ctx, `INSERT INTO pay_order_events (order_no, at, state, level, message)
					VALUES (?, ?, ?, 'info', ?)`, no, now, StateNeedsReview, "待人工已满 30 天，会话已清空；之后定论成功将无法自动关闭续费")
				if err == nil && tx.Commit() == nil {
					log.Printf("[payengine] 单据 %s 待人工已满 30 天，已清空会话", no)
				}
				continue
			}
		}
		tx.Rollback()
	}
}

// dueRenewals 返回续费待处理且到了处理时刻的成功单（按 id 升序）。会话已清空的也返回，由 renewOnce 记成 skipped。
func dueRenewals(ctx context.Context, now int64) ([]Order, error) {
	rows, err := db.DB.QueryContext(ctx, `SELECT `+orderColumns+` FROM pay_orders
		WHERE state = ? AND renewal_state = ? AND (renewal_next_at IS NULL OR renewal_next_at <= ?) ORDER BY id`,
		StateSucceeded, RenewalPending, now)
	if err != nil {
		return nil, err
	}
	return collectOrders(rows)
}

// sessionKeepFrom 是会话保留的起算时刻：人工定论过的单从定论时刻算，其余从结束时刻算。
const sessionKeepFrom = `COALESCE(resolved_at, finished_at)`

// expiredSessions 返回已经结束、保留起算时刻不晚于 cutoff、还留着会话的终态单据。待人工的单不在其中：会话要留到人工定论。
func expiredSessions(ctx context.Context, cutoff int64) ([]Order, error) {
	rows, err := db.DB.QueryContext(ctx, `SELECT `+orderColumns+` FROM pay_orders
		WHERE session_sealed != '' AND finished_at IS NOT NULL AND `+sessionKeepFrom+` <= ? AND state IN (?, ?, ?) ORDER BY id`,
		cutoff, StateSucceeded, StateFailed, StatePreviewed)
	if err != nil {
		return nil, err
	}
	return collectOrders(rows)
}

// clearExpiredSession 清空一张到点单据的会话，记一条事件。续费待处理的单不在这里清（见 sweepSessions）。
// 条件在同一条 UPDATE 里重查：读列表之后单据刚被人工定论的，起算时刻已经变了，不会被清；待人工的单一律不清。
func clearExpiredSession(ctx context.Context, no string, cutoff int64) error {
	now := time.Now().UTC().Unix()
	tx, err := db.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE pay_orders SET session_sealed = '', updated_at = ?
		WHERE order_no = ? AND session_sealed != '' AND `+sessionKeepFrom+` <= ? AND renewal_state != ? AND state != ?`,
		now, no, cutoff, RenewalPending, StateNeedsReview)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pay_order_events (order_no, at, state, level, message)
		SELECT order_no, ?, state, 'info', ? FROM pay_orders WHERE order_no = ?`, now, "会话保留已满 1 小时，已清空", no); err != nil {
		return err
	}
	return tx.Commit()
}

// moveRenewal 条件更新续费进度：仅当单据仍是 succeeded、renewal_state 仍是 from 时生效，同时写一条事件；clear 为 true 时清空会话。
func moveRenewal(ctx context.Context, no, from, to string, set map[string]any, clear bool, level, msg string) error {
	keys := make([]string, 0, len(set))
	for k := range set {
		if !settableColumns[k] || k == "renewal_state" {
			return fmt.Errorf("%w: 不允许更新列 %q", ErrBadRequest, k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	now := time.Now().UTC().Unix()
	sqlText := "UPDATE pay_orders SET renewal_state = ?, updated_at = ?"
	args := []any{to, now}
	if clear {
		sqlText += ", session_sealed = ''"
	}
	for _, k := range keys {
		sqlText += ", " + k + " = ?"
		args = append(args, set[k])
	}
	sqlText += " WHERE order_no = ? AND state = ? AND renewal_state = ?"
	args = append(args, no, StateSucceeded, from)
	tx, err := db.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, sqlText, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var cur string
		switch err := tx.QueryRowContext(ctx, `SELECT renewal_state FROM pay_orders WHERE order_no = ?`, no).Scan(&cur); {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return err
		}
		return fmt.Errorf("%w: 续费状态当前为 %q，期望 %q", ErrStateChanged, cur, from)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pay_order_events (order_no, at, state, level, message) VALUES (?, ?, ?, ?, ?)`,
		no, now, StateSucceeded, level, msg); err != nil {
		return err
	}
	return tx.Commit()
}
