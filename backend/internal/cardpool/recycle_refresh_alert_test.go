package cardpool

import (
	"context"
	"strings"
	"testing"
	"time"

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

// 刷新失败告警和回收提交失败告警用不同的 key，互不压掉。
func TestRecycleRefreshAlertKeySeparateFromRecycle(t *testing.T) {
	s, _, _, al := newTestService(t)
	s.alert("recycle:c1", "回收提交失败")
	c := &Card{CardID: "c1", PoolState: PoolReleasing}
	s.noteRefreshFailure(c, errText("x"))
	s.opts.Now = func() time.Time { return testNow().Add(31 * time.Minute) }
	s.noteRefreshFailure(c, errText("x"))
	if al.Count() != 2 {
		t.Fatalf("回收提交告警之后，刷新失败告警仍应发出: %v", al.Texts())
	}
}

type errText string

func (e errText) Error() string { return string(e) }
