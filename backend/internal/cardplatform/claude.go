package cardplatform

import "strings"

// ClaudeBarePlan 去掉 claude_ 前缀的裸档位（本站界面与码上都用裸键）。
func ClaudeBarePlan(plan string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(plan)), "claude_")
}

// ClaudeIssuePlan 发给卡台的发码档位：显式带 claude_ 前缀。卡台落库时会去掉前缀。
func ClaudeIssuePlan(plan string) string {
	return "claude_" + ClaudeBarePlan(plan)
}
