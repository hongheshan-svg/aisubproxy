package db_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func TestMigrateCardPoolIdempotent(t *testing.T) {
	dbtest.Open(t)
	for i := 0; i < 2; i++ {
		if err := db.MigrateCardPool(); err != nil {
			t.Fatalf("第 %d 次迁移: %v", i+1, err)
		}
	}
	var n int
	err := db.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN
		('mw_cardholders','mw_cards','mw_card_orders','mw_card_transactions','mw_3ds_otps','mw_card_outcomes','mw_product_health')`).Scan(&n)
	if err != nil || n != 7 {
		t.Fatalf("新表数量 = %d, err=%v", n, err)
	}
}

// 已有数据库升级（C1-04）：旧的卡表没有「进入回收的时间」，迁移补上这一列，旧行为空（汇总按 updated_at 兜底），可重复执行。
func TestMigrateCardPoolAddsReleasedAt(t *testing.T) {
	dbtest.Open(t)
	if _, err := db.DB.Exec(`ALTER TABLE mw_cards DROP COLUMN released_at`); err != nil {
		t.Fatalf("模拟旧库：%v", err)
	}
	if _, err := db.DB.Exec(`INSERT INTO mw_cards (card_id, product_id, source, pool_state, created_at, updated_at)
		VALUES ('old1', 'p1', 'opened', 'releasing', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.MigrateCardPool(); err != nil {
			t.Fatalf("第 %d 次迁移: %v", i+1, err)
		}
	}
	var v sql.NullInt64
	if err := db.DB.QueryRow(`SELECT released_at FROM mw_cards WHERE card_id = 'old1'`).Scan(&v); err != nil {
		t.Fatalf("迁移后应有 released_at 列: %v", err)
	}
	if v.Valid {
		t.Fatalf("旧行的 released_at 应为空: %v", v)
	}
}

func TestWebhookEventColumnsAdded(t *testing.T) {
	c, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	if err := db.MigrateMarsWallex(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertMarsWallexWebhookEvent("m1", "T", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateCardPool(); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var note string
	var next sql.NullInt64
	err = db.DB.QueryRow(`SELECT process_attempts, process_note, next_attempt_at FROM marswallex_webhook_events WHERE message_id='m1'`).
		Scan(&attempts, &note, &next)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || note != "" || next.Valid {
		t.Fatalf("attempts=%d note=%q next=%v", attempts, note, next)
	}
}

func TestInflightTransferUniquePerCard(t *testing.T) {
	dbtest.Open(t)
	ins := func(no, kind, card, state string) error {
		_, err := db.DB.Exec(`INSERT INTO mw_card_orders (out_order_no, kind, card_id, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, 1)`, no, kind, card, state)
		return err
	}
	if err := ins("o1", "recharge", "c1", "processing"); err != nil {
		t.Fatal(err)
	}
	if err := ins("o2", "withdraw", "c1", "pending"); err == nil {
		t.Fatal("同一张卡在途资金单应被拒绝")
	}
	if err := ins("o3", "recharge", "c2", "pending"); err != nil {
		t.Fatalf("其他卡应可插入: %v", err)
	}
	if _, err := db.DB.Exec(`UPDATE mw_card_orders SET state='success' WHERE out_order_no='o1'`); err != nil {
		t.Fatal(err)
	}
	if err := ins("o4", "withdraw", "c1", "pending"); err != nil {
		t.Fatalf("在途单结束后应可再插: %v", err)
	}
	// 改状态单不受限制
	if err := ins("o5", "status", "c1", "processing"); err != nil {
		t.Fatalf("status 单不应受限: %v", err)
	}
	if err := ins("o6", "status", "c1", "processing"); err != nil {
		t.Fatalf("status 单不应受限: %v", err)
	}
	// needs_review 同样占位
	if _, err := db.DB.Exec(`UPDATE mw_card_orders SET state='needs_review' WHERE out_order_no='o4'`); err != nil {
		t.Fatal(err)
	}
	if err := ins("o7", "recharge", "c1", "pending"); err == nil {
		t.Fatal("needs_review 应继续占位")
	}
}

func TestListWebhookEventsIncludesProcessFields(t *testing.T) {
	dbtest.Open(t)
	if _, err := db.InsertMarsWallexWebhookEvent("m1", "T", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE marswallex_webhook_events SET process_attempts=3, next_attempt_at=1234, process_note='重试中' WHERE message_id='m1'`); err != nil {
		t.Fatal(err)
	}
	evs, err := db.ListMarsWallexWebhookEvents(10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("evs=%v err=%v", evs, err)
	}
	e := evs[0]
	if e.ProcessAttempts != 3 || e.NextAttemptAt == nil || *e.NextAttemptAt != 1234 || e.ProcessNote != "重试中" {
		t.Fatalf("%+v", e)
	}
}

// 未处理事件的部分索引（兜底扫描用）。
func TestWebhookUnprocessedPartialIndex(t *testing.T) {
	dbtest.Open(t)
	var sqlText string
	if err := db.DB.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_mw_webhook_unprocessed'`).Scan(&sqlText); err != nil {
		t.Fatalf("索引不存在: %v", err)
	}
	if !strings.Contains(sqlText, "processed_at IS NULL") {
		t.Fatalf("应为部分索引: %s", sqlText)
	}
}

// 验证码表按事件去重：event_id 唯一（NULL 不受限）；旧库（没有 event_id 列）迁移时补列。
func TestOTPEventIDColumnAndUniqueIndex(t *testing.T) {
	c, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	if err := db.MigrateMarsWallex(); err != nil {
		t.Fatal(err)
	}
	// 旧版表结构
	if _, err := db.DB.Exec(`CREATE TABLE mw_3ds_otps (
	id INTEGER PRIMARY KEY AUTOINCREMENT, card_id TEXT NOT NULL,
	otp_sealed TEXT NOT NULL DEFAULT '', merchant_name TEXT NOT NULL DEFAULT '',
	amount TEXT NOT NULL DEFAULT '', currency TEXT NOT NULL DEFAULT '',
	received_at INTEGER NOT NULL, consumed_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO mw_3ds_otps (card_id, received_at) VALUES ('c0', 1)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.MigrateCardPool(); err != nil {
			t.Fatalf("第 %d 次迁移: %v", i+1, err)
		}
	}
	ins := func(ev any) error {
		_, err := db.DB.Exec(`INSERT INTO mw_3ds_otps (card_id, received_at, event_id) VALUES ('c1', 1, ?)`, ev)
		return err
	}
	if err := ins(7); err != nil {
		t.Fatal(err)
	}
	if err := ins(7); err == nil {
		t.Fatal("同一事件不应写入两条")
	}
	if err := ins(nil); err != nil {
		t.Fatalf("event_id 为 NULL 不受唯一约束: %v", err)
	}
	if err := ins(nil); err != nil {
		t.Fatalf("event_id 为 NULL 不受唯一约束: %v", err)
	}
}

func TestMigrateCardPoolWalletIdempotent(t *testing.T) {
	c, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	if err := db.MigrateMarsWallex(); err != nil {
		t.Fatal(err)
	}
	// 子项目 2 的旧表（无钱包列）与一条旧单据
	for _, s := range []string{
		`CREATE TABLE mw_cardholders (card_holder_id TEXT PRIMARY KEY, first_name TEXT NOT NULL DEFAULT '', last_name TEXT NOT NULL DEFAULT '',
			country_code TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT '', remark TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '',
			synced_at INTEGER, created_at INTEGER NOT NULL)`,
		`CREATE TABLE mw_card_orders (id INTEGER PRIMARY KEY AUTOINCREMENT, out_order_no TEXT NOT NULL UNIQUE, kind TEXT NOT NULL,
			card_id TEXT NOT NULL DEFAULT '', card_holder_id TEXT NOT NULL DEFAULT '', product_id TEXT NOT NULL DEFAULT '',
			amount_cents INTEGER NOT NULL DEFAULT 0, currency TEXT NOT NULL DEFAULT '', target_status TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL, platform_txn_id TEXT NOT NULL DEFAULT '', ever_unknown INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0,
			next_check_at INTEGER, first_sent_at INTEGER, last_error TEXT NOT NULL DEFAULT '', requested_by TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT '', alerted_stale INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, finished_at INTEGER)`,
		`INSERT INTO mw_card_orders (out_order_no, kind, card_id, state, created_at, updated_at) VALUES ('old1', 'recharge', 'c1', 'success', 1, 1)`,
		`INSERT INTO mw_cardholders (card_holder_id, created_at) VALUES ('h1', 1)`,
	} {
		if _, err := c.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := db.MigrateCardPool(); err != nil {
			t.Fatalf("第 %d 次迁移: %v", i+1, err)
		}
	}
	var a, b, p, d, oh string
	if err := c.QueryRow(`SELECT buy_currency, payer_account_no, payee_account_no, detail FROM mw_card_orders WHERE out_order_no='old1'`).
		Scan(&a, &b, &p, &d); err != nil {
		t.Fatal(err)
	}
	if a != "" || b != "" || p != "" || d != "" {
		t.Fatalf("旧单据新列应为空串: %q %q %q %q", a, b, p, d)
	}
	if err := c.QueryRow(`SELECT out_card_holder_id FROM mw_cardholders WHERE card_holder_id='h1'`).Scan(&oh); err != nil || oh != "" {
		t.Fatalf("out_card_holder_id=%q err=%v", oh, err)
	}
	var n int
	if err := c.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN
		('mw_accounts','mw_cardholder_submissions','uq_mw_orders_wallet_inflight')`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("新对象数量 = %d, err=%v", n, err)
	}
}

func TestWalletInflightIndex(t *testing.T) {
	dbtest.Open(t)
	ins := func(no, kind, state string) error {
		_, err := db.DB.Exec(`INSERT INTO mw_card_orders (out_order_no, kind, state, created_at, updated_at)
			VALUES (?, ?, ?, 1, 1)`, no, kind, state)
		return err
	}
	if err := ins("w1", "conversion", "unknown"); err != nil {
		t.Fatal(err)
	}
	if err := ins("w2", "conversion", "pending"); err == nil {
		t.Fatal("在途兑换单应拒绝第二笔")
	}
	if err := ins("w3", "wallet_transfer", "pending"); err != nil {
		t.Fatalf("钱包转账单应可插入: %v", err)
	}
	if _, err := db.DB.Exec(`UPDATE mw_card_orders SET state='success' WHERE out_order_no='w1'`); err != nil {
		t.Fatal(err)
	}
	if err := ins("w4", "conversion", "pending"); err != nil {
		t.Fatalf("兑换单结束后应可再插: %v", err)
	}
}
