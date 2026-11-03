//go:build live

package chatgpt

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func TestLiveCreateCheckout(t *testing.T) {
	path := os.Getenv("CHATGPT_SESSION_FILE")
	if path == "" {
		t.Skip("未设置 CHATGPT_SESSION_FILE")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseSession(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(os.Getenv("CHATGPT_PROXY"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Refresh(ctx, &s); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	acc, err := c.Account(ctx, s)
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	fmt.Println("PlanType:", acc.PlanType)
	co, err := c.CreateCheckout(ctx, s, "chatgptplusplan", "PH", "PHP")
	if err != nil {
		t.Fatalf("CreateCheckout: %v", err)
	}
	id := co.SessionID
	if len(id) > 12 {
		id = id[:12]
	}
	fmt.Println("SessionID:", id)
}
