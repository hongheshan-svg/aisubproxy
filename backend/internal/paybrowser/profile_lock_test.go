package paybrowser

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// 已知问题清单【涉及敏感数据】：进程崩溃、被杀或在线更新重载后，Chrome 用户目录（里有会话 cookie）最长在 /tmp 留约 2 小时。
// 修法：用户目录旁放一个锁文件，浏览器活着时本进程一直持有排他锁（flock）；进程不在了锁随之释放，清理时拿得到锁就立即删。
// Docker 里程序每次都是 PID 1，所以不靠 PID 判断。

func mkProfile(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, profilePrefix+name)
	if err := os.MkdirAll(filepath.Join(dir, "Default"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Default", "Cookies"), []byte("session"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

type killLog struct{ dirs []string }

func (k *killLog) kill(dir string) { k.dirs = append(k.dirs, dir) }

func TestHoldProfileLock(t *testing.T) {
	dir := mkProfile(t, t.TempDir(), "a")
	lock, err := holdProfileLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir + profileLockSuffix)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("锁文件应在用户目录旁、权限 0600: %v %v", st, err)
	}
	if _, gone := ownerGone(dir + profileLockSuffix); gone {
		t.Fatal("锁还被持有（同一进程的另一个打开也算），不能判为主人已不在")
	}
	if _, err := holdProfileLock(dir); err == nil {
		t.Fatal("锁文件已存在时不应再建")
	}
	lock.Close() // 相当于进程退出：锁随之释放
	release, gone := ownerGone(dir + profileLockSuffix)
	if !gone {
		t.Fatal("锁释放后应判为主人已不在")
	}
	release()
}

// 主人已经不在（锁能拿到）、锁文件建好超过 1 分钟：立即删目录和锁文件，并先结束用这个目录的孤儿 Chrome。
func TestCleanProfilesDeadOwner(t *testing.T) {
	root := t.TempDir()
	dir := mkProfile(t, root, "dead")
	lock, err := holdProfileLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	k := &killLog{}
	later := time.Now().Add(2 * profileLockGrace)

	cleanProfilesIn(root, later, k.kill)
	if !exists(dir) || len(k.dirs) != 0 {
		t.Fatal("主人还在（持有锁）时不能删")
	}
	lock.Close()
	cleanProfilesIn(root, time.Now(), k.kill)
	if !exists(dir) {
		t.Fatal("锁文件刚建好不到 1 分钟时不删：主人可能还没来得及拿锁")
	}
	cleanProfilesIn(root, later, k.kill)
	if exists(dir) || exists(dir+profileLockSuffix) {
		t.Fatal("主人不在了应立即删目录和锁文件，不等 1 小时")
	}
	if !reflect.DeepEqual(k.dirs, []string{dir}) {
		t.Fatalf("删之前应先结束用这个目录的进程: %v", k.dirs)
	}
}

// 主人还在：目录再旧也不删（在线付款可能还在用）。
func TestCleanProfilesLiveOwnerKeptEvenIfOld(t *testing.T) {
	root := t.TempDir()
	dir := mkProfile(t, root, "live")
	lock, err := holdProfileLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	old := time.Now().Add(-3 * time.Hour)
	for _, p := range []string{filepath.Join(dir, "Default"), dir, dir + profileLockSuffix} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	k := &killLog{}
	cleanProfilesIn(root, time.Now(), k.kill)
	if !exists(dir) || len(k.dirs) != 0 {
		t.Fatal("持有锁的目录不能删")
	}
}

// 关闭时先删目录再删锁文件，中间崩溃会留下孤立的锁文件：没人持有就删，有人持有不动。
func TestCleanProfilesOrphanLockFile(t *testing.T) {
	root := t.TempDir()
	dir := mkProfile(t, root, "gone")
	lock, err := holdProfileLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	k := &killLog{}
	cleanProfilesIn(root, time.Now(), k.kill)
	if !exists(dir + profileLockSuffix) {
		t.Fatal("有人持有的锁文件不动")
	}
	lock.Close()
	cleanProfilesIn(root, time.Now(), k.kill)
	if exists(dir + profileLockSuffix) {
		t.Fatal("没人持有的孤立锁文件应删")
	}
}

// 锁文件位置是符号链接：不当锁用，按老规则（1 小时没变化）处理；也不跟随链接去拿别处文件的锁。
func TestCleanProfilesLockSymlinkIgnored(t *testing.T) {
	root := t.TempDir()
	dir := mkProfile(t, root, "link")
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dir+profileLockSuffix); err != nil {
		t.Fatal(err)
	}
	if _, gone := ownerGone(dir + profileLockSuffix); gone {
		t.Fatal("不应跟随符号链接拿锁")
	}
	k := &killLog{}
	cleanProfilesIn(root, time.Now().Add(2*profileLockGrace), k.kill)
	if !exists(dir) || !exists(target) || len(k.dirs) != 0 {
		t.Fatal("锁文件是链接时按老规则：刚变化过的目录不删，链接指向的文件不动")
	}
}

// 孤儿 Chrome 按命令行里完整的 --user-data-dir=<目录> 参数认，前缀相同的别的目录、只是字符串里带这段的进程不算；
// PID 1 和本进程不动。
func TestFindProfileProcs(t *testing.T) {
	proc := t.TempDir()
	const dir = "/tmp/cdk-pay-chrome-abc"
	write := func(pid, cmdline string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(proc, pid), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(proc, pid, "cmdline"), []byte(cmdline), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("123", "/usr/bin/chromium\x00--user-data-dir="+dir+"\x00--headless=new\x00")
	write("124", "/usr/bin/chromium\x00--user-data-dir="+dir+"def\x00")
	write("125", "bash\x00-c\x00echo --user-data-dir="+dir+"\x00")
	write("126", "/usr/bin/chromium\x00--type=renderer\x00--user-data-dir="+dir+"\x00")
	write("1", "/app/cdk-recharge\x00--user-data-dir="+dir+"\x00")
	write(strconv.Itoa(os.Getpid()), "self\x00--user-data-dir="+dir+"\x00")
	write("self", "x\x00--user-data-dir="+dir+"\x00")
	if err := os.MkdirAll(filepath.Join(proc, "999"), 0o700); err != nil { // 没有 cmdline（进程刚退出）
		t.Fatal(err)
	}
	if got := findProfileProcs(proc, dir); !reflect.DeepEqual(got, []int{123, 126}) {
		t.Fatalf("应只认出 123 和 126: %v", got)
	}
	if got := findProfileProcs(filepath.Join(proc, "missing"), dir); len(got) != 0 {
		t.Fatalf("没有 /proc（macOS）时什么也不做: %v", got)
	}
}
