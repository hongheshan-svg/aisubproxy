//go:build !linux

package paybrowser

import "syscall"

// setDeathSignal 在非 Linux 平台无对应机制，什么也不做。
func setDeathSignal(*syscall.SysProcAttr) {}
