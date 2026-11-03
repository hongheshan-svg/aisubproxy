package cardpool

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

func countAlerts(a *alertLog, sub string) int {
	n := 0
	for _, t := range a.Texts() {
		if strings.Contains(t, sub) {
			n++
		}
	}
	return n
}

// MO-05：只剩冻结余额的 releasing 卡，超过 24 小时每天告警一次，超过 7 天转 disabled 并告警。
func TestReleasingFrozenOnlyAlertsThenDisables(t *testing.T) {
	s, mw, clock, alerts := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing})
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "0.00", "1.00", "ACTIVE")))

	mustAdvance(t, s) // 第一次发现只剩冻结余额，开始计时
	clock.Advance(12 * time.Hour)
	mustAdvance(t, s)
	if n := countAlerts(alerts, "c1"); n != 0 {
		t.Fatalf("12 小时内不应告警: %v", alerts.Texts())
	}

	clock.Advance(13 * time.Hour) // 共 25 小时
	mustAdvance(t, s)
	if n := countAlerts(alerts, "c1"); n != 1 {
		t.Fatalf("超过 24 小时应告警 1 次: %v", alerts.Texts())
	}
	clock.Advance(601 * time.Second)
	mustAdvance(t, s)
	if n := countAlerts(alerts, "c1"); n != 1 {
		t.Fatalf("同一天不应重复告警: %v", alerts.Texts())
	}

	clock.Advance(24 * time.Hour) // 下一个北京日
	mustAdvance(t, s)
	if n := countAlerts(alerts, "c1"); n != 2 {
		t.Fatalf("第二天应再告警 1 次: %v", alerts.Texts())
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolReleasing {
		t.Fatalf("未满 7 天不应停用: %+v", c)
	}

	clock.Advance(6 * 24 * time.Hour) // 超过 7 天
	mustAdvance(t, s)
	c := mustGetCard(t, "c1")
	if c.PoolState != PoolDisabled || c.DisabledReason != "冻结余额长期未释放" {
		t.Fatalf("超过 7 天应转 disabled: %+v", c)
	}
	if n := countAlerts(alerts, "已从卡池停用"); n != 1 {
		t.Fatalf("停用应告警 1 次: %v", alerts.Texts())
	}
	reqs := len(mw.Requests("GET", detailPath("c1")))
	clock.Advance(time.Hour)
	mustAdvance(t, s)
	if n := len(mw.Requests("GET", detailPath("c1"))); n != reqs {
		t.Fatalf("停用后不应再刷新: %d -> %d", reqs, n)
	}
}

// 冻结余额释放后计时清零：之后再次冻结要重新累计，不能沿用旧的开始时间。
func TestFrozenSinceResetsWhenBalanceClears(t *testing.T) {
	s, mw, clock, alerts := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing})
	mw.On("GET", detailPath("c1"),
		mwtest.OK(detailJSON("c1", "0.00", "1.00", "ACTIVE")),
		mwtest.OK(detailJSON("c1", "0.00", "0.00", "ACTIVE")),
		mwtest.OK(detailJSON("c1", "0.00", "1.00", "ACTIVE")))
	mustAdvance(t, s)
	clock.Advance(23 * time.Hour)
	mustAdvance(t, s) // 冻结清零，回到 available
	if c := mustGetCard(t, "c1"); c.PoolState != PoolAvailable {
		t.Fatalf("冻结清零应回到 available: %+v", c)
	}
	// 之后又回到 releasing 且再次冻结
	if _, err := db.DB.Exec(`UPDATE mw_cards SET pool_state = 'releasing', next_action_at = ? WHERE card_id = 'c1'`, clock.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	mustAdvance(t, s)
	clock.Advance(2 * time.Hour)
	mustAdvance(t, s)
	if n := countAlerts(alerts, "c1"); n != 0 {
		t.Fatalf("重新计时后 2 小时不应告警: %v", alerts.Texts())
	}
}

// 只剩冻结余额超过 24 小时的 releasing 卡不占补卡缺口。
func TestReplenishGapExcludesLongFrozenReleasing(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing})
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "0.00", "1.00", "ACTIVE")))
	mustAdvance(t, s)
	ctx := context.Background()
	gap := func() int {
		g, err := s.replenishGap(ctx, "p1", 1, 100)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	if g := gap(); g != 0 {
		t.Fatalf("刚冻结的 releasing 卡仍占缺口: gap=%d", g)
	}
	clock.Advance(25 * time.Hour)
	mustAdvance(t, s)
	if g := gap(); g != 1 {
		t.Fatalf("冻结超过 24 小时的卡应从缺口中剔除: gap=%d", g)
	}
}
