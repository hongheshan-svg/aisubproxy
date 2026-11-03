package handler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// opsMaintenanceEvery 是 CDK 链路后台维护（清理到期会话等）的间隔。
const opsMaintenanceEvery = 10 * time.Minute

var (
	opsAlertMu   sync.Mutex
	opsAlertFn   func(string)
	opsAlertSent = map[string]string{} // key → 最后发送时所在的北京日
)

var beijingZone = time.FixedZone("UTC+8", 8*3600)

// SetOpsAlert 注入 CDK 链路的告警发送函数（服务启动时调用）；nil 时只写日志。
func SetOpsAlert(fn func(string)) {
	opsAlertMu.Lock()
	opsAlertFn = fn
	opsAlertMu.Unlock()
}

func resetOpsAlertDedupe() {
	opsAlertMu.Lock()
	opsAlertSent = map[string]string{}
	opsAlertMu.Unlock()
}

// opsAlert 发送一条告警。文本不得含会话、完整卡密、密码。
func opsAlert(text string) {
	opsAlertMu.Lock()
	fn := opsAlertFn
	opsAlertMu.Unlock()
	if fn == nil {
		log.Printf("[cdk] 告警: %s", text)
		return
	}
	fn(text)
}

// opsAlertDaily 同一 key 在同一北京日只发一次：用于会反复触发的状态类告警。
func opsAlertDaily(key, text string, now time.Time) {
	day := now.In(beijingZone).Format("2006-01-02")
	opsAlertMu.Lock()
	if opsAlertSent[key] == day {
		opsAlertMu.Unlock()
		return
	}
	opsAlertSent[key] = day
	opsAlertMu.Unlock()
	opsAlert(text)
}

// agentExchangeStaleAfter 换码占位卡在 pending 多久算没收尾。必须明显大于发码请求的超时（3 分钟）。
const agentExchangeStaleAfter = 10 * time.Minute

// runOpsMaintenance 跑一轮 CDK 链路维护。每一项失败只记日志，不影响其它项。
func runOpsMaintenance(now time.Time) {
	sweepCDKSessionsOnce(now)
	escalateAgentExchanges(now)
}

// escalateAgentExchanges 把没收尾的换码占位转人工，并按天提醒还有多少笔等待核对。
func escalateAgentExchanges(now time.Time) {
	stale, err := db.EscalateStaleAgentExchanges(now, agentExchangeStaleAfter)
	if err != nil {
		log.Printf("[agent-exchange] 升级超时占位失败: %v", err)
	}
	for _, e := range stale {
		opsAlert(fmt.Sprintf("代理换码占位超时没有收尾（进程可能在发码途中重启），已转人工：记录 %d，旧码 ID %d。请先到卡台核对是否已发码，再到后台定论。", e.ID, e.OldUpstreamID))
	}
	n, err := db.CountAgentExchanges(db.AgentExchangeUnknown)
	if err != nil {
		log.Printf("[agent-exchange] 统计待核对记录失败: %v", err)
		return
	}
	if n > 0 {
		opsAlertDaily("agent-swap-unknown", fmt.Sprintf("还有 %d 笔代理换码结果不明，等待人工核对。", n), now)
	}
}

func sweepCDKSessionsOnce(now time.Time) {
	res, err := db.SweepCDKSessions(now)
	if err != nil {
		log.Printf("[cdk-session] 清理会话失败: %v", err)
	}
	if res.Cleared > 0 {
		log.Printf("[cdk-session] 已清空 %d 个到期或已完成订单的会话", res.Cleared)
	}
	if res.PlaintextLeft > 0 {
		opsAlertDaily("cdk-session-plaintext", fmt.Sprintf(
			"数据库里还有 %d 行用户会话是明文：APP_MASTER_KEY 缺失或不合法，加密和到期清理都已跳过。配置密钥并重启后会自动加密。", res.PlaintextLeft), now)
	}
}

// StartOpsMaintenance 启动时先跑一轮，之后每 10 分钟一轮，ctx 结束时停止。
func StartOpsMaintenance(ctx context.Context) {
	go func() {
		runOpsMaintenance(time.Now())
		ticker := time.NewTicker(opsMaintenanceEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runOpsMaintenance(time.Now())
			}
		}
	}()
}
