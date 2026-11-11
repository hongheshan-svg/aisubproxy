package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
)

func cpSetUseCount(t *testing.T, c *sql.DB, id string, n int) {
	t.Helper()
	if _, err := c.Exec(`UPDATE mw_cards SET use_count = ? WHERE card_id = ?`, n, id); err != nil {
		t.Fatal(err)
	}
}

func putSelectionUnusable(t *testing.T, body string) (int, bool) {
	t.Helper()
	w := cpDo(AdminCardPoolPutSelection, "PUT", "/x", "/x", body)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /selection: %d %s", w.Code, w.Body.String())
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	raw, ok := m["unusable_cards"]
	if !ok {
		return 0, false
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	return n, true
}

// 调低「单卡最大使用次数」（C1-11）：保存结果里带上已用次数达到新上限、将不再可用的卡数（可用和回收中的卡），
// 由管理员手动处理，系统不自动注销；没调低时不带这一项。
func TestPutSelectionReportsUsedUpCardsWhenLowered(t *testing.T) {
	c, _, _ := cpEnv(t, false)
	putSelection(t, `{"max_uses_per_card":3}`)
	cpInsertCard(t, c, "a0", cardpool.PoolAvailable, "")
	cpInsertCard(t, c, "a2", cardpool.PoolAvailable, "")
	cpSetUseCount(t, c, "a2", 2)
	cpInsertCard(t, c, "r1", cardpool.PoolReleasing, "")
	cpSetUseCount(t, c, "r1", 1)
	cpInsertCard(t, c, "l1", cardpool.PoolLeased, "tok") // 租用中：释放时按新上限自动转已用，不算
	cpSetUseCount(t, c, "l1", 2)
	cpInsertCard(t, c, "u3", cardpool.PoolUsed, "") // 已用：本来就在注销流程里，不算
	cpSetUseCount(t, c, "u3", 3)

	if n, ok := putSelectionUnusable(t, `{"max_uses_per_card":2}`); !ok || n != 1 {
		t.Fatalf("3 → 2：a2 将不再可用，应为 1: %d %v", n, ok)
	}
	if n, ok := putSelectionUnusable(t, `{"max_uses_per_card":1}`); !ok || n != 2 {
		t.Fatalf("2 → 1：a2、r1 将不再可用，应为 2: %d %v", n, ok)
	}
	if n, ok := putSelectionUnusable(t, `{"max_uses_per_card":5}`); ok {
		t.Fatalf("调高不应带不可用卡数: %d", n)
	}
	if n, ok := putSelectionUnusable(t, `{"max_uses_per_card":5,"auto_suspend":false}`); ok {
		t.Fatalf("没改上限不应带不可用卡数: %d", n)
	}
	var state string
	if err := c.QueryRow(`SELECT pool_state FROM mw_cards WHERE card_id = 'a2'`).Scan(&state); err != nil || state != cardpool.PoolAvailable {
		t.Fatalf("不自动处理卡: %s %v", state, err)
	}
}
