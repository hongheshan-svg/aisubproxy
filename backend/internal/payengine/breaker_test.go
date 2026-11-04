package payengine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// bo 是一张用来喂给熔断的历史单据。
type bo struct {
	state, failure, code, resolvedBy string
	submitted, dryRun                bool
}

var (
	boDeclined = bo{state: StateFailed, failure: FailureDeclined, submitted: true}
	boReview   = bo{state: StateNeedsReview, submitted: true}
	boSuccess  = bo{state: StateSucceeded, submitted: true}
	boFill     = bo{state: StateFailed, failure: FailureNotSubmitted, code: "fill_failed"}
	boSens     = bo{state: StateFailed, failure: FailureNotSubmitted, code: "card_sensitive_failed"}
	boExpiry   = bo{state: StateFailed, failure: FailureNotSubmitted, code: "card_expiry_invalid"}
	boNoClick  = bo{state: StateFailed, failure: FailureNotSubmitted, code: "submit_not_clicked"}
	boSession  = bo{state: StateFailed, failure: FailureNotSubmitted, code: "session_invalid"}
	boFund     = bo{state: StateFailed, failure: FailureNotSubmitted, code: "fund_failed"}
	boSubs     = bo{state: StateFailed, failure: FailureNotSubmitted, code: "already_subscribed"}
)

var boSeq int

// insertBO 写一张已结束的单据，finished_at 递增（基于 base）。返回单号。
func insertBO(t *testing.T, b bo, finishedAt int64) string {
	t.Helper()
	boSeq++
	no := fmt.Sprintf("PO%012d%08x", boSeq, boSeq)
	var submitted any
	if b.submitted {
		submitted = finishedAt - 1
	}
	dry := 0
	if b.dryRun {
		dry = 1
	}
	if _, err := db.DB.Exec(`INSERT INTO pay_orders (order_no, dry_run, account_email, state, failure, error_code, resolved_by,
		submitted_at, created_at, updated_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		no, dry, no+"@example.com", b.state, b.failure, b.code, b.resolvedBy, submitted, finishedAt-10, finishedAt, finishedAt); err != nil {
		t.Fatal(err)
	}
	return no
}

// feedBO 依次写入，结束时间按序递增；返回单号。
func feedBO(t *testing.T, base int64, list ...bo) []string {
	t.Helper()
	nos := make([]string, len(list))
	for i, b := range list {
		nos[i] = insertBO(t, b, base+int64(i))
	}
	return nos
}

func breakerNow(t *testing.T) BreakerStatus {
	t.Helper()
	st, err := computeBreaker(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestBreakerTrips(t *testing.T) {
	base := time.Now().Unix() - 1000
	cases := []struct {
		name       string
		list       []bo
		paused     bool
		reason     string
		submit     int
		fill       int
		trippedIdx int
	}{
		{"没有单据", nil, false, "", 0, 0, 0},
		{"连续 2 单被拒不触发", []bo{boDeclined, boDeclined}, false, "", 2, 0, 0},
		{"连续 3 单被拒", []bo{boDeclined, boDeclined, boDeclined}, true, BreakerReasonSubmit, 3, 0, 2},
		{"连续 3 单待人工", []bo{boReview, boReview, boReview}, true, BreakerReasonSubmit, 3, 0, 2},
		{"被拒和待人工混合", []bo{boDeclined, boReview, boDeclined}, true, BreakerReasonSubmit, 3, 0, 2},
		{"成功清零提交后失败", []bo{boDeclined, boDeclined, boSuccess, boDeclined, boDeclined}, false, "", 2, 0, 0},
		{"连续 3 单填表失败", []bo{boFill, boFill, boFill}, true, BreakerReasonFill, 0, 3, 2},
		{"三种填表失败码都算", []bo{boFill, boSens, boExpiry}, true, BreakerReasonFill, 0, 3, 2},
		{"连续找不到支付按钮也算", []bo{boNoClick, boNoClick, boNoClick}, true, BreakerReasonFill, 0, 3, 2},
		{"注资后金额变化也算", []bo{{state: StateFailed, failure: FailureNotSubmitted, code: "amount_changed"}, {state: StateFailed, failure: FailureNotSubmitted, code: "amount_changed"}, {state: StateFailed, failure: FailureNotSubmitted, code: "amount_changed"}}, true, BreakerReasonFill, 0, 3, 2},
		{"打开页面阶段的金额问题没花钱不计入", []bo{{state: StateFailed, failure: FailureNotSubmitted, code: "amount_over_cap"}, {state: StateFailed, failure: FailureNotSubmitted, code: "amount_unreadable"}, {state: StateFailed, failure: FailureNotSubmitted, code: "amount_over_cap"}}, false, "", 0, 0, 0},
		{"试运行填表失败不计入", []bo{{state: StateFailed, failure: FailureNotSubmitted, code: "dry_run_fill_failed", dryRun: true}, {state: StateFailed, failure: FailureNotSubmitted, code: "dry_run_fill_failed", dryRun: true}, {state: StateFailed, failure: FailureNotSubmitted, code: "dry_run_fill_failed", dryRun: true}}, false, "", 0, 0, 0},
		{"提交过的单清零填表失败", []bo{boFill, boFill, boSuccess, boFill}, false, "", 0, 1, 0},
		{"提交后失败也清零填表失败", []bo{boFill, boFill, boDeclined, boFill}, false, "", 1, 1, 0},
		{"填表失败不清零提交后失败", []bo{boDeclined, boDeclined, boFill, boDeclined}, true, BreakerReasonSubmit, 3, 0, 3},
		{"无关失败对两条都不算", []bo{boSession, boFund, boSubs, boSession, boFund, boSubs}, false, "", 0, 0, 0},
		{"无关失败不打断连续", []bo{boDeclined, boSession, boDeclined, boFund, boDeclined}, true, BreakerReasonSubmit, 3, 0, 4},
		{"无关失败不打断填表连续", []bo{boFill, boSession, boFill, boFund, boFill}, true, BreakerReasonFill, 0, 3, 4},
		{"试运行不计入", []bo{boDeclined, boDeclined, {state: StateFailed, failure: FailureDeclined, submitted: true, dryRun: true}, {state: StatePreviewed, dryRun: true}, {state: StateFailed, failure: FailureNotSubmitted, code: "fill_failed", dryRun: true}}, false, "", 2, 0, 0},
		{"人工定成成功的仍算失败", []bo{boDeclined, boDeclined, {state: StateSucceeded, submitted: true, resolvedBy: "admin"}}, true, BreakerReasonSubmit, 3, 0, 2},
		{"人工定成失败的算失败", []bo{boDeclined, boDeclined, {state: StateFailed, failure: FailureDeclined, submitted: true, resolvedBy: "admin"}}, true, BreakerReasonSubmit, 3, 0, 2},
		{"暂停后在途单成功不解除", []bo{boDeclined, boDeclined, boDeclined, boSuccess}, true, BreakerReasonSubmit, 0, 0, 2},
		{"先触发的原因优先", []bo{boFill, boFill, boFill, boDeclined, boDeclined, boDeclined}, true, BreakerReasonFill, 3, 0, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dbtest.Open(t)
			nos := feedBO(t, base, c.list...)
			st := breakerNow(t)
			if st.Paused != c.paused || st.Reason != c.reason || st.SubmittedFailureStreak != c.submit || st.FillFailureStreak != c.fill {
				t.Fatalf("%+v", st)
			}
			if c.paused {
				if st.TrippedAt == nil || *st.TrippedAt != base+int64(c.trippedIdx) {
					t.Fatalf("触发时刻: %+v", st.TrippedAt)
				}
				if len(st.OrderNos) != 3 || st.OrderNos[2] != nos[c.trippedIdx] || st.ReasonText == "" {
					t.Fatalf("单号: %+v", st)
				}
			}
		})
	}
}

// 没结束的单不算：在途单还没结论。
func TestBreakerIgnoresInflightOrders(t *testing.T) {
	dbtest.Open(t)
	base := time.Now().Unix() - 100
	feedBO(t, base, boDeclined, boDeclined)
	if _, err := db.DB.Exec(`INSERT INTO pay_orders (order_no, account_email, state, submitted_at, created_at, updated_at) VALUES ('POX', 'x@example.com', ?, ?, ?, ?)`,
		StateVerifying, base, base, base); err != nil {
		t.Fatal(err)
	}
	if st := breakerNow(t); st.Paused {
		t.Fatalf("在途单不应计入: %+v", st)
	}
}

func TestBreakerResumeRestartsCounting(t *testing.T) {
	dbtest.Open(t)
	alerts := &alertLog{}
	s := New(Options{Alert: alerts.add, ShotDir: t.TempDir()})
	bg := context.Background()
	feedBO(t, time.Now().Unix()-100, boDeclined, boDeclined, boDeclined)
	if !breakerNow(t).Paused {
		t.Fatal("应暂停")
	}
	st, err := s.ResumeBreaker(bg, "admin1", "已排查")
	if err != nil {
		t.Fatal(err)
	}
	if st.Paused || st.SubmittedFailureStreak != 0 || st.LastResumeBy != "admin1" || st.LastResumeAt == nil {
		t.Fatalf("恢复后: %+v", st)
	}
	// 再恢复一次：没暂停
	if _, err := s.ResumeBreaker(bg, "admin1", "x"); !errors.Is(err, ErrBreakerNotPaused) {
		t.Fatalf("应报告没暂停: %v", err)
	}
	// 恢复之后重新计数，要再连续 3 单才触发
	now := time.Now().Unix()
	feedBO(t, now+1, boDeclined, boDeclined)
	if st := breakerNow(t); st.Paused || st.SubmittedFailureStreak != 2 {
		t.Fatalf("恢复后 2 单不应触发: %+v", st)
	}
	insertBO(t, boDeclined, now+3)
	if st := breakerNow(t); !st.Paused || st.Reason != BreakerReasonSubmit {
		t.Fatalf("恢复后再连续 3 单应触发: %+v", st)
	}
}

// finished_at 只精确到秒：恢复那一秒已结束的单不算，恢复之后同一秒内结束的单要算。
func TestBreakerResumeSameSecond(t *testing.T) {
	dbtest.Open(t)
	// 直接写恢复点模拟：恢复时刻 at，at 这一秒已有 3 单失败
	at := time.Now().Unix() + 3600
	old := feedBO(t, at, boDeclined)
	old = append(old, insertBO(t, boDeclined, at), insertBO(t, boDeclined, at))
	if err := db.SetSetting(settingBreakerReset, fmt.Sprintf(`{"at":%d,"skip":["%s","%s","%s"],"by":"a"}`, at, old[0], old[1], old[2])); err != nil {
		t.Fatal(err)
	}
	if st := breakerNow(t); st.Paused || st.SubmittedFailureStreak != 0 {
		t.Fatalf("恢复那一秒已结束的单不应计入: %+v", st)
	}
	insertBO(t, boDeclined, at)
	insertBO(t, boDeclined, at)
	if st := breakerNow(t); st.SubmittedFailureStreak != 2 {
		t.Fatalf("恢复后同一秒结束的单要计入: %+v", st)
	}
	insertBO(t, boDeclined, at)
	if st := breakerNow(t); !st.Paused {
		t.Fatalf("应触发: %+v", st)
	}
}

// ResumeBreaker 把恢复那一秒已结束的单写进排除列表。为了不依赖恰好落在哪一秒，接下来 10 秒每秒都放 3 单。
func TestBreakerResumeRecordsSameSecondOrders(t *testing.T) {
	dbtest.Open(t)
	s := New(Options{Alert: func(string) {}, ShotDir: t.TempDir()})
	now := time.Now().Unix()
	finishedAt := map[string]int64{}
	for sec := int64(0); sec < 10; sec++ {
		for i := 0; i < 3; i++ {
			finishedAt[insertBO(t, boDeclined, now+sec)] = now + sec
		}
	}
	if _, err := s.ResumeBreaker(context.Background(), "admin1", "n"); err != nil {
		t.Fatal(err)
	}
	r, err := loadBreakerReset()
	if err != nil {
		t.Fatal(err)
	}
	if r.Note != "n" || r.By != "admin1" || r.At < now {
		t.Fatalf("恢复点: %+v", r)
	}
	if len(r.Skip) != 3 {
		t.Fatalf("恢复那一秒的 3 单都应被排除: %+v", r)
	}
	for _, no := range r.Skip {
		if finishedAt[no] != r.At {
			t.Fatalf("排除了不是恢复那一秒的单: %s", no)
		}
	}
}

func TestBreakerFailsClosedOnCorruptReset(t *testing.T) {
	dbtest.Open(t)
	alerts := &alertLog{}
	s := New(Options{Alert: alerts.add, ShotDir: t.TempDir()})
	bg := context.Background()
	if err := db.SetSetting(settingBreakerReset, `{broken`); err != nil {
		t.Fatal(err)
	}
	if !s.breakerPaused(bg) {
		t.Fatal("状态查不出来时应按暂停处理")
	}
	s.breakerPaused(bg)
	if len(alerts.Texts()) != 1 {
		t.Fatalf("应告警一次: %v", alerts.Texts())
	}
	if _, err := s.BreakerStatus(bg); err == nil {
		t.Fatal("状态接口应如实报错")
	}
	// 损坏时也能恢复，否则解不开
	st, err := s.ResumeBreaker(bg, "admin1", "修复")
	if err != nil || st.Paused {
		t.Fatalf("应能恢复: %+v %v", st, err)
	}
	if s.breakerPaused(bg) {
		t.Fatal("恢复后不应暂停")
	}
}

func TestBreakerAlertOncePerPausePerDay(t *testing.T) {
	dbtest.Open(t)
	alerts := &alertLog{}
	s := New(Options{Alert: alerts.add, ShotDir: t.TempDir()})
	bg := context.Background()
	nos := feedBO(t, time.Now().Unix()-100, boDeclined, boDeclined, boDeclined)
	for i := 0; i < 5; i++ {
		if !s.breakerPaused(bg) {
			t.Fatal("应暂停")
		}
	}
	if len(alerts.Texts()) != 1 {
		t.Fatalf("同一次暂停应只告警一次: %v", alerts.Texts())
	}
	text := alerts.Texts()[0]
	for _, no := range nos {
		if !strings.Contains(text, no) {
			t.Fatalf("告警应带单号: %s", text)
		}
	}
	if strings.Contains(text, "@example.com") {
		t.Fatalf("告警不应含邮箱: %s", text)
	}
	if _, err := s.ResumeBreaker(bg, "admin1", "n"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	feedBO(t, now+1, boFill, boFill, boFill)
	s.breakerPaused(bg)
	if len(alerts.Texts()) != 2 {
		t.Fatalf("恢复后再次暂停应再告警: %v", alerts.Texts())
	}
}

// 派发：暂停时不派发排队单，也不领取；恢复后接着派发。
func TestDispatchHonoursBreaker(t *testing.T) {
	h := newHarness(t)
	feedBO(t, time.Now().Unix()-100, boDeclined, boDeclined, boDeclined)
	h.start(t)
	o := h.submit(t, true) // 试运行单也不派发（严格按「不派发新单」）
	time.Sleep(300 * time.Millisecond)
	cur, err := getOrder(ctx, o.OrderNo)
	if err != nil {
		t.Fatal(err)
	}
	if cur.State != StateQueued {
		t.Fatalf("暂停时不应派发，当前 %s", cur.State)
	}
	if n := len(h.alerts.Texts()); n != 1 {
		t.Fatalf("应告警一次: %v", h.alerts.Texts())
	}
	if n := h.gpt.count("Account"); n != 0 {
		t.Fatalf("暂停时不应碰账号接口: %d", n)
	}
	if _, err := h.svc.ResumeBreaker(ctx, "admin1", "已排查"); err != nil {
		t.Fatal(err)
	}
	done := waitDone(t, o.OrderNo)
	if done.State != StatePreviewed {
		t.Fatalf("恢复后应派发完成: %+v", done)
	}
}

// 真实走一遍：连续 3 单填表失败后暂停并告警，第 4 单留在队列；恢复后第 4 单才派发。
func TestBreakerTripsFromRealFillFailures(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.fill = func(paybrowser.Card, paybrowser.Billing) error { return errors.New("page changed") }
	h.start(t)
	for i := 0; i < 3; i++ {
		o := h.submitAs(t, sessionFor(fmt.Sprintf("u%d@x.com", i)), false)
		done := waitDone(t, o.OrderNo)
		if done.ErrorCode != "fill_failed" {
			t.Fatalf("第 %d 单应填表失败: %+v", i, done)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	h.svc.Kick()
	for !strings.Contains(strings.Join(h.alerts.Texts(), "\n"), "付款已暂停") {
		if time.Now().After(deadline) {
			t.Fatalf("没有等到熔断告警: %v", h.alerts.Texts())
		}
		time.Sleep(5 * time.Millisecond)
	}
	st, err := h.svc.BreakerStatus(ctx)
	if err != nil || !st.Paused || st.Reason != BreakerReasonFill || len(st.OrderNos) != 3 {
		t.Fatalf("%+v %v", st, err)
	}
	fourth := h.submitAs(t, sessionFor("u4@x.com"), false)
	time.Sleep(300 * time.Millisecond)
	if cur := mustOrder(t, fourth.OrderNo); cur.State != StateQueued {
		t.Fatalf("第 4 单应留在队列: %s", cur.State)
	}
	if _, err := h.svc.ResumeBreaker(ctx, "admin1", "页面修好了"); err != nil {
		t.Fatal(err)
	}
	if done := waitDone(t, fourth.OrderNo); done.ErrorCode != "fill_failed" {
		t.Fatalf("恢复后应派发: %+v", done)
	}
}

// 暂停期间已提交在途的单照常走完确认；它成功了也不解除暂停。
func TestBreakerDoesNotStopVerification(t *testing.T) {
	h := newPayHarness(t, nil)
	feedBO(t, time.Now().Unix()-100, boDeclined, boDeclined, boDeclined)
	h.gpt.account = accountPlus
	at := h.clock.Now().Unix()
	o := presetOrder(t, "v@x.com", StateVerifying, map[string]any{
		"submitted_at": at, "card_id": testCardID, "lease_token": testLeaseToken,
		"verify_until": at + 600, "next_check_at": at,
	})
	queued := h.submitAs(t, sessionFor("q@x.com"), false)
	h.start(t)
	done := waitDone(t, o.OrderNo)
	if done.State != StateSucceeded {
		t.Fatalf("暂停时在途单也应走完确认: %+v", done)
	}
	if st := breakerNow(t); !st.Paused {
		t.Fatalf("在途单成功不应解除暂停: %+v", st)
	}
	if cur := mustOrder(t, queued.OrderNo); cur.State != StateQueued {
		t.Fatalf("新单不应派发: %s", cur.State)
	}
}

// G5：熔断不会因为时间过去而自己解除：25 小时前连续 3 单被拒触发的暂停，现在仍是暂停，只有管理员恢复才解除。
func TestBreakerStaysPausedAfter24h(t *testing.T) {
	dbtest.Open(t)
	old := time.Now().Add(-25 * time.Hour).Unix()
	nos := feedBO(t, old, boDeclined, boDeclined, boDeclined)
	st := breakerNow(t)
	if !st.Paused || st.Reason != BreakerReasonSubmit || len(st.OrderNos) != 3 || st.OrderNos[2] != nos[2] {
		t.Fatalf("25 小时前触发的暂停应仍然有效: %+v", st)
	}
	if st.TrippedAt == nil || *st.TrippedAt != old+2 {
		t.Fatalf("触发时刻: %+v", st.TrippedAt)
	}
}

// G5：查询带 LIMIT（只取最近 breakerScanLimit 张），取回来以后仍按结束时间从早到晚计算：触发单号、触发时刻和全量扫描一致。
func TestBreakerScansRecentOrdersInOrder(t *testing.T) {
	dbtest.Open(t)
	base := time.Now().Unix() - 3*breakerScanLimit
	list := make([]bo, 0, breakerScanLimit+8)
	for i := 0; i < breakerScanLimit+5; i++ {
		list = append(list, boSuccess)
	}
	list = append(list, boDeclined, boDeclined, boDeclined)
	nos := feedBO(t, base, list...)
	st := breakerNow(t)
	last := len(list) - 1
	if !st.Paused || st.Reason != BreakerReasonSubmit || st.SubmittedFailureStreak != 3 {
		t.Fatalf("最近连续 3 单被拒应触发: %+v", st)
	}
	if st.TrippedAt == nil || *st.TrippedAt != base+int64(last) || len(st.OrderNos) != 3 ||
		st.OrderNos[0] != nos[last-2] || st.OrderNos[2] != nos[last] {
		t.Fatalf("触发单号和时刻应按结束时间顺序: %+v %v", st.OrderNos, st.TrippedAt)
	}
}
