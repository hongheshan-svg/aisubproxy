package paybrowser

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// 用户目录的主人锁：Chrome 用户目录（里面有用户会话 cookie）旁边放一个 <目录>.lock，浏览器活着时本进程一直持有它的排他锁（flock）。
// 进程崩溃、被杀、在线更新重载（exec 时文件描述符随 O_CLOEXEC 关闭）之后锁由系统释放，清理时拿得到锁就说明主人已经不在，立即删目录。
// 不靠 PID 判断：Docker 里程序每次都是 PID 1，认不出上一轮。flock 在同一进程里不同的打开之间也互斥，本进程的清理不会误删自己的目录。
// 锁文件放在目录外面：用户目录会交给 Chrome 的沙箱用户（见 sandboxOptions），锁文件留在本进程用户名下。

const (
	profileLockSuffix = ".lock"
	// profileLockGrace：锁文件建好不到这么久的不判主人是否还在。建锁文件和拿锁是两步，中间被清理看到会误判。
	profileLockGrace = time.Minute
)

// holdProfileLock 在 dir 旁新建锁文件（0600，已存在就报错）并拿排他锁，锁持有到返回的文件被关闭。
func holdProfileLock(dir string) (*os.File, error) {
	f, err := os.OpenFile(dir+profileLockSuffix, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}

// releaseProfileLock 在用户目录删掉之后删锁文件、放锁。lock 为 nil（没拿到锁）时什么也不做。
func releaseProfileLock(lock *os.File) {
	if lock == nil {
		return
	}
	_ = os.Remove(lock.Name())
	_ = lock.Close()
}

// removeProfile 删掉用户目录，删干净了才删锁文件、放锁（releaseProfileLock）。
// 删不干净时（例如 Chrome 的子进程还没退完又往里写文件）打日志，只放锁、留下锁文件：下一轮清理（cleanProfilesIn）
// 看到锁文件、拿得到锁，就先结束用这个目录的残留进程再删；锁文件也删了的话，只能按老规则等一小时没变化才删。
func removeProfile(dir string, lock *os.File) {
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("[paybrowser] 删除 Chrome 临时目录 %s 失败，留给下一轮清理: %v", filepath.Base(dir), err)
		if lock != nil {
			_ = lock.Close()
		}
		return
	}
	releaseProfileLock(lock)
}

// ownerGone 试着拿 lockPath 的排他锁：拿到说明持有它的进程已经不在，锁一直持有到调用返回的 release。
// 锁文件是符号链接、打不开、锁被别人持有时返回 gone = false。
func ownerGone(lockPath string) (release func(), gone bool) {
	f, err := os.OpenFile(lockPath, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false
	}
	return func() { _ = f.Close() }, true
}

// cleanProfilesIn 清理 root 下的 Chrome 用户目录残留，now 是当前时间（测试可替换）：
//   - 旁边有锁文件的：锁文件建好超过 profileLockGrace、且拿得到锁（主人已经不在），先 kill 用这个目录的孤儿 Chrome，再删目录和锁文件；
//   - 没有锁文件的（老版本留下的，或者锁文件是符号链接）：照旧只删超过 staleProfileAge 没有变化的；
//   - 目录已经删了、只剩锁文件的（关闭时先删目录再删锁，中间崩溃会留下）：没人持有就删。
//
// ReadDir 不跟随符号链接（链接的 IsDir 为 false，直接跳过），RemoveAll 也不会穿过目录里的链接。失败只打日志。
func cleanProfilesIn(root string, now time.Time, kill func(dir string)) {
	entries, err := os.ReadDir(root)
	if err != nil {
		log.Printf("[paybrowser] 读取临时目录失败: %v", err)
		return
	}
	cutoff := now.Add(-staleProfileAge)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, profilePrefix) {
			continue
		}
		p := filepath.Join(root, name)
		switch {
		case e.IsDir():
			lockPath := p + profileLockSuffix
			if st, err := os.Lstat(lockPath); err == nil && st.Mode().IsRegular() {
				cleanLockedProfile(p, lockPath, st.ModTime(), now, kill)
				continue
			}
			if profileChangedSince(p, cutoff) {
				continue
			}
			if err := os.RemoveAll(p); err != nil {
				log.Printf("[paybrowser] 删除残留的 Chrome 临时目录 %s 失败: %v", name, err)
			}
		case e.Type().IsRegular() && strings.HasSuffix(name, profileLockSuffix):
			if _, err := os.Lstat(strings.TrimSuffix(p, profileLockSuffix)); !os.IsNotExist(err) {
				continue // 目录还在：随目录一起处理
			}
			if release, gone := ownerGone(p); gone {
				_ = os.Remove(p)
				release()
			}
		}
	}
}

// cleanLockedProfile 处理旁边有锁文件的用户目录：主人不在了就先结束用它的进程，再删目录和锁文件。锁一直拿着，删完才放。
func cleanLockedProfile(dir, lockPath string, lockedAt, now time.Time, kill func(dir string)) {
	if now.Sub(lockedAt) < profileLockGrace {
		return
	}
	release, gone := ownerGone(lockPath)
	if !gone {
		return
	}
	defer release()
	kill(dir)
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("[paybrowser] 删除主人已退出的 Chrome 临时目录 %s 失败: %v", filepath.Base(dir), err)
		return
	}
	_ = os.Remove(lockPath)
	log.Printf("[paybrowser] 已删除主人已退出的 Chrome 临时目录 %s", filepath.Base(dir))
}

// killProfileProcs 结束还在用 dir 的孤儿 Chrome（主人崩溃或重载后没人管的进程）。靠 /proc，只在 Linux 上有效；macOS 上什么也不做。
func killProfileProcs(dir string) {
	for _, pid := range findProfileProcs("/proc", dir) {
		if err := syscall.Kill(pid, syscall.SIGKILL); err == nil {
			log.Printf("[paybrowser] 已结束使用残留目录 %s 的孤儿 Chrome 进程 %d", filepath.Base(dir), pid)
		}
	}
}

// findProfileProcs 在 procRoot（/proc）里找命令行参数里有完整的 --user-data-dir=<dir> 的进程，按 PID 从小到大返回。
// 只认整个参数相等：前缀相同的别的目录、只是字符串里带这一段的命令不算。PID 1 和本进程不算。
func findProfileProcs(procRoot, dir string) []int {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	want := []byte("--user-data-dir=" + dir)
	self := os.Getpid()
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 || pid == self {
			continue
		}
		b, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		for _, arg := range bytes.Split(b, []byte{0}) {
			if bytes.Equal(arg, want) {
				out = append(out, pid)
				break
			}
		}
	}
	sort.Ints(out)
	return out
}
