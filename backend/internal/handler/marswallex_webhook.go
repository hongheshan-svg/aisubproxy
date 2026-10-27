package handler

import (
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

const marsWallexWebhookMaxBody = 1 << 20

// MarsWallexWebhook 公开的 MarsWallex Webhook 接收端：验签、去重入库。
func MarsWallexWebhook(c *gin.Context) {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, marsWallexWebhookMaxBody+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取请求体失败"})
		return
	}
	if len(raw) > marsWallexWebhookMaxBody {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "请求体过大"})
		return
	}
	pemText, err := db.GetSetting(db.SettingMarsWallexPlatformPublicKey)
	if err != nil || strings.TrimSpace(pemText) == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "平台公钥未配置"})
		return
	}
	pub, err := marswallex.ParsePublicKey(pemText)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "平台公钥无法解析"})
		return
	}
	ev, err := marswallex.ParseWebhook(c.GetHeader("F-Signature"), raw, pub, time.Now())
	if err != nil {
		switch {
		case errors.Is(err, marswallex.ErrWebhookSignature), errors.Is(err, marswallex.ErrWebhookStale):
			c.JSON(http.StatusUnauthorized, gin.H{"error": "签名无效"})
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求体无效"})
		}
		return
	}
	if _, err := db.InsertMarsWallexWebhookEvent(ev.MessageID, ev.Topic, string(ev.Raw)); err != nil {
		log.Printf("marswallex webhook: store event failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// AdminListMarsWallexWebhookEvents 列出最近收到的 Webhook（不含 payload）。
func AdminListMarsWallexWebhookEvents(c *gin.Context) {
	rows, err := db.ListMarsWallexWebhookEvents(parseLimit(c.Query("limit"), 100))
	if err != nil {
		log.Printf("marswallex webhook: list failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	events := make([]gin.H, 0, len(rows))
	for _, e := range rows {
		var processed any
		if e.ProcessedAt != nil {
			processed = e.ProcessedAt
		}
		events = append(events, gin.H{
			"id": e.ID, "message_id": e.MessageID, "topic": e.Topic,
			"received_at": e.ReceivedAt, "processed_at": processed, "process_error": e.ProcessError,
		})
	}
	c.JSON(http.StatusOK, gin.H{"events": events})
}
