package handler

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

func s2CountPayOrders(t *testing.T) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM pay_orders`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// D-8：同账号已有待人工的真实单时，后台真实付款入口必须拒绝，不建新单。
func TestS2AdminPayRealRefusedWhenEmailHasNeedsReview(t *testing.T) {
	payEnv(t)
	now := time.Now().Unix()
	if _, err := db.DB.Exec(`INSERT INTO pay_orders (order_no, account_email, state, created_at, updated_at) VALUES (?, ?, 'needs_review', ?, ?)`,
		"POS2REVIEW000001", "dup@example.com", now, now); err != nil {
		t.Fatal(err)
	}
	before := s2CountPayOrders(t)
	w := payDo(AdminPaySubmit, "POST", "/orders", "/orders", paySubmitBody("plus", "PH", paySession("Dup@Example.com"), false))
	if w.Code != http.StatusConflict {
		t.Fatalf("应 409 拒绝: %d %s", w.Code, w.Body.String())
	}
	if after := s2CountPayOrders(t); after != before {
		t.Fatalf("被拒时不能建单: %d -> %d", before, after)
	}
	// 试运行不动钱，不受这条限制
	if w := payDo(AdminPaySubmit, "POST", "/orders", "/orders", paySubmitBody("plus", "PH", paySession("dup@example.com"), true)); w.Code != http.StatusOK {
		t.Fatalf("试运行应放行: %d %s", w.Code, w.Body.String())
	}
}

// D-8：熔断状态查不出来（或已暂停）时拒绝真实付款。
func TestS2AdminPayRealRefusedWhenBreakerPaused(t *testing.T) {
	payEnv(t)
	if err := db.SetSetting("pay_breaker_reset", `{broken`); err != nil {
		t.Fatal(err)
	}
	w := payDo(AdminPaySubmit, "POST", "/orders", "/orders", paySubmitBody("plus", "PH", paySession("brk@example.com"), false))
	if w.Code != http.StatusServiceUnavailable || s2CountPayOrders(t) != 0 {
		t.Fatalf("熔断不明应拒绝且不建单: %d %s", w.Code, w.Body.String())
	}
}

// D-8：卡租约短于付款最长耗时时拒绝真实付款。
func TestS2AdminPayRealRefusedWhenLeaseTooShort(t *testing.T) {
	payEnv(t)
	savePoolLease(t, 5)
	w := payDo(AdminPaySubmit, "POST", "/orders", "/orders", paySubmitBody("plus", "PH", paySession("lease@example.com"), false))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "租约") || s2CountPayOrders(t) != 0 {
		t.Fatalf("租约过短应拒绝且不建单: %d %s", w.Code, w.Body.String())
	}
}

// D-8：条件都满足时真实付款照常建单。
func TestS2AdminPayRealPassesGates(t *testing.T) {
	payEnv(t)
	w := payDo(AdminPaySubmit, "POST", "/orders", "/orders", paySubmitBody("plus", "PH", paySession("ok@example.com"), false))
	if w.Code != http.StatusOK {
		t.Fatalf("应放行: %d %s", w.Code, w.Body.String())
	}
}
