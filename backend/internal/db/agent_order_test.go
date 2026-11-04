package db_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func orderProduct() db.AgentProduct {
	return db.AgentProduct{ID: 7, Code: "mw-plus", Provider: "marswallex", Product: "chatgpt", PlanKey: "plus", Region: "US", Name: "Plus"}
}

func orderCodes(prefix string, n int) []db.NewLocalCDK {
	out := make([]db.NewLocalCDK, n)
	for i := range out {
		out[i] = db.NewLocalCDK{Provider: "marswallex", CodeHash: fmt.Sprintf("%s-h%d", prefix, i), TokenHash: fmt.Sprintf("%s-t%d", prefix, i),
			CodePrefix: "AB", Product: "chatgpt", PlanKey: "plus", Region: "US", CodeSealed: "sealed", IssuedBy: "agent"}
	}
	return out
}

func fundAgent(t *testing.T, id, cents int64) {
	t.Helper()
	if _, _, err := db.AdjustAgentBalance(context.Background(), id, cents, fmt.Sprintf("fund-%d", id), "admin:root", "初始", acctNow); err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func nothingWritten(t *testing.T) {
	t.Helper()
	if n := countRows(t, `SELECT COUNT(*) FROM agent_orders`); n != 0 {
		t.Fatalf("订单 %d 行", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM agent_ledger WHERE kind = 'purchase'`); n != 0 {
		t.Fatalf("purchase 流水 %d 行", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM local_cdks WHERE agent_id <> 0`); n != 0 {
		t.Fatalf("码 %d 行", n)
	}
}

func TestCreateAgentOrderAtomic(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	fundAgent(t, id, 1000)
	o, ids, err := db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: id, Product: orderProduct(), UnitPriceCents: 300, RequestNo: "r1", Note: "n", Operator: "agent:alice", Codes: orderCodes("a", 2)}, acctNow)
	if err != nil {
		t.Fatal(err)
	}
	if o.ID == 0 || o.Quantity != 2 || o.TotalCents != 600 || o.UnitPriceCents != 300 || o.ProductCode != "mw-plus" || o.RequestNo != "r1" || len(ids) != 2 {
		t.Fatalf("%+v %v", o, ids)
	}
	if agentBalance(t, id) != 400 {
		t.Fatalf("余额 %d", agentBalance(t, id))
	}
	var amt, oid int64
	if err := db.DB.QueryRow(`SELECT amount_cents, order_id FROM agent_ledger WHERE agent_id = ? AND kind = 'purchase'`, id).Scan(&amt, &oid); err != nil {
		t.Fatal(err)
	}
	if amt != -600 || oid != o.ID {
		t.Fatalf("流水 %d %d", amt, oid)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM local_cdks WHERE agent_id = ? AND agent_order_id = ?`, id, o.ID); n != 2 {
		t.Fatalf("码 %d", n)
	}
	got, err := db.LocalCDKIDsForAgentOrder(ctx, id, o.ID)
	if err != nil || len(got) != 2 || got[0] != ids[0] || got[1] != ids[1] {
		t.Fatalf("%v %v", got, err)
	}
	if other, err := db.LocalCDKIDsForAgentOrder(ctx, id+1, o.ID); err != nil || len(other) != 0 {
		t.Fatalf("别人的订单不能查到码: %v %v", other, err)
	}
	g, err := db.GetAgentOrder(ctx, id, o.ID)
	if err != nil || g.ID != o.ID {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err := db.GetAgentOrder(ctx, id+1, o.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("别人的订单应 ErrNoRows: %v", err)
	}
	by, ok, err := db.GetAgentOrderByRequestNo(ctx, id, "r1")
	if err != nil || !ok || by.ID != o.ID {
		t.Fatalf("%+v %v %v", by, ok, err)
	}
	if _, ok, _ := db.GetAgentOrderByRequestNo(ctx, id, "nope"); ok {
		t.Fatal("不该找到")
	}
	list, total, err := db.ListAgentOrders(ctx, id, 10, 0)
	if err != nil || total != 1 || len(list) != 1 {
		t.Fatalf("%v %d %v", list, total, err)
	}
	if l2, n2, _ := db.ListAgentOrders(ctx, id+1, 10, 0); len(l2) != 0 || n2 != 0 {
		t.Fatal("别人的列表不该有数据")
	}
}

func TestCreateAgentOrderInsufficientLeavesNothing(t *testing.T) {
	dbtest.Open(t)
	id := newTestAgent(t, "alice")
	fundAgent(t, id, 100)
	_, _, err := db.CreateAgentOrder(context.Background(), db.NewAgentOrder{AgentID: id, Product: orderProduct(), UnitPriceCents: 300, RequestNo: "r1", Codes: orderCodes("a", 1)}, acctNow)
	var ie *db.InsufficientBalanceError
	if !errors.As(err, &ie) || ie.BalanceCents != 100 || ie.NeedCents != 300 {
		t.Fatalf("%v", err)
	}
	nothingWritten(t)
	if agentBalance(t, id) != 100 {
		t.Fatal("余额变了")
	}
}

func TestCreateAgentOrderDisabledAgent(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	fundAgent(t, id, 1000)
	if err := db.SetAgentStatus(ctx, id, db.AgentDisabled, acctNow); err != nil {
		t.Fatal(err)
	}
	_, _, err := db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: id, Product: orderProduct(), UnitPriceCents: 300, RequestNo: "r1", Codes: orderCodes("a", 1)}, acctNow)
	if !errors.Is(err, db.ErrAgentDisabled) {
		t.Fatalf("%v", err)
	}
	nothingWritten(t)
	if agentBalance(t, id) != 1000 {
		t.Fatal("余额变了")
	}
	// 停用且余额不足：仍然是停用
	_, _, err = db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: id, Product: orderProduct(), UnitPriceCents: 5000, RequestNo: "r2", Codes: orderCodes("b", 1)}, acctNow)
	if !errors.Is(err, db.ErrAgentDisabled) || errors.Is(err, db.ErrInsufficientBalance) {
		t.Fatalf("停用且余额不足应返回停用: %v", err)
	}
	nothingWritten(t)
	if agentBalance(t, id) != 1000 {
		t.Fatal("余额变了")
	}
}

func TestCreateAgentOrderDuplicateRequestNo(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	fundAgent(t, id, 1000)
	n := db.NewAgentOrder{AgentID: id, Product: orderProduct(), UnitPriceCents: 300, RequestNo: "r1", Codes: orderCodes("a", 1)}
	if _, _, err := db.CreateAgentOrder(ctx, n, acctNow); err != nil {
		t.Fatal(err)
	}
	n.Codes = orderCodes("b", 1)
	if _, _, err := db.CreateAgentOrder(ctx, n, acctNow); !errors.Is(err, db.ErrAgentOrderExists) {
		t.Fatalf("%v", err)
	}
	if agentBalance(t, id) != 700 {
		t.Fatalf("只应扣一次: %d", agentBalance(t, id))
	}
	if c := countRows(t, `SELECT COUNT(*) FROM local_cdks WHERE agent_id = ?`, id); c != 1 {
		t.Fatalf("码 %d", c)
	}
}

func TestCreateAgentOrderCodeHashCollisionRollsBack(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	fundAgent(t, id, 1000)
	if _, err := db.InsertLocalCDKs(ctx, "pre", orderCodes("dup", 1), acctNow); err != nil {
		t.Fatal(err)
	}
	codes := orderCodes("x", 2)
	codes[1].CodeHash = "dup-h0"
	if _, _, err := db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: id, Product: orderProduct(), UnitPriceCents: 300, RequestNo: "r1", Codes: codes}, acctNow); err == nil {
		t.Fatal("应失败")
	}
	nothingWritten(t)
	if agentBalance(t, id) != 1000 {
		t.Fatal("余额变了")
	}
	if c := countRows(t, `SELECT COUNT(*) FROM local_cdks`); c != 1 {
		t.Fatalf("码应只剩预先存在的 1 张: %d", c)
	}
}

func TestCreateAgentOrderConcurrentOverdraft(t *testing.T) {
	openAgentFileDB(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	fundAgent(t, id, 1000)
	var wg sync.WaitGroup
	errs := make([]error, 30)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, errs[i] = db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: id, Product: orderProduct(), UnitPriceCents: 100,
				RequestNo: fmt.Sprintf("r%d", i), Codes: orderCodes(fmt.Sprintf("c%d", i), 1)}, acctNow)
		}(i)
	}
	wg.Wait()
	ok, bad := 0, 0
	for _, e := range errs {
		if e == nil {
			ok++
		} else if errors.Is(e, db.ErrInsufficientBalance) {
			bad++
		} else {
			t.Fatalf("意外错误: %v", e)
		}
	}
	if ok != 10 || bad != 20 {
		t.Fatalf("成功 %d 余额不足 %d", ok, bad)
	}
	if agentBalance(t, id) != 0 {
		t.Fatalf("余额 %d", agentBalance(t, id))
	}
	if n := countRows(t, `SELECT COUNT(*) FROM agent_ledger WHERE kind = 'purchase'`); n != 10 {
		t.Fatalf("purchase 流水 %d", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM local_cdks WHERE agent_id = ?`, id); n != 10 {
		t.Fatalf("码 %d", n)
	}
	if bad, err := db.CheckAgentBalances(ctx); err != nil || len(bad) != 0 {
		t.Fatalf("%v %v", bad, err)
	}
}

func TestCreateAgentOrderConcurrentSameRequestNo(t *testing.T) {
	openAgentFileDB(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	fundAgent(t, id, 1000)
	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, errs[i] = db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: id, Product: orderProduct(), UnitPriceCents: 100,
				RequestNo: "same", Codes: orderCodes(fmt.Sprintf("c%d", i), 1)}, acctNow)
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, e := range errs {
		if e == nil {
			ok++
		} else if !errors.Is(e, db.ErrAgentOrderExists) {
			t.Fatalf("意外错误: %v", e)
		}
	}
	if ok != 1 || agentBalance(t, id) != 900 {
		t.Fatalf("成功 %d 余额 %d", ok, agentBalance(t, id))
	}
}

func TestExchangeInheritsAgent(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	fundAgent(t, id, 1000)
	o, ids, err := db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: id, Product: orderProduct(), UnitPriceCents: 300, RequestNo: "r1", Codes: orderCodes("a", 1)}, acctNow)
	if err != nil {
		t.Fatal(err)
	}
	n := orderCodes("new", 1)[0]
	n.ExchangedFrom = &ids[0]
	newID, err := db.ExchangeLocalCDK(ctx, ids[0], n, acctNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.GetLocalCDK(ctx, newID)
	if err != nil || c.AgentID != id || c.AgentOrderID != o.ID {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestLocalCDKSealedForAgentScoped(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	a, b := newTestAgent(t, "alice"), newTestAgent(t, "bob")
	fundAgent(t, a, 1000)
	fundAgent(t, b, 1000)
	_, ida, err := db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: a, Product: orderProduct(), UnitPriceCents: 100, RequestNo: "r1", Codes: orderCodes("a", 2)}, acctNow)
	if err != nil {
		t.Fatal(err)
	}
	_, idb, err := db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: b, Product: orderProduct(), UnitPriceCents: 100, RequestNo: "r1", Codes: orderCodes("b", 1)}, acctNow)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.LocalCDKSealedForAgent(ctx, a, append(append([]int64{}, idb...), ida...))
	if err != nil || len(got) != 2 || got[0].ID != ida[0] || got[1].ID != ida[1] || got[0].Sealed != "sealed" || got[0].CodeHash != "a-h0" {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := db.LocalCDKSealedForAgent(ctx, a, nil); err != nil || len(got) != 0 {
		t.Fatalf("空 ids: %v %v", got, err)
	}
}

func TestLoadLocalCDKRowsByAgent(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	a, b := newTestAgent(t, "alice"), newTestAgent(t, "bob")
	fundAgent(t, a, 1000)
	fundAgent(t, b, 1000)
	if _, _, err := db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: a, Product: orderProduct(), UnitPriceCents: 100, RequestNo: "r1", Codes: orderCodes("a", 2)}, acctNow); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: b, Product: orderProduct(), UnitPriceCents: 100, RequestNo: "r1", Codes: orderCodes("b", 1)}, acctNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertLocalCDKs(ctx, "adm", orderCodes("adm", 1), acctNow); err != nil {
		t.Fatal(err)
	}
	rows, err := db.LoadLocalCDKRowsByAgent(ctx, a)
	if err != nil || len(rows) != 2 {
		t.Fatalf("%d %v", len(rows), err)
	}
	for _, r := range rows {
		if r.AgentID != a || r.View.Status != db.LocalCDKUnused {
			t.Fatalf("%+v", r)
		}
	}
	if rows[0].ID < rows[1].ID {
		t.Fatal("应新的在前")
	}
	all, _ := db.LoadLocalCDKRows(ctx)
	if len(all) != 4 {
		t.Fatalf("全部 %d", len(all))
	}
}
