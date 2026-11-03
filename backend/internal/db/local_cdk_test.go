package db_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

var localCtx = context.Background()

func newLocalCDK(i int) db.NewLocalCDK {
	return db.NewLocalCDK{Provider: "marswallex", CodeHash: fmt.Sprintf("code-hash-%d", i), TokenHash: fmt.Sprintf("token-hash-%d", i),
		CodePrefix: "MW-AAAAA", Product: "gpt", PlanKey: "plus", Region: "PH", IssuedBy: "alice"}
}

func insertLocalCDK(t *testing.T, i int) db.LocalCDK {
	t.Helper()
	ids, err := db.InsertLocalCDKs(localCtx, []db.NewLocalCDK{newLocalCDK(i)}, time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.GetLocalCDK(localCtx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var orderSeq int

// addOrder 直接写一张付款单。email 留空时按序号生成，避开同邮箱在途索引。
func addOrder(t *testing.T, source, ref string, dry int, email, state, failure string, submitted bool) string {
	t.Helper()
	orderSeq++
	no := fmt.Sprintf("PO-TEST-%d", orderSeq)
	if email == "" {
		email = fmt.Sprintf("u%d@example.com", orderSeq)
	}
	var sub any
	if submitted {
		sub = int64(1234)
	}
	if _, err := db.DB.Exec(`INSERT INTO pay_orders (order_no, source, source_ref, dry_run, plan_key, region, account_email, state, failure, submitted_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'plus', 'PH', ?, ?, ?, ?, 1, 1)`, no, source, ref, dry, email, state, failure, sub); err != nil {
		t.Fatalf("写入付款单 %s/%s: %v", state, failure, err)
	}
	return no
}

func viewOf(t *testing.T, c db.LocalCDK) db.LocalCDKView {
	t.Helper()
	c2, err := db.GetLocalCDK(localCtx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	v, err := db.LocalCDKViewOf(localCtx, c2)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// PR-6：码状态从 pay_orders 派生，不回写。
func TestLocalCDKStatusDerivedFromPayOrder(t *testing.T) {
	dbtest.Open(t)
	cases := []struct {
		name        string
		state, fail string
		submitted   bool
		status, why string
	}{
		{"排队", "queued", "", false, db.LocalCDKReserved, ""},
		{"检查中", "checking", "", false, db.LocalCDKReserved, ""},
		{"注资中", "funding", "", false, db.LocalCDKReserved, ""},
		{"已提交", "submitted", "", true, db.LocalCDKReserved, ""},
		{"确认中", "verifying", "", true, db.LocalCDKReserved, ""},
		{"成功", "succeeded", "", true, db.LocalCDKConsumed, ""},
		{"待人工", "needs_review", "", true, db.LocalCDKReserved, db.LocalReasonNeedsReview},
		{"被拒", "failed", "declined", true, db.LocalCDKFailed, db.LocalReasonDeclined},
		{"干净失败", "failed", "not_submitted", false, db.LocalCDKUnused, ""},
		// 点过支付的单不管 failure 写的什么，都不能再兑
		{"提交后却记未提交", "failed", "not_submitted", true, db.LocalCDKFailed, db.LocalReasonDirtyOrder},
		{"failure 为空", "failed", "", false, db.LocalCDKFailed, db.LocalReasonDirtyOrder},
	}
	for i, tc := range cases {
		c := insertLocalCDK(t, i)
		addOrder(t, "cdk", db.LocalCDKSourceRef(c.ID), 0, "", tc.state, tc.fail, tc.submitted)
		v := viewOf(t, c)
		if v.Status != tc.status || v.Reason != tc.why {
			t.Errorf("%s: 得到 %s/%s，期望 %s/%s", tc.name, v.Status, v.Reason, tc.status, tc.why)
		}
	}
	// 没有单
	if v := viewOf(t, insertLocalCDK(t, 100)); v.Status != db.LocalCDKUnused || v.AttemptsLeft != 4 || v.Latest != nil {
		t.Fatalf("无单: %+v", v)
	}
	// 人工定论失败（引擎写成 failed/declined）后是 failed，不是 unused
	c := insertLocalCDK(t, 101)
	no := addOrder(t, "cdk", db.LocalCDKSourceRef(c.ID), 0, "", "needs_review", "", true)
	if v := viewOf(t, c); v.Status != db.LocalCDKReserved || !v.NeedsReview {
		t.Fatalf("定论前: %+v", v)
	}
	if _, err := db.DB.Exec(`UPDATE pay_orders SET state = 'failed', failure = 'declined' WHERE order_no = ?`, no); err != nil {
		t.Fatal(err)
	}
	if v := viewOf(t, c); v.Status != db.LocalCDKFailed || v.Reason != db.LocalReasonDeclined {
		t.Fatalf("定论失败后: %+v", v)
	}
	// 试运行单、其他来源的单都不影响码的状态
	c = insertLocalCDK(t, 102)
	addOrder(t, "cdk", db.LocalCDKSourceRef(c.ID), 1, "", "succeeded", "", false)
	addOrder(t, "cdk", db.LocalCDKSourceRef(c.ID), 2, "", "succeeded", "", false)
	addOrder(t, "admin_test", db.LocalCDKSourceRef(c.ID), 0, "", "succeeded", "", false)
	if v := viewOf(t, c); v.Status != db.LocalCDKUnused {
		t.Fatalf("试运行和其他来源不应影响码: %+v", v)
	}
}

// 重兑上限：首次 1 单 + 重兑 3 次，第 4 张还是干净失败就转人工；换码继承的次数算在内。
func TestLocalCDKAttemptsExhausted(t *testing.T) {
	dbtest.Open(t)
	c := insertLocalCDK(t, 1)
	ref := db.LocalCDKSourceRef(c.ID)
	for i, left := range []int{3, 2, 1} {
		addOrder(t, "cdk", ref, 0, "", "failed", "not_submitted", false)
		if v := viewOf(t, c); v.Status != db.LocalCDKUnused || v.AttemptsLeft != left {
			t.Fatalf("第 %d 次干净失败: %+v", i+1, v)
		}
	}
	addOrder(t, "cdk", ref, 0, "", "failed", "not_submitted", false)
	if v := viewOf(t, c); v.Status != db.LocalCDKFailed || v.Reason != db.LocalReasonAttemptsExhausted || v.AttemptsLeft != 0 {
		t.Fatalf("次数用完: %+v", v)
	}

	carried := insertLocalCDK(t, 2)
	carried.AttemptsCarried = 3
	if v := db.DeriveLocalCDK(carried, nil); v.AttemptsLeft != 1 || v.Status != db.LocalCDKUnused {
		t.Fatalf("继承 3 次: %+v", v)
	}
	carried.AttemptsCarried = 4
	if v := db.DeriveLocalCDK(carried, nil); v.Status != db.LocalCDKFailed {
		t.Fatalf("继承 4 次: %+v", v)
	}
}

// 一张码最多一张不是干净失败的单：库级保证，处理器里的检查和锁都绕不过。
func TestLocalCDKOneOrderIndex(t *testing.T) {
	dbtest.Open(t)
	c := insertLocalCDK(t, 1)
	ref := db.LocalCDKSourceRef(c.ID)
	// 干净失败可以有多张
	addOrder(t, "cdk", ref, 0, "", "failed", "not_submitted", false)
	addOrder(t, "cdk", ref, 0, "", "failed", "not_submitted", false)
	first := addOrder(t, "cdk", ref, 0, "", "queued", "", false)
	// 第二张在途单被库拒绝，就算邮箱不同
	_, err := db.DB.Exec(`INSERT INTO pay_orders (order_no, source, source_ref, dry_run, plan_key, region, account_email, state, created_at, updated_at)
		VALUES ('PO-DUP', 'cdk', ?, 0, 'plus', 'PH', 'other@example.com', 'queued', 1, 1)`, ref)
	if !db.IsLocalCDKOrderConflict(err) {
		t.Fatalf("第二张在途单应撞索引: %v", err)
	}
	for _, st := range []struct{ state, failure string }{{"succeeded", ""}, {"needs_review", ""}, {"failed", "declined"}} {
		if _, err := db.DB.Exec(`UPDATE pay_orders SET state = ?, failure = ? WHERE order_no = ?`, st.state, st.failure, first); err != nil {
			t.Fatal(err)
		}
		_, err := db.DB.Exec(`INSERT INTO pay_orders (order_no, source, source_ref, dry_run, plan_key, region, account_email, state, created_at, updated_at)
			VALUES ('PO-DUP2', 'cdk', ?, 0, 'plus', 'PH', 'other@example.com', 'queued', 1, 1)`, ref)
		if !db.IsLocalCDKOrderConflict(err) {
			t.Fatalf("已有 %s 单时应撞索引: %v", st.state, err)
		}
	}
	// 试运行单、其他来源的单不受影响
	addOrder(t, "cdk", ref, 1, "", "queued", "", false)
	addOrder(t, "admin_test", ref, 0, "", "queued", "", false)
	// 其他错误不会被当成这个冲突
	_, err = db.DB.Exec(`INSERT INTO pay_orders (order_no, state, created_at, updated_at) VALUES (?, 'queued', 1, 1)`, first)
	if err == nil || db.IsLocalCDKOrderConflict(err) {
		t.Fatalf("单号冲突不应识别成码冲突: %v", err)
	}
}

func TestHasRealSucceededPayOrderGate(t *testing.T) {
	dbtest.Open(t)
	check := func(want bool) {
		t.Helper()
		got, err := db.HasRealSucceededPayOrder(localCtx)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("门槛 %v，期望 %v", got, want)
		}
	}
	check(false)
	addOrder(t, "admin_test", "admin", 1, "", "succeeded", "", false) // 试运行到读金额
	addOrder(t, "admin_test", "admin", 2, "", "succeeded", "", false) // 试运行到填表
	addOrder(t, "admin_test", "admin", 1, "", "previewed", "", false)
	addOrder(t, "admin_test", "admin", 0, "", "failed", "declined", true)
	addOrder(t, "admin_test", "admin", 0, "", "needs_review", "", true)
	check(false)
	addOrder(t, "admin_test", "admin", 0, "", "succeeded", "", true)
	check(true)
}

func TestLocalCDKDisableEnableAndExchange(t *testing.T) {
	dbtest.Open(t)
	now := time.Unix(2000, 0)
	c := insertLocalCDK(t, 1)
	if changed, err := db.DisableLocalCDK(localCtx, c.ID, now); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if v := viewOf(t, c); v.Status != db.LocalCDKDisabled || v.Reason != db.LocalReasonManual {
		t.Fatalf("%+v", v)
	}
	if changed, err := db.DisableLocalCDK(localCtx, c.ID, now); err != nil || changed {
		t.Fatal("重复禁用不算改动", changed, err)
	}
	if changed, err := db.EnableLocalCDK(localCtx, c.ID, now); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if v := viewOf(t, c); v.Status != db.LocalCDKUnused {
		t.Fatalf("%+v", v)
	}
	if _, err := db.EnableLocalCDK(localCtx, 99999, now); !errors.Is(err, db.ErrLocalCDKNotFound) {
		t.Fatal(err)
	}

	// 换码：旧码禁用、新码记来源，旧码不能再启用
	addOrder(t, "cdk", db.LocalCDKSourceRef(c.ID), 0, "", "failed", "not_submitted", false)
	n := newLocalCDK(2)
	n.ExchangedFrom, n.AttemptsCarried = &c.ID, 1
	newID, err := db.ExchangeLocalCDK(localCtx, c.ID, n, now)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := db.GetLocalCDK(localCtx, c.ID)
	if old.Manual != db.LocalCDKManualDisabled || old.ReplacedBy == nil || *old.ReplacedBy != newID {
		t.Fatalf("旧码: %+v", old)
	}
	if v := viewOf(t, c); v.Status != db.LocalCDKDisabled || v.Reason != db.LocalReasonExchanged {
		t.Fatalf("%+v", v)
	}
	if _, err := db.EnableLocalCDK(localCtx, c.ID, now); !errors.Is(err, db.ErrLocalCDKReplaced) {
		t.Fatalf("被替代的旧码不能启用: %v", err)
	}
	fresh, _ := db.GetLocalCDK(localCtx, newID)
	if fresh.AttemptsCarried != 1 || fresh.ExchangedFrom == nil || *fresh.ExchangedFrom != c.ID {
		t.Fatalf("新码: %+v", fresh)
	}

	// 已有不干净的单不能换，事务回滚，新码不落库
	c3 := insertLocalCDK(t, 3)
	addOrder(t, "cdk", db.LocalCDKSourceRef(c3.ID), 0, "", "failed", "declined", true)
	if _, err := db.ExchangeLocalCDK(localCtx, c3.ID, newLocalCDK(4), now); !errors.Is(err, db.ErrLocalCDKNotExchangeable) {
		t.Fatal(err)
	}
	if v := viewOf(t, c3); v.Status != db.LocalCDKFailed {
		t.Fatalf("%+v", v)
	}
	var n4 int
	db.DB.QueryRow(`SELECT COUNT(*) FROM local_cdks WHERE code_hash = 'code-hash-4'`).Scan(&n4)
	if n4 != 0 {
		t.Fatal("回滚后不应有新码")
	}
}

func TestLoadLocalCDKRowsAndOpenCheck(t *testing.T) {
	dbtest.Open(t)
	open := func(want bool) {
		t.Helper()
		got, err := db.HasOpenLocalCDKs(localCtx, "marswallex")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("未结清 %v，期望 %v", got, want)
		}
	}
	open(false)
	a := insertLocalCDK(t, 1)
	b := insertLocalCDK(t, 2)
	open(true)
	addOrder(t, "cdk", db.LocalCDKSourceRef(a.ID), 0, "", "succeeded", "", true)
	if changed, err := db.DisableLocalCDK(localCtx, b.ID, time.Unix(1, 0)); err != nil || !changed {
		t.Fatal(changed, err)
	}
	open(false)
	rows, err := db.LoadLocalCDKRows(localCtx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != b.ID || rows[0].View.Status != db.LocalCDKDisabled || rows[1].View.Status != db.LocalCDKConsumed {
		t.Fatalf("%+v", rows)
	}
	// 在途单让禁用的码也算未结清
	addOrder(t, "cdk", db.LocalCDKSourceRef(b.ID), 0, "", "queued", "", false)
	open(true)
}

func TestHasOpenPayOrderForEmail(t *testing.T) {
	dbtest.Open(t)
	for _, tc := range []struct {
		state string
		dry   int
		want  bool
	}{{"queued", 0, true}, {"needs_review", 0, true}, {"succeeded", 0, false}, {"failed", 0, false}, {"queued", 1, false}} {
		email := fmt.Sprintf("A%s%d@Example.com", tc.state, tc.dry)
		addOrder(t, "cdk", "1"+email, tc.dry, email, tc.state, "", false)
		got, err := db.HasOpenPayOrderForEmail(localCtx, "  "+strings.ToLower(email)+" ")
		if err != nil || got != tc.want {
			t.Errorf("%s dry=%d: %v %v", tc.state, tc.dry, got, err)
		}
	}
	if got, err := db.HasOpenPayOrderForEmail(localCtx, ""); err != nil || got {
		t.Fatal(got, err)
	}
}

func TestRecentCDKBindingForEmail(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	sess := `{"user":{"email":"Mixed.Case@Example.com"},"sessionToken":"tok-1"}`
	if err := db.BindCDKSession("ZC-AAA", "tok-a", sess); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-15 * time.Minute)
	check := func(email string, since time.Time, want bool) {
		t.Helper()
		got, err := db.HasRecentCDKBindingForEmail(localCtx, email, since)
		if err != nil || got != want {
			t.Fatalf("%s: %v %v，期望 %v", email, got, err, want)
		}
	}
	check("mixed.case@example.com", since, true)
	check("other@example.com", since, false)
	check("", since, false)
	check("mixed.case@example.com", time.Now().Add(time.Hour), false) // 窗外

	// 会话清空后邮箱还在，互斥仍然有效
	if err := db.ClearCDKSessionByCode("ZC-AAA"); err != nil {
		t.Fatal(err)
	}
	check("mixed.case@example.com", since, true)

	// 只有令牌的占位行也能按令牌取邮箱
	if err := db.BindCDKSession("", "tok-b", `{"user":{"email":"b@example.com"},"sessionToken":"x"}`); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetAccountEmailByRedemptionToken("tok-b"); err != nil || got != "b@example.com" {
		t.Fatal(got, err)
	}
	if got, err := db.GetAccountEmailByRedemptionToken("nope"); err != nil || got != "" {
		t.Fatal(got, err)
	}

	// 密钥换了解不开：返回错误让调用方拒绝，不能当成「没有绑定」放行
	t.Setenv("APP_MASTER_KEY", "OTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTBmZWRjYmE=")
	if _, err := db.HasRecentCDKBindingForEmail(localCtx, "mixed.case@example.com", since); err == nil {
		t.Fatal("解不开时应报错")
	}
}

// recharge_tasks.session_json 清空后，数据库文件里连空闲页都不能留旧明文。
func TestMigrateClearsRechargeTaskSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	c, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	const secret = "PLAINTEXT-SESSION-TOKEN-MARKER"
	if _, err := c.Exec(`INSERT INTO cd_keys (code, plan_type, status) VALUES ('K1', 'plus', 'active')`); err != nil {
		t.Fatal(err)
	}
	for i, sess := range []any{`{"sessionToken":"` + secret + `"}`, nil, ""} {
		if _, err := c.Exec(`INSERT INTO recharge_tasks (task_id, cdk_code, session_json, account_email, notes) VALUES (?, 'K1', ?, 'keep@example.com', 'keep')`, fmt.Sprintf("t%d", i), sess); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	var left int
	c.QueryRow(`SELECT COUNT(*) FROM recharge_tasks WHERE COALESCE(session_json, '') != ''`).Scan(&left)
	if left != 0 {
		t.Fatalf("还有 %d 行没清", left)
	}
	var kept int
	c.QueryRow(`SELECT COUNT(*) FROM recharge_tasks WHERE account_email = 'keep@example.com' AND notes = 'keep'`).Scan(&kept)
	if kept != 3 {
		t.Fatalf("其余列应保留: %d", kept)
	}
	if v, _ := db.GetSetting("cdk_session_vacuum_pending"); v != "" {
		t.Fatalf("VACUUM 标记应已清除: %q", v)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("数据库文件里还有旧明文")
	}
	// 重复执行不出错
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
}
