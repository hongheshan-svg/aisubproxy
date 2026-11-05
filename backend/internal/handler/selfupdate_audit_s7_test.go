package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// N-5：版本号解析不了（不合 semver）时，不能静默跳过 --check 照常更新，要中止并说明。
func TestS7PreflightAbortsOnUnparsableVersion(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "started")
	t.Setenv("O3_MARK", mark)
	bin := filepath.Join(dir, "cdk-recharge")
	writeFakeBinary(t, bin, o3CheckScript("1.6.0_hotfix", `touch "$O3_MARK"; echo "启动检查通过"; exit 0`))
	err := preflightBinary(bin, "1.6.0_hotfix")
	if err == nil || !strings.Contains(err.Error(), "无法解析，已中止更新") || !strings.Contains(err.Error(), "1.6.0_hotfix") {
		t.Fatalf("应中止并提示版本号无法解析: %v", err)
	}
	if _, e := os.Stat(mark); e == nil {
		t.Fatal("版本号解析不了，不应运行 --check")
	}
}
