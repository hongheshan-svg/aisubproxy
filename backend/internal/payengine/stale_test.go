package payengine

import (
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// presetStale 预置一张处于 state 的单，并把 updated_at 改到 age 之前（用真实时钟，与 store 写 updated_at 一致）。
func presetStale(t *testing.T, email, state string, age time.Duration, set map[string]any) *Order {
	t.Helper()
	o := presetOrder(t, email, state, set)
	if _, err := db.DB.Exec(`UPDATE pay_orders SET updated_at = ? WHERE order_no = ?`, time.Now().Add(-age).Unix(), o.OrderNo); err != nil {
		t.Fatal(err)
	}
	return o
}

// S2：停在提交前中间状态、没有协程在处理、超过 5 分钟没进展的单：释放租约，记 failed/not_submitted/stale_order。
// 正在处理的、刚更新过的、已经提交的（submitted、requires_3ds）都不动。
func TestSweepStaleAbandonsStuckPreSubmit(t *testing.T) {
	h := newPayHarness(t, nil)
	h.start(t) // 启动时的恢复先做完：之后预置的单只能由扫描收敛
	lease := map[string]any{"card_id": "card-s", "lease_token": "tok-s"}
	stuck := presetStale(t, "stuck@example.com", StateFilling, 10*time.Minute, lease)
	running := presetStale(t, "running@example.com", StateFilling, 10*time.Minute, nil)
	fresh := presetStale(t, "fresh@example.com", StateFunding, time.Minute, nil)
	now := h.clock.Now().Unix()
	submitted := presetStale(t, "sub@example.com", StateSubmitted, 10*time.Minute, map[string]any{"submitted_at": now})
	threeDS := presetStale(t, "3ds@example.com", StateRequires3DS, 10*time.Minute, map[string]any{"submitted_at": now})
	h.svc.mu.Lock()
	h.svc.running[running.OrderNo] = true
	h.svc.mu.Unlock()
	t.Cleanup(func() {
		h.svc.mu.Lock()
		delete(h.svc.running, running.OrderNo)
		h.svc.mu.Unlock()
	})

	h.svc.sweepStale(ctx)

	g := mustOrder(t, stuck.OrderNo)
	assertNotSubmitted(t, g, "stale_order")
	if g.ErrorMessage != "单据长时间没有进展，已放弃（没有点支付）" {
		t.Fatalf("说明不符: %q", g.ErrorMessage)
	}
	rel := assertReleases(t, h, cardpool.ResultAborted)
	if rel[0].Lease.Token != "tok-s" || rel[0].Lease.OrderRef != stuck.OrderNo {
		t.Fatalf("应释放这单的租约: %+v", rel[0])
	}
	if a := waitAlerts(t, h, 1); !strings.Contains(a[0], stuck.OrderNo) {
		t.Fatalf("应告警并带单号: %v", a)
	}
	for _, o := range []*Order{running, fresh, submitted, threeDS} {
		if g := mustOrder(t, o.OrderNo); g.State != o.State || g.ErrorCode != "" {
			t.Fatalf("单据 %s（%s）不应被动: %+v", o.OrderNo, o.State, g)
		}
	}
	if g := mustOrder(t, submitted.OrderNo); g.SubmittedAt == nil {
		t.Fatal("已提交的单不能被清掉提交时间")
	}
}

// S2：扫描由清理协程定期执行。
func TestSweepStaleRunsPeriodically(t *testing.T) {
	old := staleSweepEvery
	staleSweepEvery = 20 * time.Millisecond
	t.Cleanup(func() { staleSweepEvery = old })
	h := newPayHarness(t, nil)
	h.start(t)
	o := presetStale(t, "stuck@example.com", StateOpening, 10*time.Minute, nil)
	g := waitDone(t, o.OrderNo)
	assertNotSubmitted(t, g, "stale_order")
	if len(h.pool.released()) != 0 {
		t.Fatal("没有租约时不应调用 Release")
	}
}

// S2：重启恢复没做完之前不扫：那时提交前的单由恢复统一收尾（service_restart）。
func TestSweepStaleWaitsForRecovery(t *testing.T) {
	h := newPayHarness(t, nil)
	o := presetStale(t, "stuck@example.com", StateFilling, 10*time.Minute, nil)
	h.svc.sweepStale(ctx)
	if g := mustOrder(t, o.OrderNo); g.State != StateFilling {
		t.Fatalf("恢复之前不应扫描: %+v", g)
	}
}
