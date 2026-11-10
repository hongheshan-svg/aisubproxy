package handler

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// MO-08 的本站码版本：ExchangeLocalCDK 已在同一个事务里禁用旧码、写好新码，只是记录 disabling → done 没写成。
// 新码照常交给代理，记录停在 disabling（不是 done）；维护循环补成 done。其间旧码不能再换，补记账也不会再发码。
func TestLocalAgentExchangeDisablingRetriedToDone(t *testing.T) {
	e := newSealedExchangeEnv(t)
	code, id := exchangeableCode(t, e)
	if _, err := db.DB.Exec(`CREATE TRIGGER swap_done_fail BEFORE UPDATE OF state ON agent_cdk_exchanges
		WHEN NEW.state = 'done' BEGIN SELECT RAISE(ABORT, 'done fail'); END`); err != nil {
		t.Fatal(err)
	}
	w := localSwap(e, code, "203.0.113.9")
	if w.Code != http.StatusOK {
		t.Fatalf("新码已发出，应交给代理: %d %s", w.Code, w.Body.String())
	}
	newID := int64(localJSON(t, w)["new_cdk_id"].(float64))
	if st, gotNew, _ := exchangeRow(t, code); st != db.AgentExchangeDisabling || gotNew != newID {
		t.Fatalf("转 done 没写成，记录不能是完成: st=%s new=%d want=%d", st, gotNew, newID)
	}
	if st := localCDKStatus(t, id); st != db.LocalCDKDisabled {
		t.Fatalf("旧码在换码事务里已禁用: %s", st)
	}

	// 写库仍然失败：维护循环再试两次，仍停在 disabling
	retryAgentExchangeDisables(time.Now())
	retryAgentExchangeDisables(time.Now())
	if st, _, _ := exchangeRow(t, code); st != db.AgentExchangeDisabling {
		t.Fatalf("仍失败时应保持 disabling: %s", st)
	}
	// 其间这张旧码始终不能再换
	if w := localSwap(e, code, "203.0.113.9"); w.Code != http.StatusConflict {
		t.Fatalf("已换过的旧码不能再换: %d %s", w.Code, w.Body.String())
	}

	// 写库恢复：下一轮补成 done，再跑也不变
	if _, err := db.DB.Exec(`DROP TRIGGER swap_done_fail`); err != nil {
		t.Fatal(err)
	}
	retryAgentExchangeDisables(time.Now())
	if st, gotNew, _ := exchangeRow(t, code); st != db.AgentExchangeDone || gotNew != newID {
		t.Fatalf("补记账后应转 done: st=%s new=%d", st, gotNew)
	}
	retryAgentExchangeDisables(time.Now())
	if st, _, _ := exchangeRow(t, code); st != db.AgentExchangeDone {
		t.Fatalf("done 的记录不应再变: %s", st)
	}
	if got := localExchangedFrom(t, id); got != 1 {
		t.Fatalf("补记账不能再发码: %d", got)
	}
	if e.zovo.count() != 0 {
		t.Fatalf("本站码不能去卡台禁用: %v", e.zovo.paths)
	}
}

// 规格 §11.3：old_upstream_id 大于 0 的是已移除的 ZovoCard 留下的历史换码记录，不改数据、不自动收尾。
//   - 停在 disabling 的：维护循环跳过它，状态、note、updated_at 都不变（不能被当成本站码补成 done）；
//   - 停在 unknown 的：照常能人工定论。released 删掉占位；issued 记下新码、停在 disabling，之后维护循环同样不动它。
//
// 全程不发任何上游（卡台）请求，定论响应里也没有 old_cdk_id、old_disabled。
func TestLegacyZovoExchangeRecordsNotAutoClosed(t *testing.T) {
	e := newLocalEnv(t)
	const stamp = "2026-11-01 00:00:00"
	insert := func(hash string, oldID int64, state, note string) int64 {
		t.Helper()
		res, err := db.DB.Exec(`INSERT INTO agent_cdk_exchanges (old_code_hash, old_upstream_id, new_upstream_id, old_code_prefix, plan, order_status, ip, state, note, created_at, updated_at)
			VALUES (?, ?, CASE WHEN ? = 'disabling' THEN 901 ELSE NULL END, 'ZC-LEGACY', 'plus', 'failed', '', ?, ?, ?, ?)`,
			hash, oldID, state, state, note, stamp, stamp)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	disablingID := insert("h-zovo-disabling", 77, db.AgentExchangeDisabling, "禁用旧码失败：boom")
	releasedID := insert("h-zovo-unknown-released", 78, db.AgentExchangeUnknown, "卡台发码超时")
	issuedID := insert("h-zovo-unknown-issued", 79, db.AgentExchangeUnknown, "卡台发码超时")

	get := func(id int64) db.AgentExchange {
		t.Helper()
		x, err := db.GetAgentExchange(id)
		if err != nil {
			t.Fatalf("记录 %d: %v", id, err)
		}
		return x
	}
	maintain := func() {
		t.Helper()
		retryAgentExchangeDisables(time.Now())
		runOpsMaintenance(time.Now())
	}

	// disabling：跑两轮维护，什么都不变
	before := get(disablingID)
	maintain()
	maintain()
	if after := get(disablingID); after != before || after.State != db.AgentExchangeDisabling || after.Note != "禁用旧码失败：boom" || after.UpdatedAt != stamp {
		t.Fatalf("历史 disabling 记录不能被自动收尾或改动: 之前 %+v 之后 %+v", before, after)
	}

	r := adminExchangeRouter()
	resolve := func(id int64, body string) map[string]any {
		t.Helper()
		w := adminExchangeDo(r, "POST", fmt.Sprintf("%s/%d/resolve", adminExchangesPath, id), body)
		if w.Code != http.StatusOK {
			t.Fatalf("记录 %d 定论应 200: %d %s", id, w.Code, w.Body.String())
		}
		out := localJSON(t, w)
		for _, k := range []string{"old_disabled", "old_cdk_id"} {
			if _, ok := out[k]; ok {
				t.Fatalf("定论响应不应有 %s: %v", k, out)
			}
		}
		return out
	}

	// unknown → released：占位删掉
	if out := resolve(releasedID, `{"result":"released","note":"卡台已下线，核对确认没发码"}`); out["result"] != "released" {
		t.Fatalf("%v", out)
	}
	if _, err := db.GetAgentExchange(releasedID); !errors.Is(err, db.ErrAgentExchangeNotFound) {
		t.Fatalf("released 之后行应删除: %v", err)
	}

	// unknown → issued：停在 disabling，维护循环之后仍不变
	if out := resolve(issuedID, `{"result":"issued","new_cdk_id":950,"note":"卡台记录里查到新码"}`); out["result"] != "issued" {
		t.Fatalf("%v", out)
	}
	issued := get(issuedID)
	if issued.State != db.AgentExchangeDisabling || issued.NewUpstreamID != 950 || issued.OldUpstreamID != 79 {
		t.Fatalf("issued 之后应停在 disabling: %+v", issued)
	}
	maintain()
	if after := get(issuedID); after != issued {
		t.Fatalf("定论 issued 的历史记录，维护循环不能再动它: 之前 %+v 之后 %+v", issued, after)
	}
	if after := get(disablingID); after != before {
		t.Fatalf("历史 disabling 记录不能被改动: %+v", after)
	}

	if e.zovo.count() != 0 {
		t.Fatalf("不应发出任何上游请求: %v", e.zovo.paths)
	}
}
