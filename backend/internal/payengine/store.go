package payengine

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// settableColumns 是 transition / move 允许写入的列（pay_orders 除
// id、order_no、state、created_at、updated_at、finished_at、session_sealed 之外的全部列）。
var settableColumns = map[string]bool{
	"source": true, "source_ref": true, "dry_run": true, "plan_key": true, "region": true,
	"currency": true, "cap_minor": true, "fund_cents": true, "account_email": true, "account_id": true,
	"checkout_session_id": true, "checkout_url": true, "amount_minor": true, "amount_text": true,
	"card_id": true, "card_last4": true, "lease_token": true, "fund_order_no": true,
	"billing_state": true, "page_outcome": true, "submitted_at": true, "failure": true,
	"error_code": true, "error_message": true, "verify_until": true, "next_check_at": true,
	"resolved_by": true, "resolve_note": true,
	"kind": true, "credit_quantity": true, "renewal_state": true, "renewal_attempts": true,
	"renewal_checked_at": true, "renewal_next_at": true, "card_saved": true, "saved_cards_before": true,
	"credit_balance_before": true, "access_until": true, "resolved_at": true,
}

const orderColumns = `id, order_no, provider, source, source_ref, dry_run, plan_key, region, currency, cap_minor, fund_cents,
	account_email, account_id, state, session_sealed, checkout_session_id, checkout_url, amount_minor, amount_text,
	card_id, card_last4, lease_token, fund_order_no, billing_state, page_outcome, submitted_at, failure, error_code,
	error_message, verify_until, next_check_at, resolved_by, resolve_note, created_at, updated_at, finished_at,
	kind, credit_quantity, renewal_state, renewal_attempts, renewal_checked_at, renewal_next_at, card_saved,
	saved_cards_before, credit_balance_before, access_until, resolved_at`

// newOrderNo 生成单号：PO + yyMMddHHmmss（UTC）+ 8 位小写十六进制，共 22 位。
func newOrderNo(now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "PO" + now.UTC().Format("060102150405") + hex.EncodeToString(b[:])
}

func isInflightConflict(err error) bool {
	var se sqlite3.Error
	if !errors.As(err, &se) || se.ExtendedCode != sqlite3.ErrConstraintUnique {
		return false
	}
	return strings.Contains(se.Error(), "pay_orders.account_email")
}

// dry_run 列的取值：0 真实付款，1 试运行到读金额，2 试运行到填表。
// 不加新列：只要不是 0 就是试运行，旧版本程序读到 2 也不会当成真实付款。
const dryRunFillValue = 2

func dryRunColumn(o *Order) int {
	switch {
	case o.DryRunFill:
		return dryRunFillValue
	case o.DryRun:
		return 1
	}
	return 0
}

// insertOrder 写入一张新单据并记一条创建事件；状态为空时取 queued。
// 同一邮箱已有未终态单据时返回 ErrInflight。
func insertOrder(ctx context.Context, o *Order) error {
	now := time.Now().UTC().Unix()
	if o.OrderNo == "" {
		o.OrderNo = newOrderNo(time.Unix(now, 0))
	}
	if o.State == "" {
		o.State = StateQueued
	}
	if o.Kind == "" {
		o.Kind = KindSubscription
	}
	o.CreatedAt, o.UpdatedAt = now, now
	tx, err := db.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO pay_orders (order_no, provider, source, source_ref, dry_run, plan_key, region, currency,
		cap_minor, fund_cents, account_email, account_id, state, session_sealed, created_at, updated_at, kind, credit_quantity)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.OrderNo, o.Provider, o.Source, o.SourceRef, dryRunColumn(o), o.PlanKey, o.Region, o.Currency,
		o.CapMinor, o.FundCents, o.AccountEmail, o.AccountID, o.State, o.SessionSealed, o.CreatedAt, o.UpdatedAt,
		o.Kind, o.CreditQuantity)
	if err != nil {
		if isInflightConflict(err) {
			return fmt.Errorf("%w: %s", ErrInflight, o.AccountEmail)
		}
		return err
	}
	if o.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pay_order_events (order_no, at, state, level, message) VALUES (?, ?, ?, 'info', ?)`,
		o.OrderNo, now, o.State, "付款单据已创建"); err != nil {
		return err
	}
	return tx.Commit()
}

// moveMode 决定 move 除了状态和列之外还动什么。
type moveMode int

const (
	moveStep           moveMode = iota // 非终态：只改状态和列
	moveEnd                            // 写终态：另设 finished_at，清空会话
	moveEndKeepSession                 // 写终态：另设 finished_at，会话留着关闭续费用，到点由续费协程清空（见 renewal.go）
	moveStepClear                      // 不写 finished_at，但清空会话：人工定论失败时用
)

// move 在事务内完成「校验 from → 更新列 → 写事件」，mode 见 moveMode。
func move(ctx context.Context, no, from, to, level, msg string, set map[string]any, mode moveMode) error {
	keys := make([]string, 0, len(set))
	for k := range set {
		if !settableColumns[k] {
			return fmt.Errorf("%w: 不允许更新列 %q", ErrBadRequest, k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	now := time.Now().UTC().Unix()
	sqlText := "UPDATE pay_orders SET state = ?, updated_at = ?"
	args := []any{to, now}
	switch mode {
	case moveEnd:
		sqlText += ", finished_at = ?, session_sealed = ''"
		args = append(args, now)
	case moveEndKeepSession:
		sqlText += ", finished_at = ?"
		args = append(args, now)
	case moveStepClear:
		sqlText += ", session_sealed = ''"
	}
	for _, k := range keys {
		sqlText += ", " + k + " = ?"
		args = append(args, set[k])
	}
	sqlText += " WHERE order_no = ? AND state = ?"
	args = append(args, no, from)

	tx, err := db.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, sqlText, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var cur string
		switch err := tx.QueryRowContext(ctx, `SELECT state FROM pay_orders WHERE order_no = ?`, no).Scan(&cur); {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return err
		}
		return fmt.Errorf("%w: 当前为 %s，期望 %s", ErrStateChanged, cur, from)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pay_order_events (order_no, at, state, level, message) VALUES (?, ?, ?, ?, ?)`,
		no, now, to, level, msg); err != nil {
		return err
	}
	return tx.Commit()
}

// transition 仅当当前状态为 from 时把状态改为 to，同时更新 set 中的列并写一条事件。
func transition(ctx context.Context, no, from, to, level, msg string, set map[string]any) error {
	return move(ctx, no, from, to, level, msg, set, moveStep)
}

// addEvent 追加一条时间线事件，不改状态，事件的 state 取单据当前状态。
func addEvent(ctx context.Context, no, level, msg, screenshot string) error {
	res, err := db.DB.ExecContext(ctx, `INSERT INTO pay_order_events (order_no, at, state, level, message, screenshot)
		SELECT order_no, ?, state, ?, ?, ? FROM pay_orders WHERE order_no = ?`,
		time.Now().UTC().Unix(), level, msg, screenshot, no)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanOrder(r rowScanner) (*Order, error) {
	var o Order
	var dry int
	err := r.Scan(&o.ID, &o.OrderNo, &o.Provider, &o.Source, &o.SourceRef, &dry, &o.PlanKey, &o.Region, &o.Currency, &o.CapMinor, &o.FundCents,
		&o.AccountEmail, &o.AccountID, &o.State, &o.SessionSealed, &o.CheckoutSessionID, &o.CheckoutURL, &o.AmountMinor, &o.AmountText,
		&o.CardID, &o.CardLast4, &o.LeaseToken, &o.FundOrderNo, &o.BillingState, &o.PageOutcome, &o.SubmittedAt, &o.Failure, &o.ErrorCode,
		&o.ErrorMessage, &o.VerifyUntil, &o.NextCheckAt, &o.ResolvedBy, &o.ResolveNote, &o.CreatedAt, &o.UpdatedAt, &o.FinishedAt,
		&o.Kind, &o.CreditQuantity, &o.RenewalState, &o.RenewalAttempts, &o.RenewalCheckedAt, &o.RenewalNextAt, &o.CardSaved,
		&o.SavedCardsBefore, &o.CreditBalanceBefore, &o.AccessUntil, &o.ResolvedAt)
	if err != nil {
		return nil, err
	}
	o.DryRun = dry != 0
	o.DryRunFill = dry == dryRunFillValue
	o.HasSession = o.SessionSealed != ""
	return &o, nil
}

// getOrder 按单号取单据，不存在返回 ErrNotFound。
func getOrder(ctx context.Context, no string) (*Order, error) {
	o, err := scanOrder(db.DB.QueryRowContext(ctx, `SELECT `+orderColumns+` FROM pay_orders WHERE order_no = ?`, no))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return o, err
}

func collectOrders(rows *sql.Rows) ([]Order, error) {
	defer rows.Close()
	out := []Order{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// OrderFilter 是单据列表的筛选条件，空字段不筛选。
type OrderFilter struct {
	State   string // 单据状态
	Renewal string // 续费进度（renewal_state）
	Kind    string // 单据种类
}

// listOrdersBy 按筛选条件、id 倒序分页列出单据；size 默认 20、上限 100。返回总数。
func listOrdersBy(ctx context.Context, f OrderFilter, page, size int) ([]Order, int, error) {
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	if page < 1 {
		page = 1
	}
	var conds []string
	args := []any{}
	for _, c := range []struct{ col, val string }{{"state", f.State}, {"renewal_state", f.Renewal}, {"kind", f.Kind}} {
		if c.val != "" {
			conds, args = append(conds, c.col+" = ?"), append(args, c.val)
		}
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM pay_orders`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := db.DB.QueryContext(ctx, `SELECT `+orderColumns+` FROM pay_orders`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, size, (page-1)*size)...)
	if err != nil {
		return nil, 0, err
	}
	list, err := collectOrders(rows)
	return list, total, err
}

// listEvents 按时间顺序返回单据时间线。
func listEvents(ctx context.Context, no string) ([]Event, error) {
	rows, err := db.DB.QueryContext(ctx, `SELECT id, order_no, at, state, level, message, screenshot
		FROM pay_order_events WHERE order_no = ? ORDER BY id`, no)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.OrderNo, &e.At, &e.State, &e.Level, &e.Message, &e.Screenshot); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ordersInStates 返回处于给定任一状态的单据（按 id 升序）。
func ordersInStates(ctx context.Context, states ...string) ([]Order, error) {
	if len(states) == 0 {
		return []Order{}, nil
	}
	args := make([]any, len(states))
	for i, s := range states {
		args[i] = s
	}
	rows, err := db.DB.QueryContext(ctx, `SELECT `+orderColumns+` FROM pay_orders WHERE state IN (?`+
		strings.Repeat(", ?", len(states)-1)+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	return collectOrders(rows)
}

// dueVerifying 返回确认中且到了下次检查时间的单据（按 id 升序）。
func dueVerifying(ctx context.Context, now int64) ([]Order, error) {
	rows, err := db.DB.QueryContext(ctx, `SELECT `+orderColumns+` FROM pay_orders
		WHERE state = ? AND (next_check_at IS NULL OR next_check_at <= ?) ORDER BY id`, StateVerifying, now)
	if err != nil {
		return nil, err
	}
	return collectOrders(rows)
}

// setNextCheck 更新确认中单据的下次检查时间，不写时间线（定时检查不刷事件）。
func setNextCheck(ctx context.Context, no string, at int64) error {
	_, err := db.DB.ExecContext(ctx, `UPDATE pay_orders SET next_check_at = ?, updated_at = ? WHERE order_no = ? AND state = ?`,
		at, time.Now().UTC().Unix(), no, StateVerifying)
	return err
}
