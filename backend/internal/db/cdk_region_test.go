package db

import (
	"database/sql"
	"testing"
)

func TestCDKRegionMigrationKeepsLegacyRows(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	old := DB
	DB = conn
	t.Cleanup(func() { DB = old; conn.Close() })
	check := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	// Existing installations have no region column. Preserve their data and
	// leave unknown rows NULL until the authoritative current page is read.
	_, err = conn.Exec(`CREATE TABLE cardplatform_cdk_codes(upstream_id INTEGER,code TEXT UNIQUE NOT NULL,code_prefix TEXT,plan TEXT,fee_amount_minor INTEGER,status TEXT,created_at DATETIME)`)
	check(err)
	_, err = conn.Exec(`INSERT INTO cardplatform_cdk_codes VALUES(1,'ZC-SYNTHETIC-OLD-CODE','ZC-SYNTHETIC','plus',15,'unused',CURRENT_TIMESTAMP)`)
	check(err)
	check(migrateCardplatformCDKRegionCol())
	check(migrateCardplatformCDKRegionCol())
	// 旧行保留，地区列补上后仍是 NULL（未知），费用不变；重复迁移不报错
	var country sql.NullString
	var fee int
	check(conn.QueryRow(`SELECT payment_country, fee_amount_minor FROM cardplatform_cdk_codes WHERE upstream_id = 1`).Scan(&country, &fee))
	if country.Valid || fee != 15 {
		t.Fatalf("旧行被改动: country=%v fee=%d", country, fee)
	}
}
