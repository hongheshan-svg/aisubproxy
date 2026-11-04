package cardpool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// fakeFullCardNo 是测试用的完整卡号：平台的卡详情接口返回未打码的卡号，定时查询绝不能把它写进告警、日志或库。
const fakeFullCardNo = "4242424242424242"

// fullDetailJSON 是带完整卡号的卡详情。
func fullDetailJSON(id, avail, status string) string {
	return `{"card_id":"` + id + `","card_no":"` + fakeFullCardNo + `","card_currency":"USD","expiry_date":"1028","status":"` + status +
		`","available_balance":"` + avail + `","freeze_balance":"0.00"}`
}

func pollLog(s *Service) *loopLog { return &loopLog{now: s.now, last: map[string]time.Time{}} }

func mustPollCards(t *testing.T, s *Service) {
	t.Helper()
	if err := s.pollCardStatusOnce(context.Background(), pollLog(s)); err != nil {
		t.Fatalf("pollCardStatusOnce: %v", err)
	}
}

func mustPollHolders(t *testing.T, s *Service) {
	t.Helper()
	if err := s.pollCardholdersOnce(context.Background(), pollLog(s)); err != nil {
		t.Fatalf("pollCardholdersOnce: %v", err)
	}
}

// captureLog 把标准日志收进缓冲区，测试结束时还原。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// noFullCardNo 断言完整卡号没有出现在告警、日志与卡表里。
func noFullCardNo(t *testing.T, alerts *alertLog, logs *bytes.Buffer) {
	t.Helper()
	for _, a := range alerts.Texts() {
		if strings.Contains(a, fakeFullCardNo) {
			t.Fatalf("告警里出现了完整卡号: %q", a)
		}
	}
	if logs != nil && strings.Contains(logs.String(), fakeFullCardNo) {
		t.Fatalf("日志里出现了完整卡号:\n%s", logs.String())
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_cards
		WHERE instr(card_id || card_label || last4 || platform_status || disabled_reason || retire_reason, ?) > 0`, fakeFullCardNo); n != 0 {
		t.Fatalf("卡表里出现了完整卡号")
	}
}

// pollCardStatus：定时查询发现卡的平台状态变化。
func pollCardStatus(card Card, status string) alertPath {
	return alertPath{"定时查询", suffixPoll, func(t *testing.T) []string {
		s, mw, _, alerts := newTestService(t)
		insertCard(t, card)
		mw.On("GET", detailPath(card.CardID), mwtest.OK(fullDetailJSON(card.CardID, "0.00", status)))
		mustPollCards(t, s)
		noFullCardNo(t, alerts, nil)
		return alerts.Texts()
	}}
}

// pollHolderStatus：定时查询发现持卡人状态变化。
func pollHolderStatus(id, old, status, remark string) alertPath {
	return alertPath{"定时查询", suffixPoll, func(t *testing.T) []string {
		s, mw, _, alerts := newTestService(t)
		insertHolder(t, id, old)
		mw.On("GET", "/v1/cardholder/"+id, mwtest.OK(holderDetailJSON(id, "", status, remark)))
		mustPollHolders(t, s)
		return alerts.Texts()
	}}
}

// ---------- A：定时查卡状态 ----------

// 可用卡在平台上被冻结：停用（原因：平台冻结）并告警一次；之后不再查它、不再告警。不写余额，完整卡号不落地。
func TestPollCardAvailableRevoked(t *testing.T) {
	s, mw, clock, alerts := newTestService(t)
	logs := captureLog(t)
	synced := clock.Now().Unix() - 3600
	insertCard(t, Card{CardID: "a1", ProductID: "p1", AvailableCents: 1234, BalanceSyncedAt: &synced})
	mw.On("GET", detailPath("a1"), mwtest.OK(fullDetailJSON("a1", "9.99", "REVOKED")))

	mustPollCards(t, s)
	c := mustGetCard(t, "a1")
	if c.PoolState != PoolDisabled || c.DisabledReason != "平台冻结" || c.PlatformStatus != "REVOKED" {
		t.Fatalf("应停用（平台冻结）: %+v", c)
	}
	if c.AvailableCents != 1234 || c.BalanceSyncedAt == nil || *c.BalanceSyncedAt != synced {
		t.Fatalf("定时查询不应写余额: %+v", c)
	}
	if got := alerts.Texts(); len(got) != 1 || got[0] != extFreezeDisabledText("a1", sourcePoll) {
		t.Fatalf("应告警一次: %v", got)
	}

	clock.Advance(25 * time.Hour)
	mustPollCards(t, s)
	if n := len(mw.Requests("GET", detailPath("a1"))); n != 1 {
		t.Fatalf("停用的卡不应再查: %d", n)
	}
	if alerts.Count() != 1 {
		t.Fatalf("第二轮不应再告警: %v", alerts.Texts())
	}
	noFullCardNo(t, alerts, logs)
}

// 租用中的卡在平台上被冻结：只告警一次，池状态与租约不变；之后每轮照查，跨天也不重复告警。
func TestPollCardLeasedRevoked(t *testing.T) {
	s, mw, clock, alerts := newTestService(t)
	exp := clock.Now().Unix() + 1800
	insertCard(t, Card{CardID: "l1", ProductID: "p1", PoolState: PoolLeased, LeaseToken: "tok", LeaseOwner: "pay:PO1", LeaseExpiresAt: &exp})
	mw.On("GET", detailPath("l1"), mwtest.OK(fullDetailJSON("l1", "5.00", "REVOKED")))

	mustPollCards(t, s)
	c := mustGetCard(t, "l1")
	if c.PoolState != PoolLeased || c.LeaseToken != "tok" || c.PlatformStatus != "REVOKED" {
		t.Fatalf("只告警、不改池状态与租约: %+v", c)
	}
	if got := alerts.Texts(); len(got) != 1 || got[0] != extFreezeLeasedText(c, sourcePoll) {
		t.Fatalf("应告警一次: %v", got)
	}
	mustPollCards(t, s)
	clock.Advance(25 * time.Hour)
	mustPollCards(t, s)
	if n := len(mw.Requests("GET", detailPath("l1"))); n != 3 {
		t.Fatalf("租用中的卡每轮都要查: %d", n)
	}
	if alerts.Count() != 1 {
		t.Fatalf("平台状态没再变化，不应再告警: %v", alerts.Texts())
	}
}

// 平台注销：可用卡、租用中的卡都退役，租用中的告警一次。
func TestPollCardCanceled(t *testing.T) {
	s, mw, _, alerts := newTestService(t)
	insertCard(t, Card{CardID: "x1", ProductID: "p1"})
	insertCard(t, Card{CardID: "x2", ProductID: "p1", PoolState: PoolLeased, LeaseToken: "tok", LeaseOwner: "pay:PO2"})
	mw.On("GET", detailPath("x1"), mwtest.OK(fullDetailJSON("x1", "0.00", "CANCELED")))
	mw.On("GET", detailPath("x2"), mwtest.OK(fullDetailJSON("x2", "0.00", "CANCELED")))

	mustPollCards(t, s)
	for _, id := range []string{"x1", "x2"} {
		if c := mustGetCard(t, id); c.PoolState != PoolRetired || c.PlatformStatus != "CANCELED" {
			t.Fatalf("%s 应退役: %+v", id, c)
		}
	}
	if got := alerts.Texts(); len(got) != 1 || !strings.Contains(got[0], "x2") || !strings.HasSuffix(got[0], suffixPoll) {
		t.Fatalf("只有租用中的 x2 告警一次: %v", got)
	}
}

// 平台状态没变：什么都不改；变成 ACTIVE 等其他值：只更新平台状态，不告警。
func TestPollCardActiveOnlyUpdatesStatus(t *testing.T) {
	s, mw, clock, alerts := newTestService(t)
	insertCard(t, Card{CardID: "s1", ProductID: "p1"})
	insertCard(t, Card{CardID: "s2", ProductID: "p1", PoolState: PoolLeased, LeaseToken: "tok", PlatformStatus: "REVOKED"})
	mw.On("GET", detailPath("s1"), mwtest.OK(fullDetailJSON("s1", "1.00", "ACTIVE")))
	mw.On("GET", detailPath("s2"), mwtest.OK(fullDetailJSON("s2", "1.00", "ACTIVE")))
	before := mustGetCard(t, "s1")
	clock.Advance(time.Minute)

	mustPollCards(t, s)
	if c := mustGetCard(t, "s1"); c.PoolState != PoolAvailable || c.PlatformStatus != "ACTIVE" || c.UpdatedAt != before.UpdatedAt {
		t.Fatalf("状态没变不应改卡: %+v", c)
	}
	if c := mustGetCard(t, "s2"); c.PoolState != PoolLeased || c.LeaseToken != "tok" || c.PlatformStatus != "ACTIVE" {
		t.Fatalf("只应更新平台状态: %+v", c)
	}
	if alerts.Count() != 0 {
		t.Fatalf("不应告警: %v", alerts.Texts())
	}
}

// 推送与定时查询先后发现同一次冻结，不论谁先到，第二个到的都不再告警（跨天也一样）。
func TestPollAndWebhookNoDuplicateAlert(t *testing.T) {
	s, mw, clock, alerts := newTestService(t)
	insertCard(t, Card{CardID: "p1", ProductID: "p1", PoolState: PoolLeased, LeaseToken: "t1"})
	insertCard(t, Card{CardID: "p2", ProductID: "p1", PoolState: PoolLeased, LeaseToken: "t2"})
	insertCard(t, Card{CardID: "p3", ProductID: "p1"})
	push := func(card string) {
		e := enqueueEvent(t, marswallex.TopicCardStatusChanged, `{"card_id":"`+card+`","card_status":"REVOKED"}`)
		mustProcessEvents(t, s)
		mustBeProcessed(t, e, "")
	}

	// 先推送、后查询
	push("p1")
	push("p3")
	if alerts.Count() != 2 {
		t.Fatalf("推送应各告警一次: %v", alerts.Texts())
	}
	clock.Advance(25 * time.Hour)
	mw.On("GET", detailPath("p1"), mwtest.OK(fullDetailJSON("p1", "0.00", "REVOKED")))
	mw.On("GET", detailPath("p2"), mwtest.OK(fullDetailJSON("p2", "0.00", "REVOKED")))
	mustPollCards(t, s)
	if got := alerts.Texts(); len(got) != 3 || alertsContaining(alerts, "卡 p1 ") != 1 || !strings.Contains(got[2], "卡 p2 ") {
		t.Fatalf("推送处理过的 p1 不应再告警，p2 告警一次: %v", got)
	}

	// 先查询、后推送
	clock.Advance(25 * time.Hour)
	push("p2")
	if alerts.Count() != 3 {
		t.Fatalf("定时查询处理过的 p2 推送到达时不应再告警: %v", alerts.Texts())
	}
}

// 一张卡查询失败只记日志，其他卡照常处理；失败的卡也算查过，排到后面。
func TestPollCardOneFailureOthersContinue(t *testing.T) {
	s, mw, _, alerts := newTestService(t)
	logs := captureLog(t)
	insertCard(t, Card{CardID: "f1", ProductID: "p1"})
	insertCard(t, Card{CardID: "f2", ProductID: "p1"})
	mw.On("GET", detailPath("f1"), mwtest.Rejected(404, "CARD_NOT_FOUND"))
	mw.On("GET", detailPath("f2"), mwtest.OK(fullDetailJSON("f2", "0.00", "REVOKED")))

	mustPollCards(t, s)
	if c := mustGetCard(t, "f2"); c.PoolState != PoolDisabled {
		t.Fatalf("f2 应照常处理: %+v", c)
	}
	if c := mustGetCard(t, "f1"); c.PoolState != PoolAvailable || c.PlatformStatus != "ACTIVE" {
		t.Fatalf("查询失败的卡不应改动: %+v", c)
	}
	if alerts.Count() != 1 {
		t.Fatalf("查询失败不告警: %v", alerts.Texts())
	}
	if !strings.Contains(logs.String(), "f1") {
		t.Fatalf("查询失败应记日志:\n%s", logs.String())
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_cards WHERE card_id = 'f1' AND status_polled_at IS NOT NULL`); n != 1 {
		t.Fatal("查询失败的卡也应记下查询时间，免得一直排在最前")
	}
}

// 每轮最多 50 张，最久没查过的（从没查过的最先）优先。
func TestPollCardBatchOldestFirst(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	base := clock.Now().Unix() - 3600
	for i := 0; i <= statusPollBatch; i++ {
		id := fmt.Sprintf("b%02d", i)
		insertCard(t, Card{CardID: id, ProductID: "p1"})
		mw.On("GET", detailPath(id), mwtest.OK(fullDetailJSON(id, "0.00", "ACTIVE")))
		if i > 0 {
			if _, err := db.DB.Exec(`UPDATE mw_cards SET status_polled_at = ? WHERE card_id = ?`, base+int64(i), id); err != nil {
				t.Fatal(err)
			}
		}
	}
	reqs := func(id string) int { return len(mw.Requests("GET", detailPath(id))) }

	mustPollCards(t, s)
	last := fmt.Sprintf("b%02d", statusPollBatch)
	if reqs("b00") != 1 || reqs("b01") != 1 || reqs(last) != 0 {
		t.Fatalf("第一轮应查从没查过的 b00 与最旧的 49 张，不查最新的 %s: b00=%d b01=%d last=%d", last, reqs("b00"), reqs("b01"), reqs(last))
	}
	clock.Advance(time.Minute)
	mustPollCards(t, s)
	if reqs(last) != 1 {
		t.Fatalf("第二轮应轮到 %s", last)
	}
}

// 卡上有我们发起、还没定论的同目标改状态单：留给对账定论，定时查询不按外部变更处理。
func TestPollCardSkipsOurStatusOrder(t *testing.T) {
	s, mw, _, alerts := newTestService(t)
	insertCard(t, Card{CardID: "o1", ProductID: "p1"})
	insertOrder(t, Order{Kind: KindStatus, CardID: "o1", TargetStatus: "REVOKED", State: StateProcessing})
	mw.On("GET", detailPath("o1"), mwtest.OK(fullDetailJSON("o1", "0.00", "REVOKED")))

	mustPollCards(t, s)
	if c := mustGetCard(t, "o1"); c.PoolState != PoolAvailable || c.PlatformStatus != "ACTIVE" {
		t.Fatalf("本系统发起的改状态不应按外部冻结处理: %+v", c)
	}
	if alerts.Count() != 0 {
		t.Fatalf("不应告警: %v", alerts.Texts())
	}
}

// 只查可用与租用中的卡（未编排的请求会让 mwtest 报错）。
func TestPollCardOnlyAvailableAndLeased(t *testing.T) {
	s, _, _, _ := newTestService(t)
	for i, st := range []string{PoolReleasing, PoolUsed, PoolRetired, PoolDisabled} {
		insertCard(t, Card{CardID: fmt.Sprintf("n%d", i), ProductID: "p1", PoolState: st})
	}
	mustPollCards(t, s)
}

// MarsWallex 未就绪：整轮跳过，不发请求、不告警。
func TestStatusPollRoundSkipsWhenNotReady(t *testing.T) {
	s, _, _, alerts := newTestService(t)
	insertCard(t, Card{CardID: "r1", ProductID: "p1"})
	insertHolder(t, "h1", "PENDING_REVIEW")
	s.opts.Client = func() (*marswallex.Client, error) { return nil, errors.New("未配置") }
	s.statusPollRound(context.Background(), pollLog(s))
	if alerts.Count() != 0 {
		t.Fatalf("不应告警: %v", alerts.Texts())
	}
}

// 后台循环按间隔跑定时查询。
func TestStartRunsStatusPoll(t *testing.T) {
	prev := cardStatusPollInterval
	cardStatusPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { cardStatusPollInterval = prev })
	s, mw, _, _ := newTestService(t)
	insertCard(t, Card{CardID: "w1", ProductID: "p1"})
	insertHolder(t, "h1", "APPROVED")
	mw.On("GET", detailPath("w1"), mwtest.OK(fullDetailJSON("w1", "0.00", "ACTIVE")))
	mw.On("GET", "/v1/cardholder/h1", mwtest.OK(holderDetailJSON("h1", "", "APPROVED", "")))
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	t.Cleanup(func() { cancel(); s.loops.Wait() })
	if !waitFor(2*time.Second, func() bool {
		return len(mw.Requests("GET", detailPath("w1"))) > 0 && len(mw.Requests("GET", "/v1/cardholder/h1")) > 0
	}) {
		t.Fatal("后台循环没有定时查询卡与持卡人")
	}
}

// ---------- B：定时查持卡人 ----------

// 状态变化按推送同样的规则告警（来源：定时查询），没变不告警；DISABLED 的不查。
func TestPollCardholders(t *testing.T) {
	s, mw, clock, alerts := newTestService(t)
	insertHolder(t, "hp", "PENDING_REVIEW")
	insertHolder(t, "hr", "PENDING_REVIEW")
	insertHolder(t, "hs", "APPROVED")
	insertHolder(t, "hd", "DISABLED")
	mw.On("GET", "/v1/cardholder/hp", mwtest.OK(holderDetailJSON("hp", "", "APPROVED", "")))
	mw.On("GET", "/v1/cardholder/hr", mwtest.OK(holderDetailJSON("hr", "", "REJECTED", "证件照模糊")))
	mw.On("GET", "/v1/cardholder/hs", mwtest.OK(holderDetailJSON("hs", "", "APPROVED", "")))
	clock.Advance(time.Minute)

	mustPollHolders(t, s)
	want := map[string]string{}
	for _, h := range []struct{ id, old, st, remark string }{{"hp", "PENDING_REVIEW", "APPROVED", ""}, {"hr", "PENDING_REVIEW", "REJECTED", "证件照模糊"}} {
		_, text := holderAlert(h.id, h.old, h.st, h.remark, sourcePoll)
		want[h.id] = text
	}
	got := alerts.Texts()
	if len(got) != 2 || got[0] != want["hp"] || got[1] != want["hr"] {
		t.Fatalf("应发 hp 通过、hr 驳回（带原因）各一次: %v", got)
	}
	if !strings.Contains(got[1], "证件照模糊") {
		t.Fatalf("驳回告警应带原因: %q", got[1])
	}
	for id, st := range map[string]string{"hp": "APPROVED", "hr": "REJECTED", "hs": "APPROVED", "hd": "DISABLED"} {
		h, err := getCardholder(context.Background(), id)
		if err != nil || h.Status != st {
			t.Fatalf("%s: %v %+v", id, err, h)
		}
		if id != "hd" && (h.SyncedAt == nil || *h.SyncedAt != clock.Now().Unix()) {
			t.Fatalf("%s 应记下同步时间: %+v", id, h)
		}
	}

	clock.Advance(25 * time.Hour)
	mustPollHolders(t, s)
	if alerts.Count() != 2 {
		t.Fatalf("状态没变不应再告警: %v", alerts.Texts())
	}
}

// 每轮最多 50 个，synced_at 最旧的优先；一个查询失败不影响其他。
func TestPollCardholdersBatchOldestFirst(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	logs := captureLog(t)
	base := clock.Now().Unix() - 3600
	for i := 0; i <= statusPollBatch; i++ {
		id := fmt.Sprintf("h%02d", i)
		insertHolder(t, id, "APPROVED")
		if _, err := db.DB.Exec(`UPDATE mw_cardholders SET synced_at = ? WHERE card_holder_id = ?`, base+int64(i), id); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			mw.On("GET", "/v1/cardholder/"+id, mwtest.Unknown())
		} else {
			mw.On("GET", "/v1/cardholder/"+id, mwtest.OK(holderDetailJSON(id, "", "APPROVED", "")))
		}
	}
	reqs := func(id string) int { return len(mw.Requests("GET", "/v1/cardholder/"+id)) }
	last := fmt.Sprintf("h%02d", statusPollBatch)

	mustPollHolders(t, s)
	if reqs("h00") == 0 || reqs("h01") != 1 || reqs(last) != 0 {
		t.Fatalf("第一轮应查最旧的 50 个: h00=%d h01=%d last=%d", reqs("h00"), reqs("h01"), reqs(last))
	}
	if !strings.Contains(logs.String(), "h00") {
		t.Fatalf("查询失败应记日志:\n%s", logs.String())
	}
	clock.Advance(time.Minute)
	mustPollHolders(t, s)
	if reqs(last) != 1 {
		t.Fatalf("第二轮应轮到 %s", last)
	}
}
