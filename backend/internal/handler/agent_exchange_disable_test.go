package handler

import (
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
