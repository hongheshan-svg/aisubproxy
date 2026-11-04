package paybrowser

import (
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

const (
	modeHeadful  = "headful"
	modeHeadless = "headless"

	xvfbDisplay = ":99"
	xvfbSocket  = "/tmp/.X11-unix/X99"
	xvfbWait    = 5 * time.Second
	xvfbDial    = 500 * time.Millisecond // 存活检测的拨号超时
)

// pickDisplay 决定显示方式：有 DISPLAY 就用它；否则装了 Xvfb 就用共享的 Xvfb；都没有则无头。
// 返回值 env 是需要追加给 Chrome 的环境变量（可能为空）。
func pickDisplay() (env string, headful bool) {
	if os.Getenv("DISPLAY") != "" {
		return "", true
	}
	if _, err := exec.LookPath("Xvfb"); err == nil && sharedXvfb() {
		return "DISPLAY=" + xvfbDisplay, true
	}
	return "", false
}

var xvfb struct {
	mu     sync.Mutex
	failed bool // 启动失败过：以后都用无头模式，不再每次等待
}

// sharedXvfb 确保共享的 Xvfb（:99）在运行：每次先拨号确认存活，不在就启动一个
// （包括先前启动的已经退出的情况）；启动失败过一次则以后都用无头模式。
func sharedXvfb() bool {
	xvfb.mu.Lock()
	defer xvfb.mu.Unlock()
	if socketAlive(xvfbSocket) {
		return true // 已在运行：本进程先前启动的，或服务重启前留下的
	}
	if xvfb.failed {
		return false
	}
	xvfb.failed = !startXvfb()
	return !xvfb.failed
}

func startXvfb() bool {
	cmd := exec.Command("Xvfb", xvfbDisplay, "-screen", "0", "1366x900x24")
	if err := cmd.Start(); err != nil {
		return false
	}
	exited := make(chan struct{})
	safego.Go("等待 Xvfb 退出", func() {
		defer close(exited)
		_ = cmd.Wait()
	})
	deadline := time.After(xvfbWait)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if socketAlive(xvfbSocket) {
				return true
			}
		case <-exited:
			return socketAlive(xvfbSocket) // 可能是 :99 已被别的实例占用
		case <-deadline:
			_ = cmd.Process.Kill()
			return false
		}
	}
}

// socketAlive 拨号确认 unix socket 有进程在监听。只看文件是否存在不可靠：
// Xvfb 崩溃后 socket 文件会留下。
func socketAlive(path string) bool {
	c, err := net.DialTimeout("unix", path, xvfbDial)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
