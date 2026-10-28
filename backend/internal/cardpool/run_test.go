package cardpool

import (
	"context"
	"testing"
	"time"
)

// waitFor 每 10ms 检查一次 cond，d 内成立返回 true。
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStartProcessesKickedEventPromptly(t *testing.T) {
	s, _, _, _ := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	// 测试库在 Cleanup 时还原，先等协程全部退出
	t.Cleanup(func() { cancel(); s.loops.Wait() })

	id := enqueueEvent(t, "SOMETHING_NEW", `{}`)
	s.Kick()
	if !waitFor(time.Second, func() bool { return getEvent(t, id).processed }) {
		t.Fatal("Kick 后 1 秒内未处理")
	}
	mustBeProcessed(t, id, "未知 topic")
}

func TestStartStopsOnCancel(t *testing.T) {
	s, _, _, _ := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	cancel()

	stopped := make(chan struct{})
	go func() { s.loops.Wait(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("cancel 后协程未退出")
	}

	kicked := make(chan struct{})
	go func() {
		for i := 0; i < 3; i++ {
			s.Kick()
		}
		close(kicked)
	}()
	select {
	case <-kicked:
	case <-time.After(time.Second):
		t.Fatal("Kick 阻塞")
	}

	id := enqueueEvent(t, "SOMETHING_NEW", `{}`)
	s.Kick()
	time.Sleep(200 * time.Millisecond)
	if getEvent(t, id).processed {
		t.Fatal("停止后不应再处理事件")
	}
}
