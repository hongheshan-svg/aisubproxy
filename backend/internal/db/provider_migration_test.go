package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 加 provider 列之前的两张表（与改造前的建表语句一致）。
const (
	legacyPayOrdersDDL = `CREATE TABLE pay_orders (
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
)`
	legacyBindingsDDL = `CREATE TABLE cdk_session_bindings (
	cdk_code TEXT PRIMARY KEY,
	session_payload TEXT NOT NULL,
	redemption_token TEXT,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
)`
)

// 旧库升级：旧 pay_orders 补成 marswallex、旧绑定补成 zovocard；重复执行不出错、不改数据；
// 补值只发生在加列那一次，之后写入的空 provider 不会被下次启动悄悄改成某一家。
func TestMigrateProviderColumnsOnLegacyDB(t *testing.T) {
	c, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })

	for _, s := range []string{
		legacyPayOrdersDDL,
		`INSERT INTO pay_orders (order_no, source, source_ref, dry_run, account_email, state, resolved_by, created_at, updated_at)
			VALUES ('PO-OLD-1', 'admin_test', 'admin', 0, 'a@example.com', 'succeeded', '', 1, 1),
			       ('PO-OLD-2', 'cdk', '7', 0, 'b@example.com', 'needs_review', '', 1, 1)`,
		legacyBindingsDDL,
		`INSERT INTO cdk_session_bindings (cdk_code, session_payload, redemption_token) VALUES ('ZC-OLD-1', '', 'rt-old')`,
		// 只有旧设置项
		`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`INSERT INTO site_settings (key, value) VALUES ('cdk_default_provider', 'marswallex')`,
	} {
		if _, err := c.Exec(s); err != nil {
			t.Fatal(err)
		}
	}

	providers := func(table, key, id string) string {
		t.Helper()
		var p string
		if err := c.QueryRow(`SELECT provider FROM `+table+` WHERE `+key+` = ?`, id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for round := 1; round <= 3; round++ {
		if err := db.Migrate(); err != nil {
			t.Fatalf("第 %d 次迁移: %v", round, err)
		}
		if p := providers("pay_orders", "order_no", "PO-OLD-1"); p != "marswallex" {
			t.Fatalf("第 %d 次: 旧单 %q", round, p)
		}
		if p := providers("pay_orders", "order_no", "PO-OLD-2"); p != "marswallex" {
			t.Fatalf("第 %d 次: 旧单 %q", round, p)
		}
		if p := providers("cdk_session_bindings", "cdk_code", "ZC-OLD-1"); p != "zovocard" {
			t.Fatalf("第 %d 次: 旧绑定 %q", round, p)
		}
		if round == 1 {
			// 迁移之后写入的空 provider 行（例如回滚到旧版本程序时写的）保持为空，按服务商未知处理
			if _, err := c.Exec(`INSERT INTO pay_orders (order_no, state, created_at, updated_at) VALUES ('PO-NEW-EMPTY', 'failed', 2, 2)`); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Exec(`INSERT INTO cdk_session_bindings (cdk_code, session_payload) VALUES ('ZC-NEW-EMPTY', '')`); err != nil {
				t.Fatal(err)
			}
		} else {
			if p := providers("pay_orders", "order_no", "PO-NEW-EMPTY"); p != "" {
				t.Fatalf("重复迁移不能补后来的空值: %q", p)
			}
			if p := providers("cdk_session_bindings", "cdk_code", "ZC-NEW-EMPTY"); p != "" {
				t.Fatalf("重复迁移不能补后来的空值: %q", p)
			}
		}
	}
	// 旧的成功单会话早已清空、续费没关，迁移时记为 skipped，不再能当订阅开通码的硬门槛（设计 2026-11-03 §3.3）；也不是点数单
	for _, kind := range []string{db.PayKindSubscription, db.PayKindCredit} {
		if ok, err := db.HasRealSucceededPayOrder(localCtx, "marswallex", kind); err != nil || ok {
			t.Fatal(kind, ok, err)
		}
	}
	if v, err := db.GetSetting("cdk_default_provider"); err != nil || v != "marswallex" {
		t.Fatal(v, err)
	}
	if v, err := db.GetSetting("cdk_provider_by_product"); err != nil || v != "" {
		t.Fatal("迁移不写产品配置", v, err)
	}
}
