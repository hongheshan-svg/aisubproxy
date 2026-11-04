package handler

import (
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// E-8：卡台批量操作后同步本地状态时，写库失败要返回给调用方，不能静默吞掉。
func TestSetLocalCDKStatusesReportsFailures(t *testing.T) {
	dbtest.Open(t)
	if err := db.DB.Close(); err != nil {
		t.Fatal(err)
	}
	failed := setLocalCDKStatuses([]int64{11, 12}, "disabled")
	if len(failed) != 2 || failed[0] != 11 || failed[1] != 12 {
		t.Fatalf("写库失败的 id 应全部返回，实际 %v", failed)
	}
}

func TestSetLocalCDKStatusesNoFailureIsEmptyNotNil(t *testing.T) {
	dbtest.Open(t)
	failed := setLocalCDKStatuses([]int64{1}, "unused")
	if failed == nil || len(failed) != 0 {
		t.Fatalf("无失败时应返回空数组，实际 %#v", failed)
	}
}
