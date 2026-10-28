package cardpool

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

const (
	pendingStaleSeconds    = 120   // pending 超过 2 分钟转 unknown
	unknownReviewSeconds   = 1800  // unknown 满 30 分钟转 needs_review
	processingAlertSeconds = 86400 // processing 满 24 小时告警
	reconcileBatch         = 50    // 每轮最多查单数
)

func scanOrders(rows *sql.Rows) ([]Order, error) {
	defer rows.Close()
	var out []Order
	for rows.Next() {
		var o Order
		if err := o.scan(rows); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// reconcileOrdersOnce 执行一轮单据对账：
//  1. pending 超过 2 分钟 → unknown（进程可能在发送途中崩溃，绝不当成未发出）；
//  2. 到期的 unknown / processing 逐条查单（每轮最多 50 条），资金单只查不重发；
//  3. unknown 满 30 分钟 → needs_review 并告警；
//  4. processing 满 24 小时告警一次。
//
// 只有第 2 步要调接口。取不到客户端时照样执行 1、3、4（只读写本地库），并在有未定论单据或待回收卡时
// 每个北京日告警一次（key clientdown）；客户端不可用本身不作为错误返回（由告警与 maintainRound 的就绪日志反映）。
func (s *Service) reconcileOrdersOnce(ctx context.Context) error {
	c, cerr := s.opts.Client()
	if err := s.markStalePending(ctx); err != nil {
		return err
	}
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if cerr == nil {
		keep(s.checkDueOrders(ctx, c))
	} else {
		keep(s.alertClientDown(ctx, cerr))
	}
	keep(s.escalateUnknown(ctx))
	keep(s.alertStaleProcessing(ctx))
	return firstErr
}

// alertClientDown 在取不到客户端、且有未定论单据或待回收（releasing / used）卡时告警，每个北京日一次（key clientdown）。
// cerr 为客户端配置错误，不含密钥。
func (s *Service) alertClientDown(ctx context.Context, cerr error) error {
	var orders, cards int
	if err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mw_card_orders WHERE state IN (?, ?, ?, ?)`,
		StatePending, StateProcessing, StateUnknown, StateNeedsReview).Scan(&orders); err != nil {
		return err
	}
	if err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mw_cards WHERE pool_state IN (?, ?)`,
		PoolReleasing, PoolUsed).Scan(&cards); err != nil {
		return err
	}
	if orders == 0 && cards == 0 {
		return nil
	}
	s.alert("clientdown", fmt.Sprintf("MarsWallex 客户端不可用：%s，有 %d 张未定论单据 / %d 张待回收卡，无法查单与回收",
		truncate(cerr.Error(), 200), orders, cards))
	return nil
}

// markStalePending 把创建超过 2 分钟仍为 pending 的单据转为 unknown，按创建时间计 30 分钟。
func (s *Service) markStalePending(ctx context.Context) error {
	now := s.now().Unix()
	res, err := db.DB.ExecContext(ctx, `UPDATE mw_card_orders SET state = ?, ever_unknown = 1,
		first_sent_at = COALESCE(first_sent_at, created_at), next_check_at = ?,
		last_error = CASE WHEN last_error = '' THEN ? ELSE last_error END, updated_at = ?
		WHERE state = ? AND created_at <= ?`,
		StateUnknown, now, "发送途中中断，结果未知", now, StatePending, now-pendingStaleSeconds)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("[cardpool] %d 张单据 pending 超过 2 分钟，已转为 unknown", n)
	}
	return nil
}

// checkDueOrders 逐条查到期的 unknown / processing 单据。单条出错不影响其他单据，返回第一个错误。
func (s *Service) checkDueOrders(ctx context.Context, c *marswallex.Client) error {
	now := s.now().Unix()
	rows, err := db.DB.QueryContext(ctx, `SELECT `+orderCols+` FROM mw_card_orders
		WHERE state IN (?, ?) AND (next_check_at IS NULL OR next_check_at <= ?)
		ORDER BY next_check_at, id LIMIT ?`, StateUnknown, StateProcessing, now, reconcileBatch)
	if err != nil {
		return err
	}
	orders, err := scanOrders(rows)
	if err != nil {
		return err
	}
	var firstErr error
	for i := range orders {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.checkOrder(ctx, c, &orders[i]); err != nil {
			log.Printf("[cardpool] 对账 %s 出错: %v", orders[i].OutOrderNo, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// checkOrder 查一次单：attempts++，next_check_at = now + backoff(attempts-1)；有定论就定论。
// 改状态单查到卡的确定状态（ACTIVE / REVOKED / CANCELED）与目标不一致、且单据仍为 unknown 时，用同一张单据重发。资金单从不重发。
func (s *Service) checkOrder(ctx context.Context, c *marswallex.Client, o *Order) error {
	r, mismatch, qerr := s.queryOrder(ctx, c, o)
	if qerr != nil {
		log.Printf("[cardpool] 查单 %s 没有结果: %v", o.OutOrderNo, qerr)
	}
	now := s.now().Unix()
	if _, err := db.DB.ExecContext(ctx, `UPDATE mw_card_orders SET attempts = attempts + 1, next_check_at = ?, updated_at = ?
		WHERE out_order_no = ? AND state = ?`,
		now+secs(backoff(o.Attempts)), now, o.OutOrderNo, o.State); err != nil {
		return err
	}
	if r.State != "" {
		_, err := s.finalize(ctx, o.OutOrderNo, r, "reconcile")
		return err
	}
	if mismatch && o.Kind == KindStatus && o.State == StateUnknown {
		return s.resendStatus(ctx, c, o)
	}
	return nil
}

// queryOrder 查一次单。返回的 r.State 为空表示没有定论（查单出错、查不到、状态不认识都算）。
// mismatch 只对改状态单有意义：查到了卡，平台状态是确定的 ACTIVE / REVOKED / CANCELED 之一，但不是目标状态。
// 注销中（INCANCELED）、空或不认识的状态既不定论也不重发，继续查单（30 分钟后照常转人工）。
func (s *Service) queryOrder(ctx context.Context, c *marswallex.Client, o *Order) (r queryResult, mismatch bool, err error) {
	switch o.Kind {
	case KindApply:
		res, err := c.GetCardApplyOrder(ctx, marswallex.OrderRef{OutOrderNo: o.OutOrderNo})
		if err != nil {
			return queryResult{}, false, err
		}
		return applyQueryResult(res), false, nil
	case KindRecharge, KindWithdraw:
		res, err := c.GetCardTransferOrder(ctx, marswallex.OrderRef{OutOrderNo: o.OutOrderNo})
		if err != nil {
			return queryResult{}, false, err
		}
		return transferQueryResult(res), false, nil
	case KindStatus:
		d, err := c.GetCard(ctx, o.CardID)
		if err != nil {
			return queryResult{}, false, err
		}
		st := normStatus(d.Status)
		if st == o.TargetStatus {
			return queryResult{State: StateSuccess}, false, nil
		}
		return queryResult{}, cardStatusTargets[st], nil
	}
	return queryResult{}, false, fmt.Errorf("未知的单据类型 %q", o.Kind)
}

// applyQueryResult 映射开卡单查单结果；SUCCESS 但没有 card_id 算没有定论。
func applyQueryResult(a *marswallex.CardApplyOrder) queryResult {
	switch normStatus(a.Status) {
	case "SUCCESS":
		if a.CardID == "" {
			return queryResult{}
		}
		return queryResult{State: StateSuccess, CardID: a.CardID, TxnID: a.TransactionID}
	case "FAILED":
		reason := a.FailureReason
		if reason == "" {
			reason = "平台返回 FAILED"
		}
		return queryResult{State: StateFailed, TxnID: a.TransactionID, Reason: reason}
	case "PROCESSING":
		return queryResult{State: StateProcessing, TxnID: a.TransactionID}
	}
	return queryResult{}
}

// transferQueryResult 映射转入、转出查单结果；状态不认识算没有定论。
func transferQueryResult(t *marswallex.CardBalanceTransaction) queryResult {
	st := transferState(t.Status)
	if st == "" {
		return queryResult{}
	}
	r := queryResult{State: st, TxnID: t.TransactionID}
	if st == StateFailed {
		r.Reason = "平台返回 FAIL"
	}
	return r
}

// resendStatus 用同一张改状态单重发（设置状态可重复执行）。结果只更新仍为读取时状态的单据，
// Webhook 已经定论时以 Webhook 为准。出现过未知的单据被拒或未发出，仍为 unknown。
func (s *Service) resendStatus(ctx context.Context, c *marswallex.Client, o *Order) error {
	log.Printf("[cardpool] %s：卡状态还不是目标状态，用同一张单据重发", describeOrder(o))
	_, err := c.UpdateCardStatus(ctx, marswallex.UpdateCardStatusRequest{CardID: o.CardID, CardStatus: o.TargetStatus})
	wctx := context.WithoutCancel(ctx)
	now := s.now().Unix()
	if err == nil {
		_, _, terr := s.transition(wctx, o.OutOrderNo, []string{o.State}, newOrderSet(StateSuccess).set("finished_at", now), nil)
		return terr
	}
	v := classifySendErr(err, o.EverUnknown)
	if IsTerminal(v.state) {
		u := newOrderSet(v.state).set("last_error", v.lastErr).set("finished_at", now)
		_, _, terr := s.transition(wctx, o.OutOrderNo, []string{o.State}, u, nil)
		return terr
	}
	_, uerr := db.DB.ExecContext(wctx, `UPDATE mw_card_orders SET last_error = ?, updated_at = ? WHERE out_order_no = ? AND state = ?`,
		v.lastErr, now, o.OutOrderNo, o.State)
	return uerr
}

// escalateUnknown 把 first_sent_at 之后满 30 分钟仍为 unknown 的单据转为 needs_review，并告警（key review:<单号>）。
func (s *Service) escalateUnknown(ctx context.Context) error {
	now := s.now().Unix()
	rows, err := db.DB.QueryContext(ctx, `SELECT `+orderCols+` FROM mw_card_orders
		WHERE state = ? AND COALESCE(first_sent_at, created_at) <= ? ORDER BY id`, StateUnknown, now-unknownReviewSeconds)
	if err != nil {
		return err
	}
	orders, err := scanOrders(rows)
	if err != nil {
		return err
	}
	for i := range orders {
		o := &orders[i]
		res, err := db.DB.ExecContext(ctx, `UPDATE mw_card_orders SET state = ?, updated_at = ? WHERE out_order_no = ? AND state = ?`,
			StateNeedsReview, now, o.OutOrderNo, StateUnknown)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			msg := fmt.Sprintf("MarsWallex %s 结果未知已满 30 分钟，已转人工核对：请到 MarsWallex 后台核对后，在卡池「确认失败」或「再查一次」", describeOrder(o))
			if o.LastError != "" {
				msg += "。最后错误：" + truncate(o.LastError, 200)
			}
			s.alert("review:"+o.OutOrderNo, msg)
		}
	}
	return nil
}

// alertStaleProcessing 对 first_sent_at 之后满 24 小时仍为 processing 的单据告警一次（alerted_stale 置 1），之后继续查单。
func (s *Service) alertStaleProcessing(ctx context.Context) error {
	now := s.now().Unix()
	rows, err := db.DB.QueryContext(ctx, `SELECT `+orderCols+` FROM mw_card_orders
		WHERE state = ? AND alerted_stale = 0 AND COALESCE(first_sent_at, created_at) <= ? ORDER BY id`,
		StateProcessing, now-processingAlertSeconds)
	if err != nil {
		return err
	}
	orders, err := scanOrders(rows)
	if err != nil {
		return err
	}
	for i := range orders {
		o := &orders[i]
		res, err := db.DB.ExecContext(ctx, `UPDATE mw_card_orders SET alerted_stale = 1, updated_at = ?
			WHERE out_order_no = ? AND state = ? AND alerted_stale = 0`, now, o.OutOrderNo, StateProcessing)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			s.alert("stale:"+o.OutOrderNo, fmt.Sprintf("MarsWallex %s 处理中已满 24 小时，仍在继续查单", describeOrder(o)))
		}
	}
	return nil
}

// truncate 按字符截断，避免告警过长。
func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
