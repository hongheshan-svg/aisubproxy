package paybrowser

import (
	"os"
	"os/exec"
	"sync"
	"time"
)

const (
	modeHeadful  = "headful"
	modeHeadless = "headless"

	xvfbDisplay = ":99"
	xvfbSocket  = "/tmp/.X11-unix/X99"
	xvfbWait    = 5 * time.Second
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
	once sync.Once
	ok   bool
}

// sharedXvfb 在进程内只启动一次共享的 Xvfb；启动失败则以后都用无头模式。
func sharedXvfb() bool {
	xvfb.once.Do(func() { xvfb.ok = startXvfb() })
	return xvfb.ok
}

func startXvfb() bool {
	if isSocket(xvfbSocket) {
		return true // :99 已有实例在运行（例如服务重启前留下的）
	}
	cmd := exec.Command("Xvfb", xvfbDisplay, "-screen", "0", "1366x900x24")
	if err := cmd.Start(); err != nil {
		return false
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	deadline := time.After(xvfbWait)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if isSocket(xvfbSocket) {
				return true
			}
		case <-exited:
			return isSocket(xvfbSocket)
		case <-deadline:
			_ = cmd.Process.Kill()
			return false
		}
	}
}

func isSocket(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}
