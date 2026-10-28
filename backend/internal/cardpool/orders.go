package cardpool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// applyParams 为一张开卡单的参数。
type applyParams struct {
	HolderID, ProductID, Currency, NamePrefix, Expiry, By string
	AmountCents                                           int64
}

// queryResult 为查单或 Webhook 给出的结论。State: processing / success / failed；空表示没有定论。
type queryResult struct{ State, CardID, TxnID, Reason string }

var backoffSteps = [...]time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second, 120 * time.Second}

const backoffMax = 300 * time.Second

// backoff 返回第 attempt 次（从 0 起）查单之后的等待间隔：10s、30s、60s、120s，之后每次 300s。
func backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt < len(backoffSteps) {
		return backoffSteps[attempt]
	}
	return backoffMax
}

func secs(d time.Duration) int64 { return int64(d / time.Second) }

// expiryMMyy 返回 now（UTC）往后推 months 个月的 MMyy。
func expiryMMyy(now time.Time, months int) string {
	return now.UTC().AddDate(0, months, 0).Format("0106")
}

// 允许的状态转换（终态永不改写）。
var (
	toTerminalFrom   = []string{StatePending, StateProcessing, StateUnknown, StateNeedsReview}
	toProcessingFrom = []string{StatePending, StateUnknown, StateNeedsReview}
)

// 改状态单允许的目标状态。
var cardStatusTargets = map[string]bool{"ACTIVE": true, "REVOKED": true, "CANCELED": true}

var kindNames = map[string]string{
	KindApply:    "开卡单",
	KindRecharge: "转入单",
	KindWithdraw: "转出单",
	KindStatus:   "改状态单",
}

// describeOrder 返回用于告警与日志的单据描述（不含卡号）。
func describeOrder(o *Order) string {
	name := kindNames[o.Kind]
	if name == "" {
		name = "单据"
	}
	var parts []string
	if o.CardID != "" {
		parts = append(parts, "卡 "+o.CardID)
	}
	if o.Kind == KindApply && o.ProductID != "" {
		parts = append(parts, "产品 "+o.ProductID)
	}
	if o.Kind == KindRecharge || o.Kind == KindWithdraw {
		if amt, err := marswallex.FormatAmount(o.AmountCents); err == nil {
			parts = append(parts, "金额 "+amt+" "+o.Currency)
		}
	}
	if o.Kind == KindStatus {
		parts = append(parts, "目标 "+o.TargetStatus)
	}
	if len(parts) == 0 {
		return name + " " + o.OutOrderNo
	}
	return name + " " + o.OutOrderNo + "（" + strings.Join(parts, "，") + "）"
}

func normStatus(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?, ", n-1) + "?"
}

// orderSet 收集单据 UPDATE 的 SET 子句，第一项总是 state。
type orderSet struct {
	state string
	exprs []string
	args  []any
}

func newOrderSet(state string) *orderSet {
	return &orderSet{state: state, exprs: []string{"state = ?"}, args: []any{state}}
}

func (u *orderSet) set(col string, v any) *orderSet { return u.expr(col+" = ?", v) }

func (u *orderSet) expr(e string, args ...any) *orderSet {
	u.exprs = append(u.exprs, e)
	u.args = append(u.args, args...)
	return u
}

// sideEffect 为与单据转换在同一事务里执行的附加操作。
type sideEffect func(ctx context.Context, tx *sql.Tx, o *Order, now int64) error

// transition 在一个事务里把单据从 from 中的某个状态改为 u.state。
// 只有本次确实完成了转换（RowsAffected == 1）才执行副作用：进入终态的通用副作用，以及 extra；
// 副作用失败则整体回滚，留待下一次定论重做。没有完成转换时（别处已经改过）返回最新单据，
// 若两边都是终态且不同则告警（key conflict:<单号>），保留先到的结果。
func (s *Service) transition(ctx context.Context, no string, from []string, u *orderSet, extra sideEffect) (*Order, bool, error) {
	now := s.now().Unix()
	cancelAfterDays := DefaultPolicy().CancelAfterDays
	if u.state == StateSuccess {
		// 事务外读取：测试库只有一个连接，事务里不能再用 db.DB
		p, err := LoadPolicy()
		if err != nil {
			log.Printf("[cardpool] 读取卡池策略失败，按默认值处理: %v", err)
		}
		cancelAfterDays = p.CancelAfterDays
	}

	tx, err := db.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	args := append([]any{}, u.args...)
	args = append(args, now, no)
	for _, f := range from {
		args = append(args, f)
	}
	q := `UPDATE mw_card_orders SET ` + strings.Join(u.exprs, ", ") + `, updated_at = ?
		WHERE out_order_no = ? AND state IN (` + placeholders(len(from)) + `)`
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return nil, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if n == 1 {
		var o Order
		if err := o.scan(tx.QueryRowContext(ctx, `SELECT `+orderCols+` FROM mw_card_orders WHERE out_order_no = ?`, no)); err != nil {
			return nil, false, err
		}
		if IsTerminal(o.State) {
			if err := terminalEffects(ctx, tx, &o, now, cancelAfterDays); err != nil {
				return nil, false, err
			}
		}
		if extra != nil {
			if err := extra(ctx, tx, &o, now); err != nil {
				return nil, false, err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return &o, true, nil
	}

	// 先释放连接再读最新单据
	_ = tx.Rollback()
	cur, err := getOrder(ctx, no)
	if err != nil {
		return nil, false, err
	}
	if IsTerminal(u.state) && IsTerminal(cur.State) && cur.State != u.state {
		s.alert("conflict:"+no, fmt.Sprintf("MarsWallex %s 定论冲突：已是 %s，又得到 %s，保留先到的结果，请人工核对",
			describeOrder(cur), cur.State, u.state))
	}
	return cur, false, nil
}

// terminalEffects 为单据进入终态时对卡的副作用。卡不存在不算错误。
func terminalEffects(ctx context.Context, tx *sql.Tx, o *Order, now int64, cancelAfterDays int) error {
	switch o.Kind {
	case KindApply:
		if o.State != StateSuccess || o.CardID == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO mw_cards (card_id, card_holder_id, product_id, currency,
			source, pool_state, next_action_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'opened', ?, ?, ?, ?)`,
			o.CardID, o.CardHolderID, o.ProductID, o.Currency, PoolReleasing, now, now, now)
		return err
	case KindRecharge, KindWithdraw:
		_, err := tx.ExecContext(ctx, `UPDATE mw_cards SET next_action_at = ?, updated_at = ? WHERE card_id = ?`, now, now, o.CardID)
		return err
	case KindStatus:
		if o.State != StateSuccess {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE mw_cards SET platform_status = ?, updated_at = ? WHERE card_id = ?`,
			o.TargetStatus, now, o.CardID); err != nil {
			return err
		}
		switch o.TargetStatus {
		case "CANCELED":
			_, err := tx.ExecContext(ctx, `UPDATE mw_cards SET pool_state = ? WHERE card_id = ?`, PoolRetired, o.CardID)
			return err
		case "REVOKED":
			// 回收流程冻结 used 的卡：记冻结时间与注销时间
			if _, err := tx.ExecContext(ctx, `UPDATE mw_cards SET frozen_at = ?, cancel_after = ?
				WHERE card_id = ? AND pool_state = ? AND frozen_at IS NULL`,
				now, now+int64(cancelAfterDays)*86400, o.CardID, PoolUsed); err != nil {
				return err
			}
			// 管理员冻结可用的卡：停用，不再参与选卡与补卡计数；解冻成功不自动启用，由管理员「启用」
			_, err := tx.ExecContext(ctx, `UPDATE mw_cards SET pool_state = ?, disabled_reason = ? WHERE card_id = ? AND pool_state = ?`,
				PoolDisabled, "管理员冻结", o.CardID, PoolAvailable)
			return err
		}
	}
	return nil
}

// isInflightConflict 判断 INSERT 是否撞上 uq_mw_orders_card_inflight（同卡已有在途的转入或转出单）。
func isInflightConflict(err error) bool {
	var se sqlite3.Error
	if !errors.As(err, &se) || se.ExtendedCode != sqlite3.ErrConstraintUnique {
		return false
	}
	return strings.Contains(se.Error(), "mw_card_orders.card_id")
}

// insertPending 写入一条 pending 单据，补全单号、状态、时间与自增 ID。
func (s *Service) insertPending(ctx context.Context, o *Order) error {
	now := s.now()
	o.OutOrderNo = newOutOrderNo(o.Kind, now)
	o.State = StatePending
	o.CreatedAt, o.UpdatedAt = now.Unix(), now.Unix()
	res, err := db.DB.ExecContext(ctx, `INSERT INTO mw_card_orders (out_order_no, kind, card_id, card_holder_id, product_id,
		amount_cents, currency, target_status, state, requested_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.OutOrderNo, o.Kind, o.CardID, o.CardHolderID, o.ProductID, o.AmountCents, o.Currency, o.TargetStatus,
		o.State, o.RequestedBy, o.CreatedAt, o.UpdatedAt)
	if err != nil {
		if isInflightConflict(err) {
			return fmt.Errorf("%w: 卡 %s 已有在途的转入或转出单", ErrCardBusy, o.CardID)
		}
		return err
	}
	o.ID, err = res.LastInsertId()
	return err
}

// sendVerdict 为一次发送结果的初判。
type sendVerdict struct{ state, txnID, lastErr string }

// classifySendErr 按 §6.2 把发送出错映射为单据状态；everUnknown 为该单据此前是否出现过结果未知。
// 出现过未知的单据，被拒或未发出都不能说明此前那次没有执行，仍为 unknown。
func classifySendErr(err error, everUnknown bool) sendVerdict {
	switch marswallex.OutcomeOf(err) {
	case marswallex.OutcomeRejected:
		if !everUnknown {
			return sendVerdict{state: StateFailed, lastErr: err.Error()}
		}
	case marswallex.OutcomeNotSent:
		if !everUnknown {
			return sendVerdict{state: StateNotSent, lastErr: err.Error()}
		}
	}
	return sendVerdict{state: StateUnknown, lastErr: err.Error()}
}

// transferState 把转入、转出的平台状态映射为单据状态；不认识的状态返回空。
func transferState(status string) string {
	switch normStatus(status) {
	case "APPLY":
		return StateProcessing
	case "SUCCESS":
		return StateSuccess
	case "FAIL":
		return StateFailed
	}
	return ""
}

// transferVerdict 为转入、转出成功响应的初判：状态不是 APPLY / SUCCESS / FAIL 时按未知处理。
func transferVerdict(t *marswallex.CardBalanceTransaction) sendVerdict {
	switch st := transferState(t.Status); st {
	case StateFailed:
		return sendVerdict{state: st, txnID: t.TransactionID, lastErr: "平台返回 FAIL"}
	case "":
		return sendVerdict{state: StateUnknown, txnID: t.TransactionID, lastErr: fmt.Sprintf("平台返回未知的转账状态 %q", t.Status)}
	default:
		return sendVerdict{state: st, txnID: t.TransactionID}
	}
}

// recordFirstSend 记录第一次发送的结果。只更新仍为 pending 的单据：Webhook 已经先定论时丢弃发送结果。
// 除 not_sent 外都写入 first_sent_at 与 attempts = 1；unknown 与 processing 在 backoff(0) 后开始查单。
func (s *Service) recordFirstSend(ctx context.Context, no string, v sendVerdict) (*Order, error) {
	now := s.now().Unix()
	u := newOrderSet(v.state)
	if v.txnID != "" {
		u.set("platform_txn_id", v.txnID)
	}
	if v.lastErr != "" {
		u.set("last_error", v.lastErr)
	}
	if v.state != StateNotSent {
		u.set("first_sent_at", now).set("attempts", 1)
	}
	switch v.state {
	case StateUnknown:
		u.set("ever_unknown", 1).set("next_check_at", now+secs(backoff(0)))
	case StateProcessing:
		u.set("next_check_at", now+secs(backoff(0)))
	}
	if IsTerminal(v.state) {
		u.set("finished_at", now)
	}
	o, applied, err := s.transition(ctx, no, []string{StatePending}, u, nil)
	if err != nil {
		return nil, err
	}
	if !applied {
		log.Printf("[cardpool] 单据 %s 的发送结果（%s）已丢弃：单据已是 %s", no, v.state, o.State)
	}
	return o, nil
}

// abortIfCanceled 在写入 pending 之后、发送之前检查 ctx：已取消时请求确定没有发出，单据记为 not_sent，返回 ctx 的错误。
// 记录失败时单据留在 pending，2 分钟后按 unknown 处理（偏保守）。
func (s *Service) abortIfCanceled(ctx context.Context, no string) error {
	cerr := ctx.Err()
	if cerr == nil {
		return nil
	}
	if _, err := s.recordFirstSend(context.WithoutCancel(ctx), no,
		sendVerdict{state: StateNotSent, lastErr: "发送前已取消: " + cerr.Error()}); err != nil {
		return errors.Join(cerr, err)
	}
	return cerr
}

// submitApply 提交一张开卡单。写入 pending 后发送，按 §6.2 初判；结果写在返回的单据里。
func (s *Service) submitApply(ctx context.Context, p applyParams) (*Order, error) {
	if p.AmountCents < 0 {
		return nil, badRequest("开卡金额不能为负数")
	}
	if p.HolderID == "" || p.ProductID == "" || p.Currency == "" {
		return nil, badRequest("开卡缺少持卡人、卡产品或币种")
	}
	amount, err := marswallex.FormatAmount(p.AmountCents)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	// 先取客户端：配置错误不留下 pending 单据
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	o := &Order{Kind: KindApply, CardHolderID: p.HolderID, ProductID: p.ProductID, AmountCents: p.AmountCents,
		Currency: p.Currency, RequestedBy: p.By}
	if err := s.insertPending(ctx, o); err != nil {
		return nil, err
	}
	if err := s.abortIfCanceled(ctx, o.OutOrderNo); err != nil {
		return nil, err
	}
	res, err := c.ApplyCard(ctx, marswallex.ApplyCardRequest{
		OutOrderNo:   o.OutOrderNo,
		ProductID:    p.ProductID,
		Amount:       amount,
		Currency:     p.Currency,
		CardHolderID: p.HolderID,
		CardName:     p.NamePrefix + fmt.Sprintf("%06d", o.ID%1000000),
		ExpiryDate:   p.Expiry,
	})
	var v sendVerdict
	if err != nil {
		v = classifySendErr(err, false)
	} else {
		v = sendVerdict{state: StateProcessing, txnID: res.TransactionID}
	}
	// 请求已经发出，记录结果不受调用方取消影响
	return s.recordFirstSend(context.WithoutCancel(ctx), o.OutOrderNo, v)
}

// submitTransfer 提交一笔转入（recharge）或转出（withdraw）。同卡已有在途的转入或转出单时返回 ErrCardBusy，不发请求。
func (s *Service) submitTransfer(ctx context.Context, kind, cardID string, amountCents int64, currency, by string) (*Order, error) {
	if kind != KindRecharge && kind != KindWithdraw {
		return nil, badRequest("单据类型只能是转入或转出")
	}
	if cardID == "" {
		return nil, badRequest("卡 ID 不能为空")
	}
	if amountCents <= 0 {
		return nil, badRequest("金额必须大于 0")
	}
	if currency == "" {
		return nil, badRequest("币种不能为空")
	}
	amount, err := marswallex.FormatAmount(amountCents)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	o := &Order{Kind: kind, CardID: cardID, AmountCents: amountCents, Currency: currency, RequestedBy: by}
	if err := s.insertPending(ctx, o); err != nil {
		return nil, err
	}
	if err := s.abortIfCanceled(ctx, o.OutOrderNo); err != nil {
		return nil, err
	}
	req := marswallex.CardTransferRequest{OutOrderNo: o.OutOrderNo, CardID: cardID, Currency: currency, Amount: amount}
	var res *marswallex.CardBalanceTransaction
	if kind == KindRecharge {
		res, err = c.RechargeCard(ctx, req)
	} else {
		res, err = c.WithdrawCard(ctx, req)
	}
	var v sendVerdict
	if err != nil {
		v = classifySendErr(err, false)
	} else {
		v = transferVerdict(res)
	}
	return s.recordFirstSend(context.WithoutCancel(ctx), o.OutOrderNo, v)
}

// submitStatus 提交一张改状态单（ACTIVE / REVOKED / CANCELED）。注销不传 account_no，余额回到默认主钱包。
func (s *Service) submitStatus(ctx context.Context, cardID, target, by string) (*Order, error) {
	target = normStatus(target)
	if !cardStatusTargets[target] {
		return nil, badRequest("目标状态只能是 ACTIVE、REVOKED 或 CANCELED")
	}
	if cardID == "" {
		return nil, badRequest("卡 ID 不能为空")
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	o := &Order{Kind: KindStatus, CardID: cardID, TargetStatus: target, RequestedBy: by}
	if err := s.insertPending(ctx, o); err != nil {
		return nil, err
	}
	if err := s.abortIfCanceled(ctx, o.OutOrderNo); err != nil {
		return nil, err
	}
	_, err = c.UpdateCardStatus(ctx, marswallex.UpdateCardStatusRequest{CardID: cardID, CardStatus: target})
	v := sendVerdict{state: StateSuccess}
	if err != nil {
		v = classifySendErr(err, false)
	}
	return s.recordFirstSend(context.WithoutCancel(ctx), o.OutOrderNo, v)
}

// finalize 是 Webhook、对账查单和人工再查共用的定论函数。
// 终态不改写，processing 不覆盖终态；开卡成功但没有 card_id 按 processing 处理；
// 副作用只在本次确实完成转换时执行，重复推送不会重复执行。r.State 为空时不做任何改动。
func (s *Service) finalize(ctx context.Context, outOrderNo string, r queryResult, source string) (*Order, error) {
	o, err := getOrder(ctx, outOrderNo)
	if err != nil {
		return nil, err
	}
	state := r.State
	if state == StateSuccess && o.Kind == KindApply && r.CardID == "" {
		state = StateProcessing
	}
	if state == "" {
		return o, nil
	}
	now := s.now().Unix()
	u := newOrderSet(state)
	if r.TxnID != "" {
		u.set("platform_txn_id", r.TxnID)
	}
	var from []string
	switch state {
	case StateProcessing:
		// 从 pending（发送结果还没回来）或 needs_review（平台说仍在处理）进入时，保证会被继续查单
		from = toProcessingFrom
		u.expr("first_sent_at = COALESCE(first_sent_at, ?)", now).
			expr("next_check_at = COALESCE(next_check_at, ?)", now+secs(backoff(0))).
			expr("attempts = MAX(attempts, 1)")
	case StateSuccess:
		from = toTerminalFrom
		if o.Kind == KindApply {
			u.set("card_id", r.CardID)
		}
		u.set("finished_at", now)
	case StateFailed:
		from = toTerminalFrom
		reason := strings.TrimSpace(r.Reason)
		if reason == "" {
			reason = "平台返回失败"
		}
		u.set("last_error", reason).set("finished_at", now)
	default:
		return nil, fmt.Errorf("未知的定论状态 %q", r.State)
	}
	got, applied, err := s.transition(ctx, outOrderNo, from, u, nil)
	if err != nil {
		return nil, err
	}
	if applied {
		log.Printf("[cardpool] %s → %s（来源 %s）", describeOrder(got), state, source)
	}
	return got, nil
}

// Recheck 立即查一次单（只接受 unknown / processing / needs_review），有定论就定论。不会重发任何请求。
func (s *Service) Recheck(ctx context.Context, outOrderNo string) (*Order, error) {
	o, err := getOrder(ctx, outOrderNo)
	if err != nil {
		return nil, err
	}
	switch o.State {
	case StateUnknown, StateProcessing, StateNeedsReview:
	default:
		return nil, badRequest("只有结果未知、处理中或待人工核对的单据可以再查一次")
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	r, _, qerr := s.queryOrder(ctx, c, o)
	if qerr != nil {
		return nil, fmt.Errorf("查单失败: %w", qerr)
	}
	return s.finalize(ctx, outOrderNo, r, "recheck")
}

// ConfirmFailed 由管理员在 MarsWallex 后台核对后，把 needs_review 的单据确认为失败。备注必填。
func (s *Service) ConfirmFailed(ctx context.Context, outOrderNo, note, by string) (*Order, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return nil, badRequest("确认失败必须填写备注")
	}
	o, err := getOrder(ctx, outOrderNo)
	if err != nil {
		return nil, err
	}
	if o.State != StateNeedsReview {
		return nil, badRequest("只有待人工核对的单据可以确认失败")
	}
	u := newOrderSet(StateFailed).set("note", note).set("finished_at", s.now().Unix())
	got, applied, err := s.transition(ctx, outOrderNo, []string{StateNeedsReview}, u,
		func(ctx context.Context, tx *sql.Tx, o *Order, now int64) error {
			if o.CardID == "" {
				return nil
			}
			_, err := tx.ExecContext(ctx, `UPDATE mw_cards SET next_action_at = ?, updated_at = ? WHERE card_id = ?`, now, now, o.CardID)
			return err
		})
	if err != nil {
		return nil, err
	}
	if !applied {
		return nil, badRequest("单据状态已变为 " + got.State + "，不能确认失败")
	}
	log.Printf("[cardpool] %s 由 %s 人工确认失败", describeOrder(got), by)
	return got, nil
}

// WaitOrder 每 500ms 查一次单据，直到 success / failed / not_sent / needs_review，或 ctx 结束（返回 ctx.Err()）。
func (s *Service) WaitOrder(ctx context.Context, outOrderNo string) (*Order, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		o, err := getOrder(ctx, outOrderNo)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			return nil, err
		}
		switch o.State {
		case StateSuccess, StateFailed, StateNotSent, StateNeedsReview:
			return o, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
