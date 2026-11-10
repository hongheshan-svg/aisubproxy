package handler

import (
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 站点未设置时的默认外观：zovo 皮肤 + 跟随系统，与前端 theme.ts / index.html 一致。
// 已在后台选过皮肤的站点保持原样（存了 skin 设置项就不走默认）。
const (
	defaultSiteSkin  = "zovo"
	defaultThemeMode = "auto"
	// 主色默认值（深青）
	defaultPrimaryColor = "#0F6B5F"
)

// PublicSiteConfig GET /api/v1/public/site — 用户端拉品牌/皮肤（无鉴权）
func PublicSiteConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"installed":     db.IsInstalled(),
		"brand_name":    settingOr("brand_name", "Recharge Portal"),
		"brand_sub":     settingOr("brand_sub", "Account Upgrade Service"),
		"skin":          settingOr("skin", defaultSiteSkin),
		"theme_mode":    settingOr("theme_mode", defaultThemeMode),
		"primary_color": settingOr("primary_color", defaultPrimaryColor),
		"logo_version":  currentLogoVersion(),
	})
}

// AdminGetSettings GET /api/v1/admin/settings
func AdminGetSettings(c *gin.Context) {
	out := gin.H{
		"brand_name":    settingOr("brand_name", "Recharge Portal"),
		"brand_sub":     settingOr("brand_sub", "Account Upgrade Service"),
		"skin":          settingOr("skin", defaultSiteSkin),
		"theme_mode":    settingOr("theme_mode", defaultThemeMode),
		"primary_color": settingOr("primary_color", defaultPrimaryColor),
		"logo_version":  currentLogoVersion(),
	}
	out["agent_swap_password_configured"] = agentSwapPasswordConfigured()
	out["agent_swap_password_weak"] = agentSwapPasswordWeak()
	out["agent_swap_daily_limit"] = agentSwapDailyLimit()
	c.JSON(http.StatusOK, out)
}

type adminSettingsBody struct {
	BrandName    *string `json:"brand_name"`
	BrandSub     *string `json:"brand_sub"`
	Skin         *string `json:"skin"`
	ThemeMode    *string `json:"theme_mode"`
	PrimaryColor *string `json:"primary_color"`
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

	// 先校验再写：任何一项非法，都不能带着其它设置项一起存进去一半
	bad := func(msg string) {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
	}
	// 文本项：按字符数（rune）判断长度，超长直接拒绝，不截断
	type textItem struct {
		key   string
		p     *string
		max   int
		label string
	}
	if body.AgentSwapDailyLimit != nil && *body.AgentSwapDailyLimit <= 0 {
		bad("代理换码每日上限必须大于 0")
		return
	}
	allowedSkins := map[string]bool{
		"zovo": true, "terracotta": true, "ocean": true, "cyber": true, "forest": true, "violet": true,
		"slate": true, "rose": true, "ember": true, "noir": true, "paper": true,
	}
	if body.Skin != nil && !allowedSkins[strings.TrimSpace(*body.Skin)] {
		bad("invalid skin")
		return
	}
	if body.ThemeMode != nil {
		if m := strings.TrimSpace(*body.ThemeMode); m != "light" && m != "dark" && m != "auto" {
			bad("invalid theme_mode")
			return
		}
	}
	var writes []struct{ key, val string }
	if body.PrimaryColor != nil {
		pc := strings.ToUpper(strings.TrimSpace(*body.PrimaryColor))
		if err := validPrimaryColor(pc); err != nil {
			bad(err.Error())
			return
		}
		writes = append(writes, struct{ key, val string }{"primary_color", pc})
	}
	for _, it := range []textItem{
		{"brand_name", body.BrandName, 40, "站点名称"},
		{"brand_sub", body.BrandSub, 80, "副标题"},
	} {
		if it.p == nil {
			continue
		}
		v := strings.TrimSpace(*it.p)
		if utf8.RuneCountInString(v) > it.max {
			bad(it.label + "最多 " + strconv.Itoa(it.max) + " 个字符")
			return
		}
		writes = append(writes, struct{ key, val string }{it.key, v})
	}

	if err := writeSiteSettings(body, writes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
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

// writeSiteSettings 把已校验的设置项写库，任何一次写失败都返回错误（不吞）。
func writeSiteSettings(body adminSettingsBody, texts []struct{ key, val string }) error {
	if body.Skin != nil {
		if err := db.SetSetting("skin", strings.TrimSpace(*body.Skin)); err != nil {
			return err
		}
	}
	if body.ThemeMode != nil {
		if err := db.SetSetting("theme_mode", strings.TrimSpace(*body.ThemeMode)); err != nil {
			return err
		}
	}
	for _, w := range texts {
		if err := db.SetSetting(w.key, w.val); err != nil {
			return err
		}
	}
	return nil
}
