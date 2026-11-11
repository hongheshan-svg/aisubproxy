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
// 日志和告警只写坏项本身（TRUSTED_PROXIES 里只有 IP 和网段，没有令牌或口令）。
func applyTrustedProxies(engine *gin.Engine, raw string, alert func(string)) {
	valid, invalid := auth.SplitTrustedProxies(raw)
	if len(invalid) > 0 {
		msg := fmt.Sprintf("TRUSTED_PROXIES 有 %d 项写错，已跳过：%s。其余 %d 项照常生效；请改成 IP 或网段（如 172.16.0.0/12），主机名不能用。",
			len(invalid), strings.Join(invalid, "、"), len(valid))
		log.Printf("⚠️  %s", msg)
		if alert != nil {
			alert(msg)
		}
	}
	if len(valid) == 0 {
		valid = nil
	}
	if err := engine.SetTrustedProxies(valid); err != nil {
		log.Printf("⚠️  设置信任代理失败: %v", err)
	}
}
