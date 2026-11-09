package cardpool

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 已知问题清单 G4：付款确认期间租约到期，卡会被释放去回收。付款引擎每次确认前续租，这里是续租本身。

func TestRenewLease(t *testing.T) {
	ctx := context.Background()
	s, _, clock, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	l := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt", OrderRef: "o1"})
	orig := *mustGetCard(t, "c1").LeaseExpiresAt

	later := clock.Now().Add(defaultLease + time.Hour)
	if err := s.RenewLease(ctx, l, later); err != nil {
		t.Fatalf("续租: %v", err)
	}
	if got := *mustGetCard(t, "c1").LeaseExpiresAt; got != later.Unix() {
		t.Fatalf("应续到 %d，实际 %d", later.Unix(), got)
	}
	// 只延长不缩短：确认协程续到 5 分钟后，不能把付款步骤还要用的租约截短
	if err := s.RenewLease(ctx, l, clock.Now().Add(time.Minute)); err != nil {
		t.Fatalf("续到更早的时间也不算错: %v", err)
	}
	if got := *mustGetCard(t, "c1").LeaseExpiresAt; got != later.Unix() {
		t.Fatalf("不应缩短租约: 原 %d，续后 %d，实际 %d", orig, later.Unix(), got)
	}

	if err := s.RenewLease(ctx, &Lease{Token: "other", CardID: "c1"}, later); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("令牌不对应拒绝: %v", err)
	}
	if err := s.RenewLease(ctx, &Lease{Token: l.Token, CardID: "nope"}, later); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("没有这张卡应拒绝: %v", err)
	}
	if err := s.RenewLease(ctx, nil, later); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("空租约应拒绝: %v", err)
	}
	if err := s.Release(ctx, l, Outcome{Result: ResultAborted}); err != nil {
		t.Fatal(err)
	}
	if err := s.RenewLease(ctx, l, later); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("已释放的租约不能续: %v", err)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolReleasing || c.LeaseExpiresAt != nil {
		t.Fatalf("续租失败不应改动已释放的卡: %+v", c)
	}
}

// 过期但还没被回收的租约，持有者续上之后就不再被到期回收。
func TestRenewExpiredLeaseBeforeReclaim(t *testing.T) {
	ctx := context.Background()
	s, _, clock, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	l := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt", OrderRef: "o1"})
	clock.Advance(defaultLease + time.Minute)
	if err := s.RenewLease(ctx, l, clock.Now().Add(5*time.Minute)); err != nil {
		t.Fatalf("过期未回收的租约应能续: %v", err)
	}
	if err := s.expireLeasesOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolLeased || c.LeaseToken != l.Token {
		t.Fatalf("续上之后不应被回收: %+v", c)
	}
}

// 到期回收先查出到期的卡、再逐张释放。中间被持有者续上的卡不能照旧释放：释放时要再核对一次到期时间。
func TestReleaseExpiredSkipsRenewed(t *testing.T) {
	ctx := context.Background()
	s, _, clock, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	l := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt", OrderRef: "o1"})
	clock.Advance(defaultLease + time.Minute)
	// 回收协程查出了 c1，还没释放时持有者续上了
	if err := s.RenewLease(ctx, l, clock.Now().Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.releaseExpiredCard(ctx, "c1", l.Token); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("已续上的租约不应按到期释放: %v", err)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolLeased {
		t.Fatalf("%+v", c)
	}
	clock.Advance(5*time.Minute + time.Second)
	if err := s.releaseExpiredCard(ctx, "c1", l.Token); err != nil {
		t.Fatalf("续的时间也过了就该释放: %v", err)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolReleasing {
		t.Fatalf("%+v", c)
	}
}
