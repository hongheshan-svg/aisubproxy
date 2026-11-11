package handler

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLayoutFiles(t *testing.T, root string) updateLayout {
	t.Helper()
	l := updateLayout{
		Bin: filepath.Join(root, "cdk-recharge"),
		Web: filepath.Join(root, "web"),
		Ver: filepath.Join(root, "VERSION"),
	}
	l.CurBin, l.CurWeb, l.CurVer = l.Bin, l.Web, l.Ver
	mustWrite(t, l.Bin, "old-bin")
	mustWrite(t, filepath.Join(l.Web, "index.html"), "old-web")
	mustWrite(t, l.Ver, "1.0.0\n")
	return l
}

// 审查修复（H1-01）：新版本号写不进去时，从备份把旧程序和旧前端还原回去。
func TestRestoreFromBackupPutsOldFilesBack(t *testing.T) {
	root := t.TempDir()
	l := writeLayoutFiles(t, root)
	bak := filepath.Join(root, "backup-1")
	if err := backupCurrentFiles(l, bak); err != nil {
		t.Fatal(err)
	}
	// 模拟已经换成新版
	mustWrite(t, l.Bin, "new-bin")
	mustWrite(t, filepath.Join(l.Web, "index.html"), "new-web")
	mustWrite(t, filepath.Join(l.Web, "extra.js"), "new-only")

	if err := restoreFromBackup(l, bak); err != nil {
		t.Fatal(err)
	}
	if readFile(t, l.Bin) != "old-bin" || readFile(t, filepath.Join(l.Web, "index.html")) != "old-web" {
		t.Fatal("旧程序或旧前端没有还原")
	}
	if _, err := os.Stat(filepath.Join(l.Web, "extra.js")); err == nil {
		t.Fatal("新版独有的前端文件应被清掉")
	}
}

func TestRestoreFromBackupFailsWithoutBackup(t *testing.T) {
	root := t.TempDir()
	l := writeLayoutFiles(t, root)
	if err := restoreFromBackup(l, filepath.Join(root, "backup-none")); err == nil {
		t.Fatal("备份里没有旧程序时应返回错误")
	}
}

func TestManualRollbackHintNamesFilesAndBackup(t *testing.T) {
	l := updateLayout{Bin: "/opt/app/cdk-recharge", Web: "/opt/app/web", Ver: "/opt/app/VERSION"}
	h := manualRollbackHint(l, "/opt/app/updates/backup-1")
	for _, s := range []string{"/opt/app/updates/backup-1", "/opt/app/cdk-recharge", "/opt/app/web", "/opt/app/VERSION"} {
		if !strings.Contains(h, s) {
			t.Fatalf("提示缺 %s：%s", s, h)
		}
	}
}

func runUpdateWithVersionWriteFailure(t *testing.T, restoreOK bool) (root, bin string, reexec chan string) {
	t.Helper()
	priv := useTestReleaseKey(t)
	bundle := buildBundle(t, "1.5.1", `[ "$1" = "--version" ] && echo 1.5.1`)
	startFakeGitHub(t, fakeRelease{version: "1.5.1", bundle: bundle, sig: signB64(priv, bundle)})
	root, bin, reexec = setupInstall(t)
	old := writeNewVersionFn
	writeNewVersionFn = func(updateLayout, string, string) error { return errors.New("disk full") }
	t.Cleanup(func() { writeNewVersionFn = old })
	if !restoreOK {
		oldR := restoreFromBackupFn
		restoreFromBackupFn = func(updateLayout, string) error { return errors.New("restore broke") }
		t.Cleanup(func() { restoreFromBackupFn = oldR })
	}
	runSeamlessUpdate("latest", "admin")
	return
}

func TestSeamlessUpdateVersionWriteFailureRestoresOld(t *testing.T) {
	root, bin, reexec := runUpdateWithVersionWriteFailure(t, true)
	assertUpdateFailedUnchanged(t, root, bin, "已还原", reexec)
}

func TestSeamlessUpdateVersionWriteAndRestoreFailureGivesManualSteps(t *testing.T) {
	_, _, reexec := runUpdateWithVersionWriteFailure(t, false)
	st := getUpdateState()
	if st.Phase != "failed" || !strings.Contains(st.Message, "手动") || !strings.Contains(st.Message, "backup-") {
		t.Fatalf("应提示手动回滚并写明备份目录：%+v", st)
	}
	select {
	case <-reexec:
		t.Fatal("不应重启")
	default:
	}
}

// 审查修复（次要 3）：备份失败就不进入替换。
func TestSeamlessUpdateBackupFailureStopsBeforeSwap(t *testing.T) {
	priv := useTestReleaseKey(t)
	bundle := buildBundle(t, "1.5.1", `[ "$1" = "--version" ] && echo 1.5.1`)
	startFakeGitHub(t, fakeRelease{version: "1.5.1", bundle: bundle, sig: signB64(priv, bundle)})
	root, bin, reexec := setupInstall(t)
	// 版本号文件换成目录：存在但读不了，备份必然失败
	_ = os.Remove(filepath.Join(root, "VERSION"))
	_ = os.MkdirAll(filepath.Join(root, "VERSION"), 0o755)

	runSeamlessUpdate("latest", "admin")

	assertUpdateFailedUnchanged(t, root, bin, "备份当前版本失败", reexec)
	if baks, _ := filepath.Glob(filepath.Join(root, "updates", "backup-*")); len(baks) != 0 {
		t.Fatalf("不完整的备份应被删掉：%v", baks)
	}
}
