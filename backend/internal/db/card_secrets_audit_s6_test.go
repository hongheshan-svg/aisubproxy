package db_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// insertLegacyWebhook 直接写一行旧 webhook_events（升级前的库里就有这些行），不经过任何业务函数。
func insertLegacyWebhook(t *testing.T, idemKey, payload string) {
	t.Helper()
	if _, err := db.DB.Exec(`INSERT INTO webhook_events (event_type, idem_key, payload) VALUES ('card_transaction', ?, ?)`, idemKey, payload); err != nil {
		t.Fatal(err)
	}
}

func s6SeedWebhooks(t *testing.T, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		p := fmt.Sprintf(`{"event":"card_transaction","n":%d,"card_number":"53787271097%05d","cvv":"123"}`, i, i)
		insertLegacyWebhook(t, fmt.Sprintf("k%d", i), p)
	}
}

func s6AllClean(t *testing.T, n int) bool {
	t.Helper()
	rows, err := db.DB.Query(`SELECT payload FROM webhook_events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cnt, clean := 0, true
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		cnt++
		if strings.Contains(p, "cvv") || strings.Contains(p, "53787271097") {
			clean = false
		}
	}
	if cnt != n {
		t.Fatalf("行数 %d != %d", cnt, n)
	}
	return clean
}

// N-4：分批清洗，中途失败已完成的批次保留、重启后接着做，最终全部清干净且可重复跑。
func TestS6MigrateWebhookPayloadsBatchedResumable(t *testing.T) {
	dbtest.Open(t)
	s6SeedWebhooks(t, 11)
	boom := errors.New("boom")
	batches := 0
	err := db.MigrateWebhookPayloadsBatched(3, func(n int) error {
		batches = n
		if n == 2 {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("应返回中断错误: %v", err)
	}
	if batches != 2 {
		t.Fatalf("应在第 2 批后中断: %d", batches)
	}
	if s6AllClean(t, 11) {
		t.Fatal("中断后不该已全部清完")
	}
	var dirty, clean int
	rows, _ := db.DB.Query(`SELECT payload FROM webhook_events ORDER BY id`)
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		if strings.Contains(p, "cvv") {
			dirty++
		} else {
			clean++
		}
	}
	rows.Close()
	if clean != 6 || dirty != 5 {
		t.Fatalf("前两批（6 行）应已提交: clean=%d dirty=%d", clean, dirty)
	}
	for i := 0; i < 2; i++ {
		if err := db.MigrateWebhookPayloads(); err != nil {
			t.Fatal(err)
		}
	}
	if !s6AllClean(t, 11) {
		t.Fatal("重启后应全部清干净")
	}
}
