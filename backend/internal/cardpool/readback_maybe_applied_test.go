package cardpool

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// 平台已生效、本地也写了，最后读回本地记录失败（H2-08 审查修复）：也算平台可能已生效（ErrMaybeApplied），
// 调用方据此记失败审计、提示勿重复提交。子账户的读回不受调用方取消影响。

// logHook 在日志里出现 match 时执行一次 fn：用来在「写完本地、读回之前」那一刻取消请求的 ctx。
type logHook struct {
	match string
	fn    func()
	once  sync.Once
}

func (h *logHook) Write(p []byte) (int, error) {
	if strings.Contains(string(p), h.match) {
		h.once.Do(h.fn)
	}
	return len(p), nil
}

func hookLog(t *testing.T, match string, fn func()) {
	t.Helper()
	log.SetOutput(&logHook{match: match, fn: fn})
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
}

// corruptOn 建一个触发器：表 table 上 event（如 "AFTER INSERT"）发生且 when 成立时，把这一行的 created_at 改成读不出来的文本，
// 之后按主键读回这一行会报扫描错误（模拟写成功、读回失败）。
func corruptOn(t *testing.T, table, event, when, key string) {
	t.Helper()
	q := fmt.Sprintf(`CREATE TRIGGER corrupt_%s %s ON %s WHEN %s BEGIN
		UPDATE %s SET created_at = 'bad' WHERE %s = NEW.%s; END`, table, event, table, when, table, key, key)
	if _, err := db.DB.Exec(q); err != nil {
		t.Fatal(err)
	}
}

func TestCreateSubAccountReadBackFailureMaybeApplied(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	mw.On("POST", "/v1/accounts", mwtest.OK(`{"account_no":"S1","account_name":"Ops","out_account_no":"ACX","status":"ACTIVE"}`))
	corruptOn(t, "mw_accounts", "AFTER INSERT", "1", "account_no")
	_, err := s.CreateSubAccount(context.Background(), "Ops", "admin")
	if !errors.Is(err, ErrMaybeApplied) {
		t.Fatalf("平台已建好、读回失败应算可能已生效: %v", err)
	}
}

func TestCreateSubAccountReadBackIgnoresCancel(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	mw.On("POST", "/v1/accounts", mwtest.OK(`{"account_no":"S1","account_name":"Ops","out_account_no":"ACX","status":"ACTIVE"}`))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hookLog(t, "创建子账户 S1", cancel) // 本地写完、读回之前管理员断开
	a, err := s.CreateSubAccount(ctx, "Ops", "admin")
	if err != nil || a == nil || a.AccountNo != "S1" {
		t.Fatalf("平台和本地都已生效，读回不应因请求取消失败: %+v %v", a, err)
	}
}

func TestSetSubAccountStatusReadBackFailureMaybeApplied(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	mw.On("GET", "/v1/accounts/page", mwtest.Reply{Body: fmt.Sprintf(twoAccounts, "ACTIVE")})
	if err := s.SyncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	mw.On("PUT", "/v1/accounts/S1/status", mwtest.OK(`{"account_no":"S1","out_account_no":"ACX"}`))
	corruptOn(t, "mw_accounts", "AFTER UPDATE OF status", "NEW.status = 'DISABLED'", "account_no")
	_, err := s.SetSubAccountStatus(ctx, "S1", "DISABLED", "暂停使用", "admin")
	if !errors.Is(err, ErrMaybeApplied) {
		t.Fatalf("平台已启停、读回失败应算可能已生效: %v", err)
	}
}

func TestSetSubAccountStatusReadBackIgnoresCancel(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	mw.On("GET", "/v1/accounts/page", mwtest.Reply{Body: fmt.Sprintf(twoAccounts, "ACTIVE")})
	if err := s.SyncAccounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	mw.On("PUT", "/v1/accounts/S1/status", mwtest.OK(`{"account_no":"S1","out_account_no":"ACX"}`))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hookLog(t, "将子账户 S1 置为", cancel)
	a, err := s.SetSubAccountStatus(ctx, "S1", "DISABLED", "暂停使用", "admin")
	if err != nil || a == nil || a.Status != "DISABLED" {
		t.Fatalf("平台和本地都已生效，读回不应因请求取消失败: %+v %v", a, err)
	}
}

func TestCreateCardholderReadBackFailureMaybeApplied(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply mwtest.Reply
		state string
	}{
		{"平台成功", mwtest.OK(`{"card_holder_id":"h1"}`), SubmissionLinked},
		{"平台结果不明", mwtest.Unknown(), SubmissionUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mw, _, _ := newTestService(t)
			mw.On("POST", pathCreateHolder, tc.reply)
			corruptOn(t, "mw_cardholder_submissions", "AFTER UPDATE OF state", "NEW.state = '"+tc.state+"'", "out_card_holder_id")
			sub, err := s.CreateCardholder(context.Background(), validUSProfile(), "admin")
			if sub != nil || !errors.Is(err, ErrMaybeApplied) {
				t.Fatalf("提交记录已写、读回失败应算可能已生效: %+v %v", sub, err)
			}
			for _, v := range piiValues {
				if strings.Contains(err.Error(), v) {
					t.Fatalf("错误信息含资料值 %q: %q", v, err.Error())
				}
			}
		})
	}
}
