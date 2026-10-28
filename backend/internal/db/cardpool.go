package db

// MigrateCardPool 创建卡池相关表与索引，并给 Webhook 事件表补处理字段、给验证码表补来源事件列。可重复执行。
func MigrateCardPool() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS mw_cardholders (
	card_holder_id TEXT PRIMARY KEY,
	first_name TEXT NOT NULL DEFAULT '', last_name TEXT NOT NULL DEFAULT '',
	country_code TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT '',
	remark TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '',
	synced_at INTEGER, created_at INTEGER NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS mw_cards (
	card_id TEXT PRIMARY KEY,
	card_holder_id TEXT NOT NULL DEFAULT '', product_id TEXT NOT NULL,
	card_label TEXT NOT NULL DEFAULT '', card_country TEXT NOT NULL DEFAULT '',
	currency TEXT NOT NULL DEFAULT '', last4 TEXT NOT NULL DEFAULT '', expiry TEXT NOT NULL DEFAULT '',
	platform_status TEXT NOT NULL DEFAULT '',
	available_cents INTEGER NOT NULL DEFAULT 0, frozen_cents INTEGER NOT NULL DEFAULT 0, balance_synced_at INTEGER,
	source TEXT NOT NULL, pool_state TEXT NOT NULL,
	lease_token TEXT NOT NULL DEFAULT '', lease_owner TEXT NOT NULL DEFAULT '',
	lease_expires_at INTEGER, lease_sensitive INTEGER NOT NULL DEFAULT 0,
	use_count INTEGER NOT NULL DEFAULT 0, last_sensitive_at INTEGER,
	retire_reason TEXT NOT NULL DEFAULT '', disabled_reason TEXT NOT NULL DEFAULT '',
	frozen_at INTEGER, cancel_after INTEGER, next_action_at INTEGER,
	created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_mw_cards_pick ON mw_cards(product_id, pool_state)`,
		`CREATE TABLE IF NOT EXISTS mw_card_orders (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	out_order_no TEXT NOT NULL UNIQUE, kind TEXT NOT NULL,
	card_id TEXT NOT NULL DEFAULT '', card_holder_id TEXT NOT NULL DEFAULT '', product_id TEXT NOT NULL DEFAULT '',
	amount_cents INTEGER NOT NULL DEFAULT 0, currency TEXT NOT NULL DEFAULT '', target_status TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL, platform_txn_id TEXT NOT NULL DEFAULT '',
	ever_unknown INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0,
	next_check_at INTEGER, first_sent_at INTEGER, last_error TEXT NOT NULL DEFAULT '',
	requested_by TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '', alerted_stale INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, finished_at INTEGER
)`,
		`CREATE INDEX IF NOT EXISTS idx_mw_orders_due ON mw_card_orders(state, next_check_at)`,
		`CREATE INDEX IF NOT EXISTS idx_mw_orders_card ON mw_card_orders(card_id, created_at)`,
		// 同一张卡同一时刻最多一条在途的转入/转出单
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_mw_orders_card_inflight ON mw_card_orders(card_id)
	WHERE kind IN ('recharge','withdraw') AND state IN ('pending','processing','unknown','needs_review')`,
		`CREATE TABLE IF NOT EXISTS mw_card_transactions (
	transaction_id TEXT PRIMARY KEY, kind TEXT NOT NULL, card_id TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT '', decline_reason TEXT NOT NULL DEFAULT '',
	txn_amount TEXT NOT NULL DEFAULT '', txn_currency TEXT NOT NULL DEFAULT '',
	billing_amount TEXT NOT NULL DEFAULT '', billing_currency TEXT NOT NULL DEFAULT '',
	merchant_name TEXT NOT NULL DEFAULT '', origin_transaction_id TEXT NOT NULL DEFAULT '',
	auth_transaction_id TEXT NOT NULL DEFAULT '', occurred_at INTEGER, updated_at INTEGER NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_mw_txn_card ON mw_card_transactions(card_id, occurred_at)`,
		`CREATE TABLE IF NOT EXISTS mw_3ds_otps (
	id INTEGER PRIMARY KEY AUTOINCREMENT, card_id TEXT NOT NULL,
	otp_sealed TEXT NOT NULL DEFAULT '', merchant_name TEXT NOT NULL DEFAULT '',
	amount TEXT NOT NULL DEFAULT '', currency TEXT NOT NULL DEFAULT '',
	received_at INTEGER NOT NULL, consumed_at INTEGER, event_id INTEGER
)`,
		`CREATE INDEX IF NOT EXISTS idx_mw_otp_card ON mw_3ds_otps(card_id, received_at)`,
		`CREATE TABLE IF NOT EXISTS mw_card_outcomes (
	id INTEGER PRIMARY KEY AUTOINCREMENT, card_id TEXT NOT NULL, product_id TEXT NOT NULL,
	plan_key TEXT NOT NULL, order_ref TEXT NOT NULL DEFAULT '', email_norm TEXT NOT NULL,
	result TEXT NOT NULL, error_code TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_mw_outcome_prod ON mw_card_outcomes(product_id, plan_key, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_mw_outcome_card ON mw_card_outcomes(card_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS mw_product_health (
	product_id TEXT NOT NULL, plan_key TEXT NOT NULL,
	suspended_at INTEGER, reason TEXT NOT NULL DEFAULT '',
	resumed_at INTEGER, resumed_by TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (product_id, plan_key)
)`,
	}
	for _, s := range stmts {
		if _, err := DB.Exec(s); err != nil {
			return err
		}
	}
	// 补列（仅在列缺失时添加）：Webhook 事件表的处理字段；验证码表的来源事件（旧库建表时没有）
	cols := []struct{ table, name, ddl string }{
		{"marswallex_webhook_events", "process_attempts", "INTEGER NOT NULL DEFAULT 0"},
		{"marswallex_webhook_events", "next_attempt_at", "INTEGER"},
		{"marswallex_webhook_events", "process_note", "TEXT NOT NULL DEFAULT ''"},
		{"mw_3ds_otps", "event_id", "INTEGER"},
	}
	for _, c := range cols {
		var n int
		if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, c.table, c.name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := DB.Exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.name + ` ` + c.ddl); err != nil {
			return err
		}
	}
	// 依赖补出来的列，放在补列之后
	idx := []string{
		// 验证码按 Webhook 事件去重：同一事件重复处理只存一条；event_id 为 NULL 不受限
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_mw_otp_event ON mw_3ds_otps(event_id)`,
		// 兜底扫描只看未处理的事件
		`CREATE INDEX IF NOT EXISTS idx_mw_webhook_unprocessed ON marswallex_webhook_events(id) WHERE processed_at IS NULL`,
	}
	for _, s := range idx {
		if _, err := DB.Exec(s); err != nil {
			return err
		}
	}
	return nil
}
