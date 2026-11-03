package paybrowser_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// assertLockedAfterNoClick 断言 LockSubmit 证明了没点击，之后也点不了，页面确实没被点过。
func assertLockedAfterNoClick(t *testing.T, f *fakeCheckout, b *paybrowser.Browser, ctx context.Context) {
	t.Helper()
	if b.LockSubmit() {
		t.Fatal("没有点击时 LockSubmit 应返回 false")
	}
	if err := b.Submit(ctx); !errors.Is(err, paybrowser.ErrSubmitLocked) {
		t.Fatalf("锁定后 Submit = %v，期望 ErrSubmitLocked", err)
	}
	time.Sleep(300 * time.Millisecond)
	if n, pays := f.submitClicks.Load(), len(f.payments()); n != 0 || pays != 0 {
		t.Fatalf("不应点击：点了 %d 次，/pay %d 次", n, pays)
	}
}

// PF-8：点击前置位的标志没动过，才能证明没点击。这几种情况都走到点击之前就停了。
func TestLockSubmitProvesNotClicked(t *testing.T) {
	t.Run("找不到支付按钮", func(t *testing.T) {
		needChrome(t)
		t.Cleanup(paybrowser.SetFieldWait(1500 * time.Millisecond))
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, stripeCheckout, "nopay=1", paybrowser.LaunchOptions{})
		if err := b.Fill(ctx, testCard, testBilling); err != nil {
			t.Fatalf("Fill: %v", err)
		}
		if err := b.Submit(ctx); err == nil {
			t.Fatal("找不到支付按钮时 Submit 应报错")
		}
		assertLockedAfterNoClick(t, f, b, ctx)
	})
	t.Run("支付按钮一直不可点", func(t *testing.T) {
		needChrome(t)
		t.Cleanup(paybrowser.SetFieldWait(1500 * time.Millisecond))
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, stripeCheckout, "paydisabled=1", paybrowser.LaunchOptions{})
		if err := b.Fill(ctx, testCard, testBilling); err != nil {
			t.Fatalf("Fill: %v", err)
		}
		if err := b.Submit(ctx); err == nil {
			t.Fatal("支付按钮不可点时 Submit 应报错")
		}
		assertLockedAfterNoClick(t, f, b, ctx)
	})
	t.Run("等按钮时超时", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, stripeCheckout, "paydisabled=1", paybrowser.LaunchOptions{})
		if err := b.Fill(ctx, testCard, testBilling); err != nil {
			t.Fatalf("Fill: %v", err)
		}
		sctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		if err := b.Submit(sctx); err == nil {
			t.Fatal("超时时 Submit 应报错")
		}
		assertLockedAfterNoClick(t, f, b, ctx)
	})
	t.Run("没填表就提交", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, stripeCheckout, "", paybrowser.LaunchOptions{})
		if err := b.Submit(ctx); err == nil {
			t.Fatal("没填表时 Submit 应报错")
		}
		assertLockedAfterNoClick(t, f, b, ctx)
	})
}

// PF-8：标志置位之后的任何错误都不能证明没点击。这条直接关系重复付款：
// 鼠标事件发出去之后才报错的，页面已经收到点击，记成「未提交」用户就会再付一次。
func TestLockSubmitAfterClickError(t *testing.T) {
	boom := errors.New("CDP 连接断开")
	cases := []struct {
		name       string
		afterMouse bool
		clicks     int32 // 页面实际收到的点击
	}{
		{"鼠标发出之前出错", false, 0},
		{"鼠标发出之后出错", true, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			needChrome(t)
			f := newFakeCheckout(t)
			b, ctx := openKind(t, f, stripeCheckout, "mode=none", paybrowser.LaunchOptions{})
			if err := b.Fill(ctx, testCard, testBilling); err != nil {
				t.Fatalf("Fill: %v", err)
			}
			paybrowser.FailClick(b, c.afterMouse, boom)
			if err := b.Submit(ctx); !errors.Is(err, boom) {
				t.Fatalf("Submit = %v，期望点击错误", err)
			}
			if !b.LockSubmit() {
				t.Fatal("点击过程中出错时 LockSubmit 必须返回 true，否则会被记成未提交")
			}
			if err := b.Submit(ctx); !errors.Is(err, paybrowser.ErrAlreadySubmitted) {
				t.Fatalf("再次 Submit = %v，期望 ErrAlreadySubmitted", err)
			}
			time.Sleep(300 * time.Millisecond)
			if n := f.submitClicks.Load(); n != c.clicks {
				t.Fatalf("页面收到 %d 次点击，期望 %d 次", n, c.clicks)
			}
		})
	}
	t.Run("正常点击之后", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, stripeCheckout, "mode=none", paybrowser.LaunchOptions{})
		fillAndSubmit(t, b, ctx)
		if !b.LockSubmit() {
			t.Fatal("点击之后 LockSubmit 必须返回 true")
		}
	})
}

// 先锁定再填表（试运行的做法）：填表不受影响，但绝不点击。
func TestLockedBrowserStillFillsButNeverClicks(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	b, ctx := openKind(t, f, stripeCheckout, "mode=success", paybrowser.LaunchOptions{})
	if b.LockSubmit() {
		t.Fatal("还没点击时 LockSubmit 应返回 false")
	}
	if err := b.Fill(ctx, testCard, testBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	assertLockedAfterNoClick(t, f, b, ctx)
}

// 正在等支付按钮时被锁定：等到按钮出现也不能点。这是锁内那次复查要挡住的情形。
func TestLockWhileSubmitWaitingForButton(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	b, ctx := openKind(t, f, stripeCheckout, "latepay=1&mode=success", paybrowser.LaunchOptions{})
	if err := b.Fill(ctx, testCard, testBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	// Fill 刚填完邮编不久，按钮要过一会儿才出现
	done := make(chan error, 1)
	go func() { done <- b.Submit(ctx) }()
	time.Sleep(50 * time.Millisecond)
	locked := b.LockSubmit()
	err := <-done
	if locked {
		t.Skip("这台机器上按钮已经先出现、点击先发生了，碰不到要测的时序")
	}
	if !errors.Is(err, paybrowser.ErrSubmitLocked) {
		t.Fatalf("Submit = %v，期望 ErrSubmitLocked", err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := f.submitClicks.Load(); n != 0 {
		t.Fatalf("锁定后页面收到 %d 次点击", n)
	}
}
