package cardpool

import (
	"fmt"
	"strings"
)

// alertSource 是发现事件的路径。同一事件不论从哪条路径发现，都由同一个函数生成告警文字，
// 只有末尾的「（来源：…）」不同，其余逐字相同；告警 key 与来源无关，所以两条路径先后发现同一事件也只发一次。
type alertSource string

const (
	sourceWebhook alertSource = "平台推送"
	sourcePoll    alertSource = "定时查询"
	sourceRecycle alertSource = "清零时查询"
	sourceAdmin   alertSource = "后台操作" // 后台导入、刷新、修改、启停持卡人
)

func (src alertSource) suffix() string { return "（来源：" + string(src) + "）" }

// extFreezeDisabledText：卡在平台上被外部冻结，已从卡池停用（可用卡由推送、定时查询发现，清零中的卡由回收发现）。key extfreeze:<card_id>。
func extFreezeDisabledText(cardID string, src alertSource) string {
	return fmt.Sprintf("MarsWallex 卡 %s 在平台上被冻结（非本系统发起），已从卡池停用（原因：平台冻结），请到 MarsWallex 后台核查", cardID) +
		src.suffix()
}

// extFreezeLeasedText：租用中的卡在平台上被外部冻结，只告警、不改池状态。key extfreeze:<card_id>。
func extFreezeLeasedText(c *Card, src alertSource) string {
	return fmt.Sprintf("MarsWallex 卡 %s 租用中%s在平台上被冻结（非本系统发起），请检查对应订单", c.CardID, ownerSuffix(c)) + src.suffix()
}

// extCancelLeasedText：租用中的卡在平台上被外部注销，已退役。key extcancel:<card_id>。
func extCancelLeasedText(c *Card, src alertSource) string {
	return fmt.Sprintf("MarsWallex 卡 %s 租用中%s在平台上被注销（非本系统发起），已退役，请检查对应订单", c.CardID, ownerSuffix(c)) + src.suffix()
}

// holderAlert 返回持卡人状态由 old 变为 status 时要发的告警（spec §7.5）；key 为空表示不告警。
// 通过通知 key holderok:<id>，驳回 holderrej:<id>（单独去重，免得驳回原因被同一天更早的 holder: 告警吞掉），其余 holder:<id>。
func holderAlert(id, old, status, remark string, src alertSource) (key, text string) {
	old, status, remark = normStatus(old), normStatus(status), strings.TrimSpace(remark)
	switch status {
	case holderApproved:
		if old == holderApproved {
			return "", ""
		}
		key, text = "holderok:"+id, fmt.Sprintf("MarsWallex 持卡人 %s 已通过审核，可在策略中加入补卡持卡人", id)
	case holderPendingReview:
		if old != holderApproved {
			return "", ""
		}
		key, text = "holder:"+id, fmt.Sprintf("MarsWallex 持卡人 %s 由 APPROVED 变为 PENDING_REVIEW（修改后重新审核），自动补卡将跳过该持卡人", id)
	case holderRejected:
		reason := remark
		if reason == "" {
			reason = "平台未提供"
		}
		key, text = "holderrej:"+id, fmt.Sprintf("MarsWallex 持卡人 %s 审核被驳回（REJECTED），自动补卡将跳过该持卡人。驳回原因：%s", id, truncate(reason, 200))
	default:
		key, text = "holder:"+id, fmt.Sprintf("MarsWallex 持卡人 %s 状态变为 %s，自动补卡将跳过该持卡人", id, status)
		if remark != "" {
			text += "。平台备注：" + truncate(remark, 200)
		}
	}
	return key, text + src.suffix()
}
