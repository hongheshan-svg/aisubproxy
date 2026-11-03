package db

// MigratePay 创建付款单据与时间线表及索引。可重复执行。
func MigratePay() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS pay_orders (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	order_no TEXT NOT NULL UNIQUE,
	source TEXT NOT NULL DEFAULT '', source_ref TEXT NOT NULL DEFAULT '',
	dry_run INTEGER NOT NULL DEFAULT 0,
	plan_key TEXT NOT NULL DEFAULT '', region TEXT NOT NULL DEFAULT '', currency TEXT NOT NULL DEFAULT '',
	cap_minor INTEGER NOT NULL DEFAULT 0, fund_cents INTEGER NOT NULL DEFAULT 0,
	account_email TEXT NOT NULL DEFAULT '', account_id TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL, session_sealed TEXT NOT NULL DEFAULT '',
	checkout_session_id TEXT NOT NULL DEFAULT '', checkout_url TEXT NOT NULL DEFAULT '',
	amount_minor INTEGER NOT NULL DEFAULT 0, amount_text TEXT NOT NULL DEFAULT '',
	card_id TEXT NOT NULL DEFAULT '', card_last4 TEXT NOT NULL DEFAULT '', lease_token TEXT NOT NULL DEFAULT '',
	fund_order_no TEXT NOT NULL DEFAULT '', billing_state TEXT NOT NULL DEFAULT '',
	page_outcome TEXT NOT NULL DEFAULT '',
	submitted_at INTEGER,
	failure TEXT NOT NULL DEFAULT '', error_code TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '',
	verify_until INTEGER, next_check_at INTEGER,
	resolved_by TEXT NOT NULL DEFAULT '', resolve_note TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, finished_at INTEGER
)`,
		`CREATE INDEX IF NOT EXISTS idx_pay_orders_state ON pay_orders(state)`,
		// 付款熔断每轮派发都按结束时间扫真实付款单
		`CREATE INDEX IF NOT EXISTS idx_pay_orders_finished ON pay_orders(finished_at) WHERE dry_run = 0`,
		// 同一邮箱同一时刻最多一张未终态单据
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_pay_orders_inflight ON pay_orders(account_email)
	WHERE state NOT IN ('succeeded','failed','needs_review','previewed')`,
		`CREATE TABLE IF NOT EXISTS pay_order_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	order_no TEXT NOT NULL, at INTEGER NOT NULL,
	state TEXT NOT NULL DEFAULT '', level TEXT NOT NULL DEFAULT '',
	message TEXT NOT NULL DEFAULT '', screenshot TEXT NOT NULL DEFAULT ''
)`,
		`CREATE INDEX IF NOT EXISTS idx_pay_events_order ON pay_order_events(order_no)`,
	}
	for _, s := range stmts {
		if _, err := DB.Exec(s); err != nil {
			return err
		}
	}
	return nil
}
