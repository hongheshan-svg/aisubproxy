package db_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// openAgentFileDB 文件库 + 25 个连接 + 生产同款 DSN：测并发扣款用。
func openAgentFileDB(t *testing.T) {
	t.Helper()
	c, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "agent.db")+"?_journal_mode=WAL&_busy_timeout=10000&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(25)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
}

func agentBalance(t *testing.T, id int64) int64 {
	t.Helper()
	a, err := db.GetAgent(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return a.BalanceCents
}

func ledgerCount(t *testing.T, id int64) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_ledger WHERE agent_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAdjustAgentBalanceIdempotent(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	e1, replay, err := db.AdjustAgentBalance(ctx, id, 500, "k1", "admin:root", "补偿", acctNow)
	if err != nil || replay {
		t.Fatalf("第一次: %v replay=%v", err, replay)
	}
	if e1.ID == 0 || e1.Kind != db.LedgerAdjust || e1.ExtRef != "admin:k1" || e1.BalanceAfterCents != 500 || e1.OrderID != nil {
		t.Fatalf("%+v", e1)
	}
	e2, replay, err := db.AdjustAgentBalance(ctx, id, 500, "k1", "admin:root", "补偿", acctNow.Add(time.Hour))
	if err != nil || !replay || e2.ID != e1.ID {
		t.Fatalf("第二次: %+v %v %v", e2, replay, err)
	}
	if agentBalance(t, id) != 500 || ledgerCount(t, id) != 1 {
		t.Fatalf("余额应只加一次: %d, 流水 %d", agentBalance(t, id), ledgerCount(t, id))
	}
	// 同一个键换了金额，不能当成重放
	if _, _, err := db.AdjustAgentBalance(ctx, id, 600, "k1", "admin:root", "补偿", acctNow); !errors.Is(err, db.ErrLedgerRefConflict) {
		t.Fatalf("got %v", err)
	}
	// 停用的代理商也能调账
	if err := db.SetAgentStatus(ctx, id, db.AgentDisabled, acctNow); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AdjustAgentBalance(ctx, id, -200, "k2", "admin:root", "扣回", acctNow); err != nil {
		t.Fatalf("停用后调账: %v", err)
	}
	if agentBalance(t, id) != 300 {
		t.Fatalf("余额 %d", agentBalance(t, id))
	}
	if _, _, err := db.AdjustAgentBalance(ctx, 9999, 100, "k3", "admin:root", "x", acctNow); !errors.Is(err, db.ErrAgentNotFound) {
		t.Fatalf("got %v", err)
	}
	// 键已被另一个代理商用过
	other := newTestAgent(t, "bob")
	if _, _, err := db.AdjustAgentBalance(ctx, other, 500, "k1", "admin:root", "x", acctNow); !errors.Is(err, db.ErrLedgerRefConflict) {
		t.Fatalf("got %v", err)
	}
}

func TestAdjustAgentBalanceRejectsOverdraft(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	if _, _, err := db.AdjustAgentBalance(ctx, id, 300, "k0", "admin:root", "初始", acctNow); err != nil {
		t.Fatal(err)
	}
	_, _, err := db.AdjustAgentBalance(ctx, id, -500, "k1", "admin:root", "扣多了", acctNow)
	if !errors.Is(err, db.ErrInsufficientBalance) {
		t.Fatalf("got %v", err)
	}
	var ib *db.InsufficientBalanceError
	if !errors.As(err, &ib) || ib.BalanceCents != 300 || ib.NeedCents != 500 {
		t.Fatalf("%+v", ib)
	}
	if agentBalance(t, id) != 300 || ledgerCount(t, id) != 1 {
		t.Fatalf("余额 %d 流水 %d", agentBalance(t, id), ledgerCount(t, id))
	}
}

func TestAdjustAgentBalanceNeedsNote(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	for name, c := range map[string]struct {
		amt       int64
		key, note string
	}{
		"空原因":   {100, "k", ""},
		"空白原因":  {100, "k", " \t\n"},
		"金额为 0": {0, "k", "x"},
		"空幂等键":  {100, "", "x"},
	} {
		if _, _, err := db.AdjustAgentBalance(ctx, id, c.amt, c.key, "admin:root", c.note, acctNow); err == nil {
			t.Fatalf("%s 应被拒绝", name)
		}
	}
	if agentBalance(t, id) != 0 || ledgerCount(t, id) != 0 {
		t.Fatal("被拒绝的调账不应改余额或写流水")
	}
}

func TestCreditAgentIdempotentAndConflict(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	e1, replay, err := db.CreditAgent(ctx, id, db.LedgerTopup, 1000, "EP-1", "system:epay", "充值", acctNow)
	if err != nil || replay || e1.BalanceAfterCents != 1000 {
		t.Fatalf("%+v %v %v", e1, replay, err)
	}
	e2, replay, err := db.CreditAgent(ctx, id, db.LedgerTopup, 1000, "EP-1", "system:epay", "充值", acctNow)
	if err != nil || !replay || e2.ID != e1.ID {
		t.Fatalf("%+v %v %v", e2, replay, err)
	}
	if _, _, err := db.CreditAgent(ctx, id, db.LedgerTopup, 2000, "EP-1", "system:epay", "充值", acctNow); !errors.Is(err, db.ErrLedgerRefConflict) {
		t.Fatalf("换金额: %v", err)
	}
	other := newTestAgent(t, "bob")
	if _, _, err := db.CreditAgent(ctx, other, db.LedgerTopup, 1000, "EP-1", "system:epay", "充值", acctNow); !errors.Is(err, db.ErrLedgerRefConflict) {
		t.Fatalf("换代理商: %v", err)
	}
	if agentBalance(t, id) != 1000 || agentBalance(t, other) != 0 || ledgerCount(t, id) != 1 {
		t.Fatal("冲突和重放都不应改余额")
	}
	// 参数校验
	for name, f := range map[string]func() error{
		"kind 不是 topup": func() error {
			_, _, err := db.CreditAgent(ctx, id, db.LedgerAdjust, 100, "R", "s", "", acctNow)
			return err
		},
		"金额为 0": func() error {
			_, _, err := db.CreditAgent(ctx, id, db.LedgerTopup, 0, "R", "s", "", acctNow)
			return err
		},
		"金额为负": func() error {
			_, _, err := db.CreditAgent(ctx, id, db.LedgerTopup, -5, "R", "s", "", acctNow)
			return err
		},
		"空单号": func() error {
			_, _, err := db.CreditAgent(ctx, id, db.LedgerTopup, 100, "", "s", "", acctNow)
			return err
		},
	} {
		if f() == nil {
			t.Fatalf("%s 应被拒绝", name)
		}
	}
	if _, _, err := db.CreditAgent(ctx, 9999, db.LedgerTopup, 100, "EP-X", "s", "", acctNow); !errors.Is(err, db.ErrAgentNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestCreditAgentConcurrentSameRef(t *testing.T) {
	openAgentFileDB(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	replays, firsts := 0, 0
	errs := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, replay, err := db.CreditAgent(ctx, id, db.LedgerTopup, 1000, "EP-CONC", "system:epay", "充值", acctNow)
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			if replay {
				replays++
			} else {
				firsts++
			}
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发入账出错: %v", err)
	}
	if firsts != 1 || replays != n-1 {
		t.Fatalf("应 1 次真入账 %d 次重放，得到 %d / %d", n-1, firsts, replays)
	}
	if agentBalance(t, id) != 1000 || ledgerCount(t, id) != 1 {
		t.Fatalf("余额 %d 流水 %d", agentBalance(t, id), ledgerCount(t, id))
	}
}

func TestLedgerSummaryOpeningClosing(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	day := func(d int) time.Time { return time.Date(2026, 11, d, 12, 0, 0, 0, time.UTC) }
	mustAdj := func(amt int64, key string, at time.Time) {
		t.Helper()
		if _, _, err := db.AdjustAgentBalance(ctx, id, amt, key, "admin:root", "x", at); err != nil {
			t.Fatal(err)
		}
	}
	mustAdj(1000, "a", day(1))
	mustAdj(-300, "b", day(2))
	mustAdj(500, "c", day(3))
	mustAdj(-100, "d", day(3).Add(time.Hour))
	from, to := day(3).Add(-time.Hour).Unix(), day(4).Unix()
	s, err := db.AgentLedgerSummary(ctx, id, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if s != (db.LedgerSummary{OpeningCents: 700, ClosingCents: 1100, InCents: 500, OutCents: 100}) {
		t.Fatalf("%+v", s)
	}
	// 范围内没有流水：期末等于期初
	s, _ = db.AgentLedgerSummary(ctx, id, day(10).Unix(), day(11).Unix())
	if s != (db.LedgerSummary{OpeningCents: 1100, ClosingCents: 1100}) {
		t.Fatalf("%+v", s)
	}
	// 范围之前没有流水：期初为 0
	s, _ = db.AgentLedgerSummary(ctx, id, day(1).Add(-time.Hour).Unix(), day(2).Unix())
	if s != (db.LedgerSummary{OpeningCents: 0, ClosingCents: 1000, InCents: 1000}) {
		t.Fatalf("%+v", s)
	}
	// 列表：[from, to) 左闭右开，id 降序，总数不受分页影响
	list, total, err := db.ListAgentLedger(ctx, id, day(2).Unix(), day(3).Unix()+3600, 2, 0)
	if err != nil || total != 2 || len(list) != 2 || list[0].AmountCents != 500 || list[1].AmountCents != -300 {
		t.Fatalf("%+v %d %v", list, total, err)
	}
	list, total, _ = db.ListAgentLedger(ctx, id, 0, day(30).Unix(), 2, 1)
	if total != 4 || len(list) != 2 || list[0].AmountCents != 500 {
		t.Fatalf("分页: %+v %d", list, total)
	}
	if _, total, _ = db.ListAgentLedger(ctx, id, day(3).Unix(), day(3).Unix(), 10, 0); total != 0 {
		t.Fatalf("空范围 total=%d", total)
	}
}

func TestCheckAgentBalancesDetectsTamper(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	newTestAgent(t, "bob") // 没有流水、余额 0：正常
	if _, _, err := db.CreditAgent(ctx, id, db.LedgerTopup, 1000, "EP-1", "s", "", acctNow); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AdjustAgentBalance(ctx, id, -250, "k", "admin:root", "x", acctNow); err != nil {
		t.Fatal(err)
	}
	if got, err := db.CheckAgentBalances(ctx); err != nil || len(got) != 0 {
		t.Fatalf("正常时应为空: %+v %v", got, err)
	}
	if _, err := db.DB.Exec(`UPDATE agents SET balance_cents = balance_cents + 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	got, err := db.CheckAgentBalances(ctx)
	if err != nil || len(got) != 1 || got[0] != (db.AgentBalanceMismatch{AgentID: id, BalanceCents: 751, LedgerSumCents: 750, LastAfterCents: 750}) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestAgentTotalsSince(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	if _, _, err := db.CreditAgent(ctx, id, db.LedgerTopup, 5000, "EP-1", "s", "", acctNow); err != nil {
		t.Fatal(err)
	}
	// purchase 流水由下单（后续任务）产生；这里直接插入，只验证合计口径
	ins := func(amt, after, orderID, at int64) {
		if _, err := db.DB.Exec(`INSERT INTO agent_ledger (agent_id, kind, amount_cents, balance_after_cents, order_id, created_at) VALUES (?, 'purchase', ?, ?, ?, ?)`,
			id, amt, after, orderID, at); err != nil {
			t.Fatal(err)
		}
	}
	ins(-100, 4900, 1, 1000) // 上月
	ins(-200, 4700, 2, 2000) // 本月，不是今天
	ins(-300, 4400, 3, 3000) // 今天
	got, err := db.AgentTotalsSince(ctx, 2500, 1500)
	if err != nil || got != (db.AgentTotals{BalanceCents: 5000, TodayOrderCents: 300, MonthOrderCents: 500}) {
		t.Fatalf("%+v %v", got, err)
	}
}

// 顺带验证 Task 1 的流水唯一约束：非空 ext_ref 在同一个 kind 下唯一，空串不受限。
func TestAgentLedgerExtRefUniqueness(t *testing.T) {
	dbtest.Open(t)
	id := newTestAgent(t, "alice")
	ins := func(kind, ext string, order int64) error {
		_, err := db.DB.Exec(`INSERT INTO agent_ledger (agent_id, kind, amount_cents, balance_after_cents, order_id, ext_ref, created_at) VALUES (?, ?, 1, 1, ?, ?, 1)`,
			id, kind, order, ext)
		return err
	}
	if err := ins("topup", "R1", 0); err != nil {
		t.Fatal(err)
	}
	if err := ins("topup", "R1", 0); err == nil {
		t.Fatal("同一个 kind 下非空 ext_ref 重复应被拒绝")
	}
	if err := ins("adjust", "R1", 0); err != nil {
		t.Fatalf("不同 kind 可以用同一个 ext_ref: %v", err)
	}
	for i := int64(1); i <= 2; i++ {
		if err := ins("purchase", "", i); err != nil {
			t.Fatalf("空 ext_ref 不应受限: %v", err)
		}
	}
}
