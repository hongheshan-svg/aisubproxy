package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// reconcileAgentBalances 每个北京日核对一次代理商的余额和流水（余额 = 流水之和 = 最后一条流水的变动后余额）。
// 结果写进 agent_reconcile_last（形状同 agentReconcileView）；有对不上的发一条告警，只写代理商 ID 和差额。
// 当天已核对过就跳过；核对本身出错只记日志，不写结果，下一轮再试。
func reconcileAgentBalances(now time.Time) {
	today := now.In(beijingZone).Format("2006-01-02")
	raw, err := db.GetSetting(agentReconcileKey)
	if err != nil {
		log.Printf("[agent-reconcile] 读取上次核对结果失败: %v", err)
		return
	}
	if strings.TrimSpace(raw) != "" {
		var last agentReconcileView
		if json.Unmarshal([]byte(raw), &last) == nil && last.At > 0 && time.Unix(last.At, 0).In(beijingZone).Format("2006-01-02") == today {
			return
		}
	}
	list, err := db.CheckAgentBalances(context.Background())
	if err != nil {
		log.Printf("[agent-reconcile] 核对余额失败: %v", err)
		return
	}
	res := agentReconcileView{At: now.Unix(), OK: len(list) == 0, Mismatches: []agentReconcileMismatch{}}
	parts := make([]string, 0, len(list))
	for _, m := range list {
		diff := m.BalanceCents - m.LedgerSumCents
		if diff == 0 {
			diff = m.BalanceCents - m.LastAfterCents
		}
		res.Mismatches = append(res.Mismatches, agentReconcileMismatch{AgentID: m.AgentID, DiffCents: diff})
		parts = append(parts, fmt.Sprintf("代理商 %d 差 %s 美元", m.AgentID, agent.FormatCents(diff)))
	}
	b, err := json.Marshal(res)
	if err != nil {
		log.Printf("[agent-reconcile] 序列化核对结果失败: %v", err)
		return
	}
	if err := db.SetSetting(agentReconcileKey, string(b)); err != nil {
		log.Printf("[agent-reconcile] 保存核对结果失败: %v", err)
	}
	if len(list) > 0 {
		opsAlertDaily("agent-reconcile", "代理商余额与流水对不上："+strings.Join(parts, "；")+"。请到后台代理商页核对流水。", now)
	}
}
