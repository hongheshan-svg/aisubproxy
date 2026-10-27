package cardpool

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAlertDedupePerBeijingDay(t *testing.T) {
	s, _, clock, log := newTestService(t)
	clock.Set(time.Date(2026, 10, 28, 15, 59, 0, 0, time.UTC)) // 北京 23:59
	s.alert("k", "a")
	s.alert("k", "b")
	if log.Count() != 1 {
		t.Fatalf("同日同 key 只发一次, got %d", log.Count())
	}
	clock.Advance(2 * time.Minute) // 北京次日 00:01
	s.alert("k", "c")
	if log.Count() != 2 {
		t.Fatalf("跨日应再发, got %d", log.Count())
	}
	s.alert("", "d")
	s.alert("", "e")
	if log.Count() != 4 {
		t.Fatalf("空 key 每次都发, got %d", log.Count())
	}
}

func TestBeijingDayStart(t *testing.T) {
	got := beijingDayStart(time.Date(2026, 10, 28, 15, 59, 0, 0, time.UTC))
	want := time.Date(2026, 10, 28, 0, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	if !got.Equal(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestBadRequestAndTerminal(t *testing.T) {
	err := badRequest("坏了")
	if !errors.Is(err, ErrBadRequest) || err.Error() != "坏了" {
		t.Fatalf("got %v", err)
	}
	for _, s := range []string{StateSuccess, StateFailed, StateNotSent} {
		if !IsTerminal(s) {
			t.Fatal(s)
		}
	}
	for _, s := range []string{StatePending, StateProcessing, StateUnknown, StateNeedsReview} {
		if IsTerminal(s) {
			t.Fatal(s)
		}
	}
}

func TestGetCardNotFound(t *testing.T) {
	newTestService(t)
	ctx := context.Background()
	if _, err := getCard(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if _, err := getOrder(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestInsertAndGetCardOrder(t *testing.T) {
	newTestService(t)
	ctx := context.Background()
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	c, err := getCard(ctx, "c1")
	if err != nil || c.Expiry != "1028" || c.PoolState != PoolAvailable || c.Currency != "USD" || c.Source != "opened" || c.CreatedAt == 0 {
		t.Fatalf("%+v %v", c, err)
	}
	o := insertOrder(t, Order{Kind: KindRecharge, CardID: "c1"})
	got, err := getOrder(ctx, o.OutOrderNo)
	if err != nil || got.State != StatePending || got.CardID != "c1" {
		t.Fatalf("%+v %v", got, err)
	}
}
