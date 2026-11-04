package server

import (
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/config"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 审计 O3 组 auth N-2：命令行 --check 复用启动时的同一套检查，只读打开数据库，不建表、不迁移、不写库。

const (
	o3StrongJWT  = "o3-0123456789abcdef0123456789abcdef"
	o3MasterKey  = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	o3AdminHash  = "$2a$10$abcdefghijklmnopqrstuuJ9H0pQe5w3vYV6M2bq0n6pQnBq6Y8xK" // 只是占位，测试不校验口令
	o3PlainCardK = "plain-card-api-key-o3"
)

// o3Env 设好一套能通过启动检查的环境变量；各测试再改自己关心的那一项。
func o3Env(t *testing.T) {
	t.Helper()
	t.Setenv("JWT_SECRET", o3StrongJWT)
	t.Setenv("APP_MASTER_KEY", o3MasterKey)
	t.Setenv("ADMIN_USERNAME", "")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("INSTALL_MODE", "wizard")
}

// o3NewDBFile 在临时目录建一个完整建好表的库文件（模拟已安装的站），seed 写入测试数据后关闭，
// 并把 DB_PATH 指向它。建库期间临时替换 db.DB，结束后还原。
func o3NewDBFile(t *testing.T, seed func(c *sql.DB)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cdk_recharge.db")
	c, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=10000")
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	old := db.DB
	db.DB = c
	err = db.Migrate()
	db.DB = old
	if err != nil {
		c.Close()
		t.Fatal(err)
	}
	if seed != nil {
		seed(c)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_PATH", path)
	return path
}

func o3Exec(t *testing.T, c *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := c.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func o3SeedAdmin(t *testing.T, c *sql.DB, username string) {
	t.Helper()
	o3Exec(t, c, `INSERT INTO admin_users (username, password_hash, display_name, is_active) VALUES (?, ?, 'x', 1)`, username, o3AdminHash)
}

func o3FileSum(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func o3Setting(t *testing.T, path, key string) string {
	t.Helper()
	c, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var v string
	if err := c.QueryRow(`SELECT value FROM site_settings WHERE key = ?`, key).Scan(&v); err != nil {
		t.Fatalf("读 %s: %v", key, err)
	}
	return v
}

func o3Check(mode string) error {
	return Check(&config.Config{Server: config.ServerConfig{Mode: mode}})
}

// 已保存旧版明文的卡台 API Key、APP_MASTER_KEY 没配：启动会拒绝，--check 也必须拒绝，且不动库。
func TestO3CheckRejectsPlainCardSecretWithoutMasterKey(t *testing.T) {
	o3Env(t)
	path := o3NewDBFile(t, func(c *sql.DB) {
		o3SeedAdmin(t, c, "admin")
		o3Exec(t, c, `INSERT INTO site_settings (key, value) VALUES ('card_api_key', ?)`, o3PlainCardK)
	})
	t.Setenv("APP_MASTER_KEY", "")
	before := o3FileSum(t, path)
	err := o3Check("release")
	if err == nil || !strings.Contains(err.Error(), "APP_MASTER_KEY") || !strings.Contains(err.Error(), "card_api_key") {
		t.Fatalf("明文卡台密钥 + 没有主密钥应拒绝，并说明原因: %v", err)
	}
	if strings.Contains(err.Error(), o3PlainCardK) {
		t.Fatalf("错误信息不能带密钥内容: %v", err)
	}
	if o3FileSum(t, path) != before {
		t.Fatal("--check 不能改库文件")
	}
	// 主密钥格式不对同样拒绝
	t.Setenv("APP_MASTER_KEY", "not-base64-32-bytes")
	if err := o3Check("release"); err == nil {
		t.Fatal("主密钥不合法应拒绝")
	}
}

// 条件都满足：--check 通过，但不做启动时的迁移（明文密钥仍是明文、库文件不变）。
func TestO3CheckPassesWithoutMigrating(t *testing.T) {
	o3Env(t)
	path := o3NewDBFile(t, func(c *sql.DB) {
		o3SeedAdmin(t, c, "admin")
		o3Exec(t, c, `INSERT INTO site_settings (key, value) VALUES ('card_api_key', ?)`, o3PlainCardK)
	})
	before := o3FileSum(t, path)
	if err := o3Check("release"); err != nil {
		t.Fatalf("条件都满足应通过: %v", err)
	}
	if got := o3Setting(t, path, "card_api_key"); got != o3PlainCardK {
		t.Fatalf("--check 不能做加密迁移，库里的值应原样保留，实际 %q", got)
	}
	if o3FileSum(t, path) != before {
		t.Fatal("--check 不能改库文件")
	}
}

// JWT_SECRET 是示例占位值：release 模式拒绝；已配置上游密钥时非 release 也拒绝。
func TestO3CheckRejectsExampleJWTSecret(t *testing.T) {
	o3Env(t)
	o3NewDBFile(t, func(c *sql.DB) { o3SeedAdmin(t, c, "admin") })
	t.Setenv("JWT_SECRET", "change-me-to-random-32chars-min")
	if err := o3Check("release"); err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Fatalf("release 模式下示例 JWT_SECRET 应拒绝: %v", err)
	}
	if err := o3Check("debug"); err != nil {
		t.Fatalf("debug 模式、没配上游密钥时只告警不拒绝（与启动一致）: %v", err)
	}

	o3NewDBFile(t, func(c *sql.DB) {
		o3SeedAdmin(t, c, "admin")
		o3Exec(t, c, `INSERT INTO site_settings (key, value) VALUES ('card_api_key', 'v1:sealed-already')`)
	})
	if err := o3Check("debug"); err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Fatalf("已配置卡台 API Key 时示例 JWT_SECRET 应拒绝: %v", err)
	}
}

// 还没有这个用户名的管理员、ADMIN_PASSWORD 太弱：启动会拒绝，--check 也拒绝；管理员已存在时不看 ADMIN_PASSWORD。
func TestO3CheckRejectsWeakAdminPassword(t *testing.T) {
	o3Env(t)
	withAdmin := o3NewDBFile(t, func(c *sql.DB) { o3SeedAdmin(t, c, "admin") })
	o3NewDBFile(t, nil) // DB_PATH 指向这个还没有管理员的库
	t.Setenv("ADMIN_PASSWORD", "admin123456")
	if err := o3Check("release"); err == nil || !strings.Contains(err.Error(), "ADMIN_PASSWORD") {
		t.Fatalf("要建号且口令太弱应拒绝: %v", err)
	}
	if strings.Contains(fmtErr(o3Check("release")), "admin123456") {
		t.Fatal("错误信息不能带口令")
	}

	t.Setenv("DB_PATH", withAdmin)
	if err := o3Check("release"); err != nil {
		t.Fatalf("管理员已存在时不按 ADMIN_PASSWORD 建号，应通过: %v", err)
	}
}

func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// 旧版本留下的库（只有部分表、没有新版本的列）：--check 只读需要的几项，不建表、不加列。
func TestO3CheckDoesNotCreateTablesOnLegacyDB(t *testing.T) {
	o3Env(t)
	path := filepath.Join(t.TempDir(), "legacy.db")
	c, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	o3Exec(t, c, `CREATE TABLE admin_users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL, display_name TEXT, is_active INTEGER DEFAULT 1)`)
	o3Exec(t, c, `CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	o3SeedAdmin(t, c, "admin")
	c.Close()
	t.Setenv("DB_PATH", path)
	before := o3FileSum(t, path)

	if err := o3Check("release"); err != nil {
		t.Fatalf("旧库应能通过检查: %v", err)
	}
	if o3FileSum(t, path) != before {
		t.Fatal("--check 不能建表、加列或改库文件")
	}
	c, err = sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var n int
	if err := c.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("表数应仍是 2，实际 %d (%v)", n, err)
	}
}

// 库文件不存在：报错，不新建文件。
func TestO3CheckMissingDBFile(t *testing.T) {
	o3Env(t)
	path := filepath.Join(t.TempDir(), "nope", "cdk_recharge.db")
	t.Setenv("DB_PATH", path)
	if err := o3Check("release"); err == nil {
		t.Fatal("库文件不存在应报错")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("不能新建库文件: %v", err)
	}
}

// 运行中的站（另一个连接正开着库、WAL 模式）：--check 能读到已提交的数据，且检查期间写不进去，结束后 db.DB 还原。
func TestO3CheckReadOnlyWhileServerHoldsDB(t *testing.T) {
	o3Env(t)
	path := o3NewDBFile(t, func(c *sql.DB) { o3SeedAdmin(t, c, "admin") })
	live, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=10000")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	o3Exec(t, live, `INSERT INTO site_settings (key, value) VALUES ('card_api_key', ?)`, o3PlainCardK)

	t.Setenv("APP_MASTER_KEY", "")
	if err := o3Check("release"); err == nil {
		t.Fatal("应读到运行中的站刚写入的明文密钥并拒绝")
	}

	ro, err := db.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Exec(`INSERT INTO site_settings (key, value) VALUES ('o3_probe', 'x')`); err == nil {
		t.Fatal("只读连接不应能写入")
	}

	sentinel := db.DB
	_ = o3Check("release")
	if db.DB != sentinel {
		t.Fatal("Check 结束后应还原 db.DB")
	}
}
