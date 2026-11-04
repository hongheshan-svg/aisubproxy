package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// CardPlatformWebhook POST /api/v1/webhooks/cardplatform
// 卡台开发者页配置的回调地址指向此处。
// 验签：X-Signature = hex(HMAC-SHA256(webhook_secret, raw body))
// 文档：zovocard-openapi-zh.md §7
func CardPlatformWebhook(c *gin.Context) {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	secret, serr := db.GetCardSecret(db.SettingWebhookSecret)
	secret = strings.TrimSpace(secret)
	if serr != nil {
		log.Printf("webhook: 解密 webhook_secret 失败: %v", serr)
		c.Status(http.StatusServiceUnavailable)
		return
	}
	if secret == "" {
		// 未配置密钥时拒绝，避免裸奔
		log.Printf("webhook: webhook_secret not configured")
		c.Status(http.StatusServiceUnavailable)
		return
	}
	got := strings.TrimSpace(c.GetHeader("X-Signature"))
	if got == "" {
		c.Status(http.StatusUnauthorized)
		return
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(raw)
	expect := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(got)), []byte(strings.ToLower(expect))) != 1 {
		c.Status(http.StatusUnauthorized)
		return
	}

	// 尽快 200；入库 best-effort
	var payload map[string]interface{}
	_ = json.Unmarshal(raw, &payload)

	eventType := ""
	if v, ok := payload["event"].(string); ok {
		eventType = v
	}
	if v, ok := payload["type"].(string); ok && eventType == "" {
		eventType = v
	}
	// 幂等键
	idem := webhookIdemKey(payload, eventType, raw)
	// 原文入库前脱敏：卡号只留前 6 后 4，去掉 CVV、有效期（业务处理仍用未脱敏的 payload）
	if err := db.InsertWebhookEvent(eventType, idem, db.SanitizeWebhookPayload(string(raw))); err != nil {
		if !strings.Contains(err.Error(), "UNIQUE") && !strings.Contains(err.Error(), "unique") {
			// 非唯一冲突的写库错误：回 500 让卡台重试，不能回 200 把事件丢掉
			log.Printf("webhook store: %v", err)
			c.Status(http.StatusInternalServerError)
			return
		}
		// 唯一冲突：这个事件已处理过，直接回 200，不再回写状态（否则陈旧的重发会把状态改回去）
		c.Status(http.StatusOK)
		return
	} else {
		// 若是 GPT 直充完成，可写审计
		if eventType == "gpt_direct.completed" {
			db.WriteAudit("webhook", "gpt_direct.completed", idem, c.ClientIP())
		}
	}
	// 本站 CDK 状态：兑换完成 → consumed（避免列表仍显示「未使用」）
	if strings.HasPrefix(strings.ToLower(eventType), "gpt_direct.") {
		applyCDKStatusFromWebhook(payload, eventType)
		clearSessionFromWebhook(payload, eventType)
	}
	// 卡健康：失败/成功终态观察（同卡多邮箱失败 → 拉黑）
	if strings.HasPrefix(strings.ToLower(eventType), "gpt_direct.") {
		observeFromWebhookPayload(payload)
	}
	c.Status(http.StatusOK)
}

// applyCDKStatusFromWebhook 根据卡台终态回写本站 SQLite 中的 CDK status。
func applyCDKStatusFromWebhook(payload map[string]interface{}, eventType string) {
	if payload == nil {
		return
	}
	cdkID := anyToInt64(payload["cdk_id"])
	if cdkID <= 0 {
		return
	}
	et := strings.ToLower(strings.TrimSpace(eventType))
	st := strings.ToLower(strings.TrimSpace(strAny(payload["cdk_status"])))
	if st == "" {
		if strings.Contains(et, "completed") {
			st = "consumed"
		} else {
			return
		}
	}
	// 失败回传 unused 时，勿把已 consumed/disabled 降级回去
	if st == "unused" || st == "reserved" {
		if cur := db.GetCardplatformCDKStatus(cdkID); cur == "consumed" || cur == "disabled" {
			return
		}
	}
	_ = db.UpdateCardplatformCDKStatus(cdkID, st)
}

// clearSessionFromWebhook 兑换完成事件：清空该码绑定的用户会话。
func clearSessionFromWebhook(payload map[string]interface{}, eventType string) {
	if !strings.Contains(strings.ToLower(eventType), "completed") {
		return
	}
	// 只按上游 ID 取码，不按前缀兜底：前缀撞上别的码会清掉别人的会话。
	if code := exactCDKCodeByID(anyToInt64(payload["cdk_id"])); code != "" {
		clearSessionAfterCompleted(code, "")
	}
}

func webhookIdemKey(p map[string]interface{}, eventType string, raw []byte) string {
	str := func(k string) string {
		if v, ok := p[k]; ok {
			switch t := v.(type) {
			case string:
				return t
			case float64:
				return strings.TrimSuffix(strings.TrimSuffix(jsonNumber(t), ".0"), ".")
			}
		}
		return ""
	}
	switch eventType {
	case "card_transaction":
		return strings.Join([]string{eventType, str("auth_id"), str("type"), str("status")}, "|")
	case "card_operation":
		return strings.Join([]string{eventType, str("operation"), str("operation_id"), str("status")}, "|")
	case "gpt_direct.completed":
		if id := str("order_id"); id != "" {
			return "gpt_direct.completed|order|" + id
		}
		if id := str("client_request_id"); id != "" {
			return "gpt_direct.completed|client|" + id
		}
	}
	// 卡台文档要求按 event_id 去重
	if id := str("event_id"); id != "" {
		return eventType + "|event|" + id
	}
	// fallback：不含时间，同一份原文重发得到同一个键
	h := sha256.Sum256(append([]byte(eventType+"|"), raw...))
	return hex.EncodeToString(h[:16])
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// AdminListWebhooks GET /api/v1/admin/webhooks/events
func AdminListWebhooks(c *gin.Context) {
	limit := 50
	rows, err := db.ListWebhookEvents(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list failed"})
		return
	}
	// 脱敏：不返回完整 card_number
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		out = append(out, gin.H{
			"id":         r.ID,
			"event_type": r.EventType,
			"idem_key":   r.IdemKey,
			"created_at": r.CreatedAt,
			"payload":    sanitizeWebhookPayload(r.Payload),
		})
	}
	urlHint := ""
	if host := c.Request.Host; host != "" {
		scheme := "https"
		if c.Request.TLS == nil && !strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
			// 生产经 Caddy 会带 proto
			if p := c.GetHeader("X-Forwarded-Proto"); p != "" {
				scheme = p
			}
		} else if strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		urlHint = scheme + "://" + host + "/api/v1/webhooks/cardplatform"
	}
	sec, secErr := db.GetCardSecret(db.SettingWebhookSecret)
	resp := gin.H{
		"events":              out,
		"webhook_url":         urlHint,
		"webhook_secret_set":  strings.TrimSpace(sec) != "",
		"webhook_secret_hint": maskSecret(sec),
	}
	if secErr != nil {
		// 密文还在但解不开（APP_MASTER_KEY 没配或已更换）：告诉前端，别显示成「未配置」
		resp["webhook_secret_set"] = true
		resp["webhook_secret_error"] = "已保存的值解不开：APP_MASTER_KEY 没配置或已更换，请重新填写"
	}
	c.JSON(http.StatusOK, resp)
}

func sanitizeWebhookPayload(raw string) interface{} {
	var m map[string]interface{}
	if json.Unmarshal([]byte(raw), &m) != nil {
		return raw
	}
	// 入库时已脱敏；这里再过一遍，兼顾没跑过清洗的旧数据
	var out interface{}
	if json.Unmarshal([]byte(db.SanitizeWebhookPayload(raw)), &out) == nil {
		return out
	}
	return m
}
