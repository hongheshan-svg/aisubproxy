package payengine

import (
	"testing"
	"time"
)

type drainResult struct {
	left   int
	resume func()
}

// startDrain 在后台调用 Drain，结果从返回的通道取。
func startDrain(h *harness, max time.Duration) <-chan drainResult {
	done := make(chan drainResult, 1)
	go func() {
		left, resume := h.svc.Drain(max, 20*time.Millisecond)
		done <- drainResult{left, resume}
	}()
	return done
}

// S1：停止服务时先停止派发，在途单走完（到确认或终态）之前 Drain 不返回；排队的单不再派发，恢复派发后照常处理。
func TestDrainWaitsForInFlightAndStopsDispatch(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.openGate = make(chan struct{})
	h.start(t)
	first := h.submit(t, true)
	waitState(t, first.OrderNo, StateOpening)
	second := h.submitAs(t, sessionFor("second@example.com"), true)

	done := startDrain(h, 5*time.Second)
	select {
	case r := <-done:
		t.Fatalf("在途单没走完不应返回: %+v", r)
	case <-time.After(150 * time.Millisecond):
	}
	close(h.br.openGate)
	var r drainResult
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("在途单走完后应返回")
	}
	if r.left != 0 {
		t.Fatalf("走完后应没有在途单: %d", r.left)
	}
	if g := waitDone(t, first.OrderNo); g.State != StatePreviewed {
		t.Fatalf("在途单应照常走完: %+v", g)
	}
	// 停止派发：排队的单不动
	h.svc.Kick()
	time.Sleep(150 * time.Millisecond)
	if g := mustOrder(t, second.OrderNo); g.State != StateQueued {
		t.Fatalf("停止派发后排队的单不应开始处理: %+v", g)
	}
	// 恢复派发（在线更新重载失败时）：排队的单接着处理
	r.resume()
	r.resume() // 重复调用无害
	if g := waitDone(t, second.OrderNo); g.State != StatePreviewed {
		t.Fatalf("恢复派发后排队的单应照常处理: %+v", g)
	}
}

// S1：在途单超过等待上限还没走完，Drain 照样返回，并报告还剩几张；单据不被打断。
func TestDrainGivesUpAtLimit(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.openGate = make(chan struct{})
	t.Cleanup(func() { close(h.br.openGate) })
	h.start(t)
	o := h.submit(t, true)
	waitState(t, o.OrderNo, StateOpening)

	begin := time.Now()
	var r drainResult
	select {
	case r = <-startDrain(h, 200*time.Millisecond):
	case <-time.After(5 * time.Second):
		t.Fatal("超过上限应返回")
	}
	if el := time.Since(begin); el < 200*time.Millisecond {
		t.Fatalf("不应早于上限返回: %v", el)
	}
	if r.left != 1 {
		t.Fatalf("应报告还剩 1 张在途单: %d", r.left)
	}
	if g := mustOrder(t, o.OrderNo); g.State != StateOpening {
		t.Fatalf("等待超时不应打断单据: %+v", g)
	}
}

// S1：在途状态是离开排队之后、进入确认之前的那几个；排队、确认中和终态不算。
func TestCountProcessing(t *testing.T) {
	newPayHarness(t, nil) // 只用它建好的内存库和主密钥
	for i, st := range []string{StateQueued, StateChecking, StateLinkCreated, StateOpening, StateFunding, StateFilling,
		StateSubmitted, StateRequires3DS, StateVerifying, StateSucceeded, StateFailed, StateNeedsReview, StatePreviewed} {
		presetOrder(t, "u"+string(rune('a'+i))+"@example.com", st, nil)
	}
	n, err := CountProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Fatalf("在途单应为 7 张: %d", n)
	}
}
