package cardpool

import (
	"context"
	"errors"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

func TestCardInfo(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := newTestService(t)
	if _, err := db.DB.Exec(`INSERT INTO mw_cardholders (card_holder_id, first_name, last_name, status, created_at) VALUES ('h1', 'JOHN', 'DOE', 'APPROVED', 1)`); err != nil {
		t.Fatal(err)
	}
	insertCard(t, Card{CardID: "c1", CardHolderID: "h1", ProductID: "p1", Last4: "4242"})
	insertCard(t, Card{CardID: "c2", CardHolderID: "h-missing", ProductID: "p1", Last4: "1111", Currency: "PHP"})

	got, err := s.CardInfo(ctx, "c1")
	if err != nil || got != (CardInfo{Last4: "4242", Currency: "USD", HolderFirstName: "JOHN", HolderLastName: "DOE"}) {
		t.Fatalf("卡信息不符: %+v %v", got, err)
	}
	got, err = s.CardInfo(ctx, "c2")
	if err != nil || got != (CardInfo{Last4: "1111", Currency: "PHP"}) {
		t.Fatalf("持卡人不存在时姓名应为空: %+v %v", got, err)
	}
	if _, err := s.CardInfo(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知卡应为 ErrNotFound: %v", err)
	}
}
