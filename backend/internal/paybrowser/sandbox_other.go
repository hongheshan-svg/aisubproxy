//go:build !linux

package paybrowser

import "syscall"

// homeInProfile：macOS 上把 HOME 换成临时目录后 Chrome 打不开本机页面（实测），开发机沿用继承来的 HOME。
const homeInProfile = false

// setDeathSignal 在非 Linux 平台无对应机制，什么也不做。
func setDeathSignal(*syscall.SysProcAttr) {}
