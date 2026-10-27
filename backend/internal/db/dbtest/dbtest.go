// Package dbtest 提供测试用的内存数据库辅助函数，只应被测试代码 import。
package dbtest

import (
	"database/sql"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// Open 打开内存 SQLite 并建好全部表，替换包级 db.DB，测试结束后还原。
func Open(t testing.TB) {
	t.Helper()
	c, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
}

// SetMasterKey 用固定的 32 字节 base64 设置 APP_MASTER_KEY。
func SetMasterKey(t *testing.T) {
	t.Helper()
	t.Setenv("APP_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
}
