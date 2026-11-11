package handler

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// E-10：已结束（consumed）的码不能再启用，和禁用侧一样返回 409，不写审计。
func TestAdminEnableRejectsEndedCDK(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	_, id := e.issueCode("plus", "PH")
	e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, "a@example.com", "succeeded", "", true)

	w := e.do("POST", fmt.Sprintf("/admin/cdks/%d/enable", id), "")
	if w.Code != http.StatusConflict {
		t.Fatalf("已结束的码启用应 409，实际 %d %s", w.Code, w.Body.String())
	}
	if a := localAudit(t); strings.Contains(a, "local_cdk_enable") {
		t.Fatalf("被拒绝的启用不该写审计: %s", a)
	}
}
