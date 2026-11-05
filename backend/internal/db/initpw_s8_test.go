package db_test

import (
	"bytes"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// S8：随机初始管理员密码不再写进日志，改写到权限 0600 的文件。

// s8Open 用内存库跑 Migrate（会走 ensureDefaultAdmin），返回 Migrate 的错误。
func s8Open(t *testing.T) error {
	t.Helper()
	c, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	return db.Migrate()
}

func s8Env(t *testing.T, dir string) string {
	t.Helper()
	t.Setenv("DB_PATH", filepath.Join(dir, "x.db"))
	t.Setenv("INSTALL_MODE", "auto")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("ADMIN_USERNAME", "s8admin")
	return filepath.Join(dir, "initial-admin-password.txt")
}

func s8CaptureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

func TestS8RandomPasswordWrittenToFileNotLog(t *testing.T) {
	want := s8Env(t, t.TempDir())
	if got := db.InitialAdminPasswordPath(); got != want {
		t.Fatalf("路径应在数据库同目录：got %s want %s", got, want)
	}
	buf := s8CaptureLog(t)
	if err := s8Open(t); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(want)
	if err != nil {
		t.Fatalf("初始密码文件应存在：%v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("权限应为 0600，实际 %v", st.Mode().Perm())
	}
	raw, _ := os.ReadFile(want)
	content := string(raw)
	if !strings.Contains(content, "s8admin") {
		t.Fatalf("文件应含用户名：%q", content)
	}
	// 找出密码行：文件里除用户名外的 16 位字母数字串
	var pw string
	for _, f := range strings.FieldsFunc(content, func(r rune) bool { return r == '\n' || r == ' ' || r == ':' || r == '：' }) {
		if len(f) == 16 {
			pw = f
		}
	}
	if pw == "" {
		t.Fatalf("文件里找不到 16 位初始密码：%q", content)
	}
	var hash string
	if err := db.DB.QueryRow(`SELECT password_hash FROM admin_users WHERE username='s8admin'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.VerifyAdminPassword(hash, pw); !ok {
		t.Fatalf("库里的哈希应与文件里的密码匹配")
	}
	logs := buf.String()
	if strings.Contains(logs, pw) {
		t.Fatalf("日志里不能出现初始密码：%s", logs)
	}
	if !strings.Contains(logs, want) {
		t.Fatalf("日志应写出文件位置 %s：%s", want, logs)
	}
}

func TestS8UnwritableDirRefusesAndCreatesNoAdmin(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 数据库路径的父目录是普通文件，初始密码文件无法创建
	s8Env(t, dir)
	t.Setenv("DB_PATH", filepath.Join(blocker, "x.db"))
	buf := s8CaptureLog(t)
	err := s8Open(t)
	if err == nil {
		t.Fatal("写不了文件应拒绝启动")
	}
	if !strings.Contains(err.Error(), "ADMIN_PASSWORD") || !strings.Contains(err.Error(), "wizard") {
		t.Fatalf("错误应写明替代办法：%v", err)
	}
	var n int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM admin_users`).Scan(&n)
	if n != 0 {
		t.Fatalf("不应建号，实际 %d 个管理员", n)
	}
	// 错误和日志里都没有 16 位随机串之类的密码泄漏：错误里不应含任何 16 位纯字母数字词
	for _, w := range strings.Fields(err.Error() + " " + buf.String()) {
		if len(w) == 16 && strings.Trim(w, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") == "" {
			t.Fatalf("疑似密码出现在错误或日志里：%q", w)
		}
	}
}

func TestS8ExistingLooseFileBecomes0600(t *testing.T) {
	path := s8Env(t, t.TempDir())
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s8Open(t); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("覆盖写入后权限应为 0600，实际 %v", st.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "old") {
		t.Fatalf("应覆盖旧内容：%q", raw)
	}
}

func TestS8AdminPasswordEnvWritesNoFile(t *testing.T) {
	path := s8Env(t, t.TempDir())
	t.Setenv("ADMIN_PASSWORD", "Str0ngPassw0rd-S8x")
	if err := s8Open(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("用 ADMIN_PASSWORD 建号不应写文件：%v", err)
	}
}

func TestS8RemoveInitialAdminPasswordFile(t *testing.T) {
	path := s8Env(t, t.TempDir())
	db.RemoveInitialAdminPasswordFile() // 不存在：不报错不 panic
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	db.RemoveInitialAdminPasswordFile()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("文件应被删除：%v", err)
	}
}
