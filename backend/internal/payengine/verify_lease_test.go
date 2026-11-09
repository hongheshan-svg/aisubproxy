package payengine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
)

// 已知问题清单 G4：确认协程一张一张查，付款步骤又可能用掉大半个租约，确认还没定论卡就被释放去回收。
// 修法：每查一张单之前，把所有确认中单据的租约续到 verifyLeaseAhead 之后；单次检查最多 VerifyCheck，一张单卡住不拖住后面的单。

// renewCall 是一次 RenewLease 的参数，at 是续租时测试时钟的时刻。
type renewCall struct {
	cardID, token string
	until, at     time.Time
}

type renewLog struct {
	mu    sync.Mutex
	calls []renewCall
}

func (r *renewLog) add(c renewCall) {
	r.mu.Lock()
	r.calls = append(r.calls, c)
	r.mu.Unlock()
}

func (r *renewLog) list() []renewCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]renewCall(nil), r.calls...)
}

func (r *renewLog) forCard(cardID string) []renewCall {
	var out []renewCall
	for _, c := range r.list() {
		if c.cardID == cardID {
			out = append(out, c)
		}
	}
	return out
}

func recordRenewals(h *harness, err error) *renewLog {
	log := &renewLog{}
	h.pool.renew = func(l *cardpool.Lease, until time.Time) error {
		log.add(renewCall{cardID: l.CardID, token: l.Token, until: until, at: h.clock.Now()})
		return err
	}
	return log
}

// verifyingWithLease 预置一张确认中、带租约的单据：页面结果不明，确认期限 10 分钟后。
func verifyingWithLease(t *testing.T, h *harness, email, cardID, token string, set map[string]any) *Order {
	t.Helper()
	now := h.clock.Now().Unix()
	base := map[string]any{"submitted_at": now - 60, "card_id": cardID, "lease_token": token,
		"page_outcome": "unknown", "verify_until": now + 600, "next_check_at": now}
	for k, v := range set {
		base[k] = v
	}
	return presetOrder(t, email, StateVerifying, base)
}

func TestVerifyRenewsLeaseBeforeCheck(t *testing.T) {
	h := newPayHarness(t, nil)
	o := verifyingWithLease(t, h, "a@x.com", testCardID, testLeaseToken, nil)
	renewals := recordRenewals(h, nil)
	var renewedFirst bool
	h.gpt.accountFn = func(int) (chatgpt.Account, error) {
		renewedFirst = len(renewals.forCard(testCardID)) > 0
		return accountFree, nil
	}
	start := h.clock.Now()
	h.svc.verifyDue(ctx)

	got := renewals.forCard(testCardID)
	if len(got) == 0 || got[0].token != testLeaseToken {
		t.Fatalf("确认前应按单据上的租约续租: %+v", got)
	}
	if want := start.Add(verifyLeaseAhead); !got[0].until.Equal(want) {
		t.Fatalf("应续到 %s 之后，实际 %s", verifyLeaseAhead, got[0].until.Sub(start))
	}
	if !renewedFirst {
		t.Fatal("应先续租再查账号")
	}
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying {
		t.Fatalf("未开通、未到期应继续等: %+v", g)
	}
}

// 没到检查时间的确认中单据（比如在等 429 的 Retry-After）也要续：两次检查之间可能隔好几分钟。
// 没有租约的单、不在确认中的单不续。
func TestVerifyRenewsAllVerifyingLeases(t *testing.T) {
	h := newPayHarness(t, nil)
	now := h.clock.Now().Unix()
	verifyingWithLease(t, h, "due@x.com", "card-due", "tok-due", nil)
	verifyingWithLease(t, h, "wait@x.com", "card-wait", "tok-wait", map[string]any{"next_check_at": now + 300})
	verifyingWithLease(t, h, "nolease@x.com", "", "", nil)
	presetOrder(t, "done@x.com", StateSucceeded, map[string]any{"card_id": "card-done", "lease_token": "tok-done"})
	renewals := recordRenewals(h, nil)
	h.gpt.account = accountFree

	h.svc.verifyDue(ctx)

	if len(renewals.forCard("card-wait")) == 0 {
		t.Fatalf("没到检查时间的确认中单据也应续租: %+v", renewals.list())
	}
	if len(renewals.forCard("card-due")) == 0 {
		t.Fatalf("到期检查的单应续租: %+v", renewals.list())
	}
	for _, c := range renewals.list() {
		if c.cardID == "" || c.cardID == "card-done" {
			t.Fatalf("没有租约或已定论的单不应续租: %+v", c)
		}
	}
	// 没有到期单据的一轮也续
	before := len(renewals.forCard("card-wait"))
	h.clock.Advance(10 * time.Second)
	if err := setNextCheck(ctx, mustOrderByEmail(t, "due@x.com").OrderNo, now+300); err != nil {
		t.Fatal(err)
	}
	if err := setNextCheck(ctx, mustOrderByEmail(t, "nolease@x.com").OrderNo, now+300); err != nil {
		t.Fatal(err)
	}
	h.svc.verifyDue(ctx)
	if len(renewals.forCard("card-wait")) <= before {
		t.Fatal("没有到期单据的轮次也应续租")
	}
}

// 前面的单查得慢（这里一次查了 4 分钟），后面单的租约要在查它之前重新续，而不是只在一轮开始时续一次。
func TestVerifyRenewsBeforeEachCheck(t *testing.T) {
	h := newPayHarness(t, nil)
	verifyingWithLease(t, h, "slow@x.com", "card-a", "tok-a", nil)
	verifyingWithLease(t, h, "next@x.com", "card-b", "tok-b", nil)
	renewals := recordRenewals(h, nil)
	var bRenewedAt time.Time
	h.gpt.accountSess = func(s chatgpt.Session, _ int) (chatgpt.Account, error) {
		if s.Email == "slow@x.com" {
			h.clock.Advance(4 * time.Minute)
		} else if got := renewals.forCard("card-b"); len(got) > 0 {
			bRenewedAt = got[len(got)-1].at
		}
		return accountFree, nil
	}
	start := h.clock.Now()
	h.svc.verifyDue(ctx)
	if want := start.Add(4 * time.Minute); !bRenewedAt.Equal(want) {
		t.Fatalf("查第二张单之前应按当时的时间再续一次（期望 %s，实际 %s）", want, bRenewedAt)
	}
}

// 续租失败（租约已被卡池收回）不影响确认本身，只在时间线记一次，不每轮都记。
func TestVerifyLeaseRenewFailureNotedOnce(t *testing.T) {
	h := newPayHarness(t, nil)
	o := verifyingWithLease(t, h, "a@x.com", testCardID, testLeaseToken, nil)
	recordRenewals(h, cardpool.ErrLeaseInvalid)
	h.gpt.account = accountFree
	for i := 0; i < 3; i++ {
		h.svc.verifyDue(ctx)
		h.clock.Advance(time.Second)
	}
	if n := h.gpt.count("Account"); n != 3 {
		t.Fatalf("续租失败也应照常确认: Account %d 次", n)
	}
	if n := countEvents(t, o.OrderNo, "续租"); n != 1 {
		t.Fatalf("续租失败应只记一次时间线，实际 %d", n)
	}
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying {
		t.Fatalf("续租失败不应定论: %+v", g)
	}
}

// 单次检查超过 VerifyCheck 就放弃这一次，接着查后面的单；这张单记一次时间线，按 VerifyEvery 再查。
func TestVerifyCheckTimeoutDoesNotBlockOthers(t *testing.T) {
	h := newPayHarness(t, func(t *Timeouts) {
		t.VerifyCheck = 50 * time.Millisecond
		t.VerifyEvery = 15 * time.Second
	})
	stuck := verifyingWithLease(t, h, "stuck@x.com", "card-a", "tok-a", nil)
	ok := verifyingWithLease(t, h, "ok@x.com", "card-b", "tok-b", nil)
	h.pool.release = func(*cardpool.Lease, cardpool.Outcome) error { return nil }
	h.gpt.accountCtx = func(c context.Context, s chatgpt.Session, _ int) (chatgpt.Account, error) {
		if s.Email == "stuck@x.com" {
			<-c.Done()
			return chatgpt.Account{}, c.Err()
		}
		return accountPlus, nil
	}
	now := h.clock.Now().Unix()
	done := make(chan struct{})
	go func() { h.svc.verifyDue(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("单次检查超时没有生效，一张单卡住拖住了整轮确认")
	}
	if g := mustOrder(t, ok.OrderNo); g.State != StateSucceeded {
		t.Fatalf("后面的单应照常定论: %+v", g)
	}
	g := mustOrder(t, stuck.OrderNo)
	if g.State != StateVerifying {
		t.Fatalf("没到期限的超时单应继续等: %+v", g)
	}
	if g.NextCheckAt == nil || *g.NextCheckAt != now+15 {
		t.Fatalf("超时单应按 VerifyEvery 再查: %+v", g.NextCheckAt)
	}
	if n := countEvents(t, stuck.OrderNo, "超过"); n != 1 {
		t.Fatalf("超时应记一次时间线，实际 %d", n)
	}
}

// 过了最后期限还查不完：和一直查不到账号一样转人工。否则这张单每轮超时、每轮续租，一直占着卡。
func TestVerifyCheckTimeoutPastDeadlineEscalates(t *testing.T) {
	h := newPayHarness(t, func(t *Timeouts) { t.VerifyCheck = 50 * time.Millisecond })
	now := h.clock.Now().Unix()
	o := verifyingWithLease(t, h, "stuck@x.com", testCardID, testLeaseToken, map[string]any{"verify_until": now - 1})
	h.gpt.accountCtx = func(c context.Context, _ chatgpt.Session, _ int) (chatgpt.Account, error) {
		<-c.Done()
		return chatgpt.Account{}, c.Err()
	}
	h.svc.verifyDue(ctx)
	g := mustOrder(t, o.OrderNo)
	if g.State != StateNeedsReview || g.ErrorCode != "verify_unreachable" || !strings.Contains(g.ErrorMessage, "超过") {
		t.Fatalf("过了期限还查不完应转人工: %s / %s / %s", g.State, g.ErrorCode, g.ErrorMessage)
	}
	assertReleases(t, h, cardpool.ResultAborted)
	if a := h.alerts.Texts(); len(a) != 1 || !strings.Contains(a[0], o.OrderNo) {
		t.Fatalf("转人工应告警: %v", a)
	}
}

// 被拒单过了 verify_until 还有 declineExtra 可以等证据：这段时间里超时只是再查，不转人工。
func TestVerifyCheckTimeoutDeclinedWaitsExtra(t *testing.T) {
	h := newPayHarness(t, func(t *Timeouts) { t.VerifyCheck = 50 * time.Millisecond })
	h.svc.recovered.Store(true)
	now := h.clock.Now().Unix()
	o := verifyingWithLease(t, h, "stuck@x.com", testCardID, testLeaseToken, map[string]any{"page_outcome": "declined", "verify_until": now - 1})
	h.gpt.accountCtx = func(c context.Context, _ chatgpt.Session, _ int) (chatgpt.Account, error) {
		<-c.Done()
		return chatgpt.Account{}, c.Err()
	}
	h.svc.verifyDue(ctx)
	if g := mustOrder(t, o.OrderNo); g.State != StateVerifying {
		t.Fatalf("被拒单还在补充等待期内，超时不应转人工: %s / %s", g.State, g.ErrorMessage)
	}
	h.clock.Advance(h.timeouts.Verify + time.Second)
	if err := setNextCheck(ctx, o.OrderNo, h.clock.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	h.svc.verifyDue(ctx)
	if g := mustOrder(t, o.OrderNo); g.State != StateNeedsReview || g.ErrorCode != "verify_unreachable" {
		t.Fatalf("补充等待期也过了应转人工: %s / %s", g.State, g.ErrorCode)
	}
}

func TestVerifyCheckDefaultTimeout(t *testing.T) {
	if d := DefaultTimeouts().VerifyCheck; d != 45*time.Second {
		t.Fatalf("单次检查默认最多 45 秒: %s", d)
	}
	if verifyLeaseAhead != 5*time.Minute {
		t.Fatalf("确认期间每次续租到 5 分钟后: %s", verifyLeaseAhead)
	}
	svc := New(Options{Timeouts: &Timeouts{}})
	if svc.t.VerifyCheck != 45*time.Second {
		t.Fatalf("没配时应取默认值，不能是 0（0 会让每次检查立刻超时）: %s", svc.t.VerifyCheck)
	}
}

func mustOrderByEmail(t *testing.T, email string) *Order {
	t.Helper()
	list, err := ordersInStates(ctx, StateVerifying, StateSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	for i := range list {
		if list[i].AccountEmail == email {
			return &list[i]
		}
	}
	t.Fatalf("没有 %s 的单", email)
	return nil
}
