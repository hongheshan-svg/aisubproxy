package payengine

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// D-6：人工定论后把用卡结果补记进卡池健康度。转待人工时租约已按 aborted 释放，定论不再碰租约，只补记结果；
// 判断依据与确认协程一致：成功记 success；失败只在提交过、页面明确显示被拒时记 failed，其余（没提交、校验错误、结果不明）不记。

// 生产卡池必须实现补记能力，否则 Resolve 会静默跳过补记
var _ outcomeRecorder = (*cardpool.Service)(nil)

type obRecorded struct {
	cardID, plan, ref, email string
	o                        cardpool.Outcome
}

type obRecordingPool struct {
	*fakePool
	mu   sync.Mutex
	got  []obRecorded
	fail error
}

func (p *obRecordingPool) RecordOutcome(_ context.Context, cardID, planKey, orderRef, email string, o cardpool.Outcome) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail != nil {
		return p.fail
	}
	p.got = append(p.got, obRecorded{cardID, planKey, orderRef, email, o})
	return nil
}

func (p *obRecordingPool) records() []obRecorded {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]obRecorded(nil), p.got...)
}

func obRecordingHarness(t *testing.T) (*harness, *obRecordingPool) {
	t.Helper()
	h := newPayHarness(t, nil)
	rp := &obRecordingPool{fakePool: h.pool}
	h.rebuild(rp)
	return h, rp
}

func TestObResolveRecordsCardOutcome(t *testing.T) {
	submitted := func(h *harness) map[string]any {
		return map[string]any{"submitted_at": h.clock.Now().Unix(), "card_id": testCardID, "account_id": "acc-1"}
	}
	cases := []struct {
		name, result, page string
		notSubmitted       bool
		want               string // 空表示不记
	}{
		{"定论成功", StateSucceeded, string(paybrowser.OutcomeUnknown), false, cardpool.ResultSuccess},
		{"页面显示被拒、定论失败", StateFailed, string(paybrowser.OutcomeDeclined), false, cardpool.ResultFailed},
		{"结果不明、定论失败", StateFailed, string(paybrowser.OutcomeUnknown), false, ""},
		{"页面校验错误、定论失败", StateFailed, string(paybrowser.OutcomeFormRejected), false, ""},
		{"没提交过、定论失败", StateFailed, "", true, ""},
		{"没提交过、定论成功", StateSucceeded, "", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, rp := obRecordingHarness(t)
			set := submitted(h)
			if tc.notSubmitted {
				delete(set, "submitted_at")
			}
			if tc.page != "" {
				set["page_outcome"] = tc.page
			}
			o := presetOrder(t, "resolve-outcome@example.com", StateNeedsReview, set)
			if err := h.svc.Resolve(ctx, o.OrderNo, tc.result, "核对过", "alice"); err != nil {
				t.Fatal(err)
			}
			got := rp.records()
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("不应补记: %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].o.Result != tc.want || got[0].cardID != testCardID || got[0].ref != o.OrderNo ||
				got[0].plan != "plus" || got[0].email != "resolve-outcome@example.com" {
				t.Fatalf("应补记一条 %s: %+v", tc.want, got)
			}
			if rel := h.pool.count("Release"); rel != 0 {
				t.Fatalf("定论不能再释放租约: %d", rel)
			}
		})
	}
}

// 补记失败只记时间线，不影响定论本身；卡池没有补记能力时照常定论。
func TestObResolveOutcomeRecordFailureDoesNotBlock(t *testing.T) {
	h, rp := obRecordingHarness(t)
	rp.fail = errors.New("db locked")
	o := presetOrder(t, "resolve-recfail@example.com", StateNeedsReview, map[string]any{"submitted_at": h.clock.Now().Unix(), "card_id": testCardID})
	if err := h.svc.Resolve(ctx, o.OrderNo, StateSucceeded, "核对已开通", "alice"); err != nil {
		t.Fatalf("补记失败不应让定论失败: %v", err)
	}
	if g := mustOrder(t, o.OrderNo); g.State != StateSucceeded {
		t.Fatalf("定论应生效: %+v", g)
	}
	waitEvent(t, o.OrderNo, "补记用卡结果失败")

	plain := newPayHarness(t, nil) // fakePool 没有 RecordOutcome
	o2 := presetOrder(t, "resolve-plain@example.com", StateNeedsReview, map[string]any{"submitted_at": plain.clock.Now().Unix(), "card_id": testCardID})
	if err := plain.svc.Resolve(ctx, o2.OrderNo, StateSucceeded, "核对已开通", "alice"); err != nil {
		t.Fatal(err)
	}
}
