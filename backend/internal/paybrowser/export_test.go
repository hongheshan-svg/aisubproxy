package paybrowser

import (
	"context"
	"time"

	"github.com/chromedp/chromedp"
)

// 测试专用的导出项，生产代码路径不使用。

// LoopbackProxy 让 Launch 对回环地址也走代理（--proxy-bypass-list=<-loopback>），
// 以便测试用本机代理与本机仿造页。
func LoopbackProxy(o LaunchOptions) LaunchOptions {
	o.proxyLoopback = true
	return o
}

// ProfileDir 返回浏览器的临时用户目录。
func ProfileDir(b *Browser) string { return b.dir }

// ChromeIdentity 决定以哪个系统用户启动 Chrome。
var ChromeIdentity = chromeIdentity

// SocketAlive 判断 unix socket 是否有进程在监听。
var SocketAlive = socketAlive

// Truncate 把页面提示文字去掉首尾空白并截断到 500 字。
var Truncate = truncateMessage

// ClassifyPageError 按文字内容判断页面提示属于被拒还是表单校验未通过。
var ClassifyPageError = classifyPageError

// ScanBeforeClick 执行一次点击前的提示收集，返回收集到的文字集合。
// 不走 b.do：它收尾时释放句柄用的是调用方的 ctx，页面卡死时会等满那个 ctx，量不出收集本身的耗时。
func ScanBeforeClick(ctx context.Context, b *Browser) (map[string]bool, error) {
	var got map[string]bool
	err := b.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		got = b.scanBeforeClick(c)
		return nil
	}))
	return got, err
}

// ShortenFillWaits 缩短地址字段的等待时限，返回恢复函数。用来测「一直等不到」的情形，免得每个用例等满 15 秒。
func ShortenFillWaits(address time.Duration) (restore func()) {
	old := addressWait
	addressWait = address
	return func() { addressWait = old }
}

// InstallHintFor 返回当前部署方式下的安装提示。
var InstallHintFor = installHint

// ExplainSandboxError 给 Chrome 沙箱类错误加上中文说明。
var ExplainSandboxError = explainSandboxError
