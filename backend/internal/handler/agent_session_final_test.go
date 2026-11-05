package handler

import "testing"

// L1：1 个正确码先结算（清掉账号键）后，其余在途错码的结算不能把新建的 gate 的 inflight 减成负数，
// 也不能让账号级名额放出超过 5 次校验。
func TestAgentTOTPSlotSettlesOnOwnGateFinal(t *testing.T) {
	resetAgentAuthState()
	const id = int64(901)
	var slots []*agentTOTPSlot
	for i := 0; i < agentTOTPAcctMax; i++ {
		g := agentTOTPReserve(id)
		if g == nil {
			t.Fatalf("第 %d 个名额应放行", i+1)
		}
		slots = append(slots, &agentTOTPSlot{id: id, gate: g})
	}
	if agentTOTPReserve(id) != nil {
		t.Fatal("名额占满后不应再放行")
	}
	slots[0].ok() // 正确码先结算，账号键被清零
	validated := 1
	fresh := agentTOTPReserve(id) // 账号键已清，新请求拿到新 gate
	if fresh == nil {
		t.Fatal("正确码结算后应能再占名额")
	}
	slotFresh := &agentTOTPSlot{id: id, gate: fresh}
	for _, s := range slots[1:] { // 其余 4 个错码随后结算：落在它们自己的旧 gate 上
		s.fail()
		validated++
	}
	agentGateMu.Lock()
	cur := agentGates[id]
	agentGateMu.Unlock()
	if cur != fresh || fresh.inflight != 1 || fresh.fails != 0 {
		t.Fatalf("旧名额的结算不应落到新 gate 上: inflight=%d fails=%d", fresh.inflight, fresh.fails)
	}
	slotFresh.fail()
	slotFresh.fail() // 重复结算无效
	if fresh.inflight < 0 || fresh.fails != 1 {
		t.Fatalf("新 gate 计数异常: inflight=%d fails=%d", fresh.inflight, fresh.fails)
	}
	// 同一个 gate 上，fail 也不能把 inflight 减成负数
	g := &agentTOTPGate{}
	(&agentTOTPSlot{gate: g}).fail()
	if g.inflight < 0 {
		t.Fatalf("inflight 不应为负: %d", g.inflight)
	}
	if validated > agentTOTPAcctMax {
		t.Fatalf("旧 gate 上走到校验的次数不应超过 %d: %d", agentTOTPAcctMax, validated)
	}
}
