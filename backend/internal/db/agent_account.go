package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// 代理商账号的增改查。余额不在这里改，只能经 agent_ledger.go 里的 applyAgentBalance。

const (
	AgentActive   = "active"
	AgentDisabled = "disabled"
)

var (
	ErrAgentNotFound         = errors.New("代理商不存在")
	ErrAgentUsernameTaken    = errors.New("登录名已被使用")
	ErrAgentDisabled         = errors.New("代理商已停用")
	ErrAgentTOTPAlreadyBound = errors.New("已绑定两步验证")
)

// Agent 是代理商账号，不含密码哈希和两步验证密文。
type Agent struct {
	ID                 int64
	Username           string
	DisplayName        string
	MustChangePassword bool
	TOTPBound          bool
	TierID             int64
	TierName           string
	BalanceCents       int64
	Status             string
	SessionVersion     int64
	Note               string
	LastLoginAt        *int64
	CreatedAt          int64
	UpdatedAt          int64
}

// AgentSecrets 是登录校验要用的敏感字段，只在登录流程里读取。
type AgentSecrets struct {
	PasswordHash string
	TOTPSealed   string
	TOTPLastStep int64
}

type NewAgent struct {
	Username, DisplayName, PasswordHash string
	TierID                              int64
	Note                                string
}

const agentSelect = `SELECT a.id, a.username, a.display_name, a.must_change_password, a.totp_sealed <> '',
	a.tier_id, COALESCE(t.name, ''), a.balance_cents, a.status, a.session_version, a.note,
	a.last_login_at, a.created_at, a.updated_at
FROM agents a LEFT JOIN agent_tiers t ON t.id = a.tier_id`

func scanAgent(r rowScanner) (Agent, error) {
	var a Agent
	var must, bound int
	var last int64
	if err := r.Scan(&a.ID, &a.Username, &a.DisplayName, &must, &bound, &a.TierID, &a.TierName,
		&a.BalanceCents, &a.Status, &a.SessionVersion, &a.Note, &last, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return Agent{}, err
	}
	a.MustChangePassword = must != 0
	a.TOTPBound = bound != 0
	if last > 0 {
		a.LastLoginAt = &last
	}
	return a, nil
}

// CreateAgent 建号：下次登录必须改密码，会话版本 1，余额 0。登录名不区分大小写唯一。
func CreateAgent(ctx context.Context, a NewAgent, now time.Time) (int64, error) {
	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ok, err := rowExists(ctx, tx, "agent_tiers", a.TierID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrAgentTierNotFound
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO agents (username, display_name, password_hash, must_change_password,
		tier_id, balance_cents, status, session_version, note, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, 0, 'active', 1, ?, ?, ?)`,
		a.Username, a.DisplayName, a.PasswordHash, a.TierID, a.Note, now.Unix(), now.Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrAgentUsernameTaken
		}
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func GetAgent(ctx context.Context, id int64) (Agent, error) {
	a, err := scanAgent(DB.QueryRowContext(ctx, agentSelect+` WHERE a.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrAgentNotFound
	}
	return a, err
}

// GetAgentByUsername 按登录名取账号和敏感字段，登录名不区分大小写。
func GetAgentByUsername(ctx context.Context, username string) (Agent, AgentSecrets, error) {
	var s AgentSecrets
	var id int64
	err := DB.QueryRowContext(ctx, `SELECT id, password_hash, totp_sealed, totp_last_step FROM agents WHERE username = ?`, username).
		Scan(&id, &s.PasswordHash, &s.TOTPSealed, &s.TOTPLastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, AgentSecrets{}, ErrAgentNotFound
	}
	if err != nil {
		return Agent{}, AgentSecrets{}, err
	}
	a, err := GetAgent(ctx, id)
	if err != nil {
		return Agent{}, AgentSecrets{}, err
	}
	return a, s, nil
}

func GetAgentSecrets(ctx context.Context, id int64) (AgentSecrets, error) {
	var s AgentSecrets
	err := DB.QueryRowContext(ctx, `SELECT password_hash, totp_sealed, totp_last_step FROM agents WHERE id = ?`, id).
		Scan(&s.PasswordHash, &s.TOTPSealed, &s.TOTPLastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSecrets{}, ErrAgentNotFound
	}
	return s, err
}

// ListAgents 按 id 降序。
func ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := DB.QueryContext(ctx, agentSelect+` ORDER BY a.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAgentProfile 改显示名、等级和备注。等级必须存在。
func UpdateAgentProfile(ctx context.Context, id int64, displayName string, tierID int64, note string, now time.Time) error {
	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ok, err := rowExists(ctx, tx, "agent_tiers", tierID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAgentTierNotFound
	}
	res, err := tx.ExecContext(ctx, `UPDATE agents SET display_name = ?, tier_id = ?, note = ?, updated_at = ? WHERE id = ?`,
		displayName, tierID, note, now.Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAgentNotFound
	}
	return tx.Commit()
}

// SetAgentStatus 启用或停用。停用时会话版本加 1，已登录的会话立刻失效。
func SetAgentStatus(ctx context.Context, id int64, status string, now time.Time) error {
	if status != AgentActive && status != AgentDisabled {
		return fmt.Errorf("状态不合法: %q", status)
	}
	bump := 0
	if status == AgentDisabled {
		bump = 1
	}
	res, err := DB.ExecContext(ctx, `UPDATE agents SET status = ?, session_version = session_version + ?, updated_at = ? WHERE id = ?`,
		status, bump, now.Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// SetAgentPassword 换密码哈希，会话版本加 1，返回新的版本号。
func SetAgentPassword(ctx context.Context, id int64, hash string, mustChange bool, now time.Time) (int64, error) {
	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE agents SET password_hash = ?, must_change_password = ?, session_version = session_version + 1, updated_at = ? WHERE id = ?`,
		hash, agentBoolInt(mustChange), now.Unix(), id)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrAgentNotFound
	}
	var sv int64
	if err := tx.QueryRowContext(ctx, `SELECT session_version FROM agents WHERE id = ?`, id).Scan(&sv); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return sv, nil
}

// BindAgentTOTP 绑定两步验证，只在还没绑定时生效。step 记为已用过的时间窗，避免绑定用的码被重复使用。
func BindAgentTOTP(ctx context.Context, id int64, sealed string, step int64, now time.Time) error {
	res, err := DB.ExecContext(ctx, `UPDATE agents SET totp_sealed = ?, totp_last_step = ?, updated_at = ? WHERE id = ? AND totp_sealed = ''`,
		sealed, step, now.Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	var one int
	if err := DB.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return ErrAgentNotFound
	} else if err != nil {
		return err
	}
	return ErrAgentTOTPAlreadyBound
}

// ResetAgentTOTP 清掉两步验证，会话版本加 1。
func ResetAgentTOTP(ctx context.Context, id int64, now time.Time) error {
	res, err := DB.ExecContext(ctx, `UPDATE agents SET totp_sealed = '', totp_last_step = 0, session_version = session_version + 1, updated_at = ? WHERE id = ?`,
		now.Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// UseAgentTOTPStep 占用一个时间窗：只有比上次用过的更新才成功，同一个动态码不能用两次。
func UseAgentTOTPStep(ctx context.Context, id, step int64) (bool, error) {
	res, err := DB.ExecContext(ctx, `UPDATE agents SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, id, step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func TouchAgentLogin(ctx context.Context, id int64, now time.Time) error {
	res, err := DB.ExecContext(ctx, `UPDATE agents SET last_login_at = ?, updated_at = ? WHERE id = ?`, now.Unix(), now.Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// CountAgentCodes 按代理商统计本站码的数量，不属于代理商的码（agent_id = 0）不算。
func CountAgentCodes(ctx context.Context) (map[int64]int, error) {
	rows, err := DB.QueryContext(ctx, `SELECT agent_id, COUNT(*) FROM local_cdks WHERE agent_id <> 0 GROUP BY agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
