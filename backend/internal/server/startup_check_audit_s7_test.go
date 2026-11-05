package server

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/config"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// N-3：缺表的只读库上，--check 不因缺表失败（该条检查跳过）。
func TestS7CheckToleratesMissingTables(t *testing.T) {
	o3Env(t)
	t.Setenv("ADMIN_USERNAME", "admin")
	t.Setenv("ADMIN_PASSWORD", "S7-Strong-Passw0rd-xyz")
	path := filepath.Join(t.TempDir(), "old.db")
	c, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(`CREATE TABLE unrelated (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	c.Close()
	t.Setenv("DB_PATH", path)
	if err := Check(&config.Config{Server: config.ServerConfig{Mode: "release"}}); err != nil {
		t.Fatalf("缺表不应让 --check 失败: %v", err)
	}
}

// N-3：缺表只在 --check 里跳过；正式启动（不容忍）仍把缺表当错误。
func TestS7StartupChecksStrictOnMissingTables(t *testing.T) {
	o3Env(t)
	path := filepath.Join(t.TempDir(), "old.db")
	c, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	if err := startupChecks("release", false); err == nil {
		t.Fatal("启动时缺表应报错")
	}
}
