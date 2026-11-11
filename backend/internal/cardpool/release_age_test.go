package cardpool

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// releasedAt 读卡的「进入回收的时间」；为空返回 0。
func releasedAt(t *testing.T, cardID string) int64 {
	t.Helper()
	var v sql.NullInt64
	if err := db.DB.QueryRow(`SELECT released_at FROM mw_cards WHERE card_id = ?`, cardID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v.Int64
}

func summaryText(t *testing.T, s *Service, al *alertLog) string {
	t.Helper()
	before := al.Count()
	if err := s.dailyReviewSummary(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := al.Texts()
	if len(got) == before {
		return ""
	}
	return got[len(got)-1]
}

// 每日汇总「释放超过 24 小时仍未回收」按进入回收的时间数（C1-04）：回收每次重试都会改 updated_at，
// 卡住多天的卡以前永远数不到。只数回收中的卡；老数据没有进入回收时间的按 updated_at 兜底。
func TestDailySummaryCountsStuckReleasingByReleasedAt(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, al := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	clock.Set(time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC)) // 北京 12:00
	insertCard(t, Card{CardID: "c1", ProductID: "p1", UseCount: 0})
	savePolicy(t, func(p *Policy) { p.MaxUsesPerCard = 3 })
	l := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt"})
	if err := s.Release(ctx, l, Outcome{Result: ResultAborted}); err != nil {
		t.Fatal(err)
	}
	t0 := clock.Now().Unix()
	if got := releasedAt(t, "c1"); got != t0 {
		t.Fatalf("释放时应写入进入回收的时间: %d，应为 %d", got, t0)
	}

	// 只剩冻结余额：回收每 10 分钟刷新一次，updated_at 一直是新的，进入回收的时间不变
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "0.00", "1.00", "ACTIVE")))
	for i := 0; i < 6*25; i++ {
		_ = s.advanceCardsOnce(ctx)
		clock.Advance(10 * time.Minute)
	}
	c := mustGetCard(t, "c1")
	if c.PoolState != PoolReleasing || c.UpdatedAt < clock.Now().Unix()-600 {
		t.Fatalf("卡应仍在回收中、updated_at 刚更新过: %+v", c)
	}
	if got := releasedAt(t, "c1"); got != t0 {
		t.Fatalf("回收推进不应改进入回收的时间: %d，应为 %d", got, t0)
	}

	now := clock.Now().Unix()
	// 老数据：没有进入回收的时间，按 updated_at 兜底
	insertCard(t, Card{CardID: "old-stale", ProductID: "p1", PoolState: PoolReleasing, UpdatedAt: now - 2*86400})
	insertCard(t, Card{CardID: "old-fresh", ProductID: "p1", PoolState: PoolReleasing, UpdatedAt: now - 3600})
	// 已用的卡不算，即使进入回收很久了
	insertCard(t, Card{CardID: "u1", ProductID: "p1", PoolState: PoolUsed})
	if _, err := db.DB.Exec(`UPDATE mw_cards SET released_at = ? WHERE card_id = 'u1'`, now-3*86400); err != nil {
		t.Fatal(err)
	}
	// 刚进入回收的不算
	insertCard(t, Card{CardID: "r-new", ProductID: "p1", PoolState: PoolReleasing, UpdatedAt: now - 60})
	if _, err := db.DB.Exec(`UPDATE mw_cards SET released_at = ? WHERE card_id = 'r-new'`, now-3600); err != nil {
		t.Fatal(err)
	}

	text := summaryText(t, s, al)
	if !strings.Contains(text, "2 张卡释放超过 24 小时仍未回收") {
		t.Fatalf("应数到 c1 和 old-stale 两张: %q", text)
	}
}

// 开卡成功建卡、导入卡（都直接进入回收）时写入进入回收的时间；再次释放时更新为新的时间。
func TestReleasedAtWrittenOnApplyImportAndRelease(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	savePolicy(t, func(p *Policy) { p.MaxUsesPerCard = 3 })

	o := insertOrder(t, Order{Kind: KindApply, CardHolderID: "h1", ProductID: "p1", Currency: "USD", State: StateProcessing})
	if _, err := s.finalize(ctx, o.OutOrderNo, queryResult{State: StateSuccess, CardID: "n1"}, "test"); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "n1"); c.PoolState != PoolReleasing {
		t.Fatalf("开卡成功应建卡并进入回收: %+v", c)
	}
	if got := releasedAt(t, "n1"); got != clock.Now().Unix() {
		t.Fatalf("开卡成功建卡应写入进入回收的时间: %d", got)
	}

	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("GET", detailPath("c1"), mwtest.OK(cardJSON("USD", "0.00", "0.00", "ACTIVE")))
	clock.Advance(time.Minute)
	if _, err := s.ImportCard(ctx, "c1", "p1", "", "admin"); err != nil {
		t.Fatal(err)
	}
	if got := releasedAt(t, "c1"); got != clock.Now().Unix() {
		t.Fatalf("导入卡应写入进入回收的时间: %d", got)
	}

	// 清零回到可用，再租再放：进入回收的时间是第二次释放的时间（n1 不参与）
	if _, err := db.DB.Exec(`UPDATE mw_cards SET pool_state = 'retired' WHERE card_id = 'n1'`); err != nil {
		t.Fatal(err)
	}
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "0.00", "0.00", "ACTIVE")))
	mustAdvance(t, s)
	if c := mustGetCard(t, "c1"); c.PoolState != PoolAvailable {
		t.Fatalf("清零后应回到可用: %+v", c)
	}
	clock.Advance(2 * time.Hour)
	l := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt"})
	if l.CardID != "c1" {
		t.Fatalf("应租到 c1: %+v", l)
	}
	clock.Advance(time.Minute)
	if err := s.Release(ctx, l, Outcome{Result: ResultAborted}); err != nil {
		t.Fatal(err)
	}
	if got := releasedAt(t, "c1"); got != clock.Now().Unix() {
		t.Fatalf("再次释放应更新进入回收的时间: %d，应为 %d", got, clock.Now().Unix())
	}
}
