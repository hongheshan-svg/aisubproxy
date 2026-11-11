package server

import (
	"fmt"
	"log"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/auth"
)

// applyTrustedProxies 按 TRUSTED_PROXIES 设置 gin 信任的反向代理（B1-01）。
// 写错的项（比如主机名、少一段的网段）跳过，其余照常生效；有坏项时打日志并发一条告警，
// 否则 gin 遇到第一个坏项就丢掉后面全部，所有访客会被当成同一个来源去限流，页面上却看不出来。
// 坏项可能是误填的带账号口令的代理地址，进日志和告警前打码（maskBadProxy）。
func applyTrustedProxies(engine *gin.Engine, raw string, alert func(string)) {
	valid, invalid := auth.SplitTrustedProxies(raw)
	if len(invalid) > 0 {
		shown := make([]string, len(invalid))
		for i, v := range invalid {
			shown[i] = maskBadProxy(v, i+1)
		}
		msg := fmt.Sprintf("TRUSTED_PROXIES 有 %d 项写错，已跳过：%s。其余 %d 项照常生效；请改成 IP 或网段（如 172.16.0.0/12），主机名不能用。",
			len(invalid), strings.Join(shown, "、"), len(valid))
		if alert != nil {
			alert(msg) // 告警函数自己会打日志，这里不再重复
		} else {
			log.Printf("⚠️  %s", msg)
		}
	}
	if len(valid) == 0 {
		valid = nil
	}
	if err := engine.SetTrustedProxies(valid); err != nil {
		log.Printf("⚠️  设置信任代理失败: %v", err)
	}
}

// maskBadProxy 给写错的项打码：含 @ 或 :// 的不输出原文，其余截到 64 个字符。
func maskBadProxy(v string, n int) string {
	if strings.Contains(v, "@") || strings.Contains(v, "://") {
		return fmt.Sprintf("第 %d 项格式不对（含账号或协议，已隐去）", n)
	}
	if r := []rune(v); len(r) > 64 {
		return string(r[:64]) + "…"
	}
	return v
}
