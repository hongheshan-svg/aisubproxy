package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

type finalStatusCode struct {
	ID         int64  `json:"id"`
	Code       string `json:"code"`
	Status     string `json:"status"`
	ReplacedBy *int64 `json:"replaced_by"`
}

// finalMakeReplacement 给 oldID 造一张换码换出的新码（exchanged_from 指向旧码），返回新码 ID。
func finalMakeReplacement(t *testing.T, oldID int64) int64 {
	t.Helper()
	res, err := db.DB.Exec(`INSERT INTO local_cdks (provider, code_hash, token_hash, code_prefix, product, plan_key, region, status, exchanged_from, attempts_carried, issued_by, note, created_at, updated_at, agent_id, agent_order_id)
		SELECT provider, 'final-h-' || id, 'final-t-' || id, code_prefix, product, plan_key, region, status, id, 0, issued_by, note, created_at, updated_at, agent_id, agent_order_id FROM local_cdks WHERE id = ?`, oldID)
	if err != nil {
		t.Fatalf("造换出的新码: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func finalDecodeCodes(t *testing.T, raw string) []finalStatusCode {
	t.Helper()
	var r struct {
		Codes []finalStatusCode `json:"codes"`
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("解析: %v %s", err, raw)
	}
	return r.Codes
}

// F2：订单导出和同号重放给出的码带状态；换码换出的新码 ID 带在 replaced_by 里；别的代理商的码混不进来。
func TestAgentOrderCodesCarryStatusFinal(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	f.topUp()
	_, bs := f.seedBuyer("bob", 1000)
	theirs := f.mustOrder(bs, orderReq("mw-plus-ph", 1, "", "req-fs-b", 500))
	req := orderReq("mw-plus-ph", 3, "", "req-fs-a", 500)
	r := f.mustOrder(f.s, req)
	exchanged, disabled, fresh := r.Codes[0].ID, r.Codes[1].ID, r.Codes[2].ID
	newID := finalMakeReplacement(t, exchanged)
	if ok, err := db.DisableLocalCDK(context.Background(), disabled, time.Now()); err != nil || !ok {
		t.Fatalf("禁用: %v %v", ok, err)
	}
	check := func(name, raw string) {
		t.Helper()
		got := finalDecodeCodes(t, raw)
		if len(got) != 3 {
			t.Fatalf("%s: 应 3 张: %s", name, raw)
		}
		for _, c := range got {
			switch c.ID {
			case exchanged:
				if c.Status != "disabled" || c.ReplacedBy == nil || *c.ReplacedBy != newID {
					t.Fatalf("%s: 已换码应 disabled 且 replaced_by=%d: %+v", name, newID, c)
				}
			case disabled:
				if c.Status != "disabled" || c.ReplacedBy != nil {
					t.Fatalf("%s: 被禁用的码: %+v", name, c)
				}
			case fresh:
				if c.Status != "unused" || c.ReplacedBy != nil {
					t.Fatalf("%s: 未用的码: %+v", name, c)
				}
			default:
				t.Fatalf("%s: 混进了别的码 %d (别人的 %d)", name, c.ID, theirs.Codes[0].ID)
			}
		}
	}
	w := f.agentDo(f.s, "POST", fmt.Sprintf("%s/%d/export", orderPath, r.Order.ID), "")
	check("导出", w.Body.String())
	w = f.post(f.s, req)
	check("重放", w.Body.String())
}

// F2：下单成功响应里的码也带 status=unused。
func TestAgentPlaceOrderCodesStatusFinal(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	w := f.post(f.s, orderReq("mw-plus-ph", 1, "", "req-fs-new", 500))
	got := finalDecodeCodes(t, w.Body.String())
	if w.Code != 200 || len(got) != 1 || got[0].Status != "unused" || got[0].ReplacedBy != nil {
		t.Fatalf("下单结果的码应带 status=unused: %d %s", w.Code, w.Body.String())
	}
}

// F2：代理商还有很多别的码时，订单导出只含这笔订单的码，且状态读取只按这些 id。
func TestAgentOrderCodesOnlyThisOrderFinal(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	f.topUp()
	f.mustOrder(f.s, orderReq("mw-plus-ph", 5, "", "req-fo-other", 500))
	r := f.mustOrder(f.s, orderReq("mw-plus-ph", 2, "", "req-fo-this", 500))
	w := f.agentDo(f.s, "POST", fmt.Sprintf("%s/%d/export", orderPath, r.Order.ID), "")
	if got := finalDecodeCodes(t, w.Body.String()); len(got) != 2 {
		t.Fatalf("应只含本单 2 张: %s", w.Body.String())
	}
	rows, err := db.LoadLocalCDKRowsByAgentIDs(context.Background(), f.aid, []int64{r.Codes[0].ID})
	if err != nil || len(rows) != 1 || rows[0].ID != r.Codes[0].ID {
		t.Fatalf("按 id 只应读到 1 张: %v %d", err, len(rows))
	}
	if rows, _ := db.LoadLocalCDKRowsByAgentIDs(context.Background(), f.aid+99, []int64{r.Codes[0].ID}); len(rows) != 0 {
		t.Fatal("别的代理商读不到")
	}
}
