package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// coreInput 是直接调下单核心的参数：alice 买 mw-plus-ph，异次元来源、不比价。
func (f *orderFix) coreInput(reqNo string, qty int, expected int64) agentOrderInput {
	return agentOrderInput{
		AgentID: f.aid, Username: "alice", ProductCode: "mw-plus-ph", Quantity: qty, RequestNo: reqNo,
		ExpectedUnitPriceCents: expected, Source: db.AgentOrderSourceACG, ExternalRef: "202611050000000001",
	}
}

// 异次元来源直接调核心：建单、扣款，订单记下来源和对方单号。
func TestPlaceAgentOrder_ACGSource(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	out, err := placeAgentOrder(context.Background(), f.coreInput("acg:202611050000000001", 1, 0))
	if err != nil {
		t.Fatalf("下单应成功: %v", err)
	}
	if out.Replayed || len(out.Codes) != 1 {
		t.Fatalf("应新建并有 1 张码: replayed=%v codes=%d", out.Replayed, len(out.Codes))
	}
	if c := out.Codes[0]; c.ID <= 0 || c.Code == "" || c.Unavailable || c.Status != agentCStatusUnused {
		t.Fatalf("码不对: id=%d unavailable=%v status=%q", c.ID, c.Unavailable, c.Status)
	}
	o := out.Order
	if o.Source != db.AgentOrderSourceACG || o.ExternalRef != "202611050000000001" || o.RequestNo != "acg:202611050000000001" ||
		o.Quantity != 1 || o.UnitPriceCents != 500 || o.TotalCents != 500 || o.AgentID != f.aid {
		t.Fatalf("订单不对: %+v", o)
	}
	got, found, err := db.GetAgentOrderByRequestNo(context.Background(), f.aid, "acg:202611050000000001")
	if err != nil || !found || got.Source != db.AgentOrderSourceACG || got.ExternalRef != "202611050000000001" {
		t.Fatalf("库里的订单不对: found=%v err=%v %+v", found, err, got)
	}
	if b := balanceOf(t, f.aid); b != 500 {
		t.Fatalf("余额应为 500: %d", b)
	}
	row, err := db.GetLocalCDKByCode(context.Background(), out.Codes[0].Code)
	if err != nil || row.IssuedBy != "agent:alice" || row.AgentOrderID != o.ID {
		t.Fatalf("码的行不对: err=%v issued_by=%q order=%d", err, row.IssuedBy, row.AgentOrderID)
	}
}

// 同号再调原样重放、不再扣钱；同号换数量是冲突。
func TestPlaceAgentOrder_ReplayAndConflict(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	ctx := context.Background()
	in := f.coreInput("acg:202611050000000001", 1, 0)
	first, err := placeAgentOrder(ctx, in)
	if err != nil || first.Replayed {
		t.Fatalf("第一次应新建: replayed=%v err=%v", first.Replayed, err)
	}
	again, err := placeAgentOrder(ctx, in)
	if err != nil || !again.Replayed {
		t.Fatalf("同号应重放: replayed=%v err=%v", again.Replayed, err)
	}
	if again.Order.ID != first.Order.ID || len(again.Codes) != len(first.Codes) {
		t.Fatalf("重放的订单不对: 第一次 %d/%d 张，重放 %d/%d 张", first.Order.ID, len(first.Codes), again.Order.ID, len(again.Codes))
	}
	for i := range first.Codes {
		if again.Codes[i].ID != first.Codes[i].ID || again.Codes[i].Code != first.Codes[i].Code {
			t.Fatalf("第 %d 张码和第一次不同: %d / %d", i, first.Codes[i].ID, again.Codes[i].ID)
		}
	}
	if b := balanceOf(t, f.aid); b != 500 {
		t.Fatalf("重放不应再扣钱: 余额 %d", b)
	}

	in.Quantity = 2
	if _, err := placeAgentOrder(ctx, in); !errors.Is(err, errRequestConflict) {
		t.Fatalf("同号换数量应是请求号冲突: %v", err)
	}
	if b := balanceOf(t, f.aid); b != 500 {
		t.Fatalf("冲突不应扣钱: 余额 %d", b)
	}
}

// 带期望单价时比价，不同返回现价；0 表示不比价，按现价扣。
func TestPlaceAgentOrder_PriceCheck(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	ctx := context.Background()
	_, err := placeAgentOrder(ctx, f.coreInput("acg:price-1", 1, 499))
	var pc *priceChangedError
	if !errors.As(err, &pc) || pc.CurrentCents != 500 {
		t.Fatalf("期望单价不对应返回价格已变化和现价 500: %v", err)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM agent_orders WHERE agent_id = ?`, f.aid); n != 0 {
		t.Fatalf("价格变化不应建单: %d", n)
	}
	if b := balanceOf(t, f.aid); b != 1000 {
		t.Fatalf("价格变化不应扣钱: 余额 %d", b)
	}

	out, err := placeAgentOrder(ctx, f.coreInput("acg:price-2", 1, 0))
	if err != nil || out.Order.UnitPriceCents != 500 {
		t.Fatalf("不比价应按现价 500 下单: err=%v %+v", err, out.Order)
	}
	if b := balanceOf(t, f.aid); b != 500 {
		t.Fatalf("应按 500 扣: 余额 %d", b)
	}
}

// 就绪检查比 ctx 慢：ctx 一到就按暂时无法出码返回，不建单、不扣钱。
func TestPlaceAgentOrder_ReadyCtxCancelled(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	p, err := db.GetAgentProductByCode(context.Background(), "mw-plus-ph")
	if err != nil {
		t.Fatal(err)
	}
	cs, ok := agentProductReady(context.Background(), p)
	if !ok {
		t.Fatal("夹具里的商品应能出码")
	}
	old := agentReadyCheck
	// 慢的检查不碰库和全局变量：测试结束后它还会睡完剩下的时间
	agentReadyCheck = func(context.Context, db.AgentProduct) (provider.CardSupply, bool) {
		time.Sleep(time.Second)
		return cs, true
	}
	t.Cleanup(func() { agentReadyCheck = old })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = placeAgentOrder(ctx, f.coreInput("acg:ready-1", 1, 0))
	if !errors.Is(err, errProductNotReady) {
		t.Fatalf("就绪检查被 ctx 打断应返回暂时无法出码: %v", err)
	}
	if d := time.Since(start); d >= 500*time.Millisecond {
		t.Fatalf("应在 ctx 到期时返回，不等慢的检查: %v", d)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM agent_orders WHERE agent_id = ?`, f.aid); n != 0 {
		t.Fatalf("不应建单: %d", n)
	}
	if b := balanceOf(t, f.aid); b != 1000 {
		t.Fatalf("不应扣钱: 余额 %d", b)
	}
}

// 后台下单不能用 acg: 开头的请求号：那是异次元对接专用的，混用会让两边的订单互相重放。
func TestAgentPlaceOrder_RejectsACGPrefix(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	r := f.expectRefused(f.s, f.aid, orderReq("mw-plus-ph", 1, "", "acg:1", 500), 400, "invalid_request")
	if r.Message != "请求号不能以 acg: 开头" {
		t.Fatalf("提示不对: %q", r.Message)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM agent_orders WHERE agent_id = ?`, f.aid); n != 0 {
		t.Fatalf("不应建单: %d", n)
	}
}
