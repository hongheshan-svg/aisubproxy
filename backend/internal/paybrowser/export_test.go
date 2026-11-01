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
