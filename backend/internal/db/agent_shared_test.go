package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func TestMigrateAgents_AddsOrderSourceColumns(t *testing.T) {
	dbtest.Open(t)
	if _, err := db.DB.Exec(`DROP TABLE agent_orders`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`CREATE TABLE agent_orders (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_id INTEGER NOT NULL,
	product_id INTEGER NOT NULL,
	product_code TEXT NOT NULL,
	provider TEXT NOT NULL,
	product TEXT NOT NULL,
	plan_key TEXT NOT NULL,
	region TEXT NOT NULL,
	product_name TEXT NOT NULL,
	quantity INTEGER NOT NULL CHECK (quantity BETWEEN 1 AND 200),
	unit_price_cents INTEGER NOT NULL,
	total_cents INTEGER NOT NULL,
	request_no TEXT NOT NULL,
	note TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	UNIQUE (agent_id, request_no)
)`); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO agent_orders (agent_id, product_id, product_code, provider, product, plan_key, region, product_name,
		quantity, unit_price_cents, total_cents, request_no, created_at) VALUES (1, 1, 'c', 'p', 'x', 'k', 'US', 'n', 1, 1, 1, 'old', 1)`
	if _, err := db.DB.Exec(ins); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.MigrateAgents(); err != nil {
			t.Fatalf("第 %d 次迁移: %v", i+1, err)
		}
	}
	var src, ref string
	if err := db.DB.QueryRow(`SELECT source, external_ref FROM agent_orders WHERE request_no = 'old'`).Scan(&src, &ref); err != nil {
		t.Fatal(err)
	}
	if src != "web" || ref != "" {
		t.Fatalf("旧行应为 web/空: %q %q", src, ref)
	}
	if _, err := db.DB.Exec(`INSERT INTO agent_orders (agent_id, product_id, product_code, provider, product, plan_key, region, product_name,
		quantity, unit_price_cents, total_cents, request_no, created_at, source) VALUES (1, 1, 'c', 'p', 'x', 'k', 'US', 'n', 1, 1, 1, 'bad', 1, 'x')`); err == nil {
		t.Fatal("非法来源应被 CHECK 拒绝")
	}
}

func TestCreateAgentOrder_Source(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	fundAgent(t, id, 10000)
	w, _, err := db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: id, Product: orderProduct(), UnitPriceCents: 100, RequestNo: "w1", Operator: "agent:alice", Codes: orderCodes("w", 1)}, acctNow)
	if err != nil {
		t.Fatal(err)
	}
	if w.Source != "web" || w.ExternalRef != "" {
		t.Fatalf("默认应为 web: %+v", w)
	}
	a, _, err := db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: id, Product: orderProduct(), UnitPriceCents: 100, RequestNo: "acg:202611050000000001",
		Source: db.AgentOrderSourceACG, ExternalRef: "202611050000000001", Operator: "agent:alice", Codes: orderCodes("a", 1)}, acctNow)
	if err != nil {
		t.Fatal(err)
	}
	if a.Source != "acg" || a.ExternalRef != "202611050000000001" {
		t.Fatalf("%+v", a)
	}
	by, ok, err := db.GetAgentOrderByRequestNo(ctx, id, "acg:202611050000000001")
	if err != nil || !ok || by.Source != "acg" || by.ExternalRef != "202611050000000001" {
		t.Fatalf("%+v %v %v", by, ok, err)
	}
	list, _, err := db.ListAgentOrders(ctx, id, 10, 0)
	if err != nil || len(list) != 2 || list[0].Source != "acg" || list[0].ExternalRef != "202611050000000001" || list[1].Source != "web" {
		t.Fatalf("%+v %v", list, err)
	}
}

func TestAgentOrderRefs(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	other := newTestAgent(t, "bob")
	fundAgent(t, id, 10000)
	fundAgent(t, other, 10000)
	w, _, err := db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: id, Product: orderProduct(), UnitPriceCents: 100, RequestNo: "w1", Operator: "x", Codes: orderCodes("w", 1)}, acctNow)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: id, Product: orderProduct(), UnitPriceCents: 100, RequestNo: "acg:9",
		Source: db.AgentOrderSourceACG, ExternalRef: "9", Operator: "x", Codes: orderCodes("a", 1)}, acctNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CreateAgentOrder(ctx, db.NewAgentOrder{AgentID: other, Product: orderProduct(), UnitPriceCents: 100, RequestNo: "o1", Operator: "x", Codes: orderCodes("o", 1)}, acctNow); err != nil {
		t.Fatal(err)
	}
	refs, err := db.AgentOrderRefs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[w.ID] != (db.AgentOrderRef{ProductCode: "mw-plus", Source: "web"}) ||
		refs[a.ID] != (db.AgentOrderRef{ProductCode: "mw-plus", Source: "acg", ExternalRef: "9"}) {
		t.Fatalf("%+v", refs)
	}
}

func TestAgentSharedCreds_Lifecycle(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	a1 := newTestAgent(t, "alice")
	a2 := newTestAgent(t, "bob")
	t0 := acctNow
	c, err := db.GetAgentSharedCreds(ctx, a1)
	if err != nil || c.Exists || c.AgentID != a1 {
		t.Fatalf("没有行: %+v %v", c, err)
	}
	if err := db.SetAgentSharedKey(ctx, a1, "agAAAAAAAAAAAAAA", "s1", t0); err != nil {
		t.Fatal(err)
	}
	c, err = db.GetAgentSharedCredsByAppID(ctx, "agAAAAAAAAAAAAAA")
	if err != nil || !c.Exists || c.AgentID != a1 || c.KeySealed != "s1" || c.KeyCreatedAt != t0.Unix() {
		t.Fatalf("%+v %v", c, err)
	}
	if err := db.SetAgentSharedKey(ctx, a1, "agAAAAAAAAAAAAAA", "s2", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	c, _ = db.GetAgentSharedCreds(ctx, a1)
	if c.KeySealed != "s2" || c.AppID != "agAAAAAAAAAAAAAA" {
		t.Fatalf("%+v", c)
	}
	if err := db.SetAgentSharedKey(ctx, a2, "agBBBBBBBBBBBBBB", "s3", t0); err != nil {
		t.Fatal(err)
	}
	if err := db.DisableAgentShared(ctx, a1, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAgentSharedCredsByAppID(ctx, "agAAAAAAAAAAAAAA"); !errors.Is(err, db.ErrAgentSharedNotFound) {
		t.Fatalf("旧商户 ID 应查不到: %v", err)
	}
	c, _ = db.GetAgentSharedCreds(ctx, a1)
	if c.AppID != "" || c.KeySealed != "" || c.KeyCreatedAt != 0 {
		t.Fatalf("%+v", c)
	}
	if err := db.DisableAgentShared(ctx, a2, t0); err != nil {
		t.Fatalf("两行 app_id 都为 NULL 不应撞唯一约束: %v", err)
	}
	if err := db.DisableAgentShared(ctx, 9999, t0); err != nil {
		t.Fatalf("没有行应什么也不做: %v", err)
	}
}

func TestAgentSharedCreds_AdminDisabled(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	if err := db.SetAgentSharedKey(ctx, id, "agAAAAAAAAAAAAAA", "s1", acctNow); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAgentSharedAdminDisabled(ctx, id, true, acctNow); err != nil {
		t.Fatal(err)
	}
	c, err := db.GetAgentSharedCredsByAppID(ctx, "agAAAAAAAAAAAAAA")
	if err != nil || !c.AdminDisabled || c.KeySealed != "s1" {
		t.Fatalf("%+v %v", c, err)
	}
	if err := db.SetAgentSharedKey(ctx, id, "agAAAAAAAAAAAAAA", "s2", acctNow); !errors.Is(err, db.ErrAgentSharedAdminDisabled) {
		t.Fatalf("应被拒: %v", err)
	}
	if c, _ = db.GetAgentSharedCreds(ctx, id); c.KeySealed != "s1" {
		t.Fatalf("密钥不应变: %+v", c)
	}
	if err := db.SetAgentSharedAdminDisabled(ctx, id, false, acctNow); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAgentSharedKey(ctx, id, "agAAAAAAAAAAAAAA", "s2", acctNow); err != nil {
		t.Fatal(err)
	}
	if c, _ = db.GetAgentSharedCreds(ctx, id); c.KeySealed != "s2" || c.AdminDisabled {
		t.Fatalf("%+v", c)
	}
	// 没有行时也能关闭，且不动密钥
	other := newTestAgent(t, "bob")
	if err := db.SetAgentSharedAdminDisabled(ctx, other, true, acctNow); err != nil {
		t.Fatal(err)
	}
	if c, _ = db.GetAgentSharedCreds(ctx, other); !c.Exists || !c.AdminDisabled || c.AppID != "" {
		t.Fatalf("%+v", c)
	}
}

func TestAgentSharedCreds_AllowlistAndLowBalance(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	want := []string{"1.2.3.4", "10.0.0.0/8"}
	if err := db.SetAgentSharedAllowlist(ctx, id, want, acctNow); err != nil {
		t.Fatal(err)
	}
	c, _ := db.GetAgentSharedCreds(ctx, id)
	if len(c.IPAllowlist) != 2 || c.IPAllowlist[0] != want[0] || c.IPAllowlist[1] != want[1] {
		t.Fatalf("%v", c.IPAllowlist)
	}
	if err := db.SetAgentSharedAllowlist(ctx, id, nil, acctNow); err != nil {
		t.Fatal(err)
	}
	if c, _ = db.GetAgentSharedCreds(ctx, id); c.IPAllowlist != nil {
		t.Fatalf("应为 nil: %#v", c.IPAllowlist)
	}
	if err := db.SetAgentSharedLowBalance(ctx, id, 500, acctNow); err != nil {
		t.Fatal(err)
	}
	if c, _ = db.GetAgentSharedCreds(ctx, id); c.LowBalanceCents != 500 {
		t.Fatalf("%+v", c)
	}
	if err := db.SetAgentSharedLowBalance(ctx, id, -1, acctNow); err == nil {
		t.Fatal("负数应报错")
	}
}

func TestTouchAgentSharedUsed_OncePerMinute(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	if err := db.SetAgentSharedKey(ctx, id, "agAAAAAAAAAAAAAA", "s1", acctNow); err != nil {
		t.Fatal(err)
	}
	t0 := acctNow
	for _, step := range []struct {
		at   time.Time
		want int64
	}{{t0, t0.Unix()}, {t0.Add(30 * time.Second), t0.Unix()}, {t0.Add(61 * time.Second), t0.Add(61 * time.Second).Unix()}} {
		if err := db.TouchAgentSharedUsed(ctx, id, step.at); err != nil {
			t.Fatal(err)
		}
		if c, _ := db.GetAgentSharedCreds(ctx, id); c.LastUsedAt != step.want {
			t.Fatalf("在 %v 后 last_used_at=%d，想要 %d", step.at, c.LastUsedAt, step.want)
		}
	}
}

func TestAgentSharedFailures(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	t0 := acctNow
	rec := func(no, reason string, at time.Time) {
		t.Helper()
		if err := db.RecordAgentSharedFailure(ctx, db.NewSharedFailure{AgentID: id, RequestNo: no, ProductCode: "mw-plus", Quantity: 2, Reason: reason, Message: "m-" + reason}, at); err != nil {
			t.Fatal(err)
		}
	}
	rec("r1", "not_ready", t0)
	rec("r1", "insufficient_balance", t0.Add(10*time.Second))
	list, total, err := db.ListAgentSharedFailures(ctx, id, 0, 10, 0)
	if err != nil || total != 1 || len(list) != 1 {
		t.Fatalf("%+v %d %v", list, total, err)
	}
	f := list[0]
	if f.Attempts != 2 || f.Reason != "insufficient_balance" || f.Message != "m-insufficient_balance" || f.FirstAt != t0.Unix() || f.LastAt != t0.Add(10*time.Second).Unix() || f.ResolvedOrderID != nil {
		t.Fatalf("%+v", f)
	}
	rec("r2", "busy", t0.Add(20*time.Second))
	tot, ins, err := db.CountAgentSharedFailures(ctx, id, 0)
	if err != nil || tot != 2 || ins != 1 {
		t.Fatalf("%d %d %v", tot, ins, err)
	}
	if tot, ins, _ = db.CountAgentSharedFailures(ctx, id, t0.Add(15*time.Second).Unix()); tot != 1 || ins != 0 {
		t.Fatalf("since 过滤: %d %d", tot, ins)
	}
	if err := db.ResolveAgentSharedFailure(ctx, id, "r1", 42); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveAgentSharedFailure(ctx, id, "nope", 43); err != nil {
		t.Fatalf("没有行不报错: %v", err)
	}
	if tot, ins, _ = db.CountAgentSharedFailures(ctx, id, 0); tot != 1 || ins != 0 {
		t.Fatalf("解决后不再计数: %d %d", tot, ins)
	}
	list, _, _ = db.ListAgentSharedFailures(ctx, id, 0, 10, 0)
	if len(list) != 2 || list[0].RequestNo != "r2" || list[1].RequestNo != "r1" || list[1].ResolvedOrderID == nil || *list[1].ResolvedOrderID != 42 {
		t.Fatalf("%+v", list)
	}
	// 已解决的行不会被再次 Resolve 覆盖
	if err := db.ResolveAgentSharedFailure(ctx, id, "r1", 99); err != nil {
		t.Fatal(err)
	}
	if list, _, _ = db.ListAgentSharedFailures(ctx, id, 0, 10, 0); *list[1].ResolvedOrderID != 42 {
		t.Fatalf("已解决的不应被覆盖: %+v", list[1])
	}
	rec("r1", "insufficient_balance", t0.Add(30*time.Second))
	list, total, _ = db.ListAgentSharedFailures(ctx, id, 0, 10, 0)
	if total != 2 || list[0].RequestNo != "r1" || list[0].ResolvedOrderID != nil || list[0].Attempts != 3 {
		t.Fatalf("%+v", list)
	}
	if tot, ins, _ = db.CountAgentSharedFailures(ctx, id, 0); tot != 2 || ins != 1 {
		t.Fatalf("%d %d", tot, ins)
	}
	// 分页
	if page, total, _ := db.ListAgentSharedFailures(ctx, id, 0, 1, 1); total != 2 || len(page) != 1 || page[0].RequestNo != "r2" {
		t.Fatalf("%+v %d", page, total)
	}
	n, err := db.PruneAgentSharedFailures(ctx, t0.Add(25*time.Second).Unix())
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if _, total, _ = db.ListAgentSharedFailures(ctx, id, 0, 10, 0); total != 1 {
		t.Fatalf("剩 %d", total)
	}
}

func TestAgentSharedCreds_AppIDTaken(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	a1 := newTestAgent(t, "alice")
	a2 := newTestAgent(t, "bob")
	if err := db.SetAgentSharedKey(ctx, a1, "agAAAAAAAAAAAAAA", "s1", acctNow); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAgentSharedKey(ctx, a2, "agAAAAAAAAAAAAAA", "s2", acctNow); !errors.Is(err, db.ErrAgentSharedAppIDTaken) {
		t.Fatalf("商户 ID 被占用应返回 ErrAgentSharedAppIDTaken: %v", err)
	}
	if c, _ := db.GetAgentSharedCreds(ctx, a2); c.Exists && c.AppID != "" {
		t.Fatalf("撞约束后不应写入: %+v", c)
	}
}
