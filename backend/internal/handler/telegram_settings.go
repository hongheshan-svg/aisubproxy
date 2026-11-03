package handler

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/notify"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

const (
	// settingTelegramBotToken 用 APP_MASTER_KEY 加密保存（键名作为 AAD）
	settingTelegramBotToken = "telegram_bot_token"
	settingTelegramChatID   = "telegram_chat_id"
	// legacyTelegramTokenKey 是旧版设置接口明文保存的令牌（从来没被读过），启动时加密迁移后删除
	legacyTelegramTokenKey = "telegram_token"
)

var (
	// BotFather 给的令牌：机器人数字 ID + 冒号 + 一串字母数字
	telegramTokenRe = regexp.MustCompile(`^\d{5,15}:[A-Za-z0-9_-]{30,64}$`)
	// 私聊填用户数字 ID，群组是负数（超级群以 -100 开头），频道可以填 @用户名
	telegramChatIDRe = regexp.MustCompile(`^(-?\d{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
)

// TelegramSettings 读后台保存的 Telegram 配置，供 notify.SetSettingsSource 使用。没保存令牌时不需要 APP_MASTER_KEY。
func TelegramSettings() (token, chatID string, err error) {
	chatID, err = db.GetSetting(settingTelegramChatID)
	if err != nil {
		return "", "", err
	}
	token, err = db.GetSecretSetting(settingTelegramBotToken)
	if err != nil {
		return "", "", fmt.Errorf("解密 Telegram 令牌失败: %w", err)
	}
	return token, chatID, nil
}

// MigrateLegacyTelegramToken 把旧版明文保存的令牌加密后存到新键，再删掉明文。没有 APP_MASTER_KEY 时先不动，下次启动再试。
func MigrateLegacyTelegramToken() {
	old, err := db.GetSetting(legacyTelegramTokenKey)
	if err != nil || strings.TrimSpace(old) == "" {
		return
	}
	cur, err := db.GetSetting(settingTelegramBotToken)
	if err != nil {
		log.Printf("[telegram] 读取设置失败，旧版明文令牌暂不迁移: %v", err)
		return
	}
	if strings.TrimSpace(cur) == "" {
		if err := db.SetSecretSetting(settingTelegramBotToken, strings.TrimSpace(old)); err != nil {
			log.Printf("[telegram] 旧版明文令牌加密迁移失败，先保留（配置 APP_MASTER_KEY 后重启会再试）: %v", err)
			return
		}
	}
	if err := db.DeleteSetting(legacyTelegramTokenKey); err != nil {
		log.Printf("[telegram] 删除旧版明文令牌失败: %v", err)
		return
	}
	log.Printf("[telegram] 已把旧版明文保存的令牌加密存储，并删除明文")
}

// maskTelegramToken 只露出机器人 ID 和令牌末 4 位：123456789:****abcd。
func maskTelegramToken(token string) string {
	id, secret, ok := strings.Cut(strings.TrimSpace(token), ":")
	if !ok || len(secret) <= 4 {
		return maskSecret(token)
	}
	return id + ":****" + secret[len(secret)-4:]
}

// AdminGetTelegram GET /api/v1/admin/notify/telegram：当前配置（令牌只给脱敏提示）与实际生效的来源。
func AdminGetTelegram(c *gin.Context) {
	chatID, _ := db.GetSetting(settingTelegramChatID)
	raw, _ := db.GetSetting(settingTelegramBotToken)
	out := gin.H{
		"token_configured": strings.TrimSpace(raw) != "",
		"token_hint":       "",
		"token_error":      "",
		"chat_id":          chatID,
	}
	if strings.TrimSpace(raw) != "" {
		if tok, err := db.GetSecretSetting(settingTelegramBotToken); err == nil {
			out["token_hint"] = maskTelegramToken(tok)
		} else {
			out["token_error"] = "已保存的令牌解不开：APP_MASTER_KEY 没配置或已更换，请重新填写令牌"
		}
	}
	env := notify.EnvConfig()
	cur := notify.Current()
	_, mkErr := secretbox.LoadKey()
	out["env_configured"] = env.Ready()
	out["env_chat_id"] = env.ChatID
	out["source"] = cur.Source
	out["effective_chat_id"] = cur.ChatID
	out["master_key_ok"] = mkErr == nil
	c.JSON(http.StatusOK, out)
}

type telegramSettingsBody struct {
	Token  *string `json:"token"`
	ChatID *string `json:"chat_id"`
	// Clear 为 true 时删除后台配置（之后如有环境变量就用环境变量）
	Clear bool `json:"clear"`
}

// AdminPutTelegram PUT /api/v1/admin/notify/telegram：保存令牌（留空表示不改）和 chat id。
func AdminPutTelegram(c *gin.Context) {
	var body telegramSettingsBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if body.Clear {
		for _, k := range []string{settingTelegramBotToken, settingTelegramChatID, legacyTelegramTokenKey} {
			if err := db.DeleteSetting(k); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "清除失败"})
				return
			}
		}
		auditAdmin(c, "telegram_clear", "清除后台的 Telegram 告警配置")
		AdminGetTelegram(c)
		return
	}

	token := ""
	if body.Token != nil {
		token = strings.TrimSpace(*body.Token)
	}
	if token != "" && !telegramTokenRe.MatchString(token) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "令牌格式不对：应是 @BotFather 给的完整令牌，形如 123456789:AAH…"})
		return
	}
	chatID := ""
	if body.ChatID != nil {
		chatID = strings.TrimSpace(*body.ChatID)
		if !telegramChatIDRe.MatchString(chatID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "chat id 格式不对：私聊填你的数字 ID（可以问 @userinfobot），群组是以 - 开头的数字，频道可以填 @频道用户名"})
			return
		}
	}
	if token == "" && body.ChatID == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有要保存的内容"})
		return
	}
	if token == "" {
		// 只改 chat id：令牌必须已经在后台保存过，否则后台配置不完整，永远不会生效
		if raw, _ := db.GetSetting(settingTelegramBotToken); strings.TrimSpace(raw) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请同时填写机器人令牌"})
			return
		}
	}

	kv := map[string]string{}
	if body.ChatID != nil {
		kv[settingTelegramChatID] = chatID
	}
	if token != "" {
		mk, err := secretbox.LoadKey()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "保存令牌需要先配置 APP_MASTER_KEY（令牌要加密保存）"})
			return
		}
		sealed, err := secretbox.Seal(mk, settingTelegramBotToken, token)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "加密令牌失败"})
			return
		}
		kv[settingTelegramBotToken] = sealed
	}
	if err := db.SetSettings(kv); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
	if token != "" {
		_ = db.DeleteSetting(legacyTelegramTokenKey)
	}
	tokenNote := "令牌不变"
	if token != "" {
		tokenNote = "令牌已更新"
	}
	chatNote := "chat id 不变"
	if body.ChatID != nil {
		chatNote = "chat id=" + chatID
	}
	auditAdmin(c, "update_telegram", tokenNote+"，"+chatNote)
	AdminGetTelegram(c)
}

// AdminTestTelegram POST /api/v1/admin/notify/telegram/test：用当前生效的配置同步发一条测试消息。
func AdminTestTelegram(c *gin.Context) {
	cfg := notify.Current()
	if !cfg.Ready() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "还没有配置 Telegram：请先填写机器人令牌和 chat id 并保存"})
		return
	}
	now := time.Now().In(beijingZone).Format("2006-01-02 15:04:05")
	text := fmt.Sprintf("✅ <b>测试消息</b>\n%s 的系统告警会发到这里。\n配置来源：%s\n🕐 %s（北京时间）",
		html.EscapeString(settingOr("brand_name", "Recharge Portal")), telegramSourceLabel(cfg.Source), now)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	if err := notify.Send(ctx, cfg, text); err != nil {
		msg := telegramErrorHint(err)
		auditAdmin(c, "telegram_test", "失败："+msg)
		c.JSON(http.StatusBadGateway, gin.H{"error": msg})
		return
	}
	auditAdmin(c, "telegram_test", "成功，chat id="+cfg.ChatID+"，来源="+telegramSourceLabel(cfg.Source))
	c.JSON(http.StatusOK, gin.H{"ok": true, "source": cfg.Source, "chat_id": cfg.ChatID})
}

func telegramSourceLabel(s string) string {
	switch s {
	case notify.SourceSettings:
		return "后台配置"
	case notify.SourceEnv:
		return "环境变量"
	}
	return "未配置"
}

// telegramErrorHint 把 Telegram 的常见错误翻成处理方法，原文附在后面。err 里已经不含令牌。
func telegramErrorHint(err error) string {
	var apiErr *notify.APIError
	if !errors.As(err, &apiErr) {
		return "连不上 Telegram（" + err.Error() + "）。服务器所在网络可能访问不了 api.telegram.org，可以设置环境变量 " + notify.ProxyEnv + "（只给 Telegram 用的代理，如 http://127.0.0.1:7890）后重启服务"
	}
	d := strings.ToLower(apiErr.Description)
	hint := ""
	switch {
	case apiErr.Code == http.StatusUnauthorized:
		hint = "令牌无效：请到 @BotFather 核对令牌，重新填写"
	case strings.Contains(d, "can't initiate conversation") || strings.Contains(d, "bot was blocked"):
		hint = "机器人不能主动给你发消息：先在 Telegram 里打开这个机器人，给它发一条消息（屏蔽过的要先解除）"
	case strings.Contains(d, "chat not found"):
		hint = "找不到这个 chat id：私聊填你自己的数字 ID（可以问 @userinfobot），群组要先把机器人拉进群"
	case apiErr.Code == http.StatusTooManyRequests:
		hint = "发得太频繁，被 Telegram 限流了，稍后再试"
	}
	if hint == "" {
		return apiErr.Error()
	}
	return hint + "（" + apiErr.Error() + "）"
}
