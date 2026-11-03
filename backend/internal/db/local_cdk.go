package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

// 本地码（local_cdks）：由本站自己履约的服务商（目前只有 MarsWallex）发的码。
//
// 只存哈希和前缀，完整码只在发码响应里出现一次。表里的 status 只有人工态 active / disabled；
// 码实际处于什么状态一律从 pay_orders 派生（DeriveLocalCDK），不回写：多写一份就多一处和资金单据对不上的可能。
const (
	LocalCDKManualActive   = "active"
	LocalCDKManualDisabled = "disabled"

	LocalCDKUnused   = "unused"   // 可兑换（没有单，或全部是干净失败且没超过次数）
	LocalCDKReserved = "reserved" // 有在途单或待人工单
	LocalCDKConsumed = "consumed" // 已付款成功
	LocalCDKFailed   = "failed"   // 不能再兑换，等人工
	LocalCDKDisabled = "disabled" // 人工禁用，或已被换码替代

	LocalReasonNeedsReview       = "needs_review"       // 单据待人工
	LocalReasonDeclined          = "declined"           // 支付被拒或人工定论失败，可能有迟到授权，不能重兑
	LocalReasonDirtyOrder        = "dirty_order"        // 提交过支付或状态不明的失败单，不能重兑
	LocalReasonAttemptsExhausted = "attempts_exhausted" // 干净失败次数用完，转人工
	LocalReasonExchanged         = "exchanged"          // 已换发新码
	LocalReasonManual            = "manual"             // 管理员禁用

	// MaxLocalCDKReRedeems 同一张码（含换码继承的次数）因 not_submitted 重兑的上限。
	// 首次兑换不算重兑，所以一张码最多建 1 + 3 = 4 张单；第 4 张也是干净失败就转人工。
	MaxLocalCDKReRedeems = 3

	// localCDKSource 是 pay_orders.source 的取值。
	localCDKSource = "cdk"
)

var (
	// ErrLocalCDKNotFound 记录不存在。
	ErrLocalCDKNotFound = errors.New("本地码不存在")
	// ErrLocalCDKReplaced 这张码已被换码替代，不能再启用，否则一码变两码。
	ErrLocalCDKReplaced = errors.New("该码已换发新码，不能启用")
	// ErrLocalCDKNotExchangeable 换码的条件更新没有命中：码已禁用，或已有不能重兑的单。
	ErrLocalCDKNotExchangeable = errors.New("该码当前不能换码")
)

// LocalCDK 是 local_cdks 的一行。没有完整码，也没有哈希：哈希只用来查询，不出这个包。
type LocalCDK struct {
	ID              int64
	Provider        string
	CodePrefix      string
	Product         string
	PlanKey         string
	Region          string
	Manual          string // 人工态 active / disabled
	ExchangedFrom   *int64 // 由哪张码换来
	ReplacedBy      *int64 // 换成了哪张码
	AttemptsCarried int    // 换码继承的干净失败次数：不让换码绕过重兑上限
	IssuedBy        string
	Note            string
	CreatedAt       int64
	UpdatedAt       int64
}

// LocalCDKOrder 是派生状态用到的单据字段。
type LocalCDKOrder struct {
	OrderNo      string
	SourceRef    string
	State        string
	Failure      string
	ErrorCode    string
	AccountEmail string
	FundCents    int64
	SubmittedAt  *int64
	CreatedAt    int64
	FinishedAt   *int64
}

// CleanFailure 这张单能确定没有点过支付（failed + not_submitted + 没有提交时间）。只有它能重兑。
func (o LocalCDKOrder) CleanFailure() bool {
	return o.State == "failed" && o.Failure == "not_submitted" && o.SubmittedAt == nil
}

// LocalCDKView 是一张码的派生结果。
type LocalCDKView struct {
	Status        string
	Reason        string
	CleanFailures int            // 干净失败次数，含换码继承的
	AttemptsLeft  int            // 还能兑换几次，只对 unused 有意义
	Latest        *LocalCDKOrder // 最新一张单；没有单时为 nil
	NeedsReview   bool
}

// Redeemable 现在能不能发起兑换。
func (v LocalCDKView) Redeemable() bool { return v.Status == LocalCDKUnused }

// DeriveLocalCDK 从单据推出码的状态。orders 按 id 升序，只应包含这张码的真实付款单（dry_run = 0）。
//
// 优先级从钱的角度排：成功 > 在途 > 待人工 > 不干净的失败 > 次数用完 > 人工禁用 > 可用。
// 人工禁用不能盖住前面的状态：单已经在跑，禁用拦不住它，界面要照实显示。
func DeriveLocalCDK(c LocalCDK, orders []LocalCDKOrder) LocalCDKView {
	v := LocalCDKView{CleanFailures: c.AttemptsCarried}
	var succeeded, inflight, review bool
	dirty := ""
	for i := range orders {
		o := &orders[i]
		v.Latest = o
		switch {
		case o.State == "succeeded":
			succeeded = true
		case o.State == "needs_review":
			review = true
		case o.CleanFailure():
			v.CleanFailures++
		case o.State == "failed":
			if o.Failure == "declined" {
				dirty = LocalReasonDeclined
			} else if dirty == "" {
				dirty = LocalReasonDirtyOrder
			}
		case o.State == "previewed":
			// 只有试运行才会到这个状态，真实单出现说明数据不对，按不能重兑处理
			if dirty == "" {
				dirty = LocalReasonDirtyOrder
			}
		default:
			inflight = true
		}
	}
	v.NeedsReview = review
	if left := 1 + MaxLocalCDKReRedeems - v.CleanFailures; left > 0 {
		v.AttemptsLeft = left
	}
	switch {
	case succeeded:
		v.Status = LocalCDKConsumed
	case inflight:
		v.Status = LocalCDKReserved
	case review:
		v.Status, v.Reason = LocalCDKReserved, LocalReasonNeedsReview
	case dirty != "":
		v.Status, v.Reason = LocalCDKFailed, dirty
	case v.AttemptsLeft == 0:
		v.Status, v.Reason = LocalCDKFailed, LocalReasonAttemptsExhausted
	case c.ReplacedBy != nil:
		v.Status, v.Reason = LocalCDKDisabled, LocalReasonExchanged
	case c.Manual == LocalCDKManualDisabled:
		v.Status, v.Reason = LocalCDKDisabled, LocalReasonManual
	default:
		v.Status = LocalCDKUnused
	}
	if v.Status != LocalCDKUnused {
		v.AttemptsLeft = 0
	}
	return v
}

// MigrateLocalCDKs 建本地码表和相关索引，清掉 recharge_tasks 里的旧会话。可重复执行。
func MigrateLocalCDKs() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS local_cdks (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	provider TEXT NOT NULL,
	code_hash TEXT NOT NULL UNIQUE,
	token_hash TEXT NOT NULL UNIQUE,
	code_prefix TEXT NOT NULL,
	product TEXT NOT NULL,
	plan_key TEXT NOT NULL,
	region TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
	exchanged_from INTEGER,
	attempts_carried INTEGER NOT NULL DEFAULT 0,
	issued_by TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_local_cdks_exchanged_from ON local_cdks(exchanged_from) WHERE exchanged_from IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_pay_orders_source ON pay_orders(source, source_ref)`,
		// 一张码最多一张「不是干净失败」的真实单：在途、待人工、成功、被拒都算。这是库级保证，处理器里的检查和锁都绕不过它
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_pay_orders_cdk_one ON pay_orders(source_ref)
	WHERE source = 'cdk' AND dry_run = 0 AND NOT (state = 'failed' AND failure = 'not_submitted' AND submitted_at IS NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_cdk_sessions_bound_at ON cdk_session_bindings(session_bound_at)`,
	}
	for _, s := range stmts {
		if _, err := DB.Exec(s); err != nil {
			return err
		}
	}
	clearRechargeTaskSessions()
	return nil
}

// clearRechargeTaskSessions 清空 recharge_tasks.session_json：这张表的处理器都没有路由，旧明文会话没有任何用处。
// 其余列保留，卡密查询还在读。失败只告警，下次启动重试；要先落 VACUUM 标记再清，中途崩溃后才知道还欠一次回收。
func clearRechargeTaskSessions() {
	var n int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM recharge_tasks WHERE COALESCE(session_json, '') != ''`).Scan(&n); err != nil {
		log.Printf("⚠️  [cdk-session] 检查 recharge_tasks 旧会话失败，下次启动重试: %v", err)
		return
	}
	if n > 0 {
		if err := SetSetting(settingCDKSessionVacuum, "1"); err != nil {
			log.Printf("⚠️  [cdk-session] 记录 VACUUM 标记失败，下次启动重试: %v", err)
			return
		}
		if _, err := DB.Exec(`UPDATE recharge_tasks SET session_json = '' WHERE COALESCE(session_json, '') != ''`); err != nil {
			log.Printf("⚠️  [cdk-session] 清空 recharge_tasks 旧会话失败，下次启动重试: %v", err)
			return
		}
		log.Printf("[cdk-session] 已清空 recharge_tasks 里 %d 行旧会话", n)
	}
	reclaimPlaintextPages()
}

// NewLocalCDK 是写入一张新码需要的字段。
type NewLocalCDK struct {
	Provider, CodeHash, TokenHash, CodePrefix, Product, PlanKey, Region, IssuedBy, Note string
	ExchangedFrom                                                                       *int64
	AttemptsCarried                                                                     int
}

func insertLocalCDK(ctx context.Context, tx *sql.Tx, n NewLocalCDK, now int64) (int64, error) {
	var from any
	if n.ExchangedFrom != nil {
		from = *n.ExchangedFrom
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO local_cdks (provider, code_hash, token_hash, code_prefix, product, plan_key, region,
		status, exchanged_from, attempts_carried, issued_by, note, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?)`,
		n.Provider, n.CodeHash, n.TokenHash, n.CodePrefix, n.Product, n.PlanKey, n.Region,
		from, n.AttemptsCarried, n.IssuedBy, n.Note, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// InsertLocalCDKs 在一个事务里写入一批码，返回新 id，顺序与入参一致。整批成功或整批回滚。
func InsertLocalCDKs(ctx context.Context, list []NewLocalCDK, now time.Time) ([]int64, error) {
	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids := make([]int64, 0, len(list))
	for _, n := range list {
		id, err := insertLocalCDK(ctx, tx, n, now.Unix())
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

const localCDKColumns = `l.id, l.provider, l.code_prefix, l.product, l.plan_key, l.region, l.status, l.exchanged_from,
	(SELECT MIN(n.id) FROM local_cdks n WHERE n.exchanged_from = l.id), l.attempts_carried, l.issued_by, l.note, l.created_at, l.updated_at`

func scanLocalCDK(s interface{ Scan(...any) error }) (LocalCDK, error) {
	var c LocalCDK
	var from, replaced sql.NullInt64
	err := s.Scan(&c.ID, &c.Provider, &c.CodePrefix, &c.Product, &c.PlanKey, &c.Region, &c.Manual, &from, &replaced,
		&c.AttemptsCarried, &c.IssuedBy, &c.Note, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return c, err
	}
	if from.Valid {
		c.ExchangedFrom = &from.Int64
	}
	if replaced.Valid {
		c.ReplacedBy = &replaced.Int64
	}
	return c, nil
}

func getLocalCDKWhere(ctx context.Context, cond string, arg any) (LocalCDK, error) {
	c, err := scanLocalCDK(DB.QueryRowContext(ctx, `SELECT `+localCDKColumns+` FROM local_cdks l WHERE `+cond, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrLocalCDKNotFound
	}
	return c, err
}

// GetLocalCDK 按 id 取一行。
func GetLocalCDK(ctx context.Context, id int64) (LocalCDK, error) {
	return getLocalCDKWhere(ctx, `l.id = ?`, id)
}

// GetLocalCDKByCode 按完整码取一行。查询只比较哈希，HashCDKCode 已做大小写和空白规范化。
func GetLocalCDKByCode(ctx context.Context, code string) (LocalCDK, error) {
	return getLocalCDKWhere(ctx, `l.code_hash = ?`, HashCDKCode(code))
}

// GetLocalCDKByTokenHash 按兑换令牌的哈希取一行。
func GetLocalCDKByTokenHash(ctx context.Context, tokenHash string) (LocalCDK, error) {
	return getLocalCDKWhere(ctx, `l.token_hash = ?`, tokenHash)
}

const localCDKOrderColumns = `order_no, source_ref, state, failure, error_code, account_email, fund_cents, submitted_at, created_at, finished_at`

func collectLocalCDKOrders(rows *sql.Rows) ([]LocalCDKOrder, error) {
	defer rows.Close()
	out := []LocalCDKOrder{}
	for rows.Next() {
		var o LocalCDKOrder
		var sub, fin sql.NullInt64
		if err := rows.Scan(&o.OrderNo, &o.SourceRef, &o.State, &o.Failure, &o.ErrorCode, &o.AccountEmail, &o.FundCents, &sub, &o.CreatedAt, &fin); err != nil {
			return nil, err
		}
		if sub.Valid {
			o.SubmittedAt = &sub.Int64
		}
		if fin.Valid {
			o.FinishedAt = &fin.Int64
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// LocalCDKSourceRef 是码在 pay_orders.source_ref 里的写法。
func LocalCDKSourceRef(id int64) string { return strconv.FormatInt(id, 10) }

// LocalCDKViewOf 查这张码的单据并派生状态。查询出错时不给结果，调用方要按不可兑换处理。
func LocalCDKViewOf(ctx context.Context, c LocalCDK) (LocalCDKView, error) {
	rows, err := DB.QueryContext(ctx, `SELECT `+localCDKOrderColumns+` FROM pay_orders
		WHERE source = ? AND source_ref = ? AND dry_run = 0 ORDER BY id`, localCDKSource, LocalCDKSourceRef(c.ID))
	if err != nil {
		return LocalCDKView{}, err
	}
	orders, err := collectLocalCDKOrders(rows)
	if err != nil {
		return LocalCDKView{}, err
	}
	return DeriveLocalCDK(c, orders), nil
}

// LocalCDKRow 是列表里的一行。
type LocalCDKRow struct {
	LocalCDK
	View LocalCDKView
}

// LoadLocalCDKRows 取全部本地码并派生状态，新的在前。状态是派生值，不能用 SQL 筛选，所以整表读出来在 Go 里算；
// 本地码按批发放、数量有限，后台接口和补卡闸的调用频率都低，先读码再读单，两次查询之间没有持有结果集。
func LoadLocalCDKRows(ctx context.Context) ([]LocalCDKRow, error) {
	rows, err := DB.QueryContext(ctx, `SELECT `+localCDKColumns+` FROM local_cdks l ORDER BY l.id DESC`)
	if err != nil {
		return nil, err
	}
	out := []LocalCDKRow{}
	for rows.Next() {
		c, err := scanLocalCDK(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, LocalCDKRow{LocalCDK: c})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	orderRows, err := DB.QueryContext(ctx, `SELECT `+localCDKOrderColumns+` FROM pay_orders WHERE source = ? AND dry_run = 0 ORDER BY id`, localCDKSource)
	if err != nil {
		return nil, err
	}
	orders, err := collectLocalCDKOrders(orderRows)
	if err != nil {
		return nil, err
	}
	byRef := make(map[string][]LocalCDKOrder, len(orders))
	for _, o := range orders {
		byRef[o.SourceRef] = append(byRef[o.SourceRef], o)
	}
	for i := range out {
		out[i].View = DeriveLocalCDK(out[i].LocalCDK, byRef[LocalCDKSourceRef(out[i].ID)])
	}
	return out, nil
}

// HasOpenLocalCDKs 是否还有该服务商没结清的码（可兑换或在途）：这些码随时会用到卡。
func HasOpenLocalCDKs(ctx context.Context, provider string) (bool, error) {
	list, err := LoadLocalCDKRows(ctx)
	if err != nil {
		return false, err
	}
	for _, r := range list {
		if r.Provider == provider && (r.View.Status == LocalCDKUnused || r.View.Status == LocalCDKReserved) {
			return true, nil
		}
	}
	return false, nil
}

// DisableLocalCDK 把人工态改成 disabled，返回这次是否真的改了。已在跑的单不受影响，它结束后码才显示禁用。
func DisableLocalCDK(ctx context.Context, id int64, now time.Time) (bool, error) {
	res, err := DB.ExecContext(ctx, `UPDATE local_cdks SET status = 'disabled', updated_at = ? WHERE id = ? AND status != 'disabled'`, now.Unix(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// EnableLocalCDK 把人工态改回 active，返回这次是否真的改了。已被换码替代的码不能启用。
func EnableLocalCDK(ctx context.Context, id int64, now time.Time) (bool, error) {
	res, err := DB.ExecContext(ctx, `UPDATE local_cdks SET status = 'active', updated_at = ?
		WHERE id = ? AND status != 'active' AND NOT EXISTS (SELECT 1 FROM local_cdks n WHERE n.exchanged_from = ?)`, now.Unix(), id, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	c, err := GetLocalCDK(ctx, id)
	if err != nil {
		return false, err
	}
	if c.ReplacedBy != nil {
		return false, ErrLocalCDKReplaced
	}
	return false, nil
}

// ExchangeLocalCDK 换码：同一个事务里禁用旧码、写入新码。旧码必须 active，且除干净失败外没有别的单。
// 新码由调用方算好 AttemptsCarried 和 ExchangedFrom。条件没命中返回 ErrLocalCDKNotExchangeable，什么都不写。
func ExchangeLocalCDK(ctx context.Context, oldID int64, n NewLocalCDK, now time.Time) (int64, error) {
	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE local_cdks SET status = 'disabled', updated_at = ?
		WHERE id = ? AND status = 'active' AND NOT EXISTS (
			SELECT 1 FROM pay_orders WHERE source = ? AND source_ref = ? AND dry_run = 0
			AND NOT (state = 'failed' AND failure = 'not_submitted' AND submitted_at IS NULL))`,
		now.Unix(), oldID, localCDKSource, LocalCDKSourceRef(oldID))
	if err != nil {
		return 0, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if changed == 0 {
		return 0, ErrLocalCDKNotExchangeable
	}
	id, err := insertLocalCDK(ctx, tx, n, now.Unix())
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// IsLocalCDKOrderConflict 判断 Submit 的错误是不是撞了「一张码一张单」的唯一索引。
// 引擎只把邮箱索引的冲突映射成 ErrInflight，这个错误会原样返回。
func IsLocalCDKOrderConflict(err error) bool {
	var se sqlite3.Error
	if !errors.As(err, &se) || se.ExtendedCode != sqlite3.ErrConstraintUnique {
		return false
	}
	return strings.Contains(se.Error(), "pay_orders.source_ref")
}

// HasRealSucceededPayOrder 是否已有一笔真实付款成功的单：发 MarsWallex 码的硬门槛。
// dry_run 非 0 的都是试运行（1 到读金额、2 到填表），不算。
func HasRealSucceededPayOrder(ctx context.Context) (bool, error) {
	var n int
	err := DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM pay_orders WHERE dry_run = 0 AND state = 'succeeded' LIMIT 1)`).Scan(&n)
	return n > 0, err
}

// HasOpenPayOrderForEmail 该账号是否有真实付款单未结束或待人工。待人工的单可能已经付了钱，引擎的在途索引不管它，这里要算上。
func HasOpenPayOrderForEmail(ctx context.Context, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false, nil
	}
	var n int
	err := DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM pay_orders WHERE dry_run = 0 AND lower(account_email) = ?
		AND state NOT IN ('succeeded', 'failed', 'previewed')`, email).Scan(&n)
	return n > 0, err
}

// HasRecentCDKBindingForEmail 该账号在 since 之后是否在 ZovoCard 预检里绑定过会话。
// 邮箱是随机 nonce 的密文，不能按值查，只能把时间窗内的行逐个解开比较。
// 解不开就返回错误，由调用方按拒绝处理：不知道是不是同一个账号时不能放行付款。
func HasRecentCDKBindingForEmail(ctx context.Context, email string, since time.Time) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false, nil
	}
	rows, err := DB.QueryContext(ctx, `SELECT session_payload, account_email_sealed FROM cdk_session_bindings
		WHERE session_bound_at >= ? AND (account_email_sealed != '' OR TRIM(session_payload) != '')`, since.Unix())
	if err != nil {
		return false, err
	}
	type row struct{ stored, sealed string }
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.stored, &r.sealed); err != nil {
			rows.Close()
			return false, err
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	for _, r := range list {
		bound, err := openStoredEmail(r.sealed, r.stored)
		if err != nil {
			return false, fmt.Errorf("解开绑定邮箱失败: %w", err)
		}
		if strings.ToLower(strings.TrimSpace(bound)) == email {
			return true, nil
		}
	}
	return false, nil
}

// GetAccountEmailByRedemptionToken 按兑换令牌取绑定时记下的邮箱。只有令牌的 RT: 占位行也能查到。
// 没有绑定时返回空串；解不开时返回错误。
func GetAccountEmailByRedemptionToken(token string) (string, error) {
	if DB == nil {
		return "", errors.New("db not ready")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", nil
	}
	rows, err := DB.Query(`SELECT session_payload, account_email_sealed FROM cdk_session_bindings
		WHERE redemption_token = ? AND (account_email_sealed != '' OR TRIM(session_payload) != '')`, token)
	if err != nil {
		return "", err
	}
	var stored, sealed string
	found := false
	if rows.Next() {
		found = true
		if err := rows.Scan(&stored, &sealed); err != nil {
			rows.Close()
			return "", err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || !found {
		return "", err
	}
	return openStoredEmail(sealed, stored)
}
