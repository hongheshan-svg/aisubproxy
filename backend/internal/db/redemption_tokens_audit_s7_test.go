package db_test

import (
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// N-2：同一张码先后预览拿到 T1、T2，两个令牌都要被认。
func TestS7RepeatedPreviewKeepsOldToken(t *testing.T) {
	dbtest.Open(t)
	if err := db.BindCDKRedemptionToken("zovocard", "S7-CODE-1", "T1"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindCDKRedemptionToken("zovocard", "S7-CODE-1", "T2"); err != nil {
		t.Fatal(err)
	}
	known, err := db.KnownRedemptionTokens([]string{"T1", "T2", "T3"})
	if err != nil {
		t.Fatal(err)
	}
	if !known["T1"] || !known["T2"] || known["T3"] {
		t.Fatalf("T1/T2 应已知、T3 未知: %v", known)
	}
}

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
	known, err := db.KnownRedemptionTokens([]string{"OLDTOK"})
	if err != nil || !known["OLDTOK"] {
		t.Fatalf("旧令牌应补进新表: %v %v", known, err)
	}
}
