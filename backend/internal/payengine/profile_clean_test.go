package payengine

import (
	"testing"
	"time"
)

// 已知问题清单【涉及敏感数据】：Chrome 用户目录残留最长约 2 小时，其中一半是清理每小时才跑一次。主人已退出的目录一轮就删，
// 清理间隔就是残留的最长时间。
func TestProfileCleanEveryAtMostFiveMinutes(t *testing.T) {
	if profileCleanEvery > 5*time.Minute {
		t.Fatalf("浏览器残留清理间隔应不超过 5 分钟: %s", profileCleanEvery)
	}
}
