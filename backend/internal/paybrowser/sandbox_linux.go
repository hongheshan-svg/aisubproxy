package paybrowser

import "syscall"

// homeInProfile：Linux 上 Chrome 的 HOME 指向本次的临时用户目录。
const homeInProfile = true

// setDeathSignal 让 Go 进程退出时 Chrome 随之被杀（同 chromedp 在 Linux 上的默认做法）。
func setDeathSignal(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }
