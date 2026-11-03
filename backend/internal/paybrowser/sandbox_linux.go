package paybrowser

import "syscall"

// setDeathSignal 让 Go 进程退出时 Chrome 随之被杀（同 chromedp 在 Linux 上的默认做法）。
func setDeathSignal(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }
