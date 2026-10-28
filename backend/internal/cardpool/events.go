package cardpool

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

const (
	eventBatch       = 100 // 每轮最多处理的事件数
	eventMaxAttempts = 10  // 连续失败 10 次后停止重试
	ourStatusWindow  = 600 // 改状态单成功后 600 秒内收到的同目标推送，视为我们发起的
)

// errEventData 表示已知 topic 的 data 无法解析：标记已处理（备注「数据无法解析」）并告警，不重试。
var errEventData = errors.New("数据无法解析")

func dataError(msg string) error { return fmt.Errorf("%w: %s", errEventData, msg) }

// decodeEventData 解码事件 data；失败时包装 errEventData。
func decodeEventData(ev *marswallex.WebhookEvent, v any) error {
	if err := ev.DecodeData(v); err != nil {
		return fmt.Errorf("%w: %v", errEventData, err)
	}
	return nil
}

// finalEventError 表示处理没有成功、但重试也无济于事：标记已处理，并把原因写入 process_error。
type finalEventError struct{ msg string }

func (e *finalEventError) Error() string { return e.msg }

// otpTopic 判断该 topic 的 data 里是否可能带验证码（标记已处理或停止重试时要抹掉）。
func otpTopic(topic string) bool {
	return topic == marswallex.TopicCard3DSOTP || topic == marswallex.TopicAuthEvent
}

// eventOTP 取出事件 data 里的验证码（没有或已抹除时返回空），只用于从备注与错误里抹掉它。
func eventOTP(ev *marswallex.WebhookEvent) string {
	if ev == nil || !otpTopic(ev.Topic) {
		return ""
	}
	var d struct {
		OTP string `json:"otp"`
	}
	if json.Unmarshal(ev.Data, &d) != nil {
		return ""
	}
	if otp := strings.TrimSpace(d.OTP); otp != redactedOTP {
		return otp
	}
	return ""
}

func scrubOTP(s, otp string) string {
	if otp == "" {
		return s
	}
	return strings.ReplaceAll(s, otp, redactedOTP)
}

type queuedEvent struct {
	id       int64
	payload  string
	attempts int
}

// processEventsOnce 按 id 升序处理一轮到期的未处理事件（每轮最多 100 条），返回本轮处理的条数。
// 单条事件处理出错只记在该事件上（退避重试），不影响后续事件；返回的错误只表示读写事件表失败或 ctx 已结束。
func (s *Service) processEventsOnce(ctx context.Context) (int, error) {
	rows, err := db.DB.QueryContext(ctx, `SELECT id, payload, process_attempts FROM marswallex_webhook_events
		WHERE processed_at IS NULL AND process_attempts < ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY id LIMIT ?`, eventMaxAttempts, s.now().Unix(), eventBatch)
	if err != nil {
		return 0, err
	}
	var list []queuedEvent
	for rows.Next() {
		var e queuedEvent
		if err := rows.Scan(&e.id, &e.payload, &e.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	handler := s.eventHandler
	if handler == nil {
		handler = s.handleEvent
	}
	n := 0
	for i := range list {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		if err := s.processEvent(ctx, handler, &list[i]); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// processEvent 处理一条事件并记录结果。
func (s *Service) processEvent(ctx context.Context, handler func(context.Context, *marswallex.WebhookEvent) (string, error), e *queuedEvent) error {
	ev, err := marswallex.DecodeWebhookPayload([]byte(e.payload))
	if err != nil {
		log.Printf("[cardpool] Webhook 事件 #%d 无法解析，标记为已处理", e.id)
		return s.finishEvent(ctx, e, nil, "无法解析", "")
	}
	note, herr := handler(ctx, ev)
	if cerr := ctx.Err(); cerr != nil {
		// 中途被取消：不记结果、不计失败次数，下次整条重新处理
		return cerr
	}
	otp := eventOTP(ev)
	note = scrubOTP(note, otp)
	var fe *finalEventError
	switch {
	case herr == nil:
		return s.finishEvent(ctx, e, ev, note, "")
	case errors.Is(herr, errEventData):
		msg := scrubOTP(herr.Error(), otp)
		log.Printf("[cardpool] Webhook 事件 #%d（%s）%s", e.id, ev.Topic, msg)
		s.alert(fmt.Sprintf("eventdata:%d", e.id), fmt.Sprintf("MarsWallex Webhook 事件 #%d（%s）数据无法解析，已标记为已处理：%s",
			e.id, ev.Topic, truncate(msg, 200)))
		return s.finishEvent(ctx, e, ev, "数据无法解析", "")
	case errors.As(herr, &fe):
		log.Printf("[cardpool] Webhook 事件 #%d（%s）无法完成，不再重试: %s", e.id, ev.Topic, scrubOTP(fe.msg, otp))
		return s.finishEvent(ctx, e, ev, note, scrubOTP(fe.msg, otp))
	}
	return s.failEvent(ctx, e, ev, scrubOTP(herr.Error(), otp))
}

// redactedPayload 返回抹掉验证码后的原文；不需要或没有改动时返回 false。
func redactedPayload(e *queuedEvent, ev *marswallex.WebhookEvent) (string, bool) {
	if ev == nil || !otpTopic(ev.Topic) {
		return "", false
	}
	out, changed, err := redactOTP(e.payload)
	if err != nil {
		log.Printf("[cardpool] Webhook 事件 #%d 抹除验证码失败: %v", e.id, err)
		return "", false
	}
	return out, changed
}

// finishEvent 标记事件已处理：写 processed_at、process_note，process_error 为空则清空；带验证码的 topic 同时抹掉原文里的验证码。
func (s *Service) finishEvent(ctx context.Context, e *queuedEvent, ev *marswallex.WebhookEvent, note, procErr string) error {
	set := "processed_at = ?, process_note = ?, process_error = ?, next_attempt_at = NULL"
	args := []any{s.now().UTC(), note, sql.NullString{String: procErr, Valid: procErr != ""}}
	if p, ok := redactedPayload(e, ev); ok {
		set += ", payload = ?"
		args = append(args, p)
	}
	args = append(args, e.id)
	// 处理已经完成，记录结果不受取消影响
	_, err := db.DB.ExecContext(context.WithoutCancel(ctx),
		`UPDATE marswallex_webhook_events SET `+set+` WHERE id = ? AND processed_at IS NULL`, args...)
	return err
}

// failEvent 记录一次处理失败：attempts + 1，next_attempt_at = now + backoff(attempts-1)。
// 满 10 次后停止重试、抹掉原文里的验证码并告警（key event:<id>）。
func (s *Service) failEvent(ctx context.Context, e *queuedEvent, ev *marswallex.WebhookEvent, msg string) error {
	attempts := e.attempts + 1
	now := s.now().Unix()
	set := "process_attempts = ?, process_error = ?, next_attempt_at = ?"
	args := []any{attempts, msg, now + secs(backoff(attempts-1))}
	stop := attempts >= eventMaxAttempts
	if stop {
		if p, ok := redactedPayload(e, ev); ok {
			set += ", payload = ?"
			args = append(args, p)
		}
	}
	// process_attempts 条件防止覆盖处理期间被「重新处理」重置的计数
	args = append(args, e.id, e.attempts)
	res, err := db.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE marswallex_webhook_events SET `+set+`
		WHERE id = ? AND processed_at IS NULL AND process_attempts = ?`, args...)
	if err != nil {
		return err
	}
	log.Printf("[cardpool] Webhook 事件 #%d（%s）处理失败（第 %d 次）: %s", e.id, ev.Topic, attempts, msg)
	if n, _ := res.RowsAffected(); n == 1 && stop {
		s.alert(fmt.Sprintf("event:%d", e.id), fmt.Sprintf("MarsWallex Webhook 事件 #%d（%s）连续处理失败 %d 次，已停止重试，"+
			"请在后台查看后「重新处理」。最后错误：%s", e.id, ev.Topic, attempts, truncate(msg, 200)))
	}
	return nil
}

// Reprocess 重置事件的处理状态（清空 processed_at、process_error、process_note、next_attempt_at，attempts 归零），
// 并通知处理协程。事件不存在返回 ErrNotFound。
func (s *Service) Reprocess(ctx context.Context, eventID int64, by string) error {
	res, err := db.DB.ExecContext(ctx, `UPDATE marswallex_webhook_events SET processed_at = NULL, process_error = NULL,
		process_note = '', next_attempt_at = NULL, process_attempts = 0 WHERE id = ?`, eventID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	log.Printf("[cardpool] Webhook 事件 #%d 由 %s 重新处理", eventID, by)
	s.Kick()
	return nil
}

// handleEvent 按 topic 处理一条事件，返回写入 process_note 的备注。所有处理都可重复执行。
// 返回包装 errEventData 的错误表示数据无法解析，*finalEventError 表示不再重试，其他错误按退避重试。
func (s *Service) handleEvent(ctx context.Context, ev *marswallex.WebhookEvent) (string, error) {
	switch ev.Topic {
	case marswallex.TopicCardApplyResult:
		return s.onApplyResult(ctx, ev)
	case marswallex.TopicCardTransferResult:
		return s.onTransferResult(ctx, ev)
	case marswallex.TopicCardStatusChanged:
		return s.onCardStatusChanged(ctx, ev)
	case marswallex.TopicCardholderStatusChange:
		return s.onCardholderStatusChange(ctx, ev)
	case marswallex.TopicAuthEvent:
		return s.onAuthEvent(ctx, ev)
	case marswallex.TopicSettleEvent:
		return s.onSettleEvent(ctx, ev)
	case marswallex.TopicCard3DSOTP:
		return s.onOTPEvent(ctx, ev)
	case marswallex.TopicAccountStatusChanged, marswallex.TopicConversionResult, marswallex.TopicTransferResult:
		return "本子项目不处理", nil
	}
	return "未知 topic", nil
}

// eventOrderState 把推送里的单据状态映射为单据状态；不认识的返回空。
func eventOrderState(status string) string {
	switch normStatus(status) {
	case "APPLY", "PROCESSING":
		return StateProcessing
	case "SUCCESS":
		return StateSuccess
	case "FAIL", "FAILED":
		return StateFailed
	}
	return ""
}

// findEventOrder 按单号找单据；单号为空且 txnID 非空时按平台交易号找最新一张。类型不在 kinds 中视为没有。
func findEventOrder(ctx context.Context, outOrderNo, txnID string, kinds ...string) (*Order, error) {
	var o *Order
	switch {
	case outOrderNo != "":
		got, err := getOrder(ctx, outOrderNo)
		if err != nil {
			return nil, err
		}
		o = got
	case txnID != "":
		args := []any{txnID}
		for _, k := range kinds {
			args = append(args, k)
		}
		var got Order
		err := got.scan(db.DB.QueryRowContext(ctx, `SELECT `+orderCols+` FROM mw_card_orders
			WHERE platform_txn_id = ? AND kind IN (`+placeholders(len(kinds))+`) ORDER BY id DESC LIMIT 1`, args...))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		o = &got
	default:
		return nil, ErrNotFound
	}
	for _, k := range kinds {
		if o.Kind == k {
			return o, nil
		}
	}
	return nil, ErrNotFound
}

// onApplyResult 定论开卡单（按 out_order_no，没有则按 transaction_id 找单据）。成功的开卡单由定论把卡入池。
func (s *Service) onApplyResult(ctx context.Context, ev *marswallex.WebhookEvent) (string, error) {
	var d marswallex.CardApplyResultData
	if err := decodeEventData(ev, &d); err != nil {
		return "", err
	}
	o, err := findEventOrder(ctx, strings.TrimSpace(d.OutOrderNo), strings.TrimSpace(d.TransactionID), KindApply)
	if errors.Is(err, ErrNotFound) {
		return "无对应本地单据", nil
	}
	if err != nil {
		return "", err
	}
	st := eventOrderState(d.Status)
	if st == "" {
		return fmt.Sprintf("单据 %s：无法识别的状态 %q", o.OutOrderNo, d.Status), nil
	}
	got, err := s.finalize(ctx, o.OutOrderNo, queryResult{State: st, CardID: strings.TrimSpace(d.CardID),
		TxnID: d.TransactionID, Reason: d.FailureReason}, "webhook")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("单据 %s：%s", got.OutOrderNo, got.State), nil
}

// onTransferResult 定论转入或转出单并刷新卡余额；没有对应本地单据时只尽力刷新池中卡的余额。
func (s *Service) onTransferResult(ctx context.Context, ev *marswallex.WebhookEvent) (string, error) {
	var d marswallex.CardTransferResultData
	if err := decodeEventData(ev, &d); err != nil {
		return "", err
	}
	o, err := findEventOrder(ctx, strings.TrimSpace(d.OutOrderNo), "", KindRecharge, KindWithdraw)
	if errors.Is(err, ErrNotFound) {
		return "无对应本地单据" + s.refreshBalanceNote(ctx, strings.TrimSpace(d.CardID)), nil
	}
	if err != nil {
		return "", err
	}
	st := eventOrderState(d.Status)
	if st == "" {
		return fmt.Sprintf("单据 %s：无法识别的状态 %q", o.OutOrderNo, d.Status), nil
	}
	r := queryResult{State: st, TxnID: d.TransactionID}
	if st == StateFailed {
		r.Reason = "平台返回 " + normStatus(d.Status)
	}
	got, err := s.finalize(ctx, o.OutOrderNo, r, "webhook")
	if err != nil {
		return "", err
	}
	note := fmt.Sprintf("单据 %s：%s", got.OutOrderNo, got.State)
	if IsTerminal(got.State) {
		note += s.refreshBalanceNote(ctx, got.CardID)
	}
	return note, nil
}

// refreshBalanceNote 尽力刷新池中卡的详情与余额，返回追加到备注的说明；失败只写备注。
func (s *Service) refreshBalanceNote(ctx context.Context, cardID string) string {
	if cardID == "" {
		return "，卡不在池中"
	}
	_, err := s.RefreshCard(ctx, cardID)
	switch {
	case err == nil:
		return "，余额已刷新"
	case errors.Is(err, ErrNotFound):
		return "，卡不在池中"
	}
	log.Printf("[cardpool] 刷新卡 %s 余额失败: %v", cardID, err)
	return "，刷新余额失败：" + truncate(err.Error(), 200)
}

// onCardStatusChanged 更新平台状态。我们发起的改状态（同卡同目标的改状态单非终态，或 600 秒内成功过）定论为成功；
// 否则按外部变更处理：CANCELED → retired（租用中告警），REVOKED → available 的卡停用并告警、leased 的卡只告警。
func (s *Service) onCardStatusChanged(ctx context.Context, ev *marswallex.WebhookEvent) (string, error) {
	var d marswallex.CardStatusChangedData
	if err := decodeEventData(ev, &d); err != nil {
		return "", err
	}
	cardID, status := strings.TrimSpace(d.CardID), normStatus(d.CardStatus)
	if cardID == "" || status == "" {
		return "", dataError("缺少 card_id 或 card_status")
	}
	card, err := getCard(ctx, cardID)
	if errors.Is(err, ErrNotFound) {
		return "卡不在池中", nil
	}
	if err != nil {
		return "", err
	}
	o, err := s.ourStatusOrder(ctx, cardID, status)
	if err != nil {
		return "", err
	}
	if o != nil {
		if !IsTerminal(o.State) {
			if o, err = s.finalize(ctx, o.OutOrderNo, queryResult{State: StateSuccess}, "webhook"); err != nil {
				return "", err
			}
		}
		if err := s.setPlatformStatus(ctx, cardID, status); err != nil {
			return "", err
		}
		return fmt.Sprintf("本系统发起的改状态（单据 %s：%s）", o.OutOrderNo, o.State), nil
	}
	return s.applyExternalStatus(ctx, card, status)
}

// ourStatusOrder 返回同卡同目标、非终态或 600 秒内成功过的最新一张改状态单；没有返回 nil。
func (s *Service) ourStatusOrder(ctx context.Context, cardID, status string) (*Order, error) {
	var o Order
	err := o.scan(db.DB.QueryRowContext(ctx, `SELECT `+orderCols+` FROM mw_card_orders
		WHERE kind = ? AND card_id = ? AND target_status = ?
		AND (state IN (?, ?, ?, ?) OR (state = ? AND finished_at >= ?))
		ORDER BY id DESC LIMIT 1`,
		KindStatus, cardID, status, StatePending, StateProcessing, StateUnknown, StateNeedsReview,
		StateSuccess, s.now().Unix()-ourStatusWindow))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Service) setPlatformStatus(ctx context.Context, cardID, status string) error {
	_, err := db.DB.ExecContext(ctx, `UPDATE mw_cards SET platform_status = ?, updated_at = ? WHERE card_id = ?`,
		status, s.now().Unix(), cardID)
	return err
}

func ownerSuffix(c *Card) string {
	if c.LeaseOwner == "" {
		return ""
	}
	return "（持有方 " + c.LeaseOwner + "）"
}

// applyExternalStatus 处理不是我们发起的卡状态变更。池状态用条件更新，读取后被别处改过就重读再判断。
func (s *Service) applyExternalStatus(ctx context.Context, c *Card, status string) (string, error) {
	for i := 0; i < 3; i++ {
		note, done, err := s.tryExternalStatus(ctx, c, status)
		if err != nil || done {
			return note, err
		}
		if c, err = getCard(ctx, c.CardID); errors.Is(err, ErrNotFound) {
			return "卡不在池中", nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("卡 %s 的池状态一直在变化，稍后重试", c.CardID)
}

// tryExternalStatus 按读取到的池状态执行一次；done = false 表示条件更新没有命中（池状态已变）。
func (s *Service) tryExternalStatus(ctx context.Context, c *Card, status string) (string, bool, error) {
	now := s.now().Unix()
	switch {
	case status == "CANCELED" && c.PoolState != PoolRetired:
		res, err := db.DB.ExecContext(ctx, `UPDATE mw_cards SET pool_state = ?, platform_status = ?, next_action_at = NULL,
			updated_at = ? WHERE card_id = ? AND pool_state = ?`, PoolRetired, status, now, c.CardID, c.PoolState)
		if err != nil {
			return "", false, err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return "", false, err
		}
		log.Printf("[cardpool] 卡 %s 在平台上被注销（非本系统发起），%s → retired", c.CardID, c.PoolState)
		if c.PoolState == PoolLeased {
			s.alert("extcancel:"+c.CardID, fmt.Sprintf("MarsWallex 卡 %s 租用中%s在平台上被注销（非本系统发起），已退役，请检查对应订单",
				c.CardID, ownerSuffix(c)))
		}
		return "外部注销：" + c.PoolState + " → retired", true, nil

	case status == "REVOKED" && c.PoolState == PoolAvailable:
		res, err := db.DB.ExecContext(ctx, `UPDATE mw_cards SET pool_state = ?, disabled_reason = ?, platform_status = ?,
			updated_at = ? WHERE card_id = ? AND pool_state = ?`, PoolDisabled, "平台冻结", status, now, c.CardID, PoolAvailable)
		if err != nil {
			return "", false, err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return "", false, err
		}
		s.alert("extfreeze:"+c.CardID, fmt.Sprintf("MarsWallex 卡 %s 在平台上被冻结（非本系统发起），已从卡池停用（原因：平台冻结），请到 MarsWallex 后台核查", c.CardID))
		return "外部冻结：available → disabled", true, nil
	}

	if err := s.setPlatformStatus(ctx, c.CardID, status); err != nil {
		return "", false, err
	}
	if status == "REVOKED" && c.PoolState == PoolLeased {
		s.alert("extfreeze:"+c.CardID, fmt.Sprintf("MarsWallex 卡 %s 租用中%s在平台上被冻结（非本系统发起），请检查对应订单",
			c.CardID, ownerSuffix(c)))
		return "外部冻结：租用中的卡，已告警", true, nil
	}
	return "平台状态更新为 " + status, true, nil
}

// onCardholderStatusChange 只更新已导入的持卡人；新状态不是 APPROVED 时告警（key holder:<id>）。
func (s *Service) onCardholderStatusChange(ctx context.Context, ev *marswallex.WebhookEvent) (string, error) {
	var d marswallex.CardholderStatusChangeData
	if err := decodeEventData(ev, &d); err != nil {
		return "", err
	}
	id, status := strings.TrimSpace(d.CardHolderID), normStatus(d.Status)
	if id == "" || status == "" {
		return "", dataError("缺少 card_holder_id 或 status")
	}
	res, err := db.DB.ExecContext(ctx, `UPDATE mw_cardholders SET status = ?, remark = ?, synced_at = ? WHERE card_holder_id = ?`,
		status, d.Remark, s.now().Unix(), id)
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "持卡人未导入", nil
	}
	if status != "APPROVED" {
		msg := fmt.Sprintf("MarsWallex 持卡人 %s 状态变为 %s，自动补卡将跳过该持卡人", id, status)
		if r := strings.TrimSpace(d.Remark); r != "" {
			msg += "。平台备注：" + truncate(r, 200)
		}
		s.alert("holder:"+id, msg)
	}
	return "持卡人状态更新为 " + status, nil
}

// onAuthEvent 写入授权交易；available 且 use_count = 0 的卡（卡号从未交出）出现授权时停用并告警。
func (s *Service) onAuthEvent(ctx context.Context, ev *marswallex.WebhookEvent) (string, error) {
	var d marswallex.AuthTransactionWebhook
	if err := decodeEventData(ev, &d); err != nil {
		return "", err
	}
	if strings.TrimSpace(d.TransactionID) == "" {
		return "", dataError("缺少 transaction_id")
	}
	t := Transaction{
		TransactionID: d.TransactionID, Kind: txnKind(d.TransactionType, "AUTH"), CardID: d.CardID,
		Status: d.Status, DeclineReason: d.DeclineReason,
		TxnAmount: d.TransactionAmount, TxnCurrency: d.TransactionCurrency,
		BillingAmount: d.BillingAmount, BillingCurrency: d.BillingCurrency,
		MerchantName: d.Merchant.MerchantName, OriginTransactionID: d.OriginTransactionID,
		OccurredAt: unixOrNil(d.TransactionTime),
	}
	if err := s.upsertTxn(ctx, t); err != nil {
		return "", err
	}
	if d.CardID == "" {
		return "交易已记录", nil
	}
	res, err := db.DB.ExecContext(ctx, `UPDATE mw_cards SET pool_state = ?, disabled_reason = ?, updated_at = ?
		WHERE card_id = ? AND pool_state = ? AND use_count = 0`,
		PoolDisabled, "未交出卡号出现授权", s.now().Unix(), d.CardID, PoolAvailable)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		s.alert("authunused:"+d.CardID, fmt.Sprintf("MarsWallex 卡 %s 从未交出卡号却出现授权（交易 %s，%s %s %s，商户 %s），已从卡池停用，请到 MarsWallex 后台核查",
			d.CardID, d.TransactionID, t.Kind, d.TransactionAmount, d.TransactionCurrency, t.MerchantName))
		return "交易已记录；卡未交出卡号出现授权，已停用", nil
	}
	return "交易已记录", nil
}

// onSettleEvent 写入结算交易。
func (s *Service) onSettleEvent(ctx context.Context, ev *marswallex.WebhookEvent) (string, error) {
	var d marswallex.SettlementTransactionWebhook
	if err := decodeEventData(ev, &d); err != nil {
		return "", err
	}
	if strings.TrimSpace(d.TransactionID) == "" {
		return "", dataError("缺少 transaction_id")
	}
	t := Transaction{
		TransactionID: d.TransactionID, Kind: txnKind(d.TransactionType, "SETTLE"), CardID: d.CardID,
		Status: d.Status, TxnAmount: d.TransactionAmount, TxnCurrency: d.TransactionCurrency,
		BillingAmount: d.BillingAmount, BillingCurrency: d.BillingCurrency,
		OriginTransactionID: d.OriginTransactionID, AuthTransactionID: d.AuthTransactionID,
		OccurredAt: unixOrNil(d.TransactionTime),
	}
	if d.Merchant != nil {
		t.MerchantName = d.Merchant.MerchantName
	}
	if err := s.upsertTxn(ctx, t); err != nil {
		return "", err
	}
	return "交易已记录", nil
}

// txnKind 取平台推送的交易类型，为空时用 def。
func txnKind(sent, def string) string {
	if k := strings.TrimSpace(sent); k != "" {
		return k
	}
	return def
}

func unixOrNil(t marswallex.Timestamp) *int64 {
	if t.IsZero() {
		return nil
	}
	v := t.Unix()
	return &v
}

// upsertTxn 按 transaction_id 写入交易，重复推送覆盖全部字段（pending 不是终态，后到的推送为准）。
func (s *Service) upsertTxn(ctx context.Context, t Transaction) error {
	_, err := db.DB.ExecContext(ctx, `INSERT INTO mw_card_transactions (`+txnCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(transaction_id) DO UPDATE SET kind = excluded.kind, card_id = excluded.card_id,
		status = excluded.status, decline_reason = excluded.decline_reason,
		txn_amount = excluded.txn_amount, txn_currency = excluded.txn_currency,
		billing_amount = excluded.billing_amount, billing_currency = excluded.billing_currency,
		merchant_name = excluded.merchant_name, origin_transaction_id = excluded.origin_transaction_id,
		auth_transaction_id = excluded.auth_transaction_id, occurred_at = excluded.occurred_at,
		updated_at = excluded.updated_at`,
		t.TransactionID, t.Kind, t.CardID, t.Status, t.DeclineReason, t.TxnAmount, t.TxnCurrency,
		t.BillingAmount, t.BillingCurrency, t.MerchantName, t.OriginTransactionID, t.AuthTransactionID,
		t.OccurredAt, s.now().Unix())
	return err
}
