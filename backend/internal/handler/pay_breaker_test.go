package handler

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

func payInsertDeclined(t *testing.T, n int) {
	t.Helper()
	now := time.Now().Unix() - 100
	for i := 0; i < n; i++ {
		no := fmt.Sprintf("PO%012d%08x", i, i)
		if _, err := db.DB.Exec(`INSERT INTO pay_orders (order_no, account_email, state, failure, submitted_at, created_at, updated_at, finished_at)
			VALUES (?, ?, 'failed', 'declined', ?, ?, ?, ?)`, no, no+"@x.com", now, now, now, now+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPayBreakerAPI(t *testing.T) {
	payEnv(t)

	// 没暂停：恢复返回 409
	if w := payDo(AdminPayBreakerResume, "POST", "/x", "/x", `{"note":"试一下"}`); w.Code != 409 {
		t.Fatalf("没暂停应 409: %d %s", w.Code, w.Body.String())
	}
	if a := payAudit(t); strings.Contains(a, "pay_breaker_resume") {
		t.Fatalf("没恢复成功不应写审计: %s", a)
	}

	payInsertDeclined(t, 3)
	w := payDo(AdminPayBreakerStatus, "GET", "/x", "/x", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var st map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st["paused"] != true || st["reason"] != "submit_failures" || st["reason_text"] == "" || st["threshold"] != float64(3) ||
		st["submitted_failure_streak"] != float64(3) || st["fill_failure_streak"] != float64(0) || st["tripped_at"] == nil {
		t.Fatalf("状态: %s", w.Body.String())
	}
	if nos, _ := st["order_nos"].([]any); len(nos) != 3 {
		t.Fatalf("单号: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "@x.com") {
		t.Fatalf("状态不应含邮箱: %s", w.Body.String())
	}

	// 没写说明不能恢复
	for _, body := range []string{`{}`, `{"note":"  "}`, `not json`} {
		if w := payDo(AdminPayBreakerResume, "POST", "/x", "/x", body); w.Code != 400 {
			t.Fatalf("%s 应 400: %d", body, w.Code)
		}
	}
	if st := payDo(AdminPayBreakerStatus, "GET", "/x", "/x", ""); !strings.Contains(st.Body.String(), `"paused":true`) {
		t.Fatalf("校验失败不应恢复: %s", st.Body.String())
	}

	w = payDo(AdminPayBreakerResume, "POST", "/x", "/x", `{"note":"已换卡段"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"paused":false`) || !strings.Contains(w.Body.String(), `"last_resume_by":"alice"`) {
		t.Fatalf("恢复: %d %s", w.Code, w.Body.String())
	}
	if a := payAudit(t); !strings.Contains(a, "pay_breaker_resume") || !strings.Contains(a, "已换卡段") {
		t.Fatalf("应写审计: %s", a)
	}
}

func TestPayBreakerAPIEngineNil(t *testing.T) {
	SetPayEngine(nil)
	for _, h := range []struct {
		name string
		do   func() int
	}{
		{"status", func() int { return payDo(AdminPayBreakerStatus, "GET", "/x", "/x", "").Code }},
		{"resume", func() int { return payDo(AdminPayBreakerResume, "POST", "/x", "/x", `{"note":"x"}`).Code }},
	} {
		if code := h.do(); code != 503 {
			t.Fatalf("%s 应 503: %d", h.name, code)
		}
	}
}

// 恢复点损坏时：状态接口如实报暂停，恢复接口能修好。
func TestPayBreakerAPICorruptReset(t *testing.T) {
	payEnv(t)
	if err := db.SetSetting("pay_breaker_reset", `{broken`); err != nil {
		t.Fatal(err)
	}
	w := payDo(AdminPayBreakerStatus, "GET", "/x", "/x", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"paused":true`) || !strings.Contains(w.Body.String(), "check_failed") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := payDo(AdminPayBreakerResume, "POST", "/x", "/x", `{"note":"修复"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
