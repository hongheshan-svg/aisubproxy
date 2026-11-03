package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func TestMigratePayIdempotent(t *testing.T) {
	dbtest.Open(t)
	if err := db.MigratePay(); err != nil {
		t.Fatal(err)
	}
	if err := db.MigratePay(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('pay_orders') WHERE name IN ('page_outcome','session_sealed')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("列检查失败 n=%d err=%v", n, err)
	}
}

// 旧库升级：点数与关闭续费的列逐列补上；旧的成功单记为 skipped（会话早已清空，关不了续费），其他状态的单不动。
// 重复执行不出错、不再补值：补值之后写入的成功单保持默认的空串。
func TestMigratePayKindRenewalColumnsOnLegacyDB(t *testing.T) {
	c, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy-renewal.db"))
	if err != nil {
		t.Fatal(err)
	}
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	for _, s := range []string{
		legacyPayOrdersDDL,
		`INSERT INTO pay_orders (order_no, source, source_ref, dry_run, account_email, state, created_at, updated_at)
			VALUES ('PO-OLD-OK', 'admin_test', 'admin', 0, 'a@example.com', 'succeeded', 1, 1),
			       ('PO-OLD-REVIEW', 'cdk', '7', 0, 'b@example.com', 'needs_review', 1, 1),
			       ('PO-OLD-FAIL', 'cdk', '8', 0, 'c@example.com', 'failed', 1, 1),
			       ('PO-OLD-PREVIEW', 'admin_test', 'admin', 1, 'd@example.com', 'previewed', 1, 1)`,
	} {
		if _, err := c.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	type row struct {
		kind, renewal, saved string
		qty, attempts        int
		checked, next        sql.NullInt64
	}
	read := func(no string) row {
		t.Helper()
		var r row
		if err := c.QueryRow(`SELECT kind, renewal_state, card_saved, credit_quantity, renewal_attempts, renewal_checked_at, renewal_next_at
			FROM pay_orders WHERE order_no = ?`, no).Scan(&r.kind, &r.renewal, &r.saved, &r.qty, &r.attempts, &r.checked, &r.next); err != nil {
			t.Fatal(err)
		}
		return r
	}
	for round := 1; round <= 3; round++ {
		if err := db.MigratePay(); err != nil {
			t.Fatalf("第 %d 次迁移: %v", round, err)
		}
		if r := read("PO-OLD-OK"); r.kind != "subscription" || r.renewal != "skipped" || r.qty != 0 || r.attempts != 0 || r.checked.Valid || r.next.Valid || r.saved != "" {
			t.Fatalf("第 %d 次: 旧成功单 %+v", round, r)
		}
		for _, no := range []string{"PO-OLD-REVIEW", "PO-OLD-FAIL", "PO-OLD-PREVIEW"} {
			if r := read(no); r.kind != "subscription" || r.renewal != "" {
				t.Fatalf("第 %d 次: 旧单 %s 不该补续费状态 %+v", round, no, r)
			}
		}
		if round == 1 {
			// 补值之后才成功的单（例如待人工的旧单后来定论成功）不会在下次启动被改成 skipped
			if _, err := c.Exec(`INSERT INTO pay_orders (order_no, state, created_at, updated_at) VALUES ('PO-NEW-OK', 'succeeded', 2, 2)`); err != nil {
				t.Fatal(err)
			}
		} else if r := read("PO-NEW-OK"); r.renewal != "" {
			t.Fatalf("重复迁移不能再补续费状态: %+v", r)
		}
	}
	var n int
	if err := c.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('pay_orders') WHERE name IN
		('kind','credit_quantity','renewal_state','renewal_attempts','renewal_checked_at','renewal_next_at','card_saved','saved_cards_before','credit_balance_before')`).Scan(&n); err != nil || n != 9 {
		t.Fatalf("新列应全部存在: n=%d err=%v", n, err)
	}
}
