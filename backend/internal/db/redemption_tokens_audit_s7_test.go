package db_test

import (
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// N-2：迁移可重复执行，并把旧绑定表里的令牌补进新表。
func TestS7MigrateBackfillsTokensRepeatably(t *testing.T) {
	dbtest.Open(t)
	if _, err := db.DB.Exec(`INSERT INTO cdk_session_bindings (cdk_code, session_payload, redemption_token, provider) VALUES ('S7-OLD', '', 'OLDTOK', 'zovocard')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`DELETE FROM cdk_redemption_tokens`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.Migrate(); err != nil {
			t.Fatalf("第 %d 次迁移: %v", i+1, err)
		}
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM cdk_redemption_tokens WHERE token = 'OLDTOK' AND cdk_code = 'S7-OLD'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("旧令牌应补进新表且只有一行: n=%d err=%v", n, err)
	}
}
