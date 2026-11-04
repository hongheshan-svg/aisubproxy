package db_test

import (
	"os"
	"testing"
)

// U3 复核（Fable）：初始密码文件不能复用已有文件（否则密码先以旧权限落盘），也不能跟随符号链接。

func TestU3SymlinkAtPathNotFollowed(t *testing.T) {
	dir := t.TempDir()
	path := s8Env(t, dir)
	target := dir + "/outside.txt"
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := s8Open(t); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "keep" {
		t.Fatalf("符号链接的目标不应被写入（目标现在有 %d 字节）", len(raw))
	}
	if st, _ := os.Stat(target); st.Mode().Perm() != 0o644 {
		t.Fatalf("符号链接的目标权限不应被改动，实际 %v", st.Mode().Perm())
	}
	lst, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if lst.Mode()&os.ModeSymlink != 0 || !lst.Mode().IsRegular() || lst.Mode().Perm() != 0o600 {
		t.Fatalf("初始密码文件应是新建的 0600 普通文件，实际 %v", lst.Mode())
	}
}

func TestU3ExistingFileReplacedNotReused(t *testing.T) {
	path := s8Env(t, t.TempDir())
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s8Open(t); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("应删掉旧文件后新建（新文件一创建就是 0600），不能截断复用旧的 0644 文件")
	}
}
