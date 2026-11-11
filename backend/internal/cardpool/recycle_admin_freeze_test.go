package cardpool

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// 管理员冻结一张回收中的卡（C1-06）：回收发现平台 REVOKED 时按「管理员冻结」停用，不发「非本系统发起」告警。
// 回收中的卡可能隔 10 分钟才再查一次（只剩冻结余额、被限流），所以冻结单成功超过 600 秒也要认得出。
func TestRecycleReleasingFrozenByAdmin(t *testing.T) {
	for _, wait := range []time.Duration{5 * time.Second, 11 * time.Minute, 3 * time.Hour} {
		t.Run(wait.String(), func(t *testing.T) {
			ctx := context.Background()
			s, mw, clock, al := newTestService(t)
			insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing, NextActionAt: ptr(clock.Now().Add(wait).Unix())})
			mw.On("PUT", pathStatus, mwtest.OK(`{"card_id":"c1","card_status":"REVOKED"}`))
			o, err := s.FreezeCard(ctx, "c1", "admin")
			if err != nil {
				t.Fatal(err)
			}
			if o.State != StateSuccess {
				if _, err := s.finalize(ctx, o.OutOrderNo, queryResult{State: StateSuccess}, "test"); err != nil {
					t.Fatal(err)
				}
			}
			clock.Advance(wait)
			mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "1.00", "0.00", "REVOKED")))
			mustAdvance(t, s)
			c := mustGetCard(t, "c1")
			if c.PoolState != PoolDisabled || c.DisabledReason != "管理员冻结" {
				t.Fatalf("管理员冻结的回收中卡应停用、原因写管理员冻结: %+v", c)
			}
			if al.Count() != 0 {
				t.Fatalf("本系统发起的冻结不应发外部冻结告警: %v", al.Texts())
			}
		})
	}
}

// 本系统冻结过、后来又本系统解冻：再出现 REVOKED 就是外部冻结，照旧按平台冻结停用并告警。
func TestRecycleReleasingFrozenAfterOurUnfreezeIsExternal(t *testing.T) {
	s, mw, clock, al := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing})
	done := ptr(clock.Now().Unix())
	insertOrder(t, Order{Kind: KindStatus, CardID: "c1", TargetStatus: "REVOKED", State: StateSuccess, FinishedAt: done})
	insertOrder(t, Order{Kind: KindStatus, CardID: "c1", TargetStatus: "ACTIVE", State: StateSuccess, FinishedAt: done})
	clock.Advance(time.Minute)
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "1.00", "0.00", "REVOKED")))

	mustAdvance(t, s)
	c := mustGetCard(t, "c1")
	if c.PoolState != PoolDisabled || c.DisabledReason != "平台冻结" {
		t.Fatalf("应按平台冻结停用: %+v", c)
	}
	if al.Count() != 1 || !strings.Contains(al.Texts()[0], "非本系统发起") {
		t.Fatalf("应发外部冻结告警: %v", al.Texts())
	}
}
