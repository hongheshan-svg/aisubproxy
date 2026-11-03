package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 密钥类键：只接受写入，读出脱敏
var secretSettingKeys = map[string]bool{
	"card_api_base":  false,
	"card_api_key":   true,
	"webhook_secret": true, // 卡台开发者页 whsec_…
	// Telegram 告警在 telegram_settings.go 单独处理（令牌加密保存）
	// agent_swap handled specially (hash)
}

// 站点未设置时的默认外观：卡台新版（zovo）+ 夜间，与前端 theme.ts / index.html 一致。
// 已在后台选过皮肤的站点保持原样（存了 skin 设置项就不走默认）。
const (
	defaultSiteSkin  = "zovo"
	defaultThemeMode = "dark"
)

// PublicSiteConfig GET /api/v1/public/site — 用户端拉品牌/皮肤（无鉴权）
func PublicSiteConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"installed":  db.IsInstalled(),
		"brand_name": settingOr("brand_name", "Recharge Portal"),
		"brand_sub":  settingOr("brand_sub", "Account Upgrade Service"),
		"skin":       settingOr("skin", defaultSiteSkin),
		"theme_mode": settingOr("theme_mode", defaultThemeMode),
	})
}

// AdminGetSettings GET /api/v1/admin/settings
func AdminGetSettings(c *gin.Context) {
	out := gin.H{
		"brand_name": settingOr("brand_name", "Recharge Portal"),
		"brand_sub":  settingOr("brand_sub", "Account Upgrade Service"),
		"skin":       settingOr("skin", defaultSiteSkin),
		"theme_mode": settingOr("theme_mode", defaultThemeMode),
	}
	// 非密钥可读
	for k, isSecret := range secretSettingKeys {
		v, _ := db.GetSetting(k)
		if isSecret {
			out[k+"_configured"] = strings.TrimSpace(v) != ""
			out[k+"_hint"] = maskSecret(v)
		} else {
			out[k] = v
		}
	}
	out["agent_swap_password_configured"] = agentSwapPasswordConfigured()
	out["agent_swap_daily_limit"] = agentSwapDailyLimit()
	c.JSON(http.StatusOK, out)
}

type adminSettingsBody struct {
	BrandName     *string `json:"brand_name"`
	BrandSub      *string `json:"brand_sub"`
	Skin          *string `json:"skin"`
	ThemeMode     *string `json:"theme_mode"`
	CardAPIBase   *string `json:"card_api_base"`
	CardAPIKey    *string `json:"card_api_key"`
	WebhookSecret *string `json:"webhook_secret"`
	// 代理失败换码密码（明文一次写入，存 bcrypt；空=不改）
	AgentSwapPassword *string `json:"agent_swap_password"`
	// 同一换码密码每个北京日最多换多少张（默认 20，必须大于 0；不传不改）
	AgentSwapDailyLimit *int `json:"agent_swap_daily_limit"`
}

// AdminPutSettings PUT /api/v1/admin/settings
func AdminPutSettings(c *gin.Context) {
	var body adminSettingsBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	setIf := func(key string, p *string, max int) error {
		if p == nil {
			return nil
		}
		v := strings.TrimSpace(*p)
		if max > 0 && len(v) > max {
			v = v[:max]
		}
		// 空字符串对密钥类表示「不修改」；若要清空用 " " 不支持清空密钥防误操作
		if v == "" && secretSettingKeys[key] {
			return nil
		}
		return db.SetSetting(key, v)
	}

	// 先校验再写：非法值不能带着其它设置项一起存进去一半
	if body.AgentSwapDailyLimit != nil && *body.AgentSwapDailyLimit <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "代理换码每日上限必须大于 0"})
		return
	}

	allowedSkins := map[string]bool{
		"zovo": true, "terracotta": true, "ocean": true, "cyber": true, "forest": true, "violet": true,
		"slate": true, "rose": true, "ember": true, "noir": true, "paper": true,
	}
	if body.Skin != nil {
		s := strings.TrimSpace(*body.Skin)
		if !allowedSkins[s] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid skin"})
			return
		}
		_ = db.SetSetting("skin", s)
	}
	if body.ThemeMode != nil {
		m := strings.TrimSpace(*body.ThemeMode)
		if m != "light" && m != "dark" && m != "auto" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid theme_mode"})
			return
		}
		_ = db.SetSetting("theme_mode", m)
	}
	_ = setIf("brand_name", body.BrandName, 40)
	_ = setIf("brand_sub", body.BrandSub, 80)
	_ = setIf("card_api_base", body.CardAPIBase, 200)
	_ = setIf("card_api_key", body.CardAPIKey, 200)
	_ = setIf("webhook_secret", body.WebhookSecret, 200)
	if body.AgentSwapDailyLimit != nil {
		if err := db.SetSetting(agentSwapDailyLimitKey, strconv.Itoa(*body.AgentSwapDailyLimit)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
			return
		}
	}
	if body.AgentSwapPassword != nil {
		pw := strings.TrimSpace(*body.AgentSwapPassword)
		if pw != "" {
			if err := setAgentSwapPassword(pw); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
	}

	auditAdmin(c, "update_settings", "site settings")
	AdminGetSettings(c)
}

func settingOr(key, def string) string {
	v, _ := db.GetSetting(key)
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func maskSecret(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if len(v) <= 4 {
		return "****"
	}
	return "****" + v[len(v)-4:]
}
