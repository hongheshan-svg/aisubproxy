package marswallex

import (
	"bytes"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Webhook topic，逐字匹配规范（注意持卡人是 CHANGE，卡与账户是 CHANGED）。
const (
	TopicCardApplyResult        = "CARD_APPLY_RESULT"
	TopicCardStatusChanged      = "CARD_STATUS_CHANGED"
	TopicCardholderStatusChange = "CARDHOLDER_STATUS_CHANGE"
	TopicAccountStatusChanged   = "ACCOUNT_STATUS_CHANGED"
	TopicAuthEvent              = "AUTH_EVENT"
	TopicSettleEvent            = "SETTLE_EVENT"
	TopicCardTransferResult     = "CARD_TRANSFER_RESULT"
	TopicConversionResult       = "CONVERSION_RESULT"
	TopicTransferResult         = "TRANSFER_RESULT"
	TopicCard3DSOTP             = "CARD_3DS_OTP"
)

var (
	// ErrWebhookSignature 表示签名头缺失/格式错误或验签失败。
	ErrWebhookSignature = errors.New("marswallex: webhook 签名无效")
	// ErrWebhookStale 表示时间戳与当前时间相差超过 WebhookWindow（过旧或过于超前）。
	ErrWebhookStale = errors.New("marswallex: webhook 时间戳超出允许窗口")
	// ErrWebhookPayload 表示请求体不是合法的 webhook JSON。
	ErrWebhookPayload = errors.New("marswallex: webhook 请求体无效")
)

// WebhookWindow 为 webhook 时间戳与当前时间的最大允许偏差。
const WebhookWindow = 5 * time.Minute

// WebhookEvent 为验签通过并归一化后的事件。Data 保证是 JSON（字符串形式的 data 已解开）。
type WebhookEvent struct {
	Topic     string
	MessageID string
	Data      json.RawMessage
	Raw       []byte
}

// ParseWebhook 验签（epoch+"&"+raw）、校验时间窗口并解析事件。
func ParseWebhook(signatureHeader string, raw []byte, platformPub *rsa.PublicKey, now time.Time) (*WebhookEvent, error) {
	epoch, sig, err := ParseSignatureHeader(signatureHeader)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWebhookSignature, err)
	}
	if platformPub == nil {
		return nil, fmt.Errorf("%w: 未配置平台公钥", ErrWebhookSignature)
	}
	if err := Verify(platformPub, epoch+"&"+string(raw), sig); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWebhookSignature, err)
	}
	ts, err := EpochTime(epoch)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWebhookSignature, err)
	}
	diff := now.Sub(ts)
	if diff < 0 {
		diff = -diff
	}
	if diff > WebhookWindow {
		return nil, ErrWebhookStale
	}

	var env struct {
		Topic      string          `json:"topic"`
		MessageID  string          `json:"message_id"`
		MessageID2 string          `json:"messageId"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWebhookPayload, err)
	}
	id := env.MessageID
	if id == "" {
		id = env.MessageID2
	}
	if id == "" {
		sum := sha256.Sum256(raw)
		id = hex.EncodeToString(sum[:])
	}
	data := env.Data
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '"' {
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			return nil, fmt.Errorf("%w: data: %v", ErrWebhookPayload, err)
		}
		if !json.Valid([]byte(s)) {
			return nil, fmt.Errorf("%w: data 字符串不是合法 JSON", ErrWebhookPayload)
		}
		data = json.RawMessage(s)
	}
	return &WebhookEvent{Topic: env.Topic, MessageID: id, Data: data, Raw: raw}, nil
}

// DecodeData 将事件 data 解码到 v。
func (e *WebhookEvent) DecodeData(v any) error {
	if len(bytes.TrimSpace(e.Data)) == 0 {
		return fmt.Errorf("%w: 缺少 data", ErrWebhookPayload)
	}
	if err := json.Unmarshal(e.Data, v); err != nil {
		return fmt.Errorf("%w: data: %v", ErrWebhookPayload, err)
	}
	return nil
}

// CardApplyResultData 为申卡结果（status: SUCCESS / FAILED）。
type CardApplyResultData struct {
	CardID        string `json:"card_id,omitempty"`
	OutOrderNo    string `json:"out_order_no,omitempty"`
	TransactionID string `json:"transaction_id,omitempty"`
	Status        string `json:"status,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
}

// CardStatusChangedData 为卡状态变更（ACTIVE / REVOKED / CANCELED）。
type CardStatusChangedData struct {
	CardID     string `json:"card_id,omitempty"`
	CardStatus string `json:"card_status,omitempty"`
}

// CardholderStatusChangeData 为持卡人状态变更；UpdateTime 为 Unix 秒。
type CardholderStatusChangeData struct {
	CardHolderID    string    `json:"card_holder_id"`
	OutCardHolderID string    `json:"out_card_holder_id"`
	Status          string    `json:"status"`
	Remark          string    `json:"remark,omitempty"`
	UpdateTime      Timestamp `json:"update_time"`
}

// AccountStatusChangedData 为账户状态变更（ACTIVE / DISABLED；MAIN / SUB_ACCOUNT）。
type AccountStatusChangedData struct {
	AccountNo     string `json:"account_no,omitempty"`
	OutAccountNo  string `json:"out_account_no,omitempty"`
	AccountStatus string `json:"account_status,omitempty"`
	AccountType   string `json:"account_type,omitempty"`
}

// AuthTransactionWebhook 为授权交易推送（无 fee_details）。
type AuthTransactionWebhook struct {
	TransactionID       string     `json:"transaction_id"`
	OriginTransactionID string     `json:"origin_transaction_id,omitempty"`
	TransactionType     string     `json:"transaction_type"`
	CardID              string     `json:"card_id"`
	Status              string     `json:"status"`
	TransactionAmount   string     `json:"transaction_amount"`
	TransactionCurrency string     `json:"transaction_currency"`
	BillingAmount       string     `json:"billing_amount"`
	BillingCurrency     string     `json:"billing_currency"`
	AuthCode            string     `json:"auth_code,omitempty"`
	Merchant            TxMerchant `json:"merchant"`
	OTP                 string     `json:"otp,omitempty"`
	DeclineReason       string     `json:"decline_reason,omitempty"`
	TransactionTime     Timestamp  `json:"transaction_time"`
}

// SettlementTransactionWebhook 为结算交易推送（无 fee_details，merchant 可选）。
type SettlementTransactionWebhook struct {
	TransactionID       string      `json:"transaction_id"`
	TransactionType     string      `json:"transaction_type"`
	OriginTransactionID string      `json:"origin_transaction_id,omitempty"`
	AuthTransactionID   string      `json:"auth_transaction_id,omitempty"`
	CardID              string      `json:"card_id"`
	Status              string      `json:"status"`
	TransactionAmount   string      `json:"transaction_amount"`
	TransactionCurrency string      `json:"transaction_currency"`
	BillingAmount       string      `json:"billing_amount"`
	BillingCurrency     string      `json:"billing_currency"`
	AuthCode            string      `json:"auth_code,omitempty"`
	ARN                 string      `json:"arn,omitempty"`
	Merchant            *TxMerchant `json:"merchant,omitempty"`
	TransactionTime     Timestamp   `json:"transaction_time"`
}

// 以下三类推送与对应查询结构一致，直接别名。
type (
	CardTransferResultData = CardBalanceTransaction
	ConversionResultData   = ConversionDetail
	TransferResultData     = TransferDetail
)

// Card3DSOTPData 为 3DS 验证码推送；CardNo 已脱敏，OTP 不得落日志。
type Card3DSOTPData struct {
	CardID              string `json:"card_id"`
	CardNo              string `json:"card_no"`
	OTP                 string `json:"otp"`
	TransactionCurrency string `json:"transaction_currency,omitempty"`
	TransactionAmount   string `json:"transaction_amount,omitempty"`
	MerchantName        string `json:"merchant_name,omitempty"`
}
