package paybrowser_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// 点数单填表后要确认「保存付款信息」没有勾上：勾着就取消，取消后照常付款，页面收到的是未勾选。
func TestEnsureNoSavePayment(t *testing.T) {
	cases := []struct {
		name, query, note string
	}{
		{"默认勾上要取消", "mode=success&savepay=on", "已取消"},
		{"本来就没勾", "mode=success&savepay=off", "未勾选"},
		{"页面没有这个勾选框", "mode=success", "页面没有"},
		// 真实点数页：两个勾选框同时在。条款文案含「store your payment method」，不能被当成「保存付款信息」取消掉，否则提交会被拦下
		{"不动同意条款", "mode=success&savepay=on&terms=1", "已取消"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			needChrome(t)
			f := newFakeCheckout(t)
			b, ctx := openKind(t, f, stripeCheckout, c.query, paybrowser.LaunchOptions{})
			if err := b.Fill(ctx, testCard, testBilling); err != nil {
				t.Fatalf("Fill: %v", err)
			}
			note, err := b.EnsureNoSavePayment(ctx)
			if err != nil || note != c.note {
				t.Fatalf("EnsureNoSavePayment = %q, %v，期望 %q", note, err, c.note)
			}
			if got := b.FillReport().SavePayment; got != c.note {
				t.Fatalf("填表报告记的是 %q", got)
			}
			if n := len(f.payments()); n != 0 {
				t.Fatalf("只处理勾选框，不能提交：仿造页收到 %d 次提交", n)
			}
			if err := b.Submit(ctx); err != nil {
				t.Fatalf("Submit: %v", err)
			}
			waitPaid(t, f, b, ctx, stripeCheckout)
			if got := onlyPayment(t, f); got.SavePayment || got.LinkSave {
				t.Fatalf("提交时「保存付款信息」= %v，Link = %v，都应为未勾选", got.SavePayment, got.LinkSave)
			}
		})
	}
}

// 取消不掉就报错，调用方按「未提交」放弃；整个过程绝不点支付按钮。
func TestEnsureNoSavePaymentStuck(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	b, ctx := openKind(t, f, stripeCheckout, "mode=success&savepay=on&savepaystuck=1", paybrowser.LaunchOptions{})
	if err := b.Fill(ctx, testCard, testBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	note, err := b.EnsureNoSavePayment(ctx)
	if err == nil || !strings.Contains(err.Error(), "保存付款信息") || note != "无法确认" {
		t.Fatalf("EnsureNoSavePayment = %q, %v，期望报错", note, err)
	}
	if got := b.FillReport().SavePayment; got != "无法确认" {
		t.Fatalf("填表报告记的是 %q", got)
	}
	for _, secret := range []string{testCard.Number, testCard.CVC, testEmail} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("错误消息泄露了 %q: %v", secret, err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if n := f.submitClicks.Load(); n != 0 {
		t.Fatalf("支付按钮被点了 %d 次", n)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("仿造页收到 %d 次提交", n)
	}
}
