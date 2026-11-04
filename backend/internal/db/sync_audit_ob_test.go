package db

import (
	"context"
	"path/filepath"
	"testing"
)

// A-20：付款状态写进库就必须真的落盘。WAL + NORMAL 掉电时可能丢最近几笔已提交的事务，
// 「已付款」回退成「处理中」后重启恢复可能再付一次，所以连接池里每个连接都要是 synchronous = FULL（2）。
func TestObInitUsesSynchronousFull(t *testing.T) {
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "ob.db"))
	old := DB
	t.Cleanup(func() {
		if DB != nil && DB != old {
			DB.Close()
		}
		DB = old
	})
	if err := Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// 连接池里多拿几个连接：DSN 参数对每个连接都要生效，不能只有第一个连接是 FULL
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		conn, err := DB.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var mode int
		if err := conn.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if mode != 2 {
			t.Fatalf("第 %d 个连接 synchronous = %d，应为 2（FULL）", i+1, mode)
		}
	}
}
