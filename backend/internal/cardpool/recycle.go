package cardpool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

const (
	recycleBatch        = 50  // 每轮最多推进的卡数
	recycleRetrySeconds = 600 // 冻结余额未清或某一步失败，10 分钟后再试
	refreshRetrySeconds = 60  // 刷新卡详情出错，1 分钟后再试
	recycleBy           = "recycle"
)

var poolStateNames = map[string]string{
	PoolReleasing: "清零",
	PoolUsed:      "清零、冻结、注销",
}

// advanceCardsOnce 推进一轮回收（§6.4），每轮最多 50 张到期（next_action_at 为空或已到）的 releasing / used 卡。
// 已冻结、等待注销的卡把 next_action_at 设为 cancel_after，到点即到期；被限流的卡（next_action_at = 失败时间 + 600）
// 即使已过 cancel_after 也要等到 next_action_at 才再处理。
//
// 卡上有非终态单据（含 needs_review）时整张跳过，由单据定论把卡推回到期。
// 单张卡出错不影响其他卡，返回第一个错误。
func (s *Service) advanceCardsOnce(ctx context.Context) error {
	now := s.now().Unix()
	rows, err := db.DB.QueryContext(ctx, `SELECT `+cardCols+` FROM mw_cards c
		WHERE c.pool_state IN (?, ?) AND (c.next_action_at IS NULL OR c.next_action_at <= ?)
		AND NOT EXISTS (SELECT 1 FROM mw_card_orders o WHERE o.card_id = c.card_id AND o.state IN (?, ?, ?, ?))
		ORDER BY COALESCE(c.next_action_at, 0), c.card_id LIMIT ?`,
		PoolReleasing, PoolUsed, now,
		StatePending, StateProcessing, StateUnknown, StateNeedsReview, recycleBatch)
	if err != nil {
		return err
	}
	var cards []Card
	for rows.Next() {
		var c Card
		if err := c.scan(rows); err != nil {
			rows.Close()
			return err
		}
		cards = append(cards, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(cards) == 0 {
		return nil
	}

	pol, perr := LoadPolicy()
	if perr != nil {
		log.Printf("[cardpool] 读取卡池策略失败，回收按默认值处理: %v", perr)
	}
	var firstErr error
	for i := range cards {
		if err := ctx.Err(); err != nil {
			return err
		}
		c := &cards[i]
		var err error
		switch {
		case c.PoolState == PoolReleasing:
			err = s.advanceReleasing(ctx, c)
		case c.FrozenAt == nil:
			err = s.advanceUsed(ctx, c, pol)
		default:
			err = s.advanceFrozen(ctx, c)
		}
		if err != nil {
			log.Printf("[cardpool] 回收卡 %s 出错: %v", c.CardID, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// advanceReleasing：刷新 → 平台已注销则退役、已冻结则停用（本系统冻结的写「管理员冻结」、不告警，见 frozenByUs；
// 其余写「平台冻结」并按外部冻结告警）→ 有可用余额就转出 → 只剩冻结余额就 10 分钟后再查 → 都为 0 回到 available。
func (s *Service) advanceReleasing(ctx context.Context, c *Card) error {
	fresh, err := s.refreshForRecycle(ctx, c)
	if err != nil || fresh == nil {
		return err
	}
	switch fresh.PlatformStatus {
	case "CANCELED":
		if _, err := s.moveCard(ctx, c.CardID, PoolReleasing, PoolRetired, ""); err != nil {
			return err
		}
		log.Printf("[cardpool] 卡 %s 已在平台注销，退役", c.CardID)
		return nil
	case "REVOKED":
		ours, err := s.frozenByUs(ctx, c.CardID)
		if err != nil {
			return err
		}
		if ours {
			// 管理员在回收中冻结了这张卡：和可用卡被管理员冻结一样停用，不当成外部冻结告警
			if _, err := s.moveCard(ctx, c.CardID, PoolReleasing, PoolDisabled, "管理员冻结"); err != nil {
				return err
			}
			log.Printf("[cardpool] 卡 %s 回收中被本系统冻结，停用", c.CardID)
			return nil
		}
		moved, err := s.moveCard(ctx, c.CardID, PoolReleasing, PoolDisabled, "平台冻结")
		if err != nil {
			return err
		}
		if moved {
			s.alert("extfreeze:"+c.CardID, extFreezeDisabledText(c.CardID, sourceRecycle))
		}
		return nil
	}
	if fresh.FrozenCents == 0 {
		if err := s.clearFrozenSince(ctx, c.CardID); err != nil {
			return err
		}
	}
	if fresh.AvailableCents > 0 {
		return s.recycleWithdraw(ctx, fresh)
	}
	if fresh.FrozenCents > 0 {
		return s.waitFrozenBalance(ctx, fresh)
	}
	if _, err := s.moveCard(ctx, c.CardID, PoolReleasing, PoolAvailable, ""); err != nil {
		return err
	}
	return nil
}

// frozenByUs 判断卡现在的冻结是不是本系统发起的：这张卡最近一张未失败（在途或已成功）的改状态单目标是 REVOKED。
// 不用 ourStatusOrder 的 600 秒窗口：回收中的卡只剩冻结余额或被限流时 10 分钟后才再查，冻结单成功可能早已超过 600 秒。
// 之后本系统又解冻过（最近一张是 ACTIVE）就不算，再出现 REVOKED 按外部冻结处理。
func (s *Service) frozenByUs(ctx context.Context, cardID string) (bool, error) {
	var target string
	err := db.DB.QueryRowContext(ctx, `SELECT target_status FROM mw_card_orders
		WHERE kind = ? AND card_id = ? AND state IN (?, ?, ?, ?, ?)
		ORDER BY id DESC LIMIT 1`,
		KindStatus, cardID, StatePending, StateProcessing, StateUnknown, StateNeedsReview, StateSuccess).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return target == "REVOKED", nil
}

// 只剩冻结余额的卡（MO-05）：一笔授权长期不结算也不撤销时，卡上的钱冻着，回收每 10 分钟查一次。
// 超过 frozenAlertAfter 每天告警一次，超过 frozenDisableAfter 转 disabled 并告警，让人工到平台核查。
var (
	frozenAlertAfter   = 24 * time.Hour
	frozenDisableAfter = 7 * 24 * time.Hour
)

// waitFrozenBalance 处理 releasing 卡「可用余额为 0、冻结余额大于 0」：记下开始时间、按时长告警或停用，其余照旧 10 分钟后再查。
func (s *Service) waitFrozenBalance(ctx context.Context, c *Card) error {
	now := s.now().Unix()
	// 只在第一次写入：之后每轮刷新都不改它，所以它是真实的「开始只剩冻结余额」时间
	if _, err := db.DB.ExecContext(ctx, `UPDATE mw_cards SET frozen_since = ? WHERE card_id = ? AND frozen_since IS NULL`, now, c.CardID); err != nil {
		return err
	}
	var since int64
	if err := db.DB.QueryRowContext(ctx, `SELECT COALESCE(frozen_since, ?) FROM mw_cards WHERE card_id = ?`, now, c.CardID).Scan(&since); err != nil {
		return err
	}
	age := time.Duration(now-since) * time.Second
	switch {
	case age > frozenDisableAfter:
		moved, err := s.moveCard(ctx, c.CardID, PoolReleasing, PoolDisabled, "冻结余额长期未释放")
		if err != nil {
			return err
		}
		if moved {
			s.alert("frozen-disabled:"+c.CardID, fmt.Sprintf("卡 %s 只剩冻结余额 %s %s 已超过 %d 天，已从卡池停用（原因：冻结余额长期未释放），请到 MarsWallex 后台核查授权或争议",
				c.CardID, centsText(c.FrozenCents), c.Currency, int(frozenDisableAfter/(24*time.Hour))))
		}
		return nil
	case age > frozenAlertAfter:
		s.alert("frozen:"+c.CardID, fmt.Sprintf("卡 %s 只剩冻结余额 %s %s 已超过 %d 小时，回收一直在等待；超过 %d 天会自动停用，请到 MarsWallex 后台核查授权或争议",
			c.CardID, centsText(c.FrozenCents), c.Currency, int(frozenAlertAfter/time.Hour), int(frozenDisableAfter/(24*time.Hour))))
	}
	at := now + recycleRetrySeconds
	return s.setNextAction(ctx, c.CardID, PoolReleasing, &at)
}

func (s *Service) clearFrozenSince(ctx context.Context, cardID string) error {
	_, err := db.DB.ExecContext(ctx, `UPDATE mw_cards SET frozen_since = NULL WHERE card_id = ? AND frozen_since IS NOT NULL`, cardID)
	return err
}

// advanceUsed（未冻结）：刷新 → 平台已注销则退役 → 平台已是 REVOKED 就直接记冻结时间（不再尝试转出：
// 冻结的卡可能不让转出，注销时余额会退回主钱包）→ 有可用余额就转出（冻结余额不用等）→ 否则提交冻结单。
func (s *Service) advanceUsed(ctx context.Context, c *Card, pol Policy) error {
	fresh, err := s.refreshForRecycle(ctx, c)
	if err != nil || fresh == nil {
		return err
	}
	if fresh.FrozenAt != nil {
		return nil // 刷新期间已被冻结单定论，下一轮按已冻结处理
	}
	if fresh.PlatformStatus == "CANCELED" {
		if _, err := s.moveCard(ctx, c.CardID, PoolUsed, PoolRetired, ""); err != nil {
			return err
		}
		log.Printf("[cardpool] 卡 %s 已在平台注销，退役", c.CardID)
		return nil
	}
	if fresh.PlatformStatus == "REVOKED" {
		now := s.now().Unix()
		cancelAfter := now + int64(pol.CancelAfterDays)*86400
		_, err := db.DB.ExecContext(ctx, `UPDATE mw_cards SET frozen_at = ?, cancel_after = ?, next_action_at = ?, updated_at = ?
			WHERE card_id = ? AND pool_state = ? AND frozen_at IS NULL`,
			now, cancelAfter, cancelAfter, now, c.CardID, PoolUsed)
		return err
	}
	if fresh.AvailableCents > 0 {
		return s.recycleWithdraw(ctx, fresh)
	}
	return s.recycleStatus(ctx, fresh, "REVOKED")
}

// advanceFrozen（已冻结的 used）：未到 cancel_after 就等到 cancel_after；到了先刷新（出错 1 分钟后再试），
// 平台已注销则直接退役，否则提交注销单。
func (s *Service) advanceFrozen(ctx context.Context, c *Card) error {
	if c.CancelAfter != nil && s.now().Unix() < *c.CancelAfter {
		return s.setNextAction(ctx, c.CardID, PoolUsed, c.CancelAfter)
	}
	fresh, err := s.refreshForRecycle(ctx, c)
	if err != nil || fresh == nil {
		return err
	}
	if fresh.PlatformStatus == "CANCELED" {
		if _, err := s.moveCard(ctx, c.CardID, PoolUsed, PoolRetired, ""); err != nil {
			return err
		}
		log.Printf("[cardpool] 卡 %s 已在平台注销，退役", c.CardID)
		return nil
	}
	return s.recycleStatus(ctx, fresh, "CANCELED")
}

// refreshForRecycle 刷新卡详情。出错时 next_action_at = now + 60 并返回错误，连续失败满 30 分钟告警（见 noteRefreshFailure）；
// 刷新成功就清掉失败计时。刷新期间卡的池状态已经变化时返回 (nil, nil)，留给下一轮按新状态处理。
func (s *Service) refreshForRecycle(ctx context.Context, c *Card) (*Card, error) {
	fresh, err := s.refreshCard(ctx, c.CardID, sourceRecycle)
	if err != nil {
		at := s.now().Unix() + refreshRetrySeconds
		if uerr := s.setNextAction(ctx, c.CardID, c.PoolState, &at); uerr != nil {
			return nil, uerr
		}
		s.noteRefreshFailure(c, err)
		return nil, fmt.Errorf("刷新卡详情失败，1 分钟后重试: %w", err)
	}
	s.refreshFailMu.Lock()
	delete(s.refreshFailSince, c.CardID)
	s.refreshFailMu.Unlock()
	if fresh.PoolState != c.PoolState {
		return nil, nil
	}
	return fresh, nil
}

// refreshFailAlertAfter：回收中刷新卡详情连续失败多久才告警。平台偶尔抖一下不告警，卡一直卡住才提醒。
const refreshFailAlertAfter = 30 * time.Minute

// noteRefreshFailure 记下卡 c 回收时刷新卡详情失败：第一次失败记时间（只在内存里，进程重启从头计时），
// 连续失败满 refreshFailAlertAfter 告警（key recycle-refresh:<卡>，每卡每个北京日一条；不和 recycle:<卡> 共用，免得互相压掉）。
// 告警文字只带 safeErr 摘要，不带平台返回的 message。
func (s *Service) noteRefreshFailure(c *Card, err error) {
	now := s.now().Unix()
	s.refreshFailMu.Lock()
	since, ok := s.refreshFailSince[c.CardID]
	if !ok {
		since = now
		s.refreshFailSince[c.CardID] = now
	}
	s.refreshFailMu.Unlock()
	age := time.Duration(now-since) * time.Second
	if age < refreshFailAlertAfter {
		return
	}
	s.alert("recycle-refresh:"+c.CardID, fmt.Sprintf("卡 %s 回收（%s）时刷新卡详情已连续失败 %d 分钟，卡停在 %s，每分钟重试一次；请到 MarsWallex 后台核查这张卡。最后错误：%s",
		c.CardID, poolStateNames[c.PoolState], int(age/time.Minute), c.PoolState, truncate(safeErr(err), 200)))
}

// recycleWithdraw 把卡上的可用余额全部转出。
func (s *Service) recycleWithdraw(ctx context.Context, c *Card) error {
	if throttled, err := s.throttled(ctx, c, KindWithdraw); err != nil || throttled {
		return err
	}
	o, err := s.submitTransfer(ctx, KindWithdraw, c.CardID, c.AvailableCents, c.Currency, recycleBy)
	return s.afterRecycleSubmit(ctx, c, o, err)
}

// recycleStatus 提交冻结（REVOKED）或注销（CANCELED）单。
func (s *Service) recycleStatus(ctx context.Context, c *Card, target string) error {
	if throttled, err := s.throttled(ctx, c, KindStatus); err != nil || throttled {
		return err
	}
	o, err := s.submitStatus(ctx, c.CardID, target, recycleBy)
	return s.afterRecycleSubmit(ctx, c, o, err)
}

// throttled 查看该卡最近一笔 kind 类型的单据：失败或未发出、且定论不满 10 分钟时，
// 把 next_action_at 设为定论时间 + 600、告警（key recycle:<卡>），返回 true。
func (s *Service) throttled(ctx context.Context, c *Card, kind string) (bool, error) {
	var o Order
	err := o.scan(db.DB.QueryRowContext(ctx, `SELECT `+orderCols+` FROM mw_card_orders
		WHERE card_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`, c.CardID, kind))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if o.State != StateFailed && o.State != StateNotSent {
		return false, nil
	}
	at := o.UpdatedAt
	if o.FinishedAt != nil {
		at = *o.FinishedAt
	}
	if at <= s.now().Unix()-recycleRetrySeconds {
		return false, nil
	}
	next := at + recycleRetrySeconds
	if err := s.setNextAction(ctx, c.CardID, c.PoolState, &next); err != nil {
		return false, err
	}
	s.alertRecycleFailed(c, &o)
	return true, nil
}

// afterRecycleSubmit 处理回收单据的提交结果：
//   - 当场失败或未发出：10 分钟后再试并告警；
//   - 已有在途的转入或转出单（并发提交）：不动，等那张单据定论；
//   - 其他提交错误：10 分钟后再试并告警；
//   - 其余（在途或已成功）：next_action_at 置空，由单据定论推动下一步（在途期间整张卡被跳过）。
func (s *Service) afterRecycleSubmit(ctx context.Context, c *Card, o *Order, err error) error {
	now := s.now().Unix()
	if err != nil {
		if errors.Is(err, ErrCardBusy) {
			return nil
		}
		next := now + recycleRetrySeconds
		if uerr := s.setNextAction(ctx, c.CardID, c.PoolState, &next); uerr != nil {
			return errors.Join(err, uerr)
		}
		s.alert("recycle:"+c.CardID, fmt.Sprintf("卡 %s 回收（%s）提交单据出错，10 分钟后重试：%s",
			c.CardID, poolStateNames[c.PoolState], truncate(err.Error(), 200)))
		return err
	}
	if o.State == StateFailed || o.State == StateNotSent {
		next := now + recycleRetrySeconds
		if o.FinishedAt != nil {
			next = *o.FinishedAt + recycleRetrySeconds
		}
		if err := s.setNextAction(ctx, c.CardID, c.PoolState, &next); err != nil {
			return err
		}
		s.alertRecycleFailed(c, o)
		return nil
	}
	return s.setNextAction(ctx, c.CardID, c.PoolState, nil)
}

func (s *Service) alertRecycleFailed(c *Card, o *Order) {
	msg := fmt.Sprintf("卡 %s 回收（%s）这一步失败：%s 为 %s，卡停在 %s，10 分钟后重试",
		c.CardID, poolStateNames[c.PoolState], describeOrder(o), o.State, c.PoolState)
	if o.LastError != "" {
		msg += "。最后错误：" + truncate(o.LastError, 200)
	}
	s.alert("recycle:"+c.CardID, msg)
}

// setNextAction 设置 next_action_at（nil 为置空），只对仍处于 state 的卡生效。
func (s *Service) setNextAction(ctx context.Context, cardID, state string, at *int64) error {
	_, err := db.DB.ExecContext(ctx, `UPDATE mw_cards SET next_action_at = ?, updated_at = ? WHERE card_id = ? AND pool_state = ?`,
		at, s.now().Unix(), cardID, state)
	return err
}

// moveCard 把卡从 from 改为 to 并清空 next_action_at；reason 非空时写入 disabled_reason。返回是否完成了转换。
func (s *Service) moveCard(ctx context.Context, cardID, from, to, reason string) (bool, error) {
	q := `UPDATE mw_cards SET pool_state = ?, next_action_at = NULL, frozen_since = NULL, updated_at = ?`
	args := []any{to, s.now().Unix()}
	if reason != "" {
		q += `, disabled_reason = ?`
		args = append(args, reason)
	}
	q += ` WHERE card_id = ? AND pool_state = ?`
	args = append(args, cardID, from)
	res, err := db.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// retireExpiringOnce 处理有效期不足或无法识别的可用卡（只读写本地库，条件更新）：
//   - 有效期还剩不到一个月（含已过期）：转 used，退役原因「即将过期」，随后按 used 流程清零、冻结、注销，补卡补上缺口；
//   - 有效期无法识别：停用（原因「有效期无法识别」），不自动注销，由管理员核对。
//
// 各自告警（key expiring:<卡> / badexpiry:<卡>，每卡每天一次）。租用中等其他状态的卡不动。
func (s *Service) retireExpiringOnce(ctx context.Context) error {
	rows, err := db.DB.QueryContext(ctx, `SELECT card_id, expiry FROM mw_cards WHERE pool_state = ? ORDER BY card_id`, PoolAvailable)
	if err != nil {
		return err
	}
	type avail struct{ cardID, expiry string }
	var list []avail
	for rows.Next() {
		var a avail
		if err := rows.Scan(&a.cardID, &a.expiry); err != nil {
			rows.Close()
			return err
		}
		list = append(list, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	now := s.now()
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, a := range list {
		if err := ctx.Err(); err != nil {
			return err
		}
		if expiryEligible(a.expiry, now) {
			continue
		}
		if _, ok := expiryEnd(a.expiry); !ok {
			moved, err := s.moveCard(ctx, a.cardID, PoolAvailable, PoolDisabled, "有效期无法识别")
			keep(err)
			if moved {
				s.alert("badexpiry:"+a.cardID, fmt.Sprintf("卡 %s 的有效期 %q 无法识别，已从卡池停用（原因：有效期无法识别），请到 MarsWallex 后台核对",
					a.cardID, truncate(a.expiry, 20)))
			}
			continue
		}
		res, err := db.DB.ExecContext(ctx, `UPDATE mw_cards SET pool_state = ?,
			retire_reason = CASE WHEN retire_reason = '' THEN ? ELSE retire_reason END, next_action_at = ?, updated_at = ?
			WHERE card_id = ? AND pool_state = ?`,
			PoolUsed, "即将过期", now.Unix(), now.Unix(), a.cardID, PoolAvailable)
		if err != nil {
			keep(err)
			continue
		}
		if n, _ := res.RowsAffected(); n == 1 {
			s.alert("expiring:"+a.cardID, fmt.Sprintf("卡 %s 有效期 %s 已不足一个月，已转为已用（退役原因：即将过期），将清零、冻结后注销",
				a.cardID, a.expiry))
		}
	}
	return firstErr
}
