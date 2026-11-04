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
	// 单据属于哪家供卡型服务商，引擎按它取卡池。加列之前只有 MarsWallex 一家供卡
	if err := addProviderColumn("pay_orders", "marswallex"); err != nil {
		return err
	}
	return addPayKindRenewalColumns()
}

// 付款单的种类（kind 列）。
const (
	PayKindSubscription = "subscription" // 订阅开通单
	PayKindCredit       = "credit"       // Codex 点数单
)

// payOrderColumn 是一列要补的 pay_orders 列：backfill 非空时在加列的同一个事务里执行，只发生一次。
type payOrderColumn struct{ name, ddl, backfill string }

// payKindRenewalColumns 是点数单与开通后关闭续费要用的列（设计 2026-11-03 §3.2、§5.2）。
//   - kind、credit_quantity：单据种类与点数数量；加列之前只有订阅开通单。
//   - renewal_*：开通成功后关闭续费的进度。旧库里已经成功的单，会话早已清空，关不了续费，加列时一次性记为 skipped。
//   - card_saved、saved_cards_before、credit_balance_before：点数单的已存卡核对结果与付款前的快照。
//   - access_until：关闭续费核实时 ChatGPT 给的权益到期时间（Unix 秒，可为空）。
var payKindRenewalColumns = []payOrderColumn{
	{"kind", `kind TEXT NOT NULL DEFAULT 'subscription'`, ""},
	{"credit_quantity", `credit_quantity INTEGER NOT NULL DEFAULT 0`, ""},
	{"renewal_state", `renewal_state TEXT NOT NULL DEFAULT ''`, `UPDATE pay_orders SET renewal_state = 'skipped' WHERE state = 'succeeded'`},
	{"renewal_attempts", `renewal_attempts INTEGER NOT NULL DEFAULT 0`, ""},
	{"renewal_checked_at", `renewal_checked_at INTEGER`, ""},
	{"renewal_next_at", `renewal_next_at INTEGER`, ""},
	{"card_saved", `card_saved TEXT NOT NULL DEFAULT ''`, ""},
	{"saved_cards_before", `saved_cards_before TEXT NOT NULL DEFAULT ''`, ""},
	{"credit_balance_before", `credit_balance_before TEXT NOT NULL DEFAULT ''`, ""},
	{"access_until", `access_until INTEGER`, ""},
}

// addPayKindRenewalColumns 逐列补齐 payKindRenewalColumns。每列先查 pragma，已存在就跳过，所以可以重复执行；
// 补值和加列在同一个事务里，之后再启动不会重复补。
func addPayKindRenewalColumns() error {
	for _, c := range payKindRenewalColumns {
		if err := addPayOrderColumn(c); err != nil {
			return err
		}
	}
	return nil
}

func addPayOrderColumn(c payOrderColumn) error {
	var n int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('pay_orders') WHERE name = ?`, c.name).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE pay_orders ADD COLUMN ` + c.ddl); err != nil {
		return err
	}
	if c.backfill != "" {
		if _, err := tx.Exec(c.backfill); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// addProviderColumn 给 table 加 provider 列（非空文本，默认空串），并在同一个事务里把已有的行补成 backfill。
// 列已经存在就什么都不做，所以可以在旧库上重复执行：补值只在加列的那一次发生。
// 之后写入的行必须自己带 provider；空值不会在下次启动被悄悄改成某一家，按「服务商未知」处理。
func addProviderColumn(table, backfill string) error {
	var n int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = 'provider'`, table).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN provider TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE `+table+` SET provider = ?`, backfill); err != nil {
		return err
	}
	return tx.Commit()
}
