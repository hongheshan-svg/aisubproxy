package cardpool

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// 卡余额和钱包余额同一套规则（C1-03）：先去空白、多余小数向下取整；负数单独成错误，和格式错误分开。
func TestParseBalanceMatchesWalletRules(t *testing.T) {
	ok := map[string]int64{
		"":         0,
		"   ":      0,
		" 0.00":    0,
		"0.00 ":    0,
		"12.345":   1234,
		"12.349":   1234,
		"12.3":     1230,
		"12":       1200,
		" 5.10\t":  510,
		"-0.00":    0,
		"-0.004":   0,
		"100.9999": 10099,
	}
	for in, want := range ok {
		got, err := parseBalance(in)
		if err != nil || got != want {
			t.Errorf("parseBalance(%q) = %d, %v；应为 %d", in, got, err, want)
		}
	}
	for _, in := range []string{"-0.35", " -12.345 ", "-1"} {
		_, err := parseBalance(in)
		if !errors.Is(err, ErrNegativeBalance) {
			t.Errorf("parseBalance(%q) 应报负数错误，得到 %v", in, err)
		}
	}
	for _, in := range []string{"1,000.00", "abc", "+1.00", "1.", ".5", "--1", "-abc"} {
		_, err := parseBalance(in)
		if err == nil {
			t.Errorf("parseBalance(%q) 应报格式错误", in)
			continue
		}
		if errors.Is(err, ErrNegativeBalance) {
			t.Errorf("parseBalance(%q) 是格式错误，不应当成负数: %v", in, err)
		}
	}
}

// 平台对卡返回 3 位小数或首尾带空白的余额时，刷新照常成功，按分向下取整（以前整条刷新失败）。
func TestRefreshCardAcceptsExtraDecimalsAndSpaces(t *testing.T) {
	s, mw, _, alerts := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing})
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "12.345", " 0.50 ", "ACTIVE")))

	c, err := s.RefreshCard(context.Background(), "c1")
	if err != nil {
		t.Fatalf("刷新应成功: %v", err)
	}
	if c.AvailableCents != 1234 || c.FrozenCents != 50 {
		t.Fatalf("余额应向下取整到分: avail=%d frozen=%d", c.AvailableCents, c.FrozenCents)
	}
	if alerts.Count() != 0 {
		t.Fatalf("正常余额不应告警: %v", alerts.Texts())
	}
}

// 平台返回负数余额：刷新失败、错误能被识别为负数，并告警（按卡每天一条）；格式错误不告警、也不算负数。
func TestRefreshCardNegativeBalanceAlertsOncePerDay(t *testing.T) {
	s, mw, clock, alerts := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolReleasing, AvailableCents: 300})
	mw.On("GET", detailPath("c1"), mwtest.OK(detailJSON("c1", "-0.35", "0.00", "ACTIVE")))

	_, err := s.RefreshCard(context.Background(), "c1")
	if !errors.Is(err, ErrNegativeBalance) {
		t.Fatalf("应返回负数余额错误: %v", err)
	}
	if !strings.Contains(err.Error(), "-0.35") || strings.Contains(err.Error(), "格式") {
		t.Fatalf("错误文字应写明负数和原值，不和格式错误混在一起: %v", err)
	}
	if c := mustGetCard(t, "c1"); c.AvailableCents != 300 {
		t.Fatalf("负数余额不应写进本地: %+v", c)
	}
	got := alerts.Texts()
	if len(got) != 1 || !strings.Contains(got[0], "c1") || !strings.Contains(got[0], "负数") || !strings.Contains(got[0], "-0.35") {
		t.Fatalf("应告警一次，写明卡和负数余额: %v", got)
	}

	if _, err := s.RefreshCard(context.Background(), "c1"); !errors.Is(err, ErrNegativeBalance) {
		t.Fatalf("第二次刷新仍应报负数: %v", err)
	}
	if alerts.Count() != 1 {
		t.Fatalf("同一天同一张卡只告警一次: %v", alerts.Texts())
	}
	clock.Advance(24 * time.Hour)
	if _, err := s.RefreshCard(context.Background(), "c1"); !errors.Is(err, ErrNegativeBalance) {
		t.Fatalf("第二天仍应报负数: %v", err)
	}
	if alerts.Count() != 2 {
		t.Fatalf("第二天应再告警一次: %v", alerts.Texts())
	}

	insertCard(t, Card{CardID: "c2", ProductID: "p1", PoolState: PoolReleasing})
	mw.On("GET", detailPath("c2"), mwtest.OK(detailJSON("c2", "1,000.00", "0.00", "ACTIVE")))
	_, err = s.RefreshCard(context.Background(), "c2")
	if err == nil || errors.Is(err, ErrNegativeBalance) {
		t.Fatalf("格式错误应报错且不算负数: %v", err)
	}
	if alerts.Count() != 2 {
		t.Fatalf("格式错误不发负数告警: %v", alerts.Texts())
	}
}

// 导入和导入预览遇到负数余额：报明确的负数错误（管理员当场看到，不另外告警）。
func TestImportAndPreviewNegativeBalance(t *testing.T) {
	s, mw, _, alerts := newTestService(t)
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("GET", detailPath("c1"), mwtest.OK(cardJSON("USD", "-1.20", "0.00", "ACTIVE")))

	_, err := s.PreviewCard(context.Background(), "c1")
	if !errors.Is(err, ErrNegativeBalance) || !strings.Contains(err.Error(), "可用余额为负数") {
		t.Fatalf("预览应报可用余额为负数: %v", err)
	}
	_, err = s.ImportCard(context.Background(), "c1", "p1", "", "admin")
	if !errors.Is(err, ErrNegativeBalance) || !strings.Contains(err.Error(), "可用余额为负数") {
		t.Fatalf("导入应报可用余额为负数: %v", err)
	}
	if alerts.Count() != 0 {
		t.Fatalf("后台当场操作不另外告警: %v", alerts.Texts())
	}
}
