package cardpool

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

func failOut(t *testing.T, s *Service, c *Card, plan, email string) {
	t.Helper()
	if err := s.recordOutcome(context.Background(), c, plan, "ord", email, Outcome{Result: ResultFailed, ErrorCode: "declined"}); err != nil {
		t.Fatal(err)
	}
}

func okOut(t *testing.T, s *Service, c *Card, plan, email string) {
	t.Helper()
	if err := s.recordOutcome(context.Background(), c, plan, "ord", email, Outcome{Result: ResultSuccess}); err != nil {
		t.Fatal(err)
	}
}

func susp(t *testing.T, s *Service, product, plan string) bool {
	t.Helper()
	v, err := s.productSuspended(context.Background(), product, plan)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestEvaluateVerdict(t *testing.T) {
	cases := []struct {
		fail, distinct, th int
		want               string
	}{
		{2, 2, 3, "need_more"},
		{3, 2, 3, "suspect"},
		{3, 1, 3, "email_suspect"},
		{3, 0, 3, "unknown_emails"},
	}
	for _, c := range cases {
		if got := evaluateVerdict(c.fail, c.distinct, c.th); got != c.want {
			t.Errorf("%+v got %s", c, got)
		}
	}
}

func TestProductSuspendsAfterThresholdDistinctEmails(t *testing.T) {
	s, _, _, al := newTestService(t)
	c := insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	failOut(t, s, &c, "gpt", "a")
	failOut(t, s, &c, "gpt", "B@x")
	failOut(t, s, &c, "gpt", "a")
	if !susp(t, s, "p1", "gpt") {
		t.Fatal("want suspended")
	}
	if al.Count() != 1 {
		t.Fatalf("alerts %v", al.Texts())
	}
	var email string
	db.DB.QueryRow(`SELECT email_norm FROM mw_card_outcomes WHERE id=2`).Scan(&email)
	if email != "b@x" {
		t.Fatalf("email %q", email)
	}
}

func TestSameEmailDoesNotSuspend(t *testing.T) {
	s, _, _, _ := newTestService(t)
	c := insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	for i := 0; i < 3; i++ {
		failOut(t, s, &c, "gpt", "a@x")
	}
	if susp(t, s, "p1", "gpt") {
		t.Fatal("should not suspend")
	}
}

func TestSuccessResetsCount(t *testing.T) {
	s, _, _, _ := newTestService(t)
	c := insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	failOut(t, s, &c, "gpt", "a")
	failOut(t, s, &c, "gpt", "b")
	okOut(t, s, &c, "gpt", "c")
	failOut(t, s, &c, "gpt", "d")
	if susp(t, s, "p1", "gpt") {
		t.Fatal("should not suspend")
	}
}

func TestWindowExcludesOld(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	c := insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	failOut(t, s, &c, "gpt", "a")
	failOut(t, s, &c, "gpt", "b")
	clock.Advance(25 * time.Hour)
	failOut(t, s, &c, "gpt", "c")
	if susp(t, s, "p1", "gpt") {
		t.Fatal("should not suspend")
	}
}

func TestResumeResetsCounting(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	ctx := context.Background()
	c := insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	if err := s.ResumeProduct(ctx, "p1", "gpt", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("want bad request, got %v", err)
	}
	failOut(t, s, &c, "gpt", "a")
	failOut(t, s, &c, "gpt", "b")
	failOut(t, s, &c, "gpt", "c")
	if !susp(t, s, "p1", "gpt") {
		t.Fatal("want suspended")
	}
	clock.Advance(time.Minute)
	if err := s.ResumeProduct(ctx, "p1", "gpt", "admin"); err != nil {
		t.Fatal(err)
	}
	if susp(t, s, "p1", "gpt") {
		t.Fatal("want resumed")
	}
	clock.Advance(time.Minute)
	failOut(t, s, &c, "gpt", "d")
	if susp(t, s, "p1", "gpt") {
		t.Fatal("should not re-suspend")
	}
	if err := s.ResumeProduct(ctx, "p1", "gpt", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("want bad request, got %v", err)
	}
}

func TestPerPlanIsolation(t *testing.T) {
	s, _, _, _ := newTestService(t)
	c := insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	failOut(t, s, &c, "gpt", "a")
	failOut(t, s, &c, "gpt", "b")
	failOut(t, s, &c, "x", "c")
	if susp(t, s, "p1", "gpt") || susp(t, s, "p1", "x") {
		t.Fatal("should not suspend")
	}
	failOut(t, s, &c, "gpt", "d")
	if !susp(t, s, "p1", "gpt") || susp(t, s, "p1", "x") {
		t.Fatal("only gpt suspended")
	}
}

func TestCardHealthOnlyWhenMaxUsesAboveOne(t *testing.T) {
	s, _, _, _ := newTestService(t)
	savePolicy(t, func(p *Policy) { p.MaxUsesPerCard = 1 })
	c1 := insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	failOut(t, s, &c1, "gpt", "a")
	failOut(t, s, &c1, "gpt", "b")
	got, _ := getCard(context.Background(), "c1")
	if got.RetireReason != "" || got.PoolState != PoolAvailable {
		t.Fatalf("card changed: %+v", got)
	}
	savePolicy(t, func(p *Policy) { p.MaxUsesPerCard = 3 })
	c2 := insertCard(t, Card{CardID: "c2", ProductID: "p2"})
	failOut(t, s, &c2, "gpt", "a")
	failOut(t, s, &c2, "gpt", "b")
	got, _ = getCard(context.Background(), "c2")
	if got.RetireReason != "健康度" || got.PoolState != PoolUsed {
		t.Fatalf("card: %+v", got)
	}
}

func TestCardHealthLeasedCardWaitsForRelease(t *testing.T) {
	s, _, _, al := newTestService(t)
	savePolicy(t, func(p *Policy) { p.MaxUsesPerCard = 3 })
	c := insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolLeased})
	failOut(t, s, &c, "gpt", "a")
	failOut(t, s, &c, "gpt", "b")
	got, _ := getCard(context.Background(), "c1")
	if got.RetireReason != "健康度" || got.PoolState != PoolLeased {
		t.Fatalf("card: %+v", got)
	}
	if al.Count() != 1 {
		t.Fatalf("alerts %v", al.Texts())
	}
}

func TestAutoSuspendOffOnlyAlerts(t *testing.T) {
	s, _, _, al := newTestService(t)
	savePolicy(t, func(p *Policy) { p.AutoSuspend = false; p.MaxUsesPerCard = 3 })
	c := insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	failOut(t, s, &c, "gpt", "a")
	failOut(t, s, &c, "gpt", "b")
	failOut(t, s, &c, "gpt", "c")
	if susp(t, s, "p1", "gpt") {
		t.Fatal("should not suspend")
	}
	got, _ := getCard(context.Background(), "c1")
	if got.RetireReason != "" || got.PoolState != PoolAvailable {
		t.Fatalf("card changed: %+v", got)
	}
	if al.Count() != 2 {
		t.Fatalf("alerts %v", al.Texts())
	}
}

func TestAbortedNotRecorded(t *testing.T) {
	s, _, _, _ := newTestService(t)
	c := insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	if err := s.recordOutcome(context.Background(), &c, "gpt", "o", "a", Outcome{Result: ResultAborted}); err != nil {
		t.Fatal(err)
	}
	var n int
	db.DB.QueryRow(`SELECT COUNT(*) FROM mw_card_outcomes`).Scan(&n)
	if n != 0 {
		t.Fatalf("n=%d", n)
	}
}

func TestHealthReport(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	c := insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	okOut(t, s, &c, "gpt", "a")
	clock.Advance(3 * 24 * time.Hour)
	okOut(t, s, &c, "gpt", "a")
	failOut(t, s, &c, "gpt", "b")
	failOut(t, s, &c, "gpt", "c")
	failOut(t, s, &c, "gpt", "d")
	okOut(t, s, &c, "x", "a")
	rep, err := s.HealthReport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep) != 2 {
		t.Fatalf("%+v", rep)
	}
	g := rep[0]
	if g.PlanKey != "gpt" || g.Success24h != 1 || g.Failed24h != 3 || g.Success7d != 2 || g.Failed7d != 3 || !g.Suspended || g.SuspendedAt == nil || g.Reason == "" {
		t.Fatalf("gpt: %+v", g)
	}
	x := rep[1]
	if x.PlanKey != "x" || x.Success24h != 1 || x.Suspended {
		t.Fatalf("x: %+v", x)
	}
}
