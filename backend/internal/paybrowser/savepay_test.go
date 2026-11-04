package paybrowser_test

import (
	"context"
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

// 「保存付款信息」做成不写 type 的 <button role="checkbox" aria-checked="true">、放在结账表单里（终审 I2）：
// 表单里不写 type 的 button 默认就是提交按钮，点它（鼠标或脚本 click()）就是一次没有先置位的付款。
// 点之前先看是不是提交控件，是就不点、报错，调用方按「未提交」放弃。托管页、内嵌页都一样。
func TestEnsureNoSavePaymentRefusesSubmitControl(t *testing.T) {
	check := func(t *testing.T, b *paybrowser.Browser, ctx context.Context) {
		t.Helper()
		note, err := b.EnsureNoSavePayment(ctx)
		if err == nil || !strings.Contains(err.Error(), "目标是提交控件，拒绝点击") || note != "无法确认" {
			t.Fatalf("EnsureNoSavePayment = %q, %v，期望「无法确认」并报「目标是提交控件，拒绝点击」", note, err)
		}
		if got := b.FillReport().SavePayment; got != "无法确认" {
			t.Fatalf("填表报告记的是 %q", got)
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Run("托管页", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, stripeCheckout, "mode=success&savepay=button", paybrowser.LaunchOptions{})
		if err := b.Fill(ctx, testCard, testBilling); err != nil {
			t.Fatalf("Fill: %v", err)
		}
		check(t, b, ctx)
		if n := f.submitClicks.Load(); n != 0 {
			t.Fatalf("支付表单被提交了 %d 次", n)
		}
		if n := len(f.payments()); n != 0 {
			t.Fatalf("仿造页收到 %d 次提交", n)
		}
	})
	t.Run("内嵌页", func(t *testing.T) {
		needChrome(t)
		t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
		f := newFakeEmbedded(t)
		b, ctx := openEmbedded(t, f, "savepaybutton=1&payclicks=1")
		if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
			t.Fatalf("Fill: %v", err)
		}
		check(t, b, ctx)
		if n := payClicks(t, ctx, b); n != "0" {
			t.Fatalf("支付按钮收到 %s 次可信点击，期望 0", n)
		}
		if n := len(f.payments()); n != 0 {
			t.Fatalf("仿造页收到 %d 次提交", n)
		}
	})
}

// 内嵌页上用鼠标点勾选框（clickBox）、选中输入框（focusField）之前也先看目标是不是提交控件：是就不点、不设焦点，报错。
// 这里直接拿主页面的支付按钮当目标：不能点它，也不能把焦点设到它上面（之后的按键会直接触发它）。
func TestEmbeddedClickRefusesSubmitControl(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "payclicks=1")
	const pay = `#buttons button[type="submit"]`

	if err := paybrowser.ClickBox(ctx, b, pay); err == nil || err.Error() != "目标是提交控件，拒绝点击" {
		t.Fatalf("clickBox = %v，期望「目标是提交控件，拒绝点击」", err)
	}
	if direct, err := paybrowser.FocusField(ctx, b, pay); err == nil || err.Error() != "目标是提交控件，拒绝点击" {
		t.Fatalf("focusField = %q, %v，期望报「目标是提交控件，拒绝点击」", direct, err)
	}
	if n, err := paybrowser.VisibleCount(ctx, b, pay+":focus"); err != nil || n != 0 {
		t.Fatalf("焦点在支付按钮上的个数 = %d, %v，期望 0", n, err)
	}
	if n := payClicks(t, ctx, b); n != "0" {
		t.Fatalf("支付按钮收到 %s 次可信点击，期望 0", n)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("仿造页收到 %d 次提交", n)
	}
}
