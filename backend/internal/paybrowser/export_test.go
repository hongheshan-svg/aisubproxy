package paybrowser

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

// InstallHintFor 返回当前部署方式下的安装提示。
var InstallHintFor = installHint

// ExplainSandboxError 给 Chrome 沙箱类错误加上中文说明。
var ExplainSandboxError = explainSandboxError
