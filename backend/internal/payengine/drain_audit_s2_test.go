package payengine

import (
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// D-7：试运行单不动钱，不算在途，不挡在线更新。
func TestS2CountProcessingIgnoresDryRun(t *testing.T) {
	newPayHarness(t, nil)
	presetOrder(t, "s2real@example.com", StateFilling, nil)
	dry := presetOrder(t, "s2dry@example.com", StateFilling, nil)
	if _, err := db.DB.Exec(`UPDATE pay_orders SET dry_run = 1 WHERE order_no = ?`, dry.OrderNo); err != nil {
		t.Fatal(err)
	}
	n, err := CountProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("只应数真实单: %d", n)
	}
}
