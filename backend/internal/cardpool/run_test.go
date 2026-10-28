package cardpool

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// waitFor 每 10ms 检查一次 cond，d 内成立返回 true。
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStartProcessesKickedEventPromptly(t *testing.T) {
	s, _, _, _ := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	// 测试库在 Cleanup 时还原，先等协程全部退出
	t.Cleanup(func() { cancel(); s.loops.Wait() })

	id := enqueueEvent(t, "SOMETHING_NEW", `{}`)
	s.Kick()
	if !waitFor(time.Second, func() bool { return getEvent(t, id).processed }) {
		t.Fatal("Kick 后 1 秒内未处理")
	}
	mustBeProcessed(t, id, "未知 topic")
}

func TestStartStopsOnCancel(t *testing.T) {
	s, _, _, _ := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	cancel()

	stopped := make(chan struct{})
	go func() { s.loops.Wait(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("cancel 后协程未退出")
	}

	kicked := make(chan struct{})
	go func() {
		for i := 0; i < 3; i++ {
			s.Kick()
		}
		close(kicked)
	}()
	select {
	case <-kicked:
	case <-time.After(time.Second):
		t.Fatal("Kick 阻塞")
	}

	id := enqueueEvent(t, "SOMETHING_NEW", `{}`)
	s.Kick()
	time.Sleep(200 * time.Millisecond)
	if getEvent(t, id).processed {
		t.Fatal("停止后不应再处理事件")
	}
}

// 只读写本地库的步骤（释放到期租约、清除过期验证码）先于需要调接口的步骤：平台变慢不能拖延它们。
func TestMaintainRoundRunsDBStepsFirst(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	past := clock.Now().Unix() - 60
	insertCard(t, Card{CardID: "L1", ProductID: "p1", PoolState: PoolLeased, LeaseToken: "tok", LeaseOwner: "o", LeaseExpiresAt: &past})
	if _, err := db.DB.Exec(`INSERT INTO mw_3ds_otps (card_id, otp_sealed, received_at) VALUES ('L1', 'v1:x', ?)`,
		clock.Now().Unix()-otpTTLSeconds-1); err != nil {
		t.Fatal(err)
	}
	mw.On("GET", detailPath("L1"), mwtest.OK(detailJSON("L1", "0.00", "0.00", "ACTIVE")))
	called, leaseReleased, otpPurged := false, false, false
	s.opts.Client = func() (*marswallex.Client, error) {
		if !called {
			called = true
			if c, err := getCard(context.Background(), "L1"); err == nil {
				leaseReleased = c.PoolState != PoolLeased
			}
			var sealed string
			if err := db.DB.QueryRow(`SELECT otp_sealed FROM mw_3ds_otps WHERE card_id = 'L1'`).Scan(&sealed); err == nil {
				otpPurged = sealed == ""
			}
		}
		return mw.Client(), nil
	}
	s.maintainRound(context.Background(), &loopLog{now: clock.Now, last: map[string]time.Time{}})
	if !called || !leaseReleased || !otpPurged {
		t.Fatalf("第一次取客户端前应已释放到期租约并清除过期验证码: called=%v lease=%v otp=%v", called, leaseReleased, otpPurged)
	}
}

// 「未就绪」只在就绪状态变化时记一次日志（就绪 → 未就绪、未就绪 → 就绪），不每 10 分钟重复。
func TestReadinessLoggedOnlyOnChange(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	broken := true
	s.opts.Client = func() (*marswallex.Client, error) {
		if broken {
			return nil, errors.New("未配置")
		}
		return mw.Client(), nil
	}
	ctx := context.Background()
	mlg := &loopLog{now: clock.Now, last: map[string]time.Time{}}
	rlg := &loopLog{now: clock.Now, last: map[string]time.Time{}}
	round := func() {
		s.maintainRound(ctx, mlg)
		s.replenishRound(ctx, rlg)
	}
	for i := 0; i < 3; i++ {
		round()
		clock.Advance(11 * time.Minute)
	}
	if n := strings.Count(buf.String(), "未就绪"); n != 2 {
		t.Fatalf("对账与补卡各应只记一次未就绪, got %d:\n%s", n, buf.String())
	}
	broken = false
	round()
	round()
	if n := strings.Count(buf.String(), "已就绪"); n != 2 {
		t.Fatalf("恢复后对账与补卡各应只记一次已就绪, got %d:\n%s", n, buf.String())
	}
	broken = true
	round()
	round()
	if n := strings.Count(buf.String(), "未就绪"); n != 4 {
		t.Fatalf("再次不可用应各再记一次, got %d:\n%s", n, buf.String())
	}
}
