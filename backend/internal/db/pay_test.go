package db_test

import (
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
