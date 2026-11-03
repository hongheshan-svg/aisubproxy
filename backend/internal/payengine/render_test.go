package payengine

import (
	"strings"
	"testing"
)

// 支付页的截图要等页面真正显示出来再拍：Stripe 先把金额放进 DOM、盖着加载骨架再淡入，
// 紧跟 Open 拍到的只是骨架，留作凭证没有用。
func TestOpeningShotTakenAfterPageRendered(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	o := h.submit(t, true)
	if g := waitDone(t, o.OrderNo); g.State != StatePreviewed {
		t.Fatalf("试运行应以 previewed 结束: %+v", g)
	}
	seq := theBrowser(t, h).order()
	idx := map[string]int{}
	for i, name := range seq {
		if _, seen := idx[name]; !seen {
			idx[name] = i
		}
	}
	for _, name := range []string{"Open", "WaitRendered", "Shot", "ReadAmount"} {
		if _, ok := idx[name]; !ok {
			t.Fatalf("没有调用 %s: %v", name, seq)
		}
	}
	want := []string{"Open", "WaitRendered", "Shot", "ReadAmount"}
	for i := 1; i < len(want); i++ {
		if idx[want[i-1]] >= idx[want[i]] {
			t.Fatalf("顺序应为 打开 → 等页面显示 → 截图 → 读金额，实际 %v", seq)
		}
	}
}

// 页面一直没显示完整也照常往下走（只影响截图好不好看），并在时间线上说明截图可能是加载中的画面。
func TestPageNotRenderedStillProceeds(t *testing.T) {
	h := newHarness(t)
	h.br.notRendered = true
	h.start(t)
	o := h.submit(t, true)
	if g := waitDone(t, o.OrderNo); g.State != StatePreviewed {
		t.Fatalf("页面没显示完整不应中断试运行: %+v", g)
	}
	found := false
	for _, e := range eventStates(t, o.OrderNo) {
		if strings.Contains(e.Message, "支付页") && strings.Contains(e.Message, "没有显示完整") {
			found = true
		}
	}
	if !found {
		t.Fatalf("时间线应说明页面没显示完整: %+v", eventStates(t, o.OrderNo))
	}
}
