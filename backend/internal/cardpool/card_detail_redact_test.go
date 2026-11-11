package cardpool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// 平台拒绝时在 message 里回显完整卡号：取卡详情、取卡号的各个出口都要打码（C1-05），错误类型照常可识别。
func panReply() mwtest.Reply {
	return mwtest.Reply{Status: 400, Body: `{"code":"CARD_ERR","message":"card 4111 1111 1111 1111 / 4111111111111111 invalid"}`}
}

func mustNotLeakPAN(t *testing.T, where string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s：应返回错误", where)
	}
	text := err.Error()
	for _, leak := range []string{"4111111111111111", "4111 1111 1111 1111"} {
		if strings.Contains(text, leak) {
			t.Fatalf("%s 的错误带出了完整卡号: %s", where, text)
		}
	}
	var me *marswallex.Error
	if !errors.As(err, &me) || me.Outcome != marswallex.OutcomeRejected {
		t.Fatalf("%s：仍应能取到平台错误: %#v", where, err)
	}
}

func TestCardDetailErrorsRedactPAN(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	insertCard(t, Card{CardID: "r1", ProductID: "p1", PoolState: PoolReleasing})
	insertCard(t, Card{CardID: "a1", ProductID: "p1"})
	mw.On("GET", detailPath("r1"), panReply())
	mw.On("GET", detailPath("n1"), panReply())
	mw.On("GET", sensitivePath("a1"), panReply())

	_, err := s.RefreshCard(ctx, "r1")
	mustNotLeakPAN(t, "刷新卡", err)

	_, err = s.ImportCard(ctx, "n1", "p1", "", "admin")
	mustNotLeakPAN(t, "导入卡", err)

	o := insertOrder(t, Order{Kind: KindStatus, CardID: "r1", TargetStatus: "REVOKED", State: StateUnknown})
	_, err = s.Recheck(ctx, o.OutOrderNo)
	mustNotLeakPAN(t, "改状态单查单", err)

	l := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt"})
	_, err = s.Sensitive(ctx, l)
	mustNotLeakPAN(t, "取卡号", err)

	// 定时查卡状态：返回的错误只带结果分类和错误码（不带 message），错误码里夹带的长数字串也要打码
	insertCard(t, Card{CardID: "s1", ProductID: "p2"})
	mw.On("GET", detailPath("s1"), mwtest.Reply{Status: 400, Body: `{"code":"E4111111111111111","message":"card 4111111111111111 invalid"}`})
	err = s.pollCard(ctx, mw.Client(), &Card{CardID: "s1"})
	if err == nil || strings.Contains(err.Error(), "4111111111111111") || !strings.Contains(err.Error(), "code=E****") {
		t.Fatalf("定时查卡状态的错误应打码: %v", err)
	}
}
