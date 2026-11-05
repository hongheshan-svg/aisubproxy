package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/acgshared"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 异次元店铺共享协议的取码接口 commodity/trade（规格 7.5）：代理商的发卡站在终端客户付款后同步调用，
// 本站从代理商余额扣款、发码。下游超时后会用同一个对方订单号重发，靠下单核心按 "acg:" + 对方单号幂等重放。
// 失败一律 HTTP 200 + code 0，并（除了请求号本身不合法、主密钥不可用、代理商停用）写一条取码失败记录，
// 代理商在对接页能看到哪些客户可能已付款没收到码。

const (
	sharedMsgReqNo       = "请求号不正确"
	sharedMsgNoBalance   = "货源余额不足，请联系商家"
	sharedMsgOutOfStock  = "商品暂时缺货，请稍后再试"
	sharedMsgReqConflict = "请求号冲突"
)

// 取码失败记录的原因（agent_shared_failures.reason 的取值）。
const (
	sharedReasonNoBalance   = "insufficient_balance"
	sharedReasonNotReady    = "not_ready"
	sharedReasonUnavailable = "product_unavailable"
	sharedReasonConflict    = "request_conflict"
	sharedReasonRate        = "rate_limited"
	sharedReasonBusy        = "busy"
	sharedReasonInvalid     = "invalid_request"
)

const (
	sharedTradeRateLimit = 60 // 每个商户 ID 每分钟取码次数（规格第 10 节）
	sharedFailureCodeMax = 64 // 失败记录里的商品码最多存多少个字
	sharedRequestPrefix  = "acg:"
)

var (
	sharedTradeTimeout = 20 * time.Second // 整个取码调用的超时（下游等 30 秒），测试可调小
	// sharedSideTimeout 是写失败记录、标已解决用的超时。这两步不跟请求的 ctx：
	// trade 的 20 秒到了、或者下游断开之后，失败记录和「已解决」仍要写进去。
	sharedSideTimeout = 5 * time.Second
)

// SharedTrade POST /shared/commodity/trade。处理顺序：
//  1. request_no 是标量且是 1–64 位字母数字，否则「请求号不正确」，不记失败（防止被人刷表）；
//  2. 按商户 ID 限流，每分钟 60 次；3. 不接受种类和规格（race 非空、带 sku）；4. num 为 1–200；
//  5. 带 sharedTradeTimeout 的 ctx 调下单核心：请求号 "acg:" + 对方单号，不比价（按当时的价格扣），来源 acg；
//  6. 核心的错误按规格 7.5 的表映射成提示和失败原因；
//  7. 成功或重放：有码解不开时不交出不完整的一批，回「系统维护中」、记 busy；否则把这一号的失败记录标已解决，
//     写审计（新单 agent_order，重放 agent_codes_export），返回发货文本。
//
// 每次调用记一行日志：商户 ID、对方订单号和结果（原因编号或「成功」「重放」），不写码、密钥、签名和金额。
// shared_code 不去首尾空白，原样交给核心（查不到就是停售），只在写失败记录时截到 64 个字。
func SharedTrade(c *gin.Context) {
	f := sharedForm(c)
	appID := sharedCredsOf(c).AppID
	requestNo, scalar := f.Get("request_no")
	if !scalar || !acgshared.ValidRequestNo(requestNo) {
		sharedTradeLog(appID, "(不合法)", "invalid_request_no")
		sharedFail(c, sharedMsgReqNo)
		return
	}
	code, _ := f.Get("shared_code")
	numText, _ := f.Get("num")
	qty, qtyOK := acgshared.ParseQuantity(numText) // 不合法时 qty 为 0，失败记录里就记 0
	refuse := func(reason, msg, detail string) {
		recordSharedFailure(c, requestNo, code, qty, reason, msg)
		sharedTradeLog(appID, requestNo, reason+detail)
		sharedFail(c, msg)
	}

	if !sharedRateAllow("trade|"+appID, sharedTradeRateLimit, sharedNow()) {
		refuse(sharedReasonRate, sharedMsgRate, "")
		return
	}
	if sharedRejectSpec(c) { // 已写好「该商品没有可选规格」
		recordSharedFailure(c, requestNo, code, qty, sharedReasonInvalid, sharedMsgNoSpec)
		sharedTradeLog(appID, requestNo, sharedReasonInvalid)
		return
	}
	if !qtyOK {
		refuse(sharedReasonInvalid, sharedMsgQty, "")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), sharedTradeTimeout)
	defer cancel()
	out, err := placeAgentOrder(ctx, agentOrderInput{
		AgentID: agentID(c), Username: agentUsername(c), ProductCode: code, Quantity: qty,
		RequestNo: sharedRequestPrefix + requestNo, ExpectedUnitPriceCents: 0,
		Source: db.AgentOrderSourceACG, ExternalRef: requestNo,
	})
	if err != nil {
		sharedTradeFail(c, requestNo, err, refuse)
		return
	}

	var codes []string // 不按数量预分配：数量来自请求
	for _, x := range out.Codes {
		if x.Unavailable { // 重放时有码解不开（原因和码 ID 已由 openAgentCodes 记下并告警）
			refuse(sharedReasonBusy, sharedMsgMaint, "（有码解不开）")
			return
		}
		codes = append(codes, x.Code)
	}
	o := out.Order
	sideCtx, sideCancel := context.WithTimeout(context.Background(), sharedSideTimeout)
	if err := db.ResolveAgentSharedFailure(sideCtx, o.AgentID, requestNo, o.ID); err != nil {
		log.Printf("[acg-shared] app_id=%s trade request_no=%s 标记失败记录已解决出错: %v", appID, requestNo, err)
	}
	sideCancel()
	result := "成功"
	if out.Replayed { // 重放又交出了一遍完整码，按导出记审计
		result = "重放"
		auditAgent(c, "agent_codes_export", fmt.Sprintf("order=%d count=%d replay=true source=%s ext=%s", o.ID, len(out.Codes), db.AgentOrderSourceACG, requestNo))
	} else {
		auditAgent(c, "agent_order", fmt.Sprintf("source=%s ext=%s product=%s quantity=%d total_cents=%d order=%d", db.AgentOrderSourceACG, requestNo, o.ProductCode, o.Quantity, o.TotalCents, o.ID))
	}
	sharedTradeLog(appID, requestNo, result)
	sharedOK(c, gin.H{
		"url": "", "amount": acgshared.Money(o.TotalCents), "tradeNo": "AO" + strconv.FormatInt(o.ID, 10),
		"secret": acgshared.SecretText(c.Request.Host, codes), "leave_message": "", "stock": sharedStockReady,
	})
}

// sharedTradeFail 把下单核心的错误写成响应（规格 7.5 的失败映射）。refuse 写失败记录、记日志并回提示；
// 主密钥不可用、码加密失败、代理商停用不记失败记录（不是代理商能在对接页处理的单子问题），只记日志。
// 核心的错误里没有码；数据库忙之类的原因写进日志方便排查。
func sharedTradeFail(c *gin.Context, requestNo string, err error, refuse func(reason, msg, detail string)) {
	var (
		noKey *agentOrderNoKeyError
		short *db.InsufficientBalanceError
	)
	appID := sharedCredsOf(c).AppID
	switch {
	case errors.As(err, &short):
		refuse(sharedReasonNoBalance, sharedMsgNoBalance, "")
	case errors.Is(err, errProductNotReady):
		refuse(sharedReasonNotReady, sharedMsgOutOfStock, "")
	case errors.Is(err, errProductUnavailable):
		refuse(sharedReasonUnavailable, sharedMsgOffSale, "")
	case errors.Is(err, errRequestConflict):
		refuse(sharedReasonConflict, sharedMsgReqConflict, "")
	case errors.As(err, &noKey):
		sharedTradeLog(appID, requestNo, "no_master_key（"+noKey.Kind+"）")
		opsAlertDaily(sharedNoKeyAlertKey, sharedNoKeyAlertText, sharedNow())
		sharedFail(c, sharedMsgMaint)
	case errors.Is(err, errAgentOrderSeal): // 原因已由 sealLocalCDKCode 记下
		sharedTradeLog(appID, requestNo, "seal_failed")
		sharedFail(c, sharedMsgMaint)
	case errors.Is(err, db.ErrAgentDisabled), errors.Is(err, db.ErrAgentNotFound):
		sharedTradeLog(appID, requestNo, "agent_disabled")
		sharedFail(c, sharedMsgAgentOff)
	default: // 含 errAgentOrderBusy（数据库忙、等同号锁超时）、errAgentOrderInternal；都没有扣款，下游重发同号是安全的
		refuse(sharedReasonBusy, sharedMsgBusy, "（"+err.Error()+"）")
	}
}

// recordSharedFailure 尽力写一条取码失败记录，写不进去只记日志。用不跟请求的短超时 ctx：
// trade 的 20 秒超时到了、下游断开之后也写得进去。商品码按收到的原样截到 64 个字，数量不合法时记 0。
// 同一个对方订单号再失败时只累加次数、更新原因和最后时间（见 db.RecordAgentSharedFailure）。
func recordSharedFailure(c *gin.Context, requestNo, productCode string, qty int, reason, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), sharedSideTimeout)
	defer cancel()
	err := db.RecordAgentSharedFailure(ctx, db.NewSharedFailure{
		AgentID: agentID(c), RequestNo: requestNo, ProductCode: sharedClip(productCode, sharedFailureCodeMax),
		Quantity: qty, Reason: reason, Message: msg,
	}, sharedNow())
	if err != nil {
		log.Printf("[acg-shared] app_id=%s trade request_no=%s 写取码失败记录出错: %v", sharedCredsOf(c).AppID, requestNo, err)
	}
}

// sharedTradeLog 每次取码一行日志。request_no 已校验成字母数字（不合法时调用方传占位），result 是原因编号或「成功」「重放」。
func sharedTradeLog(appID, requestNo, result string) {
	log.Printf("[acg-shared] app_id=%s trade request_no=%s 结果=%s", appID, requestNo, result)
}

// sharedClip 截到最多 n 个字（按 UTF-8 字符；不合法的字节各算一个、原样保留）。
func sharedClip(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
