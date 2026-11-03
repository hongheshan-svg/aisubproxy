package paybrowser

import (
	"context"
	"fmt"
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

// ChromeEnv 算出 Chrome 进程能拿到的环境变量。
var ChromeEnv = chromeEnv

// WrapChromeCmd 把启动 Chrome 的命令改成经 env -i 启动。
var WrapChromeCmd = wrapChromeCmd

// HomeInProfile 表示本平台上 Chrome 的 HOME 是否指向临时用户目录。
const HomeInProfile = homeInProfile

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

// SetDeclineSettle 设置判到被拒后再看的时长，返回恢复函数。多数用例不关心它，TestMain 统一缩短。
func SetDeclineSettle(d time.Duration) (restore func()) {
	old := declineSettle
	declineSettle = d
	return func() { declineSettle = old }
}

// SetEmailSettle 设置键入邮箱后的等待，TestMain 统一缩短。
func SetEmailSettle(d time.Duration) { emailSettle = d }

// SetFieldWait 设置等字段和支付按钮的时限，返回恢复函数。用来测「一直等不到」的情形。
func SetFieldWait(d time.Duration) (restore func()) {
	old := fieldWait
	fieldWait = d
	return func() { fieldWait = old }
}

// FailClick 让点支付按钮的动作出错。afterMouse 为 true 时鼠标已经发出去才报错，为 false 时什么都没发就报错。
func FailClick(b *Browser, afterMouse bool, err error) {
	b.clickPay = func(ctx context.Context, e *elem) error {
		if afterMouse {
			if cerr := click(ctx, e); cerr != nil {
				return cerr
			}
		}
		return err
	}
}

// MaskPageText 抹掉页面文字里的邮箱和长数字串。
var MaskPageText = maskPageText

// SafeLocation 只留地址的主机和路径。
var SafeLocation = safeLocation

// InstallHintFor 返回当前部署方式下的安装提示。
var InstallHintFor = installHint

// ExplainSandboxError 给 Chrome 沙箱类错误加上中文说明。
var ExplainSandboxError = explainSandboxError

// SetCFProbeURL 把自检探测地址换成本机仿造页，返回还原函数。
func SetCFProbeURL(u string) func() {
	old := cfProbeURL
	cfProbeURL = u
	return func() { cfProbeURL = old }
}

// IsCFChallengeTitle 判断标题是不是 Cloudflare 挑战页。
var IsCFChallengeTitle = isCFChallengeTitle

// AmountRendered 只判断一次金额是否已真正显示（不等待）。
func AmountRendered(ctx context.Context, b *Browser) (bool, error) { return b.amountRendered(ctx) }

// SetPageLoadTimeout 设置打开支付页的时限，返回恢复函数。用来测「一直等不到」的情形，免得每个用例等满 60 秒。
func SetPageLoadTimeout(d time.Duration) (restore func()) {
	old := pageLoadTimeout
	pageLoadTimeout = d
	return func() { pageLoadTimeout = old }
}

// NormalizeEmbeddedAmount 把内嵌页读屏金额文字规范成「数字 + 空格 + 大写币种代码」。
var NormalizeEmbeddedAmount = normalizeEmbeddedAmount

// FieldValue 在各 frame 里找 sel 命中的第一个元素（不要求可见），返回它的 value。用来核对 iframe 里的字段。
func FieldValue(ctx context.Context, b *Browser, sel string) (string, error) {
	var v string
	err := callOnFirst(ctx, b, sel, `function () { return String(this.value) }`, &v)
	return v, err
}

// FieldChecked 在各 frame 里找 sel 命中的第一个元素（不要求可见），返回它是否勾着。
func FieldChecked(ctx context.Context, b *Browser, sel string) (bool, error) {
	var v bool
	err := callOnFirst(ctx, b, sel, `function () { return !!this.checked }`, &v)
	return v, err
}

func callOnFirst(ctx context.Context, b *Browser, sel, fn string, res any) error {
	return b.do(ctx, func(ctx context.Context) error {
		frames, err := b.frames(ctx)
		if err != nil {
			return err
		}
		e := b.findAny(ctx, frames, []string{sel})
		if e == nil {
			return fmt.Errorf("找不到 %s", sel)
		}
		return callOn(ctx, e.obj, fn, res)
	})
}
