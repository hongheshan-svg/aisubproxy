package cardpool

import (
	"context"
	"errors"
	"testing"
)

// 请求已发到平台、本地记不下结果（H2-08）：初判不是明确被拒或未发出时，错误包上 ErrMaybeApplied，
// 处理器据此留失败审计、提示勿重复提交；明确被拒、未发出的照旧返回原错误。
func TestRecordSentMarksMaybeApplied(t *testing.T) {
	s, _, _, _ := newTestService(t)
	ctx := context.Background()
	for _, st := range []string{StateUnknown, StateProcessing, StateSuccess} {
		_, err := s.recordSent(ctx, "MW-NOPE", sendVerdict{state: st})
		if !errors.Is(err, ErrMaybeApplied) {
			t.Fatalf("%s：记录失败应标为平台可能已生效: %v", st, err)
		}
	}
	for _, st := range []string{StateFailed, StateNotSent} {
		_, err := s.recordSent(ctx, "MW-NOPE", sendVerdict{state: st})
		if err == nil || errors.Is(err, ErrMaybeApplied) {
			t.Fatalf("%s：明确没生效，不应标为可能已生效: %v", st, err)
		}
	}
	o := insertOrder(t, Order{Kind: KindStatus, CardID: "c1", TargetStatus: "REVOKED"})
	got, err := s.recordSent(ctx, o.OutOrderNo, sendVerdict{state: StateUnknown})
	if err != nil || got.State != StateUnknown {
		t.Fatalf("记录成功照常返回单据: %+v %v", got, err)
	}
}
