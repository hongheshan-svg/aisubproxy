package cardpool

import (
	"context"
	"errors"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// 定时查询：没有配置 Webhook（通知地址）时，外部冻结、注销和持卡人审核结果收不到推送，改为定时向平台查询，告警与推送一致。
// 只读平台、只改本地状态，不发任何资金单或改状态单；改本地状态一律用条件更新，与推送先后发现同一变化时，后到的什么都不做。

// cardStatusPollInterval 是定时查询卡状态与持卡人状态的间隔；包级变量，测试可以改短。
var cardStatusPollInterval = 10 * time.Minute

// statusPollBatch 是每轮最多查询的卡数、持卡人数。
const statusPollBatch = 50

// statusPollRound 是定时查询的一轮：先查卡，再查持卡人。MarsWallex 未就绪时整轮跳过。
func (s *Service) statusPollRound(ctx context.Context, lg *loopLog) {
	_, err := s.opts.Client()
	lg.readiness("定时查询卡与持卡人状态", err)
	if err != nil {
		return
	}
	lg.check("定时查询卡状态", s.pollCardStatusOnce(ctx, lg))
	if ctx.Err() != nil {
		return
	}
	lg.check("定时查询持卡人状态", s.pollCardholdersOnce(ctx, lg))
}

// pollCardStatusOnce 查询一轮卡的平台状态：可用与租用中的卡，每轮最多 50 张，最久没查过的（从没查过的最先）优先。
// 单张卡出错只按循环的规则记日志，不影响其他卡；返回的错误只表示取不到客户端、读卡表失败或 ctx 已结束。
func (s *Service) pollCardStatusOnce(ctx context.Context, lg *loopLog) error {
	client, err := s.opts.Client()
	if err != nil {
		return err
	}
	rows, err := db.DB.QueryContext(ctx, `SELECT `+cardCols+` FROM mw_cards c WHERE c.pool_state IN (?, ?)
		ORDER BY COALESCE(c.status_polled_at, 0), c.card_id LIMIT ?`, PoolAvailable, PoolLeased, statusPollBatch)
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
	for i := range cards {
		if err := ctx.Err(); err != nil {
			return err
		}
		lg.check("定时查询卡 "+cards[i].CardID+" 的状态", s.pollCard(ctx, client, &cards[i]))
	}
	return nil
}

// pollCard 查一张卡的平台状态并按需处理：
//   - 只用 GetCard 读状态，不写余额（租用中的卡可能正在注资，写余额会和注资定论互相覆盖），卡详情里的其他字段（含完整卡号）一律不用；
//   - 查完先重读本地的卡（查询期间租约可能已结束、推送可能已处理），之后都以重读的为准：已不是可用、租用中的就不管（回收等流程自己处理）；
//   - 平台状态与本地 platform_status 相同就什么都不做（推送已经处理过的也在这里止步）；
//   - 有我们发起、未定论或刚成功的同目标改状态单（见 ourStatusOrder），留给对账定论；
//   - 变为 REVOKED、CANCELED 按外部变更处理，与推送是同一个 applyExternalStatus；变为其他值只更新平台状态，不告警。
//
// 不论查询成败都记下查询时间（status_polled_at），查不到的卡也排到后面，不会一直占着每轮的名额。
// 返回的错误不含平台返回的 message。
func (s *Service) pollCard(ctx context.Context, client *marswallex.Client, c *Card) error {
	d, err := client.GetCard(ctx, c.CardID)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	// 条件更新、只动这一列：查询时间只往后走
	now := s.now().Unix()
	if _, uerr := db.DB.ExecContext(ctx, `UPDATE mw_cards SET status_polled_at = ? WHERE card_id = ? AND (status_polled_at IS NULL OR status_polled_at < ?)`,
		now, c.CardID, now); uerr != nil {
		return uerr
	}
	if err != nil {
		return errors.New(safeErr(redactPAN(err)))
	}
	status := normStatus(d.Status)
	if status == "" {
		return nil
	}
	// 重读：本轮开头读到的卡可能已过时（例如租约结束进了回收），不能拿旧的池状态、持有方去处理和告警
	c, err = getCard(ctx, c.CardID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.PoolState != PoolAvailable && c.PoolState != PoolLeased {
		return nil
	}
	if status == normStatus(c.PlatformStatus) {
		return nil
	}
	o, err := s.ourStatusOrder(ctx, c.CardID, status)
	if err != nil || o != nil {
		return err
	}
	if status == "REVOKED" || status == "CANCELED" {
		_, err = s.applyExternalStatus(ctx, c, status, sourcePoll)
		return err
	}
	// 条件更新：读取之后平台状态被推送改过，就以推送为准
	_, err = db.DB.ExecContext(ctx, `UPDATE mw_cards SET platform_status = ?, updated_at = ? WHERE card_id = ? AND platform_status = ?`,
		status, now, c.CardID, c.PlatformStatus)
	return err
}

// pollCardholdersOnce 查询一轮本地已导入的持卡人：状态不是 DISABLED 的（含 PENDING_REVIEW，以及可能被风控改成 SUSPENDED 的 APPROVED），
// 每轮最多 50 个，synced_at 最旧的优先。走与后台「刷新持卡人」同一条同步逻辑（refreshCardholder），状态变化按推送同样的规则告警。
// 本地只有提交记录、还没拿到 card_holder_id 的不在此列。单个出错只记日志（不带平台 message，可能回显资料），不影响其他，
// 并记下查询时间（synced_at），排到后面。
func (s *Service) pollCardholdersOnce(ctx context.Context, lg *loopLog) error {
	if _, err := s.opts.Client(); err != nil {
		return err
	}
	rows, err := db.DB.QueryContext(ctx, `SELECT card_holder_id FROM mw_cardholders WHERE status <> ?
		ORDER BY COALESCE(synced_at, 0), card_holder_id LIMIT ?`, holderDisabled, statusPollBatch)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := s.refreshCardholder(ctx, id, sourcePoll)
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			lg.check("定时查询持卡人 "+id, errors.New(safeErr(err)))
			// 查询失败也记下查询时间（条件更新、只动这一列），一直失败的不会永远排在最前、把其他持卡人饿死
			now := s.now().Unix()
			if _, uerr := db.DB.ExecContext(ctx, `UPDATE mw_cardholders SET synced_at = ? WHERE card_holder_id = ? AND (synced_at IS NULL OR synced_at < ?)`,
				now, id, now); uerr != nil {
				return uerr
			}
		}
	}
	return nil
}
