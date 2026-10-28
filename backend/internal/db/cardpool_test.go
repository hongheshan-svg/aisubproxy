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
