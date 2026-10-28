package cardpool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

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

// advanceReleasing：刷新 → 平台已注销则退役、已冻结则停用 → 有可用余额就转出 →
// 只剩冻结余额就 10 分钟后再查 → 都为 0 回到 available。
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
		moved, err := s.moveCard(ctx, c.CardID, PoolReleasing, PoolDisabled, "平台冻结")
		if err != nil {
			return err
		}
		if moved {
			s.alert("extfreeze:"+c.CardID, fmt.Sprintf("卡 %s 在清零时发现平台状态为 REVOKED（已被冻结），已从卡池停用（原因：平台冻结），请到 MarsWallex 后台核查", c.CardID))
		}
		return nil
	}
	if fresh.AvailableCents > 0 {
		return s.recycleWithdraw(ctx, fresh)
	}
	if fresh.FrozenCents > 0 {
		at := s.now().Unix() + recycleRetrySeconds
		return s.setNextAction(ctx, c.CardID, PoolReleasing, &at)
	}
	if _, err := s.moveCard(ctx, c.CardID, PoolReleasing, PoolAvailable, ""); err != nil {
		return err
	}
	return nil
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

// refreshForRecycle 刷新卡详情。出错时 next_action_at = now + 60 并返回错误；
// 刷新期间卡的池状态已经变化时返回 (nil, nil)，留给下一轮按新状态处理。
func (s *Service) refreshForRecycle(ctx context.Context, c *Card) (*Card, error) {
	fresh, err := s.RefreshCard(ctx, c.CardID)
	if err != nil {
		at := s.now().Unix() + refreshRetrySeconds
		if uerr := s.setNextAction(ctx, c.CardID, c.PoolState, &at); uerr != nil {
			return nil, uerr
		}
		return nil, fmt.Errorf("刷新卡详情失败，1 分钟后重试: %w", err)
	}
	if fresh.PoolState != c.PoolState {
		return nil, nil
	}
	return fresh, nil
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
	q := `UPDATE mw_cards SET pool_state = ?, next_action_at = NULL, updated_at = ?`
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
