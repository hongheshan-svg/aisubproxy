package cardpool

import (
	"context"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// 各条路径告警末尾的来源。
const (
	suffixWebhook = "（来源：平台推送）"
	suffixPoll    = "（来源：定时查询）"
	suffixRecycle = "（来源：清零时查询）"
	suffixAdmin   = "（来源：后台操作）"
)

// alertPath 是发现同一事件的一条路径：在新的测试库里触发一次，返回这条路径发出的告警。
type alertPath struct {
	name, suffix string
	run          func(t *testing.T) []string
}

// pushCardStatus：平台推送 CARD_STATUS_CHANGED。
func pushCardStatus(card Card, status string) alertPath {
	return alertPath{"平台推送", suffixWebhook, func(t *testing.T) []string {
		s, _, _, alerts := newTestService(t)
		insertCard(t, card)
		e := enqueueEvent(t, marswallex.TopicCardStatusChanged, `{"card_id":"`+card.CardID+`","card_status":"`+status+`"}`)
		mustProcessEvents(t, s)
		mustBeProcessed(t, e, "")
		return alerts.Texts()
	}}
}

// recycleCardStatus：清零（releasing 回收）时刷新卡详情发现平台状态。
func recycleCardStatus(card Card, status string) alertPath {
	return alertPath{"清零时查询", suffixRecycle, func(t *testing.T) []string {
		s, mw, _, alerts := newTestService(t)
		card.PoolState = PoolReleasing
		insertCard(t, card)
		mw.On("GET", detailPath(card.CardID), mwtest.OK(detailJSON(card.CardID, "0.00", "0.00", status)))
		mustAdvance(t, s)
		return alerts.Texts()
	}}
}

// pushHolderStatus：平台推送 CARDHOLDER_STATUS_CHANGE。
func pushHolderStatus(id, old, status, remark string) alertPath {
	return alertPath{"平台推送", suffixWebhook, func(t *testing.T) []string {
		s, _, _, alerts := newTestService(t)
		insertHolder(t, id, old)
		e := holderStatusEvent(t, id, "", status, remark)
		mustProcessEvents(t, s)
		mustBeProcessed(t, e, "")
		return alerts.Texts()
	}}
}

// adminRefreshHolder：后台点「刷新持卡人」。
func adminRefreshHolder(id, old, status, remark string) alertPath {
	return alertPath{"后台刷新", suffixAdmin, func(t *testing.T) []string {
		s, mw, _, alerts := newTestService(t)
		insertHolder(t, id, old)
		mw.On("GET", "/v1/cardholder/"+id, mwtest.OK(holderDetailJSON(id, "", status, remark)))
		if _, err := s.RefreshCardholder(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		return alerts.Texts()
	}}
}

// 同一事件不论从哪条路径发现，告警文字除末尾来源外逐字相同；每条只带一个来源，且是这条路径的。
func TestAlertTextSameAcrossSources(t *testing.T) {
	avail := Card{CardID: "c1", ProductID: "p1"}
	leased := Card{CardID: "c1", ProductID: "p1", PoolState: PoolLeased, LeaseToken: "tok", LeaseOwner: "pay:PO1"}
	cases := []struct {
		name  string
		want  string // 去掉来源后的完整文字；空表示只比较各路径一致
		paths []alertPath
	}{
		{"可用卡被外部冻结", "MarsWallex 卡 c1 在平台上被冻结（非本系统发起），已从卡池停用（原因：平台冻结），请到 MarsWallex 后台核查",
			[]alertPath{pushCardStatus(avail, "REVOKED"), pollCardStatus(avail, "REVOKED"), recycleCardStatus(avail, "REVOKED")}},
		{"租用中的卡被外部冻结", "MarsWallex 卡 c1 租用中（持有方 pay:PO1）在平台上被冻结（非本系统发起），请检查对应订单",
			[]alertPath{pushCardStatus(leased, "REVOKED"), pollCardStatus(leased, "REVOKED")}},
		{"租用中的卡被外部注销", "MarsWallex 卡 c1 租用中（持有方 pay:PO1）在平台上被注销（非本系统发起），已退役，请检查对应订单",
			[]alertPath{pushCardStatus(leased, "CANCELED"), pollCardStatus(leased, "CANCELED")}},
		{"持卡人通过", "MarsWallex 持卡人 h1 已通过审核，可在策略中加入补卡持卡人",
			[]alertPath{pushHolderStatus("h1", "PENDING_REVIEW", "APPROVED", ""), pollHolderStatus("h1", "PENDING_REVIEW", "APPROVED", ""),
				adminRefreshHolder("h1", "PENDING_REVIEW", "APPROVED", "")}},
		{"持卡人驳回", "MarsWallex 持卡人 h1 审核被驳回（REJECTED），自动补卡将跳过该持卡人。驳回原因：证件照模糊",
			[]alertPath{pushHolderStatus("h1", "PENDING_REVIEW", "REJECTED", "证件照模糊"), pollHolderStatus("h1", "PENDING_REVIEW", "REJECTED", "证件照模糊"),
				adminRefreshHolder("h1", "PENDING_REVIEW", "REJECTED", "证件照模糊")}},
		{"持卡人重新审核", "MarsWallex 持卡人 h1 由 APPROVED 变为 PENDING_REVIEW（修改后重新审核），自动补卡将跳过该持卡人",
			[]alertPath{pushHolderStatus("h1", "APPROVED", "PENDING_REVIEW", ""), pollHolderStatus("h1", "APPROVED", "PENDING_REVIEW", ""),
				adminRefreshHolder("h1", "APPROVED", "PENDING_REVIEW", "")}},
		{"持卡人其他状态", "MarsWallex 持卡人 h1 状态变为 SUSPENDED，自动补卡将跳过该持卡人。平台备注：风控",
			[]alertPath{pushHolderStatus("h1", "APPROVED", "SUSPENDED", "风控"), pollHolderStatus("h1", "APPROVED", "SUSPENDED", "风控"),
				adminRefreshHolder("h1", "APPROVED", "SUSPENDED", "风控")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var bodies []string
			for _, p := range c.paths {
				var got []string
				t.Run(p.name, func(t *testing.T) { got = p.run(t) })
				if len(got) != 1 {
					t.Fatalf("%s 应告警一次: %v", p.name, got)
				}
				text := got[0]
				if !strings.HasSuffix(text, p.suffix) || strings.Count(text, "（来源：") != 1 {
					t.Fatalf("%s 的告警应以 %q 结尾、只带一个来源: %q", p.name, p.suffix, text)
				}
				bodies = append(bodies, strings.TrimSuffix(text, p.suffix))
			}
			for i, b := range bodies {
				if b != bodies[0] {
					t.Fatalf("%s 与 %s 的文字不同:\n%q\n%q", c.paths[i].name, c.paths[0].name, b, bodies[0])
				}
			}
			if c.want != "" && bodies[0] != c.want {
				t.Fatalf("文字不符:\n得到 %q\n期望 %q", bodies[0], c.want)
			}
		})
	}
}
