package handler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

const (
	// localFundCurrency 是注资币种，和 payengine 里的 fundCurrency 一致：引擎只选这个币种的卡。
	localFundCurrency = "USD"
	// walletSyncEvery 限制公开接口触发主钱包同步的频率：兑换是公开接口，不能让它带着打平台账户接口。
	walletSyncEvery = 30 * time.Second
	walletSyncLimit = 15 * time.Second
)

// planCombo 是一个套餐×地区组合的就绪情况，给后台界面展示。
type planCombo struct {
	Plan      string   `json:"plan"`
	Label     string   `json:"label"`
	Region    string   `json:"region"`
	Currency  string   `json:"currency"`
	CapMinor  int64    `json:"cap_minor"`
	FundCents int64    `json:"fund_cents"`
	Cards     int      `json:"cards"`
	PlanNote  string   `json:"plan_note,omitempty"`
	OK        bool     `json:"ok"`
	Problems  []string `json:"problems"`
}

// walletSyncFunc 决定就绪检查前怎么刷新主钱包余额：后台直接同步，公开接口限频。
type walletSyncFunc func(ctx context.Context, pool *cardpool.Service) error

func syncWalletNow(ctx context.Context, pool *cardpool.Service) error {
	ctx, cancel := context.WithTimeout(ctx, walletSyncLimit)
	defer cancel()
	return pool.SyncAccounts(ctx)
}

var (
	walletSyncMu   sync.Mutex
	walletSyncAt   time.Time
	walletSyncLast error
)

// syncWalletThrottled 最多每 30 秒真正同步一次，其间复用上一次的结果（包括失败）。
// 不跟随请求的 ctx：否则谁断开连接，这 30 秒里所有人都拿到一个「同步失败」。
func syncWalletThrottled(ctx context.Context, pool *cardpool.Service) error {
	walletSyncMu.Lock()
	defer walletSyncMu.Unlock()
	if !walletSyncAt.IsZero() && time.Since(walletSyncAt) < walletSyncEvery {
		return walletSyncLast
	}
	walletSyncLast = syncWalletNow(context.WithoutCancel(ctx), pool)
	walletSyncAt = time.Now()
	return walletSyncLast
}

func resetWalletSyncThrottle() {
	walletSyncMu.Lock()
	walletSyncAt, walletSyncLast = time.Time{}, nil
	walletSyncMu.Unlock()
}

// checkMarsWallex 是 MarsWallex 的就绪检查。plan、region 都给时检查这一个组合；否则至少要有一个可用组合。
// 发码和兑换共用：只有检查项全部通过才能发码、才能兑换。查询出错一律按未就绪。
func checkMarsWallex(ctx context.Context, plan, region string, syncWallet walletSyncFunc) (provider.Readiness, []planCombo) {
	r := provider.NewReadiness(provider.MarsWallex)
	combos := []planCombo{}

	if _, err := MarsWallexClient(); err != nil {
		log.Printf("[provider] MarsWallex 配置不完整: %v", err)
		r.FailRaw("config", "MarsWallex 凭证未配置完整，去 MarsWallex 对接页填写", err.Error())
	} else {
		r.Pass("config", "MarsWallex 配置齐全")
	}
	if _, err := secretbox.LoadKey(); err != nil {
		r.Fail("master_key", "APP_MASTER_KEY 缺失或不合法，无法加密用户会话")
	} else {
		r.Pass("master_key", "主密钥可用")
	}
	engine, pool := payEngine, cardPool
	if engine == nil {
		r.Fail("engine", "付款引擎未启动")
	} else {
		r.Pass("engine", "付款引擎已启动")
		st, err := engine.BreakerStatus(ctx)
		switch {
		case err != nil:
			log.Printf("[provider] 读取付款熔断状态失败: %v", err)
			r.Fail("breaker", "付款熔断状态查询失败，按暂停处理")
		case st.Paused:
			r.Fail("breaker", "付款已熔断暂停："+st.ReasonText)
		default:
			r.Pass("breaker", "付款没有暂停")
		}
	}
	if pool == nil {
		r.Fail("card_pool", "卡池未启动")
	} else {
		r.Pass("card_pool", "卡池已启动")
	}
	// 硬门槛：系统没有自动确认过一笔真实付款，就不能对外承诺这条路能走通。试运行单和人工定论的单都不算。
	switch ok, err := db.HasRealSucceededPayOrder(ctx); {
	case err != nil:
		log.Printf("[provider] 查询真实付款记录失败: %v", err)
		r.Fail("first_real_payment", "查询真实付款记录失败，按未通过处理")
	case !ok:
		r.Fail("first_real_payment", "需要一笔系统自动确认成功的真实付款（试运行单和人工定论的单都不算），请先在后台测试兑换跑通一笔")
	default:
		r.Pass("first_real_payment", "已有系统自动确认成功的真实付款")
	}
	if engine == nil || pool == nil {
		return r, combos
	}

	if err := syncWallet(ctx, pool); err != nil {
		log.Printf("[provider] 同步主钱包失败: %v", err)
		r.Fail("wallet_sync", "主钱包余额同步失败，无法确认余额")
	} else {
		r.Pass("wallet_sync", "主钱包余额已同步")
	}

	combos = planCombos(ctx, engine, pool, plan, region)
	if plan != "" && region != "" {
		if combos[0].OK {
			r.Pass("plan", fmt.Sprintf("%s / %s 可用", plan, region))
		}
		for _, p := range combos[0].Problems {
			r.Fail("plan", p)
		}
		return r, combos
	}
	usable := 0
	for _, c := range combos {
		if c.OK {
			usable++
		}
	}
	if usable == 0 {
		r.Fail("plan", "没有可用的套餐和地区组合")
	} else {
		r.Pass("plan", fmt.Sprintf("有 %d 个可用组合", usable))
	}
	return r, combos
}

// planCombos 列出要检查的组合。plan、region 都给时只查这一个（未启用的也要报原因）；否则列出套餐表里启用的组合。
func planCombos(ctx context.Context, engine *payengine.Service, pool *cardpool.Service, plan, region string) []planCombo {
	out := []planCombo{}
	if plan != "" && region != "" {
		return append(out, checkCombo(ctx, engine, pool, plan, region))
	}
	tbl, err := payengine.LoadPlanTable()
	if err != nil {
		log.Printf("[provider] 读取套餐表失败: %v", err)
		return out
	}
	for _, pc := range tbl {
		if plan != "" && pc.Key != plan {
			continue
		}
		for _, rg := range payengine.Regions {
			if region != "" && rg != region {
				continue
			}
			if rc, ok := pc.Regions[rg]; ok && rc.Enabled {
				out = append(out, checkCombo(ctx, engine, pool, pc.Key, rg))
			}
		}
	}
	return out
}

func checkCombo(ctx context.Context, engine *payengine.Service, pool *cardpool.Service, plan, region string) planCombo {
	c := planCombo{Plan: plan, Region: region, Problems: []string{}, PlanNote: provider.PlanNote(provider.MarsWallex, plan)}
	fail := func(format string, args ...any) { c.Problems = append(c.Problems, fmt.Sprintf(format, args...)) }
	pc, rc, err := payengine.ResolvePlan(plan, region, false)
	if err != nil {
		fail("%s / %s 不可用：%s", plan, region, err.Error())
		return c
	}
	c.Label, c.Currency, c.CapMinor, c.FundCents = pc.Label, rc.Currency, rc.CapMinor, rc.FundCents

	pol, err := cardpool.LoadPolicy()
	if err != nil {
		log.Printf("[provider] 读取卡池策略失败: %v", err)
		fail("卡池策略读取失败")
		return c
	}
	if rc.FundCents > pol.MaxFundCents {
		fail("注资金额 %d 分超过单笔上限 %d 分", rc.FundCents, pol.MaxFundCents)
	}
	if need := payengine.MaxPayDuration(engine.Timeouts()); time.Duration(pol.LeaseMinutes)*time.Minute < need {
		fail("卡租约 %d 分钟短于付款最长耗时 %s", pol.LeaseMinutes, need.Round(time.Minute))
	}
	if c.Cards, err = pool.SelectableCards(ctx, plan, localFundCurrency); err != nil {
		log.Printf("[provider] 统计可用卡失败: %v", err)
		fail("可用卡统计失败")
	} else if c.Cards == 0 {
		fail("套餐 %s 没有可用的 %s 卡", plan, localFundCurrency)
	}
	if err := pool.CheckMainWallet(ctx, localFundCurrency, rc.FundCents); err != nil {
		fail("主钱包余额不足以注资：%s", err.Error())
	}
	if left, err := pool.RemainingDailyFund(ctx); err != nil {
		log.Printf("[provider] 统计今日注资失败: %v", err)
		fail("今日注资额度统计失败")
	} else if left < rc.FundCents {
		fail("今日注资额度只剩 %d 分，不足 %d 分", left, rc.FundCents)
	}
	c.OK = len(c.Problems) == 0
	return c
}

// checkZovoCard 是 ZovoCard 的就绪检查：Key 非空且余额接口能通。
func checkZovoCard(ctx context.Context) (provider.Readiness, *cardplatform.BalanceResponse) {
	r := provider.NewReadiness(provider.ZovoCard)
	if cardplatform.LoadConfig().APIKey == "" {
		r.Fail("api_key", "未配置卡台 API Key")
		return r, nil
	}
	r.Pass("api_key", "卡台 API Key 已配置")
	ctx, cancel := context.WithTimeout(ctx, walletSyncLimit)
	defer cancel()
	bal, err := cardplatform.NewFromSettings().GetBalance(ctx)
	if err != nil {
		log.Printf("[provider] 查询卡台余额失败: %v", err)
		r.Fail("balance", "卡台余额查询失败")
		return r, nil
	}
	r.Pass("balance", "卡台余额可查询")
	return r, bal
}
