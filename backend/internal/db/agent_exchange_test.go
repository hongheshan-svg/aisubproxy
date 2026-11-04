package db_test

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func reservation(hash, fp string) db.AgentExchangeReservation {
	return db.AgentExchangeReservation{OldHash: hash, OldID: 77, OldPrefix: "ZC-OLD", Plan: "plus", OrderID: 5, OrderStatus: "failed", IP: "203.0.113.9", PasswordFP: fp}
}

// openFileDB 用真实文件库和多连接：内存测试库只有一条连接，测不出并发问题。
func openFileDB(t *testing.T) {
	t.Helper()
	c, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "ex.db"))
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

func TestReserveAgentExchangeRejectsSecondReservation(t *testing.T) {
	dbtest.Open(t)
	now := time.Date(2026, 11, 3, 4, 0, 0, 0, time.UTC)
	id, err := db.ReserveAgentCDKExchange(reservation("h1", "fp"), 20, now)
	if err != nil || id <= 0 {
		t.Fatalf("第一次占位: %d %v", id, err)
	}
	if st := db.AgentCDKExchangeState("h1"); st != db.AgentExchangePending {
		t.Fatalf("状态: %q", st)
	}
	if _, err := db.ReserveAgentCDKExchange(reservation("h1", "fp"), 20, now); !errors.Is(err, db.ErrAgentExchangeExists) {
		t.Fatalf("第二次占位应被拒: %v", err)
	}
}

func TestReserveAgentExchangeZeroLimitIsNotUnlimited(t *testing.T) {
	dbtest.Open(t)
	for _, limit := range []int{0, -1} {
		if _, err := db.ReserveAgentCDKExchange(reservation("h1", "fp"), limit, time.Now()); err == nil {
			t.Fatalf("limit=%d 应拒绝", limit)
		}
	}
	if st := db.AgentCDKExchangeState("h1"); st != "" {
		t.Fatalf("不应写入: %q", st)
	}
}

// 额度按北京日：UTC 15:59 和 16:00 分属两个北京日。
func TestReserveAgentExchangeDailyLimitUsesBeijingDay(t *testing.T) {
	dbtest.Open(t)
	before := time.Date(2026, 11, 3, 15, 59, 0, 0, time.UTC)
	after := time.Date(2026, 11, 3, 16, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if _, err := db.ReserveAgentCDKExchange(reservation(fmt.Sprintf("a%d", i), "fp"), 2, before); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ReserveAgentCDKExchange(reservation("a2", "fp"), 2, before); !errors.Is(err, db.ErrAgentExchangeDailyLimit) {
		t.Fatalf("应超限: %v", err)
	}
	if st := db.AgentCDKExchangeState("a2"); st != "" {
		t.Fatalf("超限不应写入: %q", st)
	}
	if _, err := db.ReserveAgentCDKExchange(reservation("a3", "fp"), 2, after); err != nil {
		t.Fatalf("北京时间次日应重新计数: %v", err)
	}
}

// 额度按密码：换了密码重新计数；升级前没有指纹的旧行也计入。释放的占位不占额度。
func TestReserveAgentExchangeCountsPerPassword(t *testing.T) {
	dbtest.Open(t)
	now := time.Date(2026, 11, 3, 4, 0, 0, 0, time.UTC)
	if _, err := db.DB.Exec(`INSERT INTO agent_cdk_exchanges (old_code_hash, created_at) VALUES ('legacy', ?)`, now.Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReserveAgentCDKExchange(reservation("b0", "fp1"), 2, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReserveAgentCDKExchange(reservation("b1", "fp1"), 2, now); !errors.Is(err, db.ErrAgentExchangeDailyLimit) {
		t.Fatalf("旧行应计入额度: %v", err)
	}
	id2, err := db.ReserveAgentCDKExchange(reservation("b2", "fp2"), 2, now)
	if err != nil {
		t.Fatalf("新密码（只算旧行）应能占位: %v", err)
	}
	if err := db.ReleaseAgentCDKExchange(id2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReserveAgentCDKExchange(reservation("b3", "fp2"), 2, now); err != nil {
		t.Fatalf("释放后额度应退回: %v", err)
	}
}

// 真实文件库、多连接：同一旧码 40 个并发只有一个占到；不同旧码不超过额度。
func TestReserveAgentExchangeConcurrentOnRealFile(t *testing.T) {
	openFileDB(t)
	now := time.Date(2026, 11, 3, 4, 0, 0, 0, time.UTC)
	run := func(n int, hash func(int) string, limit int) (ok, exists, overLimit, other int32) {
		var a, b, c, d atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, err := db.ReserveAgentCDKExchange(reservation(hash(i), "fp"), limit, now)
				switch {
				case err == nil:
					a.Add(1)
				case errors.Is(err, db.ErrAgentExchangeExists):
					b.Add(1)
				case errors.Is(err, db.ErrAgentExchangeDailyLimit):
					c.Add(1)
				default:
					d.Add(1)
				}
			}(i)
		}
		close(start)
		wg.Wait()
		return a.Load(), b.Load(), c.Load(), d.Load()
	}
	ok, exists, _, other := run(40, func(int) string { return "same" }, 100)
	if ok != 1 || exists != 39 || other != 0 {
		t.Fatalf("同一旧码: ok=%d exists=%d other=%d", ok, exists, other)
	}
	ok, _, over, other := run(40, func(i int) string { return fmt.Sprintf("c%d", i) }, 6)
	if ok != 5 || over != 35 || other != 0 { // 额度 6，上面已占 1
		t.Fatalf("不同旧码: ok=%d over=%d other=%d", ok, over, other)
	}
}

func TestAgentExchangeReleaseAndFinishTransitions(t *testing.T) {
	dbtest.Open(t)
	now := time.Now()
	id, err := db.ReserveAgentCDKExchange(reservation("h1", "fp"), 20, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkAgentCDKExchangeUnknown(id, "超时"); err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseAgentCDKExchange(id); !errors.Is(err, db.ErrAgentExchangeState) {
		t.Fatalf("结果不明不能被自动释放: %v", err)
	}
	if st := db.AgentCDKExchangeState("h1"); st != db.AgentExchangeUnknown {
		t.Fatalf("状态: %q", st)
	}
	if err := db.FinishAgentCDKExchange(id, 900, "ZC-NEW"); err != nil {
		t.Fatalf("事后证实成功应能收尾: %v", err)
	}
	e, err := db.GetAgentExchange(id)
	if err != nil || e.State != db.AgentExchangeDisabling || e.NewUpstreamID != 900 || e.NewCodePrefix != "ZC-NEW" {
		t.Fatalf("新码已发出但旧码还没确认禁用，应停在 disabling: %+v %v", e, err)
	}
	if err := db.CompleteAgentCDKExchangeDisable(id); err != nil {
		t.Fatal(err)
	}
	if e, _ := db.GetAgentExchange(id); e.State != db.AgentExchangeDone {
		t.Fatalf("旧码确认禁用后才是 done: %+v", e)
	}
	if err := db.CompleteAgentCDKExchangeDisable(id); !errors.Is(err, db.ErrAgentExchangeState) {
		t.Fatalf("只有 disabling 能转 done: %v", err)
	}
	if err := db.MarkAgentCDKExchangeUnknown(id, "x"); !errors.Is(err, db.ErrAgentExchangeState) {
		t.Fatalf("done 不能退回 unknown: %v", err)
	}
	if err := db.ReleaseAgentCDKExchange(id); !errors.Is(err, db.ErrAgentExchangeState) {
		t.Fatalf("done 不能被释放: %v", err)
	}
}

func TestEscalateStaleAgentExchanges(t *testing.T) {
	dbtest.Open(t)
	now := time.Date(2026, 11, 3, 4, 0, 0, 0, time.UTC)
	stale, _ := db.ReserveAgentCDKExchange(reservation("old", "fp"), 20, now.Add(-11*time.Minute))
	if _, err := db.ReserveAgentCDKExchange(reservation("fresh", "fp"), 20, now.Add(-9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	doneID, _ := db.ReserveAgentCDKExchange(reservation("done", "fp"), 20, now.Add(-time.Hour))
	if err := db.FinishAgentCDKExchange(doneID, 1, "p"); err != nil {
		t.Fatal(err)
	}
	got, err := db.EscalateStaleAgentExchanges(now, 10*time.Minute)
	if err != nil || len(got) != 1 || got[0].ID != stale {
		t.Fatalf("%+v %v", got, err)
	}
	if st := db.AgentCDKExchangeState("old"); st != db.AgentExchangeUnknown {
		t.Fatalf("应升级: %q", st)
	}
	if st := db.AgentCDKExchangeState("fresh"); st != db.AgentExchangePending {
		t.Fatalf("没到时间不升级: %q", st)
	}
	if got, err := db.EscalateStaleAgentExchanges(now, 10*time.Minute); err != nil || len(got) != 0 {
		t.Fatalf("不应重复升级: %+v %v", got, err)
	}
}

func TestResolveAgentExchangeOnlyFromUnknown(t *testing.T) {
	dbtest.Open(t)
	now := time.Now()
	id, _ := db.ReserveAgentCDKExchange(reservation("h1", "fp"), 20, now)
	if err := db.ResolveAgentExchangeReleased(id); !errors.Is(err, db.ErrAgentExchangeState) {
		t.Fatalf("pending 不能人工释放: %v", err)
	}
	if err := db.ResolveAgentExchangeIssued(id, 9, "n"); !errors.Is(err, db.ErrAgentExchangeState) {
		t.Fatalf("pending 不能人工定成已发: %v", err)
	}
	if err := db.ResolveAgentExchangeReleased(999); !errors.Is(err, db.ErrAgentExchangeNotFound) {
		t.Fatalf("不存在: %v", err)
	}
	if err := db.MarkAgentCDKExchangeUnknown(id, "x"); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveAgentExchangeIssued(id, 9, "卡台已查到"); err != nil {
		t.Fatal(err)
	}
	if e, _ := db.GetAgentExchange(id); e.State != db.AgentExchangeDisabling || e.NewUpstreamID != 9 || e.Note != "卡台已查到" {
		t.Fatalf("%+v", e)
	}
	id2, _ := db.ReserveAgentCDKExchange(reservation("h2", "fp"), 20, now)
	_ = db.MarkAgentCDKExchangeUnknown(id2, "x")
	if err := db.ResolveAgentExchangeReleased(id2); err != nil {
		t.Fatal(err)
	}
	if st := db.AgentCDKExchangeState("h2"); st != "" {
		t.Fatalf("释放后应可重试: %q", st)
	}
}

// 旧库：没有新列、没有唯一约束，同一旧码有多行（历史上多发过码）。
func TestMigrateAgentExchangesDedupesLegacyRows(t *testing.T) {
	c, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	for _, s := range []string{
		`CREATE TABLE agent_cdk_exchanges (
			id INTEGER PRIMARY KEY AUTOINCREMENT, old_code_hash TEXT NOT NULL, old_upstream_id INTEGER, new_upstream_id INTEGER,
			old_code_prefix TEXT, new_code_prefix TEXT, plan TEXT, order_id INTEGER, order_status TEXT, ip TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE admin_audit_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT, action TEXT, detail TEXT, ip TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		// id 大的反而更早：要按时间保留，不是按 id
		`INSERT INTO agent_cdk_exchanges (id, old_code_hash, old_upstream_id, new_upstream_id, order_id, ip, created_at) VALUES (1, 'dup', 77, 901, 5, '1.1.1.1', '2026-10-02 00:00:00')`,
		`INSERT INTO agent_cdk_exchanges (id, old_code_hash, old_upstream_id, new_upstream_id, order_id, ip, created_at) VALUES (2, 'dup', 77, 902, 5, '2.2.2.2', '2026-10-01 00:00:00')`,
		`INSERT INTO agent_cdk_exchanges (id, old_code_hash, old_upstream_id, new_upstream_id, order_id, ip, created_at) VALUES (3, 'dup', 77, 903, 5, '3.3.3.3', '2026-10-03 00:00:00')`,
		`INSERT INTO agent_cdk_exchanges (id, old_code_hash, old_upstream_id, new_upstream_id, created_at) VALUES (4, 'single', 78, 904, '2026-10-01 00:00:00')`,
	} {
		if _, err := c.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ { // 重跑幂等
		if err := db.MigrateAgentExchanges(); err != nil {
			t.Fatalf("第 %d 次迁移: %v", i+1, err)
		}
	}

	rows, err := c.Query(`SELECT id, state FROM agent_cdk_exchanges ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for rows.Next() {
		var id int
		var st string
		if err := rows.Scan(&id, &st); err != nil {
			t.Fatal(err)
		}
		kept = append(kept, fmt.Sprintf("%d:%s", id, st))
	}
	rows.Close()
	if strings.Join(kept, ",") != "2:done,4:done" {
		t.Fatalf("应保留每张旧码最早一行且状态为 done: %v", kept)
	}

	// 被删的行完整归档
	var n int
	if err := c.QueryRow(`SELECT COUNT(*) FROM agent_cdk_exchange_dup_archive WHERE kept_id = 2 AND old_code_hash = 'dup' AND new_upstream_id IN (901, 903)`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("归档行数: %d %v", n, err)
	}
	var ip string
	if err := c.QueryRow(`SELECT ip FROM agent_cdk_exchange_dup_archive WHERE id = 3`).Scan(&ip); err != nil || ip != "3.3.3.3" {
		t.Fatalf("归档的审计信息: %q %v", ip, err)
	}
	// 审计里能看到被删的新码，重跑不重复写
	var detail string
	if err := c.QueryRow(`SELECT detail FROM admin_audit_logs WHERE action = 'agent_exchange_dedupe' AND detail LIKE '%removed_id=1 %'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "kept_id=2") || !strings.Contains(detail, "new_cdk=901") || !strings.Contains(detail, "1.1.1.1") {
		t.Fatalf("审计内容: %s", detail)
	}
	if err := c.QueryRow(`SELECT COUNT(*) FROM admin_audit_logs WHERE action = 'agent_exchange_dedupe'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("审计行数: %d %v", n, err)
	}
	// 唯一约束生效
	if _, err := c.Exec(`INSERT INTO agent_cdk_exchanges (old_code_hash) VALUES ('dup')`); err == nil {
		t.Fatal("唯一约束应拒绝重复")
	}
}

// 事务中途失败不能丢数据：归档失败时不删行、不建索引，修好后重跑成功。
func TestMigrateAgentExchangesRollsBackOnFailure(t *testing.T) {
	dbtest.Open(t)
	if _, err := db.DB.Exec(`DROP INDEX uq_agent_ex_old_hash`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{1, 2} {
		if _, err := db.DB.Exec(`INSERT INTO agent_cdk_exchanges (id, old_code_hash, new_upstream_id, created_at) VALUES (?, 'dup', ?, '2026-10-01 00:00:00')`, id, 900+id); err != nil {
			t.Fatal(err)
		}
	}
	// 让写审计失败：把审计表藏起来
	if _, err := db.DB.Exec(`ALTER TABLE admin_audit_logs RENAME TO admin_audit_logs_hidden`); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateAgentExchanges(); err == nil {
		t.Fatal("审计写不了应报错")
	}
	var rowsLeft, archived, idx int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM agent_cdk_exchanges`).Scan(&rowsLeft)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM agent_cdk_exchange_dup_archive`).Scan(&archived)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'uq_agent_ex_old_hash'`).Scan(&idx)
	if rowsLeft != 2 || archived != 0 || idx != 0 {
		t.Fatalf("应整体回滚: rows=%d archived=%d idx=%d", rowsLeft, archived, idx)
	}
	if _, err := db.DB.Exec(`ALTER TABLE admin_audit_logs_hidden RENAME TO admin_audit_logs`); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateAgentExchanges(); err != nil {
		t.Fatal(err)
	}
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM agent_cdk_exchanges`).Scan(&rowsLeft)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM agent_cdk_exchange_dup_archive`).Scan(&archived)
	if rowsLeft != 1 || archived != 1 {
		t.Fatalf("重跑后: rows=%d archived=%d", rowsLeft, archived)
	}
}

func TestGetAgentExchangeByOldHash(t *testing.T) {
	dbtest.Open(t)
	if _, err := db.GetAgentExchangeByOldHash("nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("没有记录应返回 sql.ErrNoRows: %v", err)
	}
	id, err := db.ReserveAgentCDKExchange(reservation("h-old", "fp"), 20, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.FinishAgentCDKExchange(id, 900, "ZC-NEW"); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteAgentCDKExchangeDisable(id); err != nil {
		t.Fatal(err)
	}
	e, err := db.GetAgentExchangeByOldHash("h-old")
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != id || e.State != db.AgentExchangeDone || e.NewUpstreamID != 900 || e.NewCodePrefix != "ZC-NEW" {
		t.Fatalf("%+v", e)
	}
}

func TestCardplatformCDKCodeByUpstreamID(t *testing.T) {
	dbtest.Open(t)
	if _, err := db.DB.Exec(`INSERT INTO cardplatform_cdk_codes (upstream_id, code, code_prefix) VALUES (7, 'ZC-FULL-7', 'ZC-FU')`); err != nil {
		t.Fatal(err)
	}
	code, err := db.CardplatformCDKCodeByUpstreamID(7)
	if err != nil || code != "ZC-FULL-7" {
		t.Fatalf("%q %v", code, err)
	}
	// 同前缀但 upstream_id 不同：严格函数不能退回前缀匹配
	if _, err := db.CardplatformCDKCodeByUpstreamID(8); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("没有该 upstream_id 应返回 sql.ErrNoRows: %v", err)
	}
}
