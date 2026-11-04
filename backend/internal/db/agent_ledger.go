package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 代理商余额流水。流水只追加，没有修改或删除的路径。

const (
	LedgerPurchase = "purchase"
	LedgerAdjust   = "adjust"
	LedgerTopup    = "topup"
)

var (
	ErrInsufficientBalance = errors.New("余额不足")
	ErrLedgerRefConflict   = errors.New("外部单号已用于另一个代理商或另一个金额")
)

// InsufficientBalanceError 带上当前余额和需要的金额，方便提示还差多少。
type InsufficientBalanceError struct {
	BalanceCents, NeedCents int64
}

func (e *InsufficientBalanceError) Error() string {
	return fmt.Sprintf("余额不足：当前 %d 美分，需要 %d 美分", e.BalanceCents, e.NeedCents)
}

func (e *InsufficientBalanceError) Is(target error) bool { return target == ErrInsufficientBalance }

type LedgerEntry struct {
	ID, AgentID       int64
	Kind              string
	AmountCents       int64
	BalanceAfterCents int64
	OrderID           *int64
	ExtRef            string
	Operator, Note    string
	CreatedAt         int64
}

// applyAgentBalance 是全系统唯一修改 agents.balance_cents 的地方（迁移除外）。
// 在调用方给的事务里：条件更新余额（扣减时要求余额够；requireActive 时要求账号启用），再写一条带变动后余额的流水。
func applyAgentBalance(ctx context.Context, tx *sql.Tx, e LedgerEntry, requireActive bool, now int64) (LedgerEntry, error) {
	q := `UPDATE agents SET balance_cents = balance_cents + ?, updated_at = ? WHERE id = ? AND balance_cents + ? >= 0`
	if requireActive {
		q += ` AND status = 'active'`
	}
	res, err := tx.ExecContext(ctx, q, e.AmountCents, now, e.AgentID, e.AmountCents)
	if err != nil {
		return LedgerEntry{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return LedgerEntry{}, err
	}
	if n == 0 {
		var bal int64
		var status string
		err := tx.QueryRowContext(ctx, `SELECT balance_cents, status FROM agents WHERE id = ?`, e.AgentID).Scan(&bal, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return LedgerEntry{}, ErrAgentNotFound
		}
		if err != nil {
			return LedgerEntry{}, err
		}
		if requireActive && status != AgentActive {
			return LedgerEntry{}, ErrAgentDisabled
		}
		return LedgerEntry{}, &InsufficientBalanceError{BalanceCents: bal, NeedCents: -e.AmountCents}
	}
	if err := tx.QueryRowContext(ctx, `SELECT balance_cents FROM agents WHERE id = ?`, e.AgentID).Scan(&e.BalanceAfterCents); err != nil {
		return LedgerEntry{}, err
	}
	var orderID int64
	if e.OrderID != nil {
		orderID = *e.OrderID
	}
	e.CreatedAt = now
	ins, err := tx.ExecContext(ctx, `INSERT INTO agent_ledger (agent_id, kind, amount_cents, balance_after_cents, order_id, ext_ref, operator, note, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.AgentID, e.Kind, e.AmountCents, e.BalanceAfterCents, orderID, e.ExtRef, e.Operator, e.Note, now)
	if err != nil {
		return LedgerEntry{}, err
	}
	if e.ID, err = ins.LastInsertId(); err != nil {
		return LedgerEntry{}, err
	}
	return e, nil
}

const ledgerSelect = `SELECT id, agent_id, kind, amount_cents, balance_after_cents, order_id, ext_ref, operator, note, created_at FROM agent_ledger`

func scanLedger(r rowScanner) (LedgerEntry, error) {
	var e LedgerEntry
	var orderID int64
	if err := r.Scan(&e.ID, &e.AgentID, &e.Kind, &e.AmountCents, &e.BalanceAfterCents, &orderID, &e.ExtRef, &e.Operator, &e.Note, &e.CreatedAt); err != nil {
		return LedgerEntry{}, err
	}
	if orderID != 0 {
		e.OrderID = &orderID
	}
	return e, nil
}

// postExternalEntry 带外部单号的入账：同一个 (kind, ext_ref) 只入一次。
// 已有流水且代理商、金额一致就原样返回（replay = true），不一致返回 ErrLedgerRefConflict。
func postExternalEntry(ctx context.Context, e LedgerEntry, now time.Time) (LedgerEntry, bool, error) {
	// 并发撞上唯一索引时事务已回滚，再来一轮就会走到「已有流水」的分支；理论上最多两轮
	for attempt := 0; attempt < 3; attempt++ {
		got, replay, err := postExternalEntryOnce(ctx, e, now)
		if errors.Is(err, errRetryLedgerRef) {
			continue
		}
		return got, replay, err
	}
	return LedgerEntry{}, false, errors.New("入账重试次数过多")
}

var errRetryLedgerRef = errors.New("流水单号并发冲突，重试")

func postExternalEntryOnce(ctx context.Context, e LedgerEntry, now time.Time) (LedgerEntry, bool, error) {
	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return LedgerEntry{}, false, err
	}
	defer tx.Rollback()
	old, err := scanLedger(tx.QueryRowContext(ctx, ledgerSelect+` WHERE kind = ? AND ext_ref = ?`, e.Kind, e.ExtRef))
	switch {
	case err == nil:
		if old.AgentID != e.AgentID || old.AmountCents != e.AmountCents {
			return LedgerEntry{}, false, ErrLedgerRefConflict
		}
		return old, true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return LedgerEntry{}, false, err
	}
	got, err := applyAgentBalance(ctx, tx, e, false, now.Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return LedgerEntry{}, false, errRetryLedgerRef
		}
		return LedgerEntry{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return LedgerEntry{}, false, err
	}
	return got, false, nil
}

// AdjustAgentBalance 管理员手动调账，金额可正可负，原因必填。停用的代理商也能调。
// 同一个幂等键再次提交时返回第一次的流水（replay = true）。扣多了返回 *InsufficientBalanceError。
func AdjustAgentBalance(ctx context.Context, agentID, amountCents int64, idemKey, operator, note string, now time.Time) (LedgerEntry, bool, error) {
	if amountCents == 0 {
		return LedgerEntry{}, false, errors.New("调账金额不能为 0")
	}
	if strings.TrimSpace(note) == "" {
		return LedgerEntry{}, false, errors.New("调账原因不能为空")
	}
	if idemKey == "" {
		return LedgerEntry{}, false, errors.New("幂等键不能为空")
	}
	return postExternalEntry(ctx, LedgerEntry{
		AgentID: agentID, Kind: LedgerAdjust, AmountCents: amountCents,
		ExtRef: "admin:" + idemKey, Operator: operator, Note: note,
	}, now)
}

// CreditAgent 外部入账（充值）：金额为正，外部单号必填，kind 只能是 topup。
func CreditAgent(ctx context.Context, agentID int64, kind string, amountCents int64, extRef, operator, note string, now time.Time) (LedgerEntry, bool, error) {
	if kind != LedgerTopup {
		return LedgerEntry{}, false, fmt.Errorf("入账类型不合法: %q", kind)
	}
	if amountCents <= 0 {
		return LedgerEntry{}, false, errors.New("入账金额必须大于 0")
	}
	if extRef == "" {
		return LedgerEntry{}, false, errors.New("外部单号不能为空")
	}
	return postExternalEntry(ctx, LedgerEntry{
		AgentID: agentID, Kind: kind, AmountCents: amountCents,
		ExtRef: extRef, Operator: operator, Note: note,
	}, now)
}

// ListAgentLedger 列出 from <= created_at < to 的流水，id 降序；第二个返回值是范围内的总条数。
func ListAgentLedger(ctx context.Context, agentID, from, to int64, limit, offset int) ([]LedgerEntry, int, error) {
	var total int
	if err := DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_ledger WHERE agent_id = ? AND created_at >= ? AND created_at < ?`,
		agentID, from, to).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := DB.QueryContext(ctx, ledgerSelect+` WHERE agent_id = ? AND created_at >= ? AND created_at < ? ORDER BY id DESC LIMIT ? OFFSET ?`,
		agentID, from, to, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		e, err := scanLedger(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

type LedgerSummary struct {
	OpeningCents, ClosingCents, InCents, OutCents int64
}

// AgentLedgerSummary 期初是 from 之前最后一条流水的变动后余额（没有就是 0），
// 期末是范围内最后一条的变动后余额（范围内没有流水就等于期初）。收入、支出分别累加正数和负数的绝对值。
func AgentLedgerSummary(ctx context.Context, agentID, from, to int64) (LedgerSummary, error) {
	var s LedgerSummary
	err := DB.QueryRowContext(ctx, `SELECT balance_after_cents FROM agent_ledger WHERE agent_id = ? AND created_at < ? ORDER BY id DESC LIMIT 1`,
		agentID, from).Scan(&s.OpeningCents)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return LedgerSummary{}, err
	}
	s.ClosingCents = s.OpeningCents
	err = DB.QueryRowContext(ctx, `SELECT balance_after_cents FROM agent_ledger WHERE agent_id = ? AND created_at >= ? AND created_at < ? ORDER BY id DESC LIMIT 1`,
		agentID, from, to).Scan(&s.ClosingCents)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return LedgerSummary{}, err
	}
	err = DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN amount_cents > 0 THEN amount_cents ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN amount_cents < 0 THEN -amount_cents ELSE 0 END), 0)
		FROM agent_ledger WHERE agent_id = ? AND created_at >= ? AND created_at < ?`, agentID, from, to).Scan(&s.InCents, &s.OutCents)
	if err != nil {
		return LedgerSummary{}, err
	}
	return s, nil
}

type AgentBalanceMismatch struct {
	AgentID, BalanceCents, LedgerSumCents, LastAfterCents int64
}

// CheckAgentBalances 逐个代理商核对两个等式：余额 = 流水金额之和；余额 = 最后一条流水的变动后余额（没有流水时为 0）。
func CheckAgentBalances(ctx context.Context) ([]AgentBalanceMismatch, error) {
	rows, err := DB.QueryContext(ctx, `SELECT a.id, a.balance_cents, COALESCE(s.total, 0), COALESCE(l.balance_after_cents, 0)
FROM agents a
LEFT JOIN (SELECT agent_id, SUM(amount_cents) AS total, MAX(id) AS last_id FROM agent_ledger GROUP BY agent_id) s ON s.agent_id = a.id
LEFT JOIN agent_ledger l ON l.id = s.last_id
WHERE a.balance_cents <> COALESCE(s.total, 0) OR a.balance_cents <> COALESCE(l.balance_after_cents, 0)
ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentBalanceMismatch
	for rows.Next() {
		var m AgentBalanceMismatch
		if err := rows.Scan(&m.AgentID, &m.BalanceCents, &m.LedgerSumCents, &m.LastAfterCents); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type AgentTotals struct {
	BalanceCents, TodayOrderCents, MonthOrderCents int64
}

// AgentTotalsSince 全体代理商的余额合计，以及今天、本月的下单合计（purchase 流水金额的绝对值之和）。
func AgentTotalsSince(ctx context.Context, dayStart, monthStart int64) (AgentTotals, error) {
	var t AgentTotals
	if err := DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(balance_cents), 0) FROM agents`).Scan(&t.BalanceCents); err != nil {
		return AgentTotals{}, err
	}
	err := DB.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN created_at >= ? THEN ABS(amount_cents) ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN created_at >= ? THEN ABS(amount_cents) ELSE 0 END), 0)
		FROM agent_ledger WHERE kind = 'purchase'`, dayStart, monthStart).Scan(&t.TodayOrderCents, &t.MonthOrderCents)
	if err != nil {
		return AgentTotals{}, err
	}
	return t, nil
}

// AgentPurchaseSince 这个代理商 from <= created_at < to 的下单合计（purchase 流水金额的绝对值之和），规则同 AgentTotalsSince。
func AgentPurchaseSince(ctx context.Context, agentID, from, to int64) (int64, error) {
	var n int64
	err := DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(ABS(amount_cents)), 0) FROM agent_ledger
		WHERE agent_id = ? AND kind = 'purchase' AND created_at >= ? AND created_at < ?`, agentID, from, to).Scan(&n)
	return n, err
}
