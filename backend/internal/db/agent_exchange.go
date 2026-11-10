package db

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// 代理换码占位状态。一行占位代表「这张旧码已经换过或正在换」，唯一约束保证一张旧码只有一行。
const (
	AgentExchangePending = "pending" // 已占位，正在发码
	AgentExchangeUnknown = "unknown" // 发码结果不明，等人工定论；占位不释放
	// AgentExchangeDisabling 新码已发出，但旧码还没确认禁用。旧码和新码此刻都能兑换，所以不能算完成：
	// 由维护循环反复重试禁用，成功后才转 done（MO-08）。
	AgentExchangeDisabling = "disabling"
	AgentExchangeDone      = "done" // 新码已发出，旧码已确认禁用
)

var (
	// ErrAgentExchangeExists 这张旧码已有占位。
	ErrAgentExchangeExists = errors.New("该卡密已有换码记录")
	// ErrAgentExchangeDailyLimit 同一换码密码当天的额度用完了。
	ErrAgentExchangeDailyLimit = errors.New("代理换码已达当日上限")
	// ErrAgentExchangeNotFound 记录不存在。
	ErrAgentExchangeNotFound = errors.New("换码记录不存在")
	// ErrAgentExchangeState 记录当前状态不允许这个操作。
	ErrAgentExchangeState = errors.New("换码记录状态不允许此操作")
)

// agentExchangeTimeLayout 与 CURRENT_TIMESTAMP 写入的 UTC 文本同格式，按字符串比较即按时间比较。
const agentExchangeTimeLayout = "2006-01-02 15:04:05"

var agentExchangeDayZone = time.FixedZone("UTC+8", 8*3600)

// MigrateAgentExchanges 补占位用的列，归档旧库的重复行，再加唯一约束。可重复执行。
func MigrateAgentExchanges() error {
	cols := []struct{ name, def string }{
		{"state", `TEXT NOT NULL DEFAULT 'done'`},
		{"password_fp", `TEXT NOT NULL DEFAULT ''`},
		{"note", `TEXT NOT NULL DEFAULT ''`},
		{"updated_at", `DATETIME`},
	}
	for _, c := range cols {
		var n int
		if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agent_cdk_exchanges') WHERE name = ?`, c.name).Scan(&n); err != nil {
			return fmt.Errorf("检查 agent_cdk_exchanges.%s: %w", c.name, err)
		}
		if n > 0 {
			continue
		}
		if _, err := DB.Exec(`ALTER TABLE agent_cdk_exchanges ADD COLUMN ` + c.name + ` ` + c.def); err != nil {
			return fmt.Errorf("补列 agent_cdk_exchanges.%s: %w", c.name, err)
		}
	}
	if _, err := DB.Exec(`CREATE TABLE IF NOT EXISTS agent_cdk_exchange_dup_archive (
		id INTEGER PRIMARY KEY,
		kept_id INTEGER NOT NULL,
		old_code_hash TEXT NOT NULL,
		old_upstream_id INTEGER,
		new_upstream_id INTEGER,
		old_code_prefix TEXT,
		new_code_prefix TEXT,
		plan TEXT,
		order_id INTEGER,
		order_status TEXT,
		ip TEXT,
		created_at DATETIME,
		archived_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("建归档表: %w", err)
	}
	return dedupeAgentExchanges()
}

// dedupeAgentExchanges 一个事务内：每张旧码保留最早一行，其余归档并写审计后删除，再建唯一索引。
// 中途崩溃整个回滚，下次启动重来，不丢数据。
func dedupeAgentExchanges() error {
	tx, err := DB.Begin()
	if err != nil {
		return fmt.Errorf("开启换码去重事务: %w", err)
	}
	defer tx.Rollback()

	type dup struct {
		id, kept, oldID, newID, orderID int64
		ip, at                          string
	}
	rows, err := tx.Query(`
		WITH ranked AS (
			SELECT id, old_upstream_id, new_upstream_id, order_id, ip, created_at,
				ROW_NUMBER() OVER w AS rn, FIRST_VALUE(id) OVER w AS kept
			FROM agent_cdk_exchanges
			WINDOW w AS (PARTITION BY old_code_hash ORDER BY created_at, id)
		)
		SELECT id, kept, COALESCE(old_upstream_id, 0), COALESCE(new_upstream_id, 0), COALESCE(order_id, 0),
			COALESCE(ip, ''), COALESCE(strftime('%Y-%m-%d %H:%M:%S', created_at), '')
		FROM ranked WHERE rn > 1 ORDER BY id`)
	if err != nil {
		return fmt.Errorf("查换码重复行: %w", err)
	}
	var dups []dup
	for rows.Next() {
		var d dup
		if err := rows.Scan(&d.id, &d.kept, &d.oldID, &d.newID, &d.orderID, &d.ip, &d.at); err != nil {
			rows.Close()
			return fmt.Errorf("读换码重复行: %w", err)
		}
		dups = append(dups, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("读换码重复行: %w", err)
	}
	rows.Close()

	for _, d := range dups {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO agent_cdk_exchange_dup_archive
			(id, kept_id, old_code_hash, old_upstream_id, new_upstream_id, old_code_prefix, new_code_prefix, plan, order_id, order_status, ip, created_at)
			SELECT id, ?, old_code_hash, old_upstream_id, new_upstream_id, old_code_prefix, new_code_prefix, plan, order_id, order_status, ip, created_at
			FROM agent_cdk_exchanges WHERE id = ?`, d.kept, d.id); err != nil {
			return fmt.Errorf("归档换码重复行 %d: %w", d.id, err)
		}
		detail := fmt.Sprintf("kept_id=%d removed_id=%d old_cdk=%d new_cdk=%d order=%d ip=%s at=%s",
			d.kept, d.id, d.oldID, d.newID, d.orderID, d.ip, d.at)
		if _, err := tx.Exec(`INSERT INTO admin_audit_logs (username, action, detail, ip) VALUES ('system:migration', 'agent_exchange_dedupe', ?, '')`, detail); err != nil {
			return fmt.Errorf("写换码去重审计: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM agent_cdk_exchanges WHERE id = ?`, d.id); err != nil {
			return fmt.Errorf("删除换码重复行 %d: %w", d.id, err)
		}
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_ex_old_hash ON agent_cdk_exchanges(old_code_hash)`); err != nil {
		return fmt.Errorf("建换码唯一索引: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交换码去重事务: %w", err)
	}
	if len(dups) > 0 {
		log.Printf("[agent-exchange] 警告：发现 %d 行重复换码记录，说明这些旧码曾被多发过新码。已保留每张旧码最早一行，其余移入 agent_cdk_exchange_dup_archive，请核对多发的码（new_upstream_id）", len(dups))
	}
	return nil
}

// AgentCDKExchangeState 这张旧码现有占位的状态；没有占位返回空串。
func AgentCDKExchangeState(codeHash string) string {
	if DB == nil || codeHash == "" {
		return ""
	}
	var st string
	if err := DB.QueryRow(`SELECT state FROM agent_cdk_exchanges WHERE old_code_hash = ?`, codeHash).Scan(&st); err != nil {
		return ""
	}
	return st
}

// AgentExchangeReservation 占位时写入的信息。
type AgentExchangeReservation struct {
	OldHash     string
	OldID       int64
	OldPrefix   string
	Plan        string
	OrderID     int64
	OrderStatus string
	IP          string
	PasswordFP  string
}

// ReserveAgentCDKExchange 在调上游之前原子占位：一条语句里判断当日额度并插入。
// SQLite 的写语句开始执行就持有写锁，并发请求绕不过额度，也绕不过 old_code_hash 唯一约束。
//
// 额度按北京日统计同一密码的占位数；升级前的旧行没有指纹（”），也算进去，宁可多算不可漏算。limit 必须大于 0。
func ReserveAgentCDKExchange(r AgentExchangeReservation, limit int, now time.Time) (int64, error) {
	if DB == nil {
		return 0, errors.New("db not init")
	}
	if limit <= 0 {
		return 0, fmt.Errorf("代理换码每日上限必须大于 0，当前 %d", limit)
	}
	bj := now.In(agentExchangeDayZone)
	dayStart := time.Date(bj.Year(), bj.Month(), bj.Day(), 0, 0, 0, 0, agentExchangeDayZone).UTC().Format(agentExchangeTimeLayout)
	nowText := now.UTC().Format(agentExchangeTimeLayout)
	res, err := DB.Exec(`
		INSERT INTO agent_cdk_exchanges
			(old_code_hash, old_upstream_id, old_code_prefix, plan, order_id, order_status, ip, state, password_fp, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?
		WHERE (SELECT COUNT(*) FROM agent_cdk_exchanges
			WHERE created_at >= ? AND (password_fp = ? OR password_fp = '')) < ?
		ON CONFLICT(old_code_hash) DO NOTHING`,
		r.OldHash, r.OldID, r.OldPrefix, r.Plan, r.OrderID, r.OrderStatus, r.IP, r.PasswordFP, nowText, nowText,
		dayStart, r.PasswordFP, limit)
	if err != nil {
		return 0, fmt.Errorf("换码占位: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("换码占位: %w", err)
	}
	if n > 0 {
		return res.LastInsertId()
	}
	var exists int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM agent_cdk_exchanges WHERE old_code_hash = ?`, r.OldHash).Scan(&exists); err != nil {
		return 0, fmt.Errorf("换码占位: %w", err)
	}
	if exists > 0 {
		return 0, ErrAgentExchangeExists
	}
	return 0, ErrAgentExchangeDailyLimit
}

// FinishAgentCDKExchange 新码已发出，记下新码，进入 disabling（旧码待禁用）。
// 结果不明的占位可能事后被证实成功，所以 unknown 也能转过来。旧码确认禁用后再调 CompleteAgentCDKExchangeDisable 转 done。
func FinishAgentCDKExchange(id, newID int64, newPrefix string) error {
	res, err := DB.Exec(`UPDATE agent_cdk_exchanges
		SET state = 'disabling', new_upstream_id = ?, new_code_prefix = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND state IN ('pending', 'unknown')`, newID, newPrefix, id)
	return affectedOne(res, err)
}

// CompleteAgentCDKExchangeDisable 旧码已确认禁用：disabling → done。
func CompleteAgentCDKExchangeDisable(id int64) error {
	res, err := DB.Exec(`UPDATE agent_cdk_exchanges SET state = 'done', updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND state = 'disabling'`, id)
	return affectedOne(res, err)
}

// ReleaseAgentCDKExchange 发码确定失败时释放占位，让代理可以重试。只释放 pending：
// 已转人工的占位（比如请求拖太久被升级了）不能被自动释放。
func ReleaseAgentCDKExchange(id int64) error {
	res, err := DB.Exec(`DELETE FROM agent_cdk_exchanges WHERE id = ? AND state = 'pending'`, id)
	return affectedOne(res, err)
}

// MarkAgentCDKExchangeUnknown 发码结果不明：占位保留，转人工。
func MarkAgentCDKExchangeUnknown(id int64, reason string) error {
	res, err := DB.Exec(`UPDATE agent_cdk_exchanges SET state = 'unknown', note = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND state = 'pending'`, reason, id)
	return affectedOne(res, err)
}

func affectedOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAgentExchangeState
	}
	return nil
}

// AgentExchange 给后台看的一行换码记录。不含旧码哈希和密码指纹。
type AgentExchange struct {
	ID            int64  `json:"id"`
	OldUpstreamID int64  `json:"old_cdk_id"`
	NewUpstreamID int64  `json:"new_cdk_id"`
	OldCodePrefix string `json:"old_code_prefix"`
	NewCodePrefix string `json:"new_code_prefix"`
	Plan          string `json:"plan"`
	OrderID       int64  `json:"order_id"`
	OrderStatus   string `json:"order_status"`
	IP            string `json:"ip"`
	State         string `json:"state"`
	Note          string `json:"note"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

const agentExchangeColumns = `id, COALESCE(old_upstream_id, 0), COALESCE(new_upstream_id, 0), COALESCE(old_code_prefix, ''),
	COALESCE(new_code_prefix, ''), COALESCE(plan, ''), COALESCE(order_id, 0), COALESCE(order_status, ''), COALESCE(ip, ''),
	state, note, COALESCE(strftime('%Y-%m-%d %H:%M:%S', created_at), ''), COALESCE(strftime('%Y-%m-%d %H:%M:%S', updated_at), '')`

func scanAgentExchange(s interface{ Scan(...any) error }) (AgentExchange, error) {
	var e AgentExchange
	err := s.Scan(&e.ID, &e.OldUpstreamID, &e.NewUpstreamID, &e.OldCodePrefix, &e.NewCodePrefix, &e.Plan, &e.OrderID,
		&e.OrderStatus, &e.IP, &e.State, &e.Note, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}

// GetAgentExchange 按 id 取一行。
func GetAgentExchange(id int64) (AgentExchange, error) {
	e, err := scanAgentExchange(DB.QueryRow(`SELECT `+agentExchangeColumns+` FROM agent_cdk_exchanges WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrAgentExchangeNotFound
	}
	return e, err
}

// ListAgentExchanges 按状态列出换码记录，最新的在前；state 为空时列出全部。
func ListAgentExchanges(state string, limit int) ([]AgentExchange, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `SELECT ` + agentExchangeColumns + ` FROM agent_cdk_exchanges`
	args := []any{}
	if state = strings.TrimSpace(state); state != "" {
		q += ` WHERE state = ?`
		args = append(args, state)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentExchange{}
	for rows.Next() {
		e, err := scanAgentExchange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountAgentExchanges 某状态的记录数。
func CountAgentExchanges(state string) (int, error) {
	var n int
	err := DB.QueryRow(`SELECT COUNT(*) FROM agent_cdk_exchanges WHERE state = ?`, state).Scan(&n)
	return n, err
}

// EscalateStaleAgentExchanges 把卡在 pending 超过 olderThan 的占位升级成 unknown（进程在发码途中崩溃、或收尾写库失败），
// 返回被升级的记录。绝不自动释放：上游可能已经发了码。
func EscalateStaleAgentExchanges(now time.Time, olderThan time.Duration) ([]AgentExchange, error) {
	cut := now.Add(-olderThan).UTC().Format(agentExchangeTimeLayout)
	rows, err := DB.Query(`SELECT `+agentExchangeColumns+` FROM agent_cdk_exchanges WHERE state = 'pending' AND created_at < ?`, cut)
	if err != nil {
		return nil, err
	}
	var stale []AgentExchange
	for rows.Next() {
		e, err := scanAgentExchange(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		stale = append(stale, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	out := make([]AgentExchange, 0, len(stale))
	for _, e := range stale {
		// 条件更新：读完之后这行可能已经被正在收尾的请求定论了
		err := MarkAgentCDKExchangeUnknown(e.ID, "占位超时未收尾，结果不明")
		if errors.Is(err, ErrAgentExchangeState) {
			continue
		}
		if err != nil {
			return out, err
		}
		e.State = AgentExchangeUnknown
		out = append(out, e)
	}
	return out, nil
}

// ResolveAgentExchangeReleased 人工确认上游没发码：删除占位，允许重试。只能处理 unknown，审计由调用方写。
func ResolveAgentExchangeReleased(id int64) error {
	return resolveAgentExchange(id, func() (sql.Result, error) {
		return DB.Exec(`DELETE FROM agent_cdk_exchanges WHERE id = ? AND state = 'unknown'`, id)
	})
}

// ResolveAgentExchangeIssued 人工确认上游已发码：记下新码，占位变成 disabling（只补记账，不去上游禁用旧码）。
// 只能处理 unknown，审计由调用方写。
func ResolveAgentExchangeIssued(id, newID int64, note string) error {
	return resolveAgentExchange(id, func() (sql.Result, error) {
		return DB.Exec(`UPDATE agent_cdk_exchanges SET state = 'disabling', new_upstream_id = ?, note = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ? AND state = 'unknown'`, newID, note, id)
	})
}

func resolveAgentExchange(id int64, apply func() (sql.Result, error)) error {
	res, err := apply()
	if err := affectedOne(res, err); !errors.Is(err, ErrAgentExchangeState) {
		return err
	}
	if _, err := GetAgentExchange(id); err != nil {
		return err
	}
	return ErrAgentExchangeState
}
