package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var (
	ErrAgentSharedNotFound      = errors.New("商户 ID 不存在")
	ErrAgentSharedAdminDisabled = errors.New("对接已被管理员关闭")
)

// AgentSharedCreds 是 agent_shared_creds 的一行（异次元对接设置）。没有行时 Exists=false，其余字段为零值（AgentID 除外）。
type AgentSharedCreds struct {
	AgentID              int64
	Exists               bool
	AppID                string // 库里 NULL 读成 ""
	KeySealed            string
	KeyCreatedAt         int64
	LastUsedAt           int64
	AdminDisabled        bool
	IPAllowlist          []string // 库里逗号分隔；空串读成 nil
	LowBalanceCents      int64
	CreatedAt, UpdatedAt int64
}

const agentSharedCols = `agent_id, app_id, key_sealed, key_created_at, last_used_at, admin_disabled, ip_allowlist, low_balance_cents, created_at, updated_at`

func scanAgentSharedCreds(s rowScanner) (AgentSharedCreds, error) {
	var c AgentSharedCreds
	var appID sql.NullString
	var disabled int
	var allow string
	if err := s.Scan(&c.AgentID, &appID, &c.KeySealed, &c.KeyCreatedAt, &c.LastUsedAt, &disabled, &allow, &c.LowBalanceCents, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return AgentSharedCreds{}, err
	}
	c.Exists = true
	c.AppID = appID.String
	c.AdminDisabled = disabled != 0
	if allow != "" {
		c.IPAllowlist = strings.Split(allow, ",")
	}
	return c, nil
}

// GetAgentSharedCreds 取这个代理商的对接设置；没有行时返回 Exists=false、AgentID 填好、err=nil。
func GetAgentSharedCreds(ctx context.Context, agentID int64) (AgentSharedCreds, error) {
	c, err := scanAgentSharedCreds(DB.QueryRowContext(ctx, `SELECT `+agentSharedCols+` FROM agent_shared_creds WHERE agent_id = ?`, agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSharedCreds{AgentID: agentID}, nil
	}
	return c, err
}

// GetAgentSharedCredsByAppID 按商户 ID 查；找不到返回 ErrAgentSharedNotFound。
func GetAgentSharedCredsByAppID(ctx context.Context, appID string) (AgentSharedCreds, error) {
	c, err := scanAgentSharedCreds(DB.QueryRowContext(ctx, `SELECT `+agentSharedCols+` FROM agent_shared_creds WHERE app_id = ?`, appID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSharedCreds{}, ErrAgentSharedNotFound
	}
	return c, err
}

// SetAgentSharedKey 写入商户 ID 和密钥密文（开通或重置）；没有行就插入。
// 管理员关闭对接时不改并返回 ErrAgentSharedAdminDisabled。
func SetAgentSharedKey(ctx context.Context, agentID int64, appID, keySealed string, now time.Time) error {
	res, err := DB.ExecContext(ctx, `INSERT INTO agent_shared_creds (agent_id, app_id, key_sealed, key_created_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET app_id = excluded.app_id, key_sealed = excluded.key_sealed,
			key_created_at = excluded.key_created_at, updated_at = excluded.updated_at
		WHERE agent_shared_creds.admin_disabled = 0`, agentID, appID, keySealed, now.Unix(), now.Unix(), now.Unix())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrAgentSharedAdminDisabled
	}
	return nil
}

// DisableAgentShared 代理商自己停用对接：商户 ID 置 NULL、密钥清空。没有行时什么也不做。
func DisableAgentShared(ctx context.Context, agentID int64, now time.Time) error {
	_, err := DB.ExecContext(ctx, `UPDATE agent_shared_creds SET app_id = NULL, key_sealed = '', key_created_at = 0, updated_at = ? WHERE agent_id = ?`, now.Unix(), agentID)
	return err
}

// SetAgentSharedAdminDisabled 管理员关闭或重新允许对接；没有行就插入，不动密钥。
func SetAgentSharedAdminDisabled(ctx context.Context, agentID int64, disabled bool, now time.Time) error {
	v := 0
	if disabled {
		v = 1
	}
	_, err := DB.ExecContext(ctx, `INSERT INTO agent_shared_creds (agent_id, admin_disabled, created_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET admin_disabled = excluded.admin_disabled, updated_at = excluded.updated_at`, agentID, v, now.Unix(), now.Unix())
	return err
}

// SetAgentSharedAllowlist 保存 IP 白名单，按给定顺序逗号拼接；没有行就插入。校验由调用方负责。
func SetAgentSharedAllowlist(ctx context.Context, agentID int64, ips []string, now time.Time) error {
	_, err := DB.ExecContext(ctx, `INSERT INTO agent_shared_creds (agent_id, ip_allowlist, created_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET ip_allowlist = excluded.ip_allowlist, updated_at = excluded.updated_at`,
		agentID, strings.Join(ips, ","), now.Unix(), now.Unix())
	return err
}

// SetAgentSharedLowBalance 保存余额提醒线（分，0 关闭）；没有行就插入。负数撞 CHECK 报错。
func SetAgentSharedLowBalance(ctx context.Context, agentID int64, cents int64, now time.Time) error {
	_, err := DB.ExecContext(ctx, `INSERT INTO agent_shared_creds (agent_id, low_balance_cents, created_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET low_balance_cents = excluded.low_balance_cents, updated_at = excluded.updated_at`,
		agentID, cents, now.Unix(), now.Unix())
	return err
}

// TouchAgentSharedUsed 记录最近一次验签通过的时间，距上次不足 60 秒不写。
func TouchAgentSharedUsed(ctx context.Context, agentID int64, now time.Time) error {
	_, err := DB.ExecContext(ctx, `UPDATE agent_shared_creds SET last_used_at = ? WHERE agent_id = ? AND last_used_at <= ?`,
		now.Unix(), agentID, now.Unix()-60)
	return err
}

// NewSharedFailure 是要记的一次取码失败。
type NewSharedFailure struct {
	AgentID         int64
	RequestNo       string // 对方原始单号，不带 acg:
	ProductCode     string // 调用方已截到 64 字
	Quantity        int
	Reason, Message string
}

// AgentSharedFailure 是 agent_shared_failures 的一行。
type AgentSharedFailure struct {
	ID, AgentID            int64
	RequestNo, ProductCode string
	Quantity               int
	Reason, Message        string
	Attempts               int
	FirstAt, LastAt        int64
	ResolvedOrderID        *int64
}

// RecordAgentSharedFailure 记一次失败；同 (agent_id, request_no) 已有则次数 +1、更新商品、数量、原因、提示和最后时间，并把已解决标记清掉。
func RecordAgentSharedFailure(ctx context.Context, f NewSharedFailure, now time.Time) error {
	_, err := DB.ExecContext(ctx, `INSERT INTO agent_shared_failures (agent_id, request_no, product_code, quantity, reason, message, attempts, first_at, last_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(agent_id, request_no) DO UPDATE SET attempts = attempts + 1, product_code = excluded.product_code,
			quantity = excluded.quantity, reason = excluded.reason, message = excluded.message,
			last_at = excluded.last_at, resolved_order_id = NULL`,
		f.AgentID, f.RequestNo, f.ProductCode, f.Quantity, f.Reason, f.Message, now.Unix(), now.Unix())
	return err
}

// ResolveAgentSharedFailure 这笔对方订单号后来取码成功：只改还没解决的行，没有行不报错。
func ResolveAgentSharedFailure(ctx context.Context, agentID int64, requestNo string, orderID int64) error {
	_, err := DB.ExecContext(ctx, `UPDATE agent_shared_failures SET resolved_order_id = ? WHERE agent_id = ? AND request_no = ? AND resolved_order_id IS NULL`,
		orderID, agentID, requestNo)
	return err
}

// ListAgentSharedFailures 列出 last_at >= since 的失败记录，last_at 降序、id 降序，并返回总数。
func ListAgentSharedFailures(ctx context.Context, agentID, since int64, limit, offset int) ([]AgentSharedFailure, int, error) {
	var total int
	if err := DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_shared_failures WHERE agent_id = ? AND last_at >= ?`, agentID, since).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := DB.QueryContext(ctx, `SELECT id, agent_id, request_no, product_code, quantity, reason, message, attempts, first_at, last_at, resolved_order_id
		FROM agent_shared_failures WHERE agent_id = ? AND last_at >= ? ORDER BY last_at DESC, id DESC LIMIT ? OFFSET ?`, agentID, since, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AgentSharedFailure{}
	for rows.Next() {
		var f AgentSharedFailure
		var resolved sql.NullInt64
		if err := rows.Scan(&f.ID, &f.AgentID, &f.RequestNo, &f.ProductCode, &f.Quantity, &f.Reason, &f.Message, &f.Attempts, &f.FirstAt, &f.LastAt, &resolved); err != nil {
			return nil, 0, err
		}
		if resolved.Valid {
			v := resolved.Int64
			f.ResolvedOrderID = &v
		}
		out = append(out, f)
	}
	return out, total, rows.Err()
}

// CountAgentSharedFailures 只数 last_at >= since 且未解决的失败：total 是全部，insufficient 是其中余额不足的。
func CountAgentSharedFailures(ctx context.Context, agentID, since int64) (total, insufficient int, err error) {
	err = DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(reason = 'insufficient_balance'), 0) FROM agent_shared_failures
		WHERE agent_id = ? AND last_at >= ? AND resolved_order_id IS NULL`, agentID, since).Scan(&total, &insufficient)
	return
}

// PruneAgentSharedFailures 删 last_at < before 的行，返回删除数。
func PruneAgentSharedFailures(ctx context.Context, before int64) (int64, error) {
	res, err := DB.ExecContext(ctx, `DELETE FROM agent_shared_failures WHERE last_at < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
