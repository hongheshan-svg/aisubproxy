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

// xvfbRetryAfter：Xvfb 启动失败后这段时间内直接用无头模式（不再每次等 5 秒启动），过后再试。
const xvfbRetryAfter = 5 * time.Minute

// xvfbFallbackAlert 是退回无头模式时的告警：无头模式更容易被 Cloudflare 挑战拦下。
const xvfbFallbackAlert = "付款浏览器改用无头模式，Cloudflare 挑战可能增多：Xvfb 启动失败，5 分钟后再试"

// Xvfb 的启动、存活检测和时钟；测试可替换。
var (
	xvfbStart = startXvfb
	xvfbAlive = func() bool { return socketAlive(xvfbSocket) }
	xvfbNow   = time.Now
)

var xvfb struct {
	mu       sync.Mutex
	failedAt time.Time // 最近一次启动失败的时刻；零值表示没有失败（或已恢复）
	alerted  bool      // 这一轮退回无头模式已经告警过；Xvfb 恢复后清零，下次再退回时再告警
}

func resetXvfbState() {
	xvfb.mu.Lock()
	xvfb.failedAt, xvfb.alerted = time.Time{}, false
	xvfb.mu.Unlock()
}

// sharedXvfb 确保共享的 Xvfb（:99）在运行：每次先拨号确认存活，不在就启动一个（包括先前启动的已经退出的情况）。
// 启动失败时退回无头模式并告警一次；失败后 xvfbRetryAfter 内不再尝试，过后再试，不永久退回无头模式。
func sharedXvfb() bool {
	xvfb.mu.Lock()
	defer xvfb.mu.Unlock()
	if xvfbAlive() {
		return true // 已在运行：本进程先前启动的，或服务重启前留下的
	}
	if !xvfb.failedAt.IsZero() && xvfbNow().Sub(xvfb.failedAt) < xvfbRetryAfter {
		return false
	}
	if xvfbStart() {
		xvfb.failedAt, xvfb.alerted = time.Time{}, false
		return true
	}
	xvfb.failedAt = xvfbNow()
	if !xvfb.alerted {
		xvfb.alerted = true
		safego.Alert(xvfbFallbackAlert)
	}
	return false
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
