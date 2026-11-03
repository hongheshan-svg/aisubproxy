package db_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func TestMigratePayProbesIdempotent(t *testing.T) {
	dbtest.Open(t)
	for i := 0; i < 2; i++ {
		if err := db.MigratePayProbes(); err != nil {
			t.Fatal(err)
		}
	}
}

// 只保留最近 50 条，被淘汰记录的截图目录要返回给调用方清理。
func TestInsertPayProbeKeepsLatest50(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	var allPruned []string
	for i := 1; i <= db.PayProbeKeep+3; i++ {
		p := &db.PayProbe{CreatedAt: int64(i), Admin: "a", ChecksJSON: "[]", ResultsJSON: "[]", ShotDir: fmt.Sprintf("probe-%08x", i)}
		pruned, err := db.InsertPayProbe(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if p.ID == 0 {
			t.Fatal("没有回填 id")
		}
		allPruned = append(allPruned, pruned...)
	}
	list, err := db.ListPayProbes(ctx)
	if err != nil || len(list) != db.PayProbeKeep {
		t.Fatalf("len=%d err=%v", len(list), err)
	}
	if list[0].CreatedAt != int64(db.PayProbeKeep+3) {
		t.Errorf("应倒序，最新在前: %d", list[0].CreatedAt)
	}
	if len(allPruned) != 3 || allPruned[0] != "probe-00000001" {
		t.Errorf("淘汰的目录不对: %v", allPruned)
	}
	if p, err := db.GetPayProbe(ctx, 1); err != nil || p != nil {
		t.Errorf("最早的应已删除: %v %v", p, err)
	}
	if p, err := db.GetPayProbe(ctx, list[0].ID); err != nil || p == nil || p.Admin != "a" {
		t.Errorf("读取失败: %v %v", p, err)
	}
}
