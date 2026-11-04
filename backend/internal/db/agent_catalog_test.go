package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

var agentCtx = context.Background()
var agentNow = time.Unix(2000, 0)

// 直接用 SQL 插代理商（CreateAgent 在后续任务里才有）
func insertTestAgent(t *testing.T, username string, tierID int64) int64 {
	t.Helper()
	res, err := db.DB.Exec(`INSERT INTO agents (username, display_name, password_hash, tier_id, created_at, updated_at) VALUES (?, ?, 'x', ?, 1, 1)`,
		username, username, tierID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func newTier(t *testing.T, name string) int64 {
	t.Helper()
	id, err := db.CreateAgentTier(agentCtx, name, "", agentNow)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newProduct(t *testing.T, code, plan, region string) int64 {
	t.Helper()
	id, err := db.CreateAgentProduct(agentCtx, db.AgentProduct{Code: code, Provider: "marswallex", Product: "gpt", PlanKey: plan,
		Region: region, Name: code, Enabled: true}, agentNow)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMigrateAgentsIdempotent(t *testing.T) {
	dbtest.Open(t)
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"agent_id", "agent_order_id"} {
		var n int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('local_cdks') WHERE name = ?`, col).Scan(&n); err != nil || n != 1 {
			t.Fatalf("local_cdks 缺列 %s: n=%d err=%v", col, n, err)
		}
	}
}

func TestAgentTierNameUniqueAndInUse(t *testing.T) {
	dbtest.Open(t)
	id := newTier(t, "普通")
	if _, err := db.CreateAgentTier(agentCtx, "普通", "", agentNow); !errors.Is(err, db.ErrAgentTierNameTaken) {
		t.Fatalf("同名应返回 ErrAgentTierNameTaken: %v", err)
	}
	other := newTier(t, "金牌")
	if err := db.UpdateAgentTier(agentCtx, other, "普通", "", agentNow); !errors.Is(err, db.ErrAgentTierNameTaken) {
		t.Fatalf("改成已有名应返回 ErrAgentTierNameTaken: %v", err)
	}
	if err := db.UpdateAgentTier(agentCtx, 999, "x", "", agentNow); !errors.Is(err, db.ErrAgentTierNotFound) {
		t.Fatalf("不存在: %v", err)
	}
	insertTestAgent(t, "alice", id)
	tiers, err := db.ListAgentTiers(agentCtx)
	if err != nil || len(tiers) != 2 {
		t.Fatalf("tiers=%v err=%v", tiers, err)
	}
	for _, tr := range tiers {
		want := 0
		if tr.ID == id {
			want = 1
		}
		if tr.AgentCount != want {
			t.Fatalf("等级 %s 的 AgentCount=%d，想要 %d", tr.Name, tr.AgentCount, want)
		}
	}
	if err := db.DeleteAgentTier(agentCtx, id); !errors.Is(err, db.ErrAgentTierInUse) {
		t.Fatalf("有代理商在用应返回 ErrAgentTierInUse: %v", err)
	}
	if err := db.DeleteAgentTier(agentCtx, other); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteAgentTier(agentCtx, other); !errors.Is(err, db.ErrAgentTierNotFound) {
		t.Fatalf("重复删除: %v", err)
	}
}

func TestAgentProductUniqueComboAndCode(t *testing.T) {
	dbtest.Open(t)
	newProduct(t, "mw-plus-ph", "plus", "PH")
	_, err := db.CreateAgentProduct(agentCtx, db.AgentProduct{Code: "mw-plus-ph", Provider: "marswallex", Product: "gpt", PlanKey: "pro", Region: "US", Name: "x"}, agentNow)
	if !errors.Is(err, db.ErrAgentProductExists) {
		t.Fatalf("同编码应返回 ErrAgentProductExists: %v", err)
	}
	_, err = db.CreateAgentProduct(agentCtx, db.AgentProduct{Code: "other", Provider: "marswallex", Product: "gpt", PlanKey: "plus", Region: "PH", Name: "x"}, agentNow)
	if !errors.Is(err, db.ErrAgentProductExists) {
		t.Fatalf("同组合应返回 ErrAgentProductExists: %v", err)
	}
	if _, err := db.GetAgentProductByCode(agentCtx, "nope"); !errors.Is(err, db.ErrAgentProductNotFound) {
		t.Fatalf("不存在: %v", err)
	}
}

func TestAgentProductImmutableFields(t *testing.T) {
	dbtest.Open(t)
	id := newProduct(t, "mw-plus-ph", "plus", "PH")
	if err := db.UpdateAgentProduct(agentCtx, id, "新名字", false, 7, time.Unix(3000, 0)); err != nil {
		t.Fatal(err)
	}
	p, err := db.GetAgentProductByCode(agentCtx, "mw-plus-ph")
	if err != nil {
		t.Fatal(err)
	}
	if p.Code != "mw-plus-ph" || p.PlanKey != "plus" || p.Region != "PH" || p.Provider != "marswallex" || p.Product != "gpt" {
		t.Fatalf("不可改的字段被改了: %+v", p)
	}
	if p.Name != "新名字" || p.Enabled || p.Sort != 7 || p.UpdatedAt != 3000 || p.CreatedAt != 2000 {
		t.Fatalf("可改字段没生效: %+v", p)
	}
	if err := db.UpdateAgentProduct(agentCtx, 999, "x", true, 0, agentNow); !errors.Is(err, db.ErrAgentProductNotFound) {
		t.Fatalf("不存在: %v", err)
	}
}

func TestPutAgentTierPricesUpsertAndDelete(t *testing.T) {
	dbtest.Open(t)
	tier := newTier(t, "普通")
	p1 := newProduct(t, "mw-plus-ph", "plus", "PH")
	p2 := newProduct(t, "mw-plus-us", "plus", "US")
	price := func() map[int64]int64 {
		cells, err := db.ListAgentTierPrices(agentCtx)
		if err != nil {
			t.Fatal(err)
		}
		m := map[int64]int64{}
		for _, c := range cells {
			if c.TierID == tier {
				m[c.ProductID] = c.PriceCents
			}
		}
		return m
	}
	if err := db.PutAgentTierPrices(agentCtx, []db.AgentPriceCell{{tier, p1, 500}}, agentNow); err != nil {
		t.Fatal(err)
	}
	if m := price(); len(m) != 1 || m[p1] != 500 {
		t.Fatalf("写 500: %v", m)
	}
	if err := db.PutAgentTierPrices(agentCtx, []db.AgentPriceCell{{tier, p1, 600}}, agentNow); err != nil {
		t.Fatal(err)
	}
	if m := price(); len(m) != 1 || m[p1] != 600 {
		t.Fatalf("改 600: %v", m)
	}
	// 含负数的批次整批回滚：p2 的 700 和 p1 的删除都不生效
	err := db.PutAgentTierPrices(agentCtx, []db.AgentPriceCell{{tier, p2, 700}, {tier, p1, 0}, {tier, p2, -1}}, agentNow)
	if err == nil {
		t.Fatal("负价应报错")
	}
	if m := price(); len(m) != 1 || m[p1] != 600 {
		t.Fatalf("应整批回滚: %v", m)
	}
	// 等级或商品不存在
	if err := db.PutAgentTierPrices(agentCtx, []db.AgentPriceCell{{999, p1, 100}}, agentNow); !errors.Is(err, db.ErrAgentTierNotFound) {
		t.Fatalf("等级不存在: %v", err)
	}
	if err := db.PutAgentTierPrices(agentCtx, []db.AgentPriceCell{{tier, 999, 100}}, agentNow); !errors.Is(err, db.ErrAgentProductNotFound) {
		t.Fatalf("商品不存在: %v", err)
	}
	if err := db.PutAgentTierPrices(agentCtx, []db.AgentPriceCell{{tier, p1, 0}}, agentNow); err != nil {
		t.Fatal(err)
	}
	if m := price(); len(m) != 0 {
		t.Fatalf("写 0 应删除: %v", m)
	}
}

func TestAgentPriceForOverrideFirst(t *testing.T) {
	dbtest.Open(t)
	tier := newTier(t, "普通")
	p := newProduct(t, "mw-plus-ph", "plus", "PH")
	a := insertTestAgent(t, "alice", tier)
	if _, ok, err := db.AgentPriceFor(agentCtx, a, p); err != nil || ok {
		t.Fatalf("没价应 ok=false: %v %v", ok, err)
	}
	if err := db.PutAgentTierPrices(agentCtx, []db.AgentPriceCell{{tier, p, 500}}, agentNow); err != nil {
		t.Fatal(err)
	}
	if c, ok, err := db.AgentPriceFor(agentCtx, a, p); err != nil || !ok || c != 500 {
		t.Fatalf("等级价: %d %v %v", c, ok, err)
	}
	if err := db.PutAgentOverrides(agentCtx, a, []db.AgentOverride{{p, 450}}, agentNow); err != nil {
		t.Fatal(err)
	}
	if c, ok, err := db.AgentPriceFor(agentCtx, a, p); err != nil || !ok || c != 450 {
		t.Fatalf("覆盖价: %d %v %v", c, ok, err)
	}
	if ov, err := db.ListAgentOverrides(agentCtx, a); err != nil || len(ov) != 1 || ov[0].PriceCents != 450 {
		t.Fatalf("ListAgentOverrides: %v %v", ov, err)
	}
	if err := db.PutAgentOverrides(agentCtx, a, []db.AgentOverride{{p, -5}}, agentNow); err == nil {
		t.Fatal("负价应报错")
	}
	if err := db.PutAgentOverrides(agentCtx, a, []db.AgentOverride{{999, 5}}, agentNow); !errors.Is(err, db.ErrAgentProductNotFound) {
		t.Fatalf("商品不存在: %v", err)
	}
	if err := db.PutAgentOverrides(agentCtx, a, []db.AgentOverride{{p, 0}}, agentNow); err != nil {
		t.Fatal(err)
	}
	if c, ok, _ := db.AgentPriceFor(agentCtx, a, p); !ok || c != 500 {
		t.Fatalf("删覆盖价后应回到等级价: %d %v", c, ok)
	}
	if err := db.PutAgentTierPrices(agentCtx, []db.AgentPriceCell{{tier, p, 0}}, agentNow); err != nil {
		t.Fatal(err)
	}
	if c, ok, err := db.AgentPriceFor(agentCtx, a, p); err != nil || ok || c != 0 {
		t.Fatalf("都删掉应 0,false: %d %v %v", c, ok, err)
	}
}

func TestListAgentPurchasableSkipsDisabledAndUnpriced(t *testing.T) {
	dbtest.Open(t)
	tier := newTier(t, "普通")
	a := insertTestAgent(t, "alice", tier)
	on := newProduct(t, "mw-plus-ph", "plus", "PH")
	off := newProduct(t, "mw-plus-us", "plus", "US")
	unpriced := newProduct(t, "mw-pro-us", "pro", "US")
	viaOverride := newProduct(t, "mw-pro-ph", "pro", "PH")
	if err := db.UpdateAgentProduct(agentCtx, off, "下架", false, 0, agentNow); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateAgentProduct(agentCtx, on, "在售", true, 2, agentNow); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateAgentProduct(agentCtx, viaOverride, "覆盖价商品", true, 1, agentNow); err != nil {
		t.Fatal(err)
	}
	if err := db.PutAgentTierPrices(agentCtx, []db.AgentPriceCell{{tier, on, 500}, {tier, off, 500}}, agentNow); err != nil {
		t.Fatal(err)
	}
	if err := db.PutAgentOverrides(agentCtx, a, []db.AgentOverride{{viaOverride, 300}}, agentNow); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListAgentPurchasable(agentCtx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("应只有 2 个: %+v", list)
	}
	if list[0].ID != viaOverride || list[0].PriceCents != 300 || list[1].ID != on || list[1].PriceCents != 500 {
		t.Fatalf("顺序或价格不对: %+v", list)
	}
	_ = unpriced
}
