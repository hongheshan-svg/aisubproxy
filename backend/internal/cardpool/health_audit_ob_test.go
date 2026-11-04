package cardpool

import (
	"context"
	"errors"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// D-6：人工定论的单，租约在转待人工时已按 aborted 释放；定论后要能补记用卡结果，计入健康度。
// 补记不碰租约和卡的池状态，同一张单只记一次。

func obOutcomeRows(t *testing.T, orderRef string) (n int, result string) {
	t.Helper()
	if err := db.DB.QueryRow(`SELECT COUNT(*), COALESCE(MAX(result), '') FROM mw_card_outcomes WHERE order_ref = ?`, orderRef).Scan(&n, &result); err != nil {
		t.Fatal(err)
	}
	return n, result
}

func TestObRecordOutcomeAfterReleaseOnce(t *testing.T) {
	s, _, _, _ := newTestService(t)
	insertCard(t, Card{CardID: "c-ob", ProductID: "p-ob", PoolState: PoolReleasing})
	ctx := context.Background()
	if err := s.RecordOutcome(ctx, "c-ob", "plus", "PO-OB-1", "A@x", Outcome{Result: ResultSuccess}); err != nil {
		t.Fatal(err)
	}
	if n, r := obOutcomeRows(t, "PO-OB-1"); n != 1 || r != ResultSuccess {
		t.Fatalf("应补记一条成功: n=%d r=%q", n, r)
	}
	// 同一张单再记一次（例如重试）：不重复
	if err := s.RecordOutcome(ctx, "c-ob", "plus", "PO-OB-1", "A@x", Outcome{Result: ResultSuccess}); err != nil {
		t.Fatal(err)
	}
	if n, _ := obOutcomeRows(t, "PO-OB-1"); n != 1 {
		t.Fatalf("同一张单只记一次: %d", n)
	}
	// 不碰卡的池状态
	c, err := getCard(ctx, "c-ob")
	if err != nil || c.PoolState != PoolReleasing {
		t.Fatalf("补记不应改卡的池状态: %+v %v", c, err)
	}
	// 失败也能补记；aborted 不记
	if err := s.RecordOutcome(ctx, "c-ob", "plus", "PO-OB-2", "b@x", Outcome{Result: ResultFailed, ErrorCode: "manual"}); err != nil {
		t.Fatal(err)
	}
	if n, r := obOutcomeRows(t, "PO-OB-2"); n != 1 || r != ResultFailed {
		t.Fatalf("应补记一条失败: n=%d r=%q", n, r)
	}
	if err := s.RecordOutcome(ctx, "c-ob", "plus", "PO-OB-3", "c@x", Outcome{Result: ResultAborted}); err != nil {
		t.Fatal(err)
	}
	if n, _ := obOutcomeRows(t, "PO-OB-3"); n != 0 {
		t.Fatalf("aborted 不计入健康度: %d", n)
	}
	// 参数不对、卡不存在
	if err := s.RecordOutcome(ctx, "c-ob", "plus", "PO-OB-4", "", Outcome{Result: "weird"}); err == nil {
		t.Fatal("结果不合法应报错")
	}
	if err := s.RecordOutcome(ctx, "c-none", "plus", "PO-OB-5", "", Outcome{Result: ResultSuccess}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("卡不存在应 ErrNotFound: %v", err)
	}
	if err := s.RecordOutcome(ctx, "c-ob", "plus", "", "", Outcome{Result: ResultSuccess}); err == nil {
		t.Fatal("缺单号应报错：没有单号就没法保证只记一次")
	}
}
