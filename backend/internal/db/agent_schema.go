package db

import "fmt"

// MigrateAgents 建代理商相关的 7 张表和索引，并给 local_cdks 补两列。可以重复执行。
// 列和约束见设计文档第 4 节。金额存美分整数，时间存 Unix 秒。
func MigrateAgents() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS agent_tiers (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	note TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS agents (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT NOT NULL UNIQUE COLLATE NOCASE,
	display_name TEXT NOT NULL DEFAULT '',
	password_hash TEXT NOT NULL,
	must_change_password INTEGER NOT NULL DEFAULT 1,
	totp_sealed TEXT NOT NULL DEFAULT '',
	totp_last_step INTEGER NOT NULL DEFAULT 0,
	tier_id INTEGER NOT NULL,
	balance_cents INTEGER NOT NULL DEFAULT 0 CHECK (balance_cents >= 0),
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
	session_version INTEGER NOT NULL DEFAULT 0,
	note TEXT NOT NULL DEFAULT '',
	last_login_at INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS agent_products (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	code TEXT NOT NULL UNIQUE,
	provider TEXT NOT NULL,
	product TEXT NOT NULL,
	plan_key TEXT NOT NULL,
	region TEXT NOT NULL,
	name TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	sort INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (provider, product, plan_key, region)
)`,
		`CREATE TABLE IF NOT EXISTS agent_tier_prices (
	tier_id INTEGER NOT NULL,
	product_id INTEGER NOT NULL,
	price_cents INTEGER NOT NULL CHECK (price_cents > 0),
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (tier_id, product_id)
)`,
		`CREATE TABLE IF NOT EXISTS agent_price_overrides (
	agent_id INTEGER NOT NULL,
	product_id INTEGER NOT NULL,
	price_cents INTEGER NOT NULL CHECK (price_cents > 0),
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (agent_id, product_id)
)`,
		`CREATE TABLE IF NOT EXISTS agent_orders (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_id INTEGER NOT NULL,
	product_id INTEGER NOT NULL,
	product_code TEXT NOT NULL,
	provider TEXT NOT NULL,
	product TEXT NOT NULL,
	plan_key TEXT NOT NULL,
	region TEXT NOT NULL,
	product_name TEXT NOT NULL,
	quantity INTEGER NOT NULL CHECK (quantity BETWEEN 1 AND 200),
	unit_price_cents INTEGER NOT NULL,
	total_cents INTEGER NOT NULL,
	request_no TEXT NOT NULL,
	note TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	UNIQUE (agent_id, request_no)
)`,
		`CREATE TABLE IF NOT EXISTS agent_ledger (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_id INTEGER NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('purchase', 'adjust', 'topup')),
	amount_cents INTEGER NOT NULL CHECK (amount_cents <> 0),
	balance_after_cents INTEGER NOT NULL,
	order_id INTEGER NOT NULL DEFAULT 0,
	ext_ref TEXT NOT NULL DEFAULT '',
	operator TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
)`,
		// 同一个外部单号只能入账一次；一笔订单只扣一次钱
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_ledger_ext ON agent_ledger(kind, ext_ref) WHERE ext_ref <> ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_ledger_order ON agent_ledger(order_id) WHERE kind = 'purchase'`,
		`CREATE INDEX IF NOT EXISTS idx_agent_ledger_agent ON agent_ledger(agent_id, id)`,
	}
	for _, s := range stmts {
		if _, err := DB.Exec(s); err != nil {
			return fmt.Errorf("建代理商表: %w", err)
		}
	}
	// local_cdks 补列：属于哪个代理商、来自哪笔订单（0 表示后台发的码）
	cols := []struct{ name, def string }{
		{"agent_id", `INTEGER NOT NULL DEFAULT 0`},
		{"agent_order_id", `INTEGER NOT NULL DEFAULT 0`},
	}
	for _, c := range cols {
		var n int
		if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('local_cdks') WHERE name = ?`, c.name).Scan(&n); err != nil {
			return fmt.Errorf("检查 local_cdks.%s: %w", c.name, err)
		}
		if n > 0 {
			continue
		}
		if _, err := DB.Exec(`ALTER TABLE local_cdks ADD COLUMN ` + c.name + ` ` + c.def); err != nil {
			return fmt.Errorf("补列 local_cdks.%s: %w", c.name, err)
		}
	}
	// 补列之后再建索引，旧库才有这一列
	if _, err := DB.Exec(`CREATE INDEX IF NOT EXISTS idx_local_cdks_agent ON local_cdks(agent_id, id)`); err != nil {
		return fmt.Errorf("建 idx_local_cdks_agent: %w", err)
	}
	return nil
}
