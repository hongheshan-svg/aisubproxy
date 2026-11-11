package handler

import (
	"os"
	"path/filepath"
	"testing"
)

// H1-01：备份旧程序、旧前端、旧版本号任何一项失败都要返回错误（调用方据此中止更新）。
func TestBackupCurrentFilesCopiesAll(t *testing.T) {
	root := t.TempDir()
	l := updateLayout{
		CurBin: filepath.Join(root, "bin"),
		CurWeb: filepath.Join(root, "web"),
		CurVer: filepath.Join(root, "VERSION"),
	}
	_ = os.WriteFile(l.CurBin, []byte("bin"), 0o755)
	_ = os.MkdirAll(l.CurWeb, 0o755)
	_ = os.WriteFile(filepath.Join(l.CurWeb, "index.html"), []byte("x"), 0o644)
	_ = os.WriteFile(l.CurVer, []byte("1.0.0\n"), 0o644)
	bak := filepath.Join(root, "backup-1")
	if err := backupCurrentFiles(l, bak); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"cdk-recharge", "web/index.html", "VERSION"} {
		if _, err := os.Stat(filepath.Join(bak, p)); err != nil {
			t.Fatalf("备份缺 %s: %v", p, err)
		}
	}
}

func TestBackupCurrentFilesFailsWhenBinaryUnreadable(t *testing.T) {
	root := t.TempDir()
	// 把「程序」做成目录：Stat 成功、复制必定失败
	l := updateLayout{CurBin: filepath.Join(root, "bin")}
	_ = os.MkdirAll(l.CurBin, 0o755)
	if err := backupCurrentFiles(l, filepath.Join(root, "backup-1")); err == nil {
		t.Fatal("备份程序失败时应返回错误")
	}
}

func TestBackupCurrentFilesFailsWhenVersionUnreadable(t *testing.T) {
	root := t.TempDir()
	l := updateLayout{CurVer: filepath.Join(root, "VERSION")}
	_ = os.MkdirAll(l.CurVer, 0o755)
	if err := backupCurrentFiles(l, filepath.Join(root, "backup-1")); err == nil {
		t.Fatal("备份版本号失败时应返回错误")
	}
}

func TestWriteNewVersionReportsError(t *testing.T) {
	root := t.TempDir()
	l := updateLayout{Ver: filepath.Join(root, "no-such-dir", "x", "VERSION")}
	// 父路径是一个普通文件，写入必然失败
	_ = os.WriteFile(filepath.Join(root, "no-such-dir"), []byte("f"), 0o644)
	if err := writeNewVersion(l, "", "1.2.3"); err == nil {
		t.Fatal("写版本号失败时应返回错误")
	}
	l.Ver = filepath.Join(root, "VERSION")
	if err := writeNewVersion(l, "", "1.2.3"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(l.Ver); string(b) != "1.2.3\n" {
		t.Fatalf("got %q", b)
	}
}
