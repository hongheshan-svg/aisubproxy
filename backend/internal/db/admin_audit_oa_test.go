package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 审计 OA 组（鉴权与会话）的库层测试。

// oaOpenFileDB 打开一个临时文件库（不建表），替换包级 db.DB，测试结束还原。
func oaOpenFileDB(t *testing.T) *sql.DB {
	t.Helper()
	c, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "oa.db"))
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	return c
}

// 改造前的管理员表（没有 token_version 列）。
const oaLegacyAdminUsersDDL = `CREATE TABLE admin_users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT UNIQUE NOT NULL,
	password_hash TEXT NOT NULL,
	display_name TEXT,
	is_active INTEGER DEFAULT 1,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
)`

// A-2（R8）：已有库升级时补 token_version 列，默认 0，老账号保留；迁移可重复执行。
// 改密码让版本号加一，登录时的 bcrypt 升级不改版本号。
func TestOAMigrateAdminTokenVersionOnLegacyDB(t *testing.T) {
	c := oaOpenFileDB(t)
	hash, err := db.HashAdminPassword("Str0ngPassw0rd-OA")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{oaLegacyAdminUsersDDL} {
		if _, err := c.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Exec(`INSERT INTO admin_users (username, password_hash, display_name) VALUES ('oa_old', ?, 'Old')`, hash); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 3; round++ {
		if err := db.Migrate(); err != nil {
			t.Fatalf("第 %d 次迁移：%v", round, err)
		}
		var id int64
		if err := c.QueryRow(`SELECT id FROM admin_users WHERE username = 'oa_old'`).Scan(&id); err != nil {
			t.Fatalf("第 %d 次迁移后老账号不见了：%v", round, err)
		}
		ver, active, err := db.AdminTokenState(id)
		if err != nil || ver != 0 || !active {
			t.Fatalf("第 %d 次迁移后 ver=%d active=%v err=%v，想要 0/true/nil", round, ver, active, err)
		}
	}

	var id int64
	_ = c.QueryRow(`SELECT id FROM admin_users WHERE username = 'oa_old'`).Scan(&id)
	db.UpgradeAdminHash("oa_old", hash)
	if ver, _, _ := db.AdminTokenState(id); ver != 0 {
		t.Fatalf("bcrypt 升级不应改版本号，实际 %d", ver)
	}
	if err := db.SetAdminPassword("oa_old", "An0therStr0ngPass"); err != nil {
		t.Fatal(err)
	}
	if ver, _, _ := db.AdminTokenState(id); ver != 1 {
		t.Fatalf("改密后版本号应为 1，实际 %d", ver)
	}
	if _, _, err := db.AdminTokenState(id + 100); err == nil {
		t.Fatal("不存在的账号应返回错误")
	}
}
