package handler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// 已知问题清单【涉及敏感数据】：在线更新前的整库备份按 umask 落盘（通常 0644），备份目录 0755，同机其他用户能读。

func permOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestBackupDatabaseIsPrivate(t *testing.T) {
	dbtest.Open(t)
	dst := filepath.Join(t.TempDir(), "cdk_recharge.db")
	if err := backupDatabase(dst); err != nil {
		t.Fatal(err)
	}
	if p := permOf(t, dst); p != 0o600 {
		t.Fatalf("整库备份应为 0600，实际 %o", p)
	}
}

func TestMakeBackupDirIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "updates", "backup-20261109-120000")
	if err := makeBackupDir(dir); err != nil {
		t.Fatal(err)
	}
	if p := permOf(t, dir); p != 0o700 {
		t.Fatalf("备份目录应为 0700，实际 %o", p)
	}
	// 已经存在的目录（MkdirAll 不改权限）也要收紧
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := makeBackupDir(dir); err != nil {
		t.Fatal(err)
	}
	if p := permOf(t, dir); p != 0o700 {
		t.Fatalf("已存在的备份目录也应收紧为 0700，实际 %o", p)
	}
}

// 升级前留下的旧备份：启动时收紧到 0700 / 0600。符号链接不跟随，免得改到链接指向的别处。
func TestTightenBackups(t *testing.T) {
	upd := t.TempDir()
	old := filepath.Join(upd, "backup-20261101-000000")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(old, 0o755); err != nil {
		t.Fatal(err)
	}
	dbFile := filepath.Join(old, "cdk_recharge.db")
	if err := os.WriteFile(dbFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dbFile, 0o644); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.Chmod(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(upd, "backup-link")); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(upd, "staging-1.5.4-1")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(other, 0o755); err != nil {
		t.Fatal(err)
	}

	tightenBackups(upd)

	if p := permOf(t, old); p != 0o700 {
		t.Fatalf("旧备份目录应收紧为 0700，实际 %o", p)
	}
	if p := permOf(t, dbFile); p != 0o600 {
		t.Fatalf("旧备份里的库文件应收紧为 0600，实际 %o", p)
	}
	if p := permOf(t, elsewhere); p != 0o755 {
		t.Fatalf("不应跟随符号链接改别处的权限，实际 %o", p)
	}
	if p := permOf(t, other); p != 0o755 {
		t.Fatalf("不是备份目录的不动，实际 %o", p)
	}
	tightenBackups(filepath.Join(upd, "missing")) // 目录不存在时什么也不做，不 panic
}
