package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
)

func maskEmailForAdmin(em string) string {
	em = strings.TrimSpace(em)
	if em == "" || em == "unknown" {
		return em
	}
	at := strings.LastIndex(em, "@")
	if at < 0 {
		return keepHead([]rune(em), 3) + "***" // 不是邮箱的串（比如只有用户名）：3 个字符以内只留 1 个
	}
	return keepHead([]rune(em[:at]), 2) + "***" + em[at:] // 按字符截，中文等多字节的本地部分不会截出乱码
}

// keepHead 是打码时留下的开头：不超过 short 个字符只留 1 个（留 2 个就等于没打码），更长的留 2 个。
func keepHead(r []rune, short int) string {
	switch {
	case len(r) > short:
		return string(r[:2])
	case len(r) > 0:
		return string(r[:1])
	}
	return ""
}

func adminName(c *gin.Context) string {
	if u, ok := c.Get("username"); ok {
		if s, ok := u.(string); ok && s != "" {
			return s
		}
	}
	if u, ok := c.Get("admin"); ok {
		if s, ok := u.(string); ok && s != "" {
			return s
		}
	}
	return "admin"
}
