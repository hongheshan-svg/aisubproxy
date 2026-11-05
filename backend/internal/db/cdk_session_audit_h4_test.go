package db_test

import (
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// finalcheck2 N-1：只带令牌、没有码的兑换会话绑定走 INSERT "RT:" 兜底分支，这时也要把令牌记进令牌表，
// 否则批量查结果会把它当成未知令牌，直到下次启动 backfill 才补上。重复绑定要不报错、不重复。
func TestH4BindSessionTokenOnlyRecordsToken(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	const tok = "H4-TOK-1"
	sess := `{"user":{"email":"h4@example.test"}}`
	for i := 0; i < 2; i++ {
		if err := db.BindCDKSessionAs("zovocard", "", tok, sess, "h4@example.test"); err != nil {
			t.Fatalf("第 %d 次只带令牌绑定: %v", i+1, err)
		}
	}
	known, err := db.KnownRedemptionTokens([]string{tok, "H4-UNKNOWN"})
	if err != nil {
		t.Fatal(err)
	}
	if !known[tok] || known["H4-UNKNOWN"] {
		t.Fatalf("H4-TOK-1 应已知、H4-UNKNOWN 未知: %v", known)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM cdk_redemption_tokens WHERE token = ?`, tok).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("令牌表里应只有一行，实际 %d 行", n)
	}
}
