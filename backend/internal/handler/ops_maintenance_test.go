package handler

import (
	"sync"
	"testing"
)

// opsAlertLog 收集运维告警，并发安全。
type opsAlertLog struct {
	mu   sync.Mutex
	list []string
}

func (a *opsAlertLog) add(text string) {
	a.mu.Lock()
	a.list = append(a.list, text)
	a.mu.Unlock()
}

func (a *opsAlertLog) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.list)
}

func (a *opsAlertLog) texts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.list...)
}

// captureOpsAlerts 接管运维告警并清空「每天一次」的记录，测试结束时还原。
func captureOpsAlerts(t *testing.T) *opsAlertLog {
	t.Helper()
	a := &opsAlertLog{}
	SetOpsAlert(a.add)
	resetOpsAlertDedupe()
	t.Cleanup(func() {
		SetOpsAlert(nil)
		resetOpsAlertDedupe()
	})
	return a
}
