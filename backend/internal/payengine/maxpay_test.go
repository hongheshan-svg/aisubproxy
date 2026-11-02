package payengine

import (
	"testing"
	"time"
)

// 默认超时下的付款最长耗时：注资 3 分 + 填表 2 分 + 四次等结果 8 分 + 3DS 页面 1 分 + 验证码 5 分 + 被拒宽限 2 分
// + 确认 10 分 + 确认间隔 15 秒 + 扫描间隔 5 秒 + 关闭浏览器 5 秒 = 31 分 25 秒。
func TestMaxPayDurationDefault(t *testing.T) {
	got := MaxPayDuration(DefaultTimeouts())
	want := 31*time.Minute + 25*time.Second
	if got != want {
		t.Fatalf("MaxPayDuration = %v, 期望 %v", got, want)
	}
}

func TestServiceShotDirAndTimeouts(t *testing.T) {
	tt := DefaultTimeouts()
	tt.Verify = time.Minute
	s := New(Options{ShotDir: "/data/pay-shots", Timeouts: &tt})
	if s.ShotDir() != "/data/pay-shots" {
		t.Fatalf("ShotDir = %q", s.ShotDir())
	}
	if s.Timeouts().Verify != time.Minute {
		t.Fatalf("Timeouts().Verify = %v", s.Timeouts().Verify)
	}
}
