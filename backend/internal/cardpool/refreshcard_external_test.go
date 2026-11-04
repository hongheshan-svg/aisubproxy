package cardpool

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// adminRefreshCard：后台点「刷新卡」时发现平台状态变化。
func adminRefreshCard(card Card, status string) alertPath {
	return alertPath{"后台刷新卡", suffixAdmin, func(t *testing.T) []string {
		s, mw, _, alerts := newTestService(t)
		insertCard(t, card)
		mw.On("GET", detailPath(card.CardID), mwtest.OK(fullDetailJSON(card.CardID, "0.00", status)))
		if _, err := s.RefreshCard(context.Background(), card.CardID); err != nil {
			t.Fatal(err)
		}
		noFullCardNo(t, alerts, nil)
		return alerts.Texts()
	}}
}

// 刷新卡先发现租用中的卡被外部冻结：当场告警一次（来源：后台操作）；之后推送、定时查询都到了，也不再告警（跨天也一样）。
func TestRefreshCardLeasedRevokedThenPushAndPollAlertOnce(t *testing.T) {
	s, mw, clock, alerts := newTestService(t)
	insertCard(t, Card{CardID: "l1", ProductID: "p1", PoolState: PoolLeased, LeaseToken: "tok", LeaseOwner: "pay:PO1"})
	mw.On("GET", detailPath("l1"), mwtest.OK(fullDetailJSON("l1", "5.00", "REVOKED")))

	if _, err := s.RefreshCard(context.Background(), "l1"); err != nil {
		t.Fatal(err)
	}
	c := mustGetCard(t, "l1")
	if c.PoolState != PoolLeased || c.LeaseToken != "tok" || c.PlatformStatus != "REVOKED" || c.AvailableCents != 500 {
		t.Fatalf("租用中的卡只告警、池状态与租约不变，余额照常刷新: %+v", c)
	}
	if got := alerts.Texts(); len(got) != 1 || got[0] != extFreezeLeasedText(c, sourceAdmin) {
		t.Fatalf("刷新卡发现外部冻结应告警一次: %v", got)
	}

	clock.Advance(25 * time.Hour)
	e := enqueueEvent(t, marswallex.TopicCardStatusChanged, `{"card_id":"l1","card_status":"REVOKED"}`)
	mustProcessEvents(t, s)
	mustBeProcessed(t, e, "")
	mustPollCards(t, s)
	if alerts.Count() != 1 {
		t.Fatalf("之后的推送和定时查询不应再告警: %v", alerts.Texts())
	}
}

// 资金单推送定论后刷新余额时发现外部冻结：同样处理，来源写「平台推送」。
func TestTransferResultRefreshFindsExternalFreeze(t *testing.T) {
	s, mw, _, alerts := newTestService(t)
	insertCard(t, Card{CardID: "l1", ProductID: "p1", PoolState: PoolLeased, LeaseToken: "tok", LeaseOwner: "pay:PO1"})
	o := insertOrder(t, Order{Kind: KindRecharge, CardID: "l1", AmountCents: 500, Currency: "USD", State: StateProcessing})
	mw.On("GET", detailPath("l1"), mwtest.OK(fullDetailJSON("l1", "5.00", "REVOKED")))
	e := enqueueEvent(t, marswallex.TopicCardTransferResult,
		`{"out_order_no":"`+o.OutOrderNo+`","transaction_id":"TX-1","card_id":"l1","status":"SUCCESS"}`)
	mustProcessEvents(t, s)
	mustBeProcessed(t, e, "余额已刷新")
	c := mustGetCard(t, "l1")
	if got := alerts.Texts(); len(got) != 1 || got[0] != extFreezeLeasedText(c, sourceWebhook) {
		t.Fatalf("应告警一次（来源：平台推送）: %v", got)
	}
}

// 刷新卡发现可用卡被外部冻结：立即停用（平台冻结）并告警，之后不会再被选中租用。
func TestRefreshCardAvailableRevokedDisablesNow(t *testing.T) {
	s, mw, _, alerts := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	insertCard(t, Card{CardID: "a1", ProductID: "p1"})
	mw.On("GET", detailPath("a1"), mwtest.OK(fullDetailJSON("a1", "0.00", "REVOKED")))

	got, err := s.RefreshCard(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if got.PoolState != PoolDisabled || got.DisabledReason != "平台冻结" || got.PlatformStatus != "REVOKED" {
		t.Fatalf("应立即停用（平台冻结），返回的也应是处理后的卡: %+v", got)
	}
	if a := alerts.Texts(); len(a) != 1 || a[0] != extFreezeDisabledText("a1", sourceAdmin) {
		t.Fatalf("应告警一次: %v", a)
	}
	if _, err := s.Acquire(context.Background(), AcquireRequest{PlanKey: "gpt", Currency: "USD"}); !errors.Is(err, ErrNoCard) {
		t.Fatalf("被冻结的卡不应再被选中: %v", err)
	}
}

// 刷新卡发现可用、租用中的卡被注销：退役，租用中的告警一次。
func TestRefreshCardCanceled(t *testing.T) {
	s, mw, _, alerts := newTestService(t)
	insertCard(t, Card{CardID: "x1", ProductID: "p1"})
	insertCard(t, Card{CardID: "x2", ProductID: "p1", PoolState: PoolLeased, LeaseToken: "tok", LeaseOwner: "pay:PO2"})
	for _, id := range []string{"x1", "x2"} {
		mw.On("GET", detailPath(id), mwtest.OK(fullDetailJSON(id, "0.00", "CANCELED")))
		if _, err := s.RefreshCard(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		if c := mustGetCard(t, id); c.PoolState != PoolRetired || c.PlatformStatus != "CANCELED" {
			t.Fatalf("%s 应退役: %+v", id, c)
		}
	}
	if got := alerts.Texts(); len(got) != 1 || !strings.Contains(got[0], "x2") || !strings.HasSuffix(got[0], suffixAdmin) {
		t.Fatalf("只有租用中的 x2 告警一次: %v", got)
	}
}

// 本系统发起的冻结（同卡同目标的改状态单还没定论）经过刷新卡：照旧只写平台状态，不按外部冻结处理、不告警。
func TestRefreshCardOurFreezeNoAlert(t *testing.T) {
	s, mw, _, alerts := newTestService(t)
	insertCard(t, Card{CardID: "o1", ProductID: "p1"})
	insertOrder(t, Order{Kind: KindStatus, CardID: "o1", TargetStatus: "REVOKED", State: StateProcessing})
	mw.On("GET", detailPath("o1"), mwtest.OK(fullDetailJSON("o1", "0.00", "REVOKED")))

	if _, err := s.RefreshCard(context.Background(), "o1"); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "o1"); c.PoolState != PoolAvailable || c.PlatformStatus != "REVOKED" {
		t.Fatalf("本系统发起的冻结照旧只写平台状态: %+v", c)
	}
	if alerts.Count() != 0 {
		t.Fatalf("不应告警: %v", alerts.Texts())
	}
}

// 回收中、已停用的卡刷新到 REVOKED：照旧只写平台状态（回收流程自己处理），不告警。
func TestRefreshCardOtherPoolStatesUnchanged(t *testing.T) {
	s, mw, _, alerts := newTestService(t)
	insertCard(t, Card{CardID: "d1", ProductID: "p1", PoolState: PoolDisabled})
	mw.On("GET", detailPath("d1"), mwtest.OK(fullDetailJSON("d1", "0.00", "REVOKED")))
	if _, err := s.RefreshCard(context.Background(), "d1"); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "d1"); c.PoolState != PoolDisabled || c.PlatformStatus != "REVOKED" {
		t.Fatalf("停用的卡只写平台状态: %+v", c)
	}
	if alerts.Count() != 0 {
		t.Fatalf("不应告警: %v", alerts.Texts())
	}
}
