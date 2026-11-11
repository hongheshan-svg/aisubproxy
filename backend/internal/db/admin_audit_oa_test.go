package db_test

import (
	"database/sql"
	"path/filepath"
	"strings"
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

// A-12：用 ADMIN_PASSWORD 建号时和安装向导一样拒绝弱口令（拒绝启动），强口令照常建号；已有管理员时不检查。
func TestOAAdminPasswordEnvRejectsWeak(t *testing.T) {
	t.Setenv("SETUP_BOOTSTRAP_TOKEN", "")
	for _, tc := range []struct {
		name, mode, user, pass string
		wantErr                bool
	}{
		{"auto 历史默认口令", "auto", "", "admin123456", true},
		{"auto 太短", "auto", "", "Sh0rt", true},
		{"wizard 显式弱口令", "", "", "password123", true},
		{"和用户名相同", "auto", "operator_admin", "OPERATOR_ADMIN", true},
		{"强口令", "auto", "", "Str0ngPassw0rd-OA", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oaOpenFileDB(t)
			t.Setenv("INSTALL_MODE", tc.mode)
			t.Setenv("ADMIN_USERNAME", tc.user)
			t.Setenv("ADMIN_PASSWORD", tc.pass)
			err := db.Migrate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v，wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				if strings.Contains(err.Error(), tc.pass) {
					t.Fatalf("错误信息不应带出口令：%v", err)
				}
				if db.AdminCount() != 0 {
					t.Fatal("弱口令不应建号")
				}
			} else if db.AdminCount() != 1 {
				t.Fatal("强口令应建号")
			}
		})
	}
}

// 终审后端次要 2：ADMIN_PASSWORD 超过 72 字节时给出明确报错（不是哈希库的英文错误），不建号；--check 与启动同一结论。
func TestOAAdminPasswordEnvTooLong(t *testing.T) {
	t.Setenv("SETUP_BOOTSTRAP_TOKEN", "")
	oaOpenFileDB(t)
	t.Setenv("INSTALL_MODE", "auto")
	t.Setenv("ADMIN_USERNAME", "brand_new_admin")
	t.Setenv("ADMIN_PASSWORD", strings.Repeat("Ab1-", 19)) // 76 字节
	want := "ADMIN_PASSWORD 太长：最多 72 字节"
	err := db.Migrate()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("启动应报太长，实际 %v", err)
	}
	if err := db.CheckDefaultAdmin(); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("--check 应报太长，实际 %v", err)
	}
	if db.AdminCount() != 0 {
		t.Fatal("超长口令不应建号")
	}
}

// A-16：建表、建索引失败（这里用一张缺列的旧表让建索引失败）要终止启动，不能只打警告继续跑。
func TestOACreateTablesFailsFast(t *testing.T) {
	c := oaOpenFileDB(t)
	if _, err := c.Exec(`CREATE TABLE card_fail_events (id INTEGER PRIMARY KEY, card_id INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	err := db.Migrate()
	if err == nil {
		t.Fatal("建索引失败时 Migrate 应返回错误")
	}
	if !strings.Contains(err.Error(), "idx_card_fail_card") {
		t.Fatalf("错误里应指明是哪条语句：%v", err)
	}
}
