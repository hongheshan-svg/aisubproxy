package cardpool

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// 回收时刷新卡详情持续失败满 30 分钟才告警（C1-02）：按卡每天一条，key recycle-refresh:<卡>，
// 文字只带不含平台原文的摘要；刷新成功一次就重新计时。
func TestRecycleRefreshFailureAlertsAfter30Minutes(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, al := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing})
	fail := mwtest.Reply{Status: 400, Body: `{"code":"E_CARD","message":"platform-secret-detail"}`}
	mw.On("GET", detailPath("c1"), fail)

	run := func() {
		t.Helper()
		_ = s.advanceCardsOnce(ctx) // 刷新失败会返回错误，这里只看告警
	}
	run()
	for i := 0; i < 29; i++ {
		clock.Advance(time.Minute)
		run()
	}
	if al.Count() != 0 {
		t.Fatalf("失败不满 30 分钟不应告警: %v", al.Texts())
	}
	clock.Advance(time.Minute)
	run()
	got := al.Texts()
	if len(got) != 1 {
		t.Fatalf("满 30 分钟应告警一次: %v", got)
	}
	if !strings.Contains(got[0], "c1") || !strings.Contains(got[0], "刷新卡详情") || !strings.Contains(got[0], "E_CARD") {
		t.Fatalf("告警应写明卡、刷新卡详情和错误摘要: %s", got[0])
	}
	if strings.Contains(got[0], "platform-secret-detail") {
		t.Fatalf("告警不应带平台原文: %s", got[0])
	}
	for i := 0; i < 30; i++ {
		clock.Advance(time.Minute)
		run()
	}
	if al.Count() != 1 {
		t.Fatalf("同一天同一张卡只告警一次: %v", al.Texts())
	}

	// 成功一次（只剩冻结余额，仍在回收中）：计时清零
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "0.00", "1.00", "ACTIVE")))
	clock.Advance(time.Minute)
	run()
	if c := mustGetCard(t, "c1"); c.PoolState != PoolReleasing || c.FrozenCents != 100 {
		t.Fatalf("刷新应成功，卡仍在回收中: %+v", c)
	}

	// 第二天又失败：从头计时，10 分钟不告警，满 30 分钟再告警
	clock.Advance(24 * time.Hour)
	mw.On("GET", detailPath("c1"), fail)
	run()
	for i := 0; i < 10; i++ {
		clock.Advance(time.Minute)
		run()
	}
	if al.Count() != 1 {
		t.Fatalf("成功过一次后应重新计时，不满 30 分钟不告警: %v", al.Texts())
	}
	for i := 0; i < 20; i++ {
		clock.Advance(time.Minute)
		run()
	}
	if al.Count() != 2 {
		t.Fatalf("第二天再满 30 分钟应再告警一次: %v", al.Texts())
	}
}

// 失败计时不跨越「离开回收」（C1-02 审查修复）：刷新失败后卡离开回收，几天后回到回收、第一次刷新失败，
// 不能马上告警「已连续失败几千分钟」，要从头计时。
func TestRecycleRefreshFailTimerResetsAfterLeavingRecycle(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, al := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing})
	mw.On("GET", detailPath("c1"), mwtest.Rejected(400, "E_CARD"))
	_ = s.advanceCardsOnce(ctx)

	// 离开回收（例如被外部冻结推送转成停用），3 天后启用、再用、再释放，回到回收
	if _, err := db.DB.Exec(`UPDATE mw_cards SET pool_state = ?, next_action_at = NULL WHERE card_id = 'c1'`, PoolDisabled); err != nil {
		t.Fatal(err)
	}
	clock.Advance(3 * 24 * time.Hour)
	_ = s.advanceCardsOnce(ctx)
	if _, err := db.DB.Exec(`UPDATE mw_cards SET pool_state = ?, next_action_at = NULL WHERE card_id = 'c1'`, PoolReleasing); err != nil {
		t.Fatal(err)
	}
	_ = s.advanceCardsOnce(ctx)
	if al.Count() != 0 {
		t.Fatalf("回到回收后只失败了一次，不应告警: %v", al.Texts())
	}
	for i := 0; i < 29; i++ {
		clock.Advance(time.Minute)
		_ = s.advanceCardsOnce(ctx)
	}
	if al.Count() != 0 {
		t.Fatalf("回到回收后失败不满 30 分钟不应告警: %v", al.Texts())
	}
	clock.Advance(time.Minute)
	_ = s.advanceCardsOnce(ctx)
	if got := al.Texts(); len(got) != 1 || !strings.Contains(got[0], "已连续失败 30 分钟") {
		t.Fatalf("回到回收后满 30 分钟应告警一次，按回到回收后的时间算: %v", got)
	}
}

// 失败计时不跨越长时间没刷新：卡上的单据进了待人工、整张卡被跳过几小时，单据定论后第一次刷新失败也要从头计时。
func TestRecycleRefreshFailTimerResetsAfterLongSkip(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, al := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing})
	mw.On("GET", detailPath("c1"), mwtest.Rejected(400, "E_CARD"))
	_ = s.advanceCardsOnce(ctx)

	o := insertOrder(t, Order{Kind: KindWithdraw, CardID: "c1", AmountCents: 100, Currency: "USD", State: StateNeedsReview})
	for i := 0; i < 6; i++ {
		clock.Advance(time.Hour)
		_ = s.advanceCardsOnce(ctx) // 有未定论单据，整张卡跳过
	}
	if n := len(mw.Requests("GET", detailPath("c1"))); n != 1 {
		t.Fatalf("待人工期间不应刷新: %d", n)
	}
	if _, err := db.DB.Exec(`UPDATE mw_card_orders SET state = ? WHERE out_order_no = ?`, StateFailed, o.OutOrderNo); err != nil {
		t.Fatal(err)
	}
	_ = s.advanceCardsOnce(ctx)
	if n := len(mw.Requests("GET", detailPath("c1"))); n != 2 {
		t.Fatalf("单据定论后应再刷新: %d", n)
	}
	if al.Count() != 0 {
		t.Fatalf("隔了 6 小时只失败了一次，不应告警: %v", al.Texts())
	}
}

// 刷新失败告警和回收提交失败告警用不同的 key，互不压掉。
func TestRecycleRefreshAlertKeySeparateFromRecycle(t *testing.T) {
	s, _, _, al := newTestService(t)
	s.alert("recycle:c1", "回收提交失败")
	c := &Card{CardID: "c1", PoolState: PoolReleasing}
	s.noteRefreshFailure(c, errText("x"))
	s.opts.Now = func() time.Time { return testNow().Add(15 * time.Minute) }
	s.noteRefreshFailure(c, errText("x"))
	s.opts.Now = func() time.Time { return testNow().Add(31 * time.Minute) }
	s.noteRefreshFailure(c, errText("x"))
	if al.Count() != 2 {
		t.Fatalf("回收提交告警之后，刷新失败告警仍应发出: %v", al.Texts())
	}
}

type errText string

func (e errText) Error() string { return string(e) }
