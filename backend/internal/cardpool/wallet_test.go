package cardpool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// accountPage 生成 ListAccounts 的顶层分页响应；编号从 from 到 to（含）。
func accountPage(count, from, to int) mwtest.Reply {
	var items []string
	for i := from; i <= to; i++ {
		items = append(items, fmt.Sprintf(`{"account_no":"A%03d","account_name":"n%d","account_status":"ACTIVE","account_type":"SUB_ACCOUNT","create_time":%d}`,
			i, i, 1700000000+i))
	}
	return mwtest.Reply{Body: fmt.Sprintf(`{"count":"%d","data":[%s]}`, count, strings.Join(items, ","))}
}

func accountRow(t *testing.T, no string) Account {
	t.Helper()
	a, err := getAccount(context.Background(), no)
	if err != nil {
		t.Fatalf("读取账户 %s: %v", no, err)
	}
	return *a
}

func TestSyncAccountsAllPages(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	mw.On("GET", "/v1/accounts/page", accountPage(51, 1, 50), accountPage(51, 51, 51))
	if err := s.SyncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_accounts WHERE gone = 0`); n != 51 {
		t.Fatalf("行数 = %d，期望 51", n)
	}
	if got := len(mw.Requests("GET", "/v1/accounts/page")); got != 2 {
		t.Fatalf("请求数 = %d，期望 2", got)
	}
	// 本地补上 out_account_no，再同步时少了 A051
	if _, err := db.DB.Exec(`UPDATE mw_accounts SET out_account_no = 'AC1' WHERE account_no = 'A001'`); err != nil {
		t.Fatal(err)
	}
	mw.On("GET", "/v1/accounts/page", accountPage(50, 1, 50))
	if err := s.SyncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	if !accountRow(t, "A051").Gone {
		t.Fatal("A051 应标记 gone")
	}
	if a := accountRow(t, "A001"); a.Gone || a.OutAccountNo != "AC1" {
		t.Fatalf("A001 = %+v，out_account_no 不应被清空", a)
	}
	// 再次出现
	mw.On("GET", "/v1/accounts/page", accountPage(51, 1, 50), accountPage(51, 51, 51))
	if err := s.SyncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	if accountRow(t, "A051").Gone {
		t.Fatal("A051 再次出现应清除 gone")
	}
}

func TestSyncAccountsFailureKeepsLocal(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	mw.On("GET", "/v1/accounts/page", accountPage(2, 1, 2))
	if err := s.SyncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	// 第 1 页只有 1 条而 count=60，继续取第 2 页时失败
	mw.On("GET", "/v1/accounts/page", accountPage(60, 3, 3), mwtest.Unknown())
	if err := s.SyncAccounts(ctx); err == nil {
		t.Fatal("期望返回错误")
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_accounts`); n != 2 {
		t.Fatalf("行数 = %d，期望 2", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_accounts WHERE gone = 1`); n != 0 {
		t.Fatalf("gone 行数 = %d，期望 0", n)
	}
}

// 平台一个账户都不返回（商户至少有主钱包）时不提交：报错，本地账户不变（R13 后空 data 读作空页）。
func TestSyncAccountsRefusesZeroAccounts(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	mw.On("GET", "/v1/accounts/page", accountPage(2, 1, 2))
	if err := s.SyncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"count":"0","data":[]}`, `{"count":"0","data":null}`, `{"count":"0"}`} {
		mw.On("GET", "/v1/accounts/page", mwtest.Reply{Body: body})
		if err := s.SyncAccounts(ctx); !errors.Is(err, ErrNoAccounts) {
			t.Fatalf("%s: err = %v，期望 ErrNoAccounts", body, err)
		}
		if n := countRows(t, `SELECT COUNT(*) FROM mw_accounts WHERE gone = 0`); n != 2 {
			t.Fatalf("%s: 未消失的账户 = %d，期望 2（本地不应改动）", body, n)
		}
	}
}

func TestSyncAccountsConcurrentShare(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	p := accountPage(1, 1, 1)
	p.Hang = 200 * time.Millisecond
	mw.On("GET", "/v1/accounts/page", p)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = s.SyncAccounts(ctx) }()
	go func() { defer wg.Done(); time.Sleep(50 * time.Millisecond); errs[1] = s.SyncAccounts(ctx) }()
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errs = %v", errs)
	}
	if got := len(mw.Requests("GET", "/v1/accounts/page")); got != 1 {
		t.Fatalf("请求数 = %d，期望 1", got)
	}
}

func TestCreateSubAccount(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	for _, name := range []string{"", "  ", "a b", "a\nb", strings.Repeat("字", 51)} {
		if _, err := s.CreateSubAccount(ctx, name, "admin"); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("name %q: err = %v，期望 ErrBadRequest", name, err)
		}
	}
	if got := len(mw.Requests("POST", "/v1/accounts")); got != 0 {
		t.Fatalf("非法名称不应发请求，收到 %d", got)
	}
	mw.On("POST", "/v1/accounts", mwtest.OK(`{"account_no":"S1","account_name":"Ops","out_account_no":"ACX","status":"ACTIVE","create_time":1700000000}`))
	a, err := s.CreateSubAccount(ctx, " Ops ", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if a.AccountNo != "S1" || a.AccountType != "SUB_ACCOUNT" || a.Status != "ACTIVE" || a.OutAccountNo != "ACX" {
		t.Fatalf("账户 = %+v", a)
	}
	if accountRow(t, "S1").AccountName != "Ops" {
		t.Fatal("本地应有该账户")
	}
	reqs := mw.Requests("POST", "/v1/accounts")
	if len(reqs) != 1 {
		t.Fatalf("请求数 = %d", len(reqs))
	}
	body := string(reqs[0].Body)
	if !strings.HasPrefix(body, `{"data":{"account_name":"Ops","out_account_no":"AC`) || strings.Contains(body, "purpose") {
		t.Fatalf("请求体 = %s", body)
	}

	mw.On("POST", "/v1/accounts", mwtest.Unknown())
	_, err = s.CreateSubAccount(ctx, "Next", "admin")
	if marswallex.OutcomeOf(err) != marswallex.OutcomeUnknown {
		t.Fatalf("err = %v，期望 OutcomeUnknown", err)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_accounts`); n != 1 {
		t.Fatalf("行数 = %d，期望 1", n)
	}
}

const twoAccounts = `{"count":"2","data":[
	{"account_no":"M1","account_name":"main","account_status":"ACTIVE","account_type":"MAIN"},
	{"account_no":"S1","account_name":"sub","account_status":"%s","account_type":"SUB_ACCOUNT"}]}`

func TestSetSubAccountStatus(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	mw.On("GET", "/v1/accounts/page", mwtest.Reply{Body: fmt.Sprintf(twoAccounts, "ACTIVE")})
	if err := s.SyncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSubAccountStatus(ctx, "M1", "DISABLED", "测试原因", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("MAIN: err = %v", err)
	}
	if _, err := s.SetSubAccountStatus(ctx, "NOPE", "DISABLED", "测试原因", "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知账户: err = %v", err)
	}
	if _, err := s.SetSubAccountStatus(ctx, "S1", "FROZEN", "测试原因", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("非法状态: err = %v", err)
	}
	for _, r := range []string{"a", strings.Repeat("字", 101), "停用😀"} {
		if _, err := s.SetSubAccountStatus(ctx, "S1", "DISABLED", r, "admin"); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("remark %q: err = %v", r, err)
		}
	}
	if got := len(mw.Requests("PUT", "/v1/accounts/S1/status")); got != 0 {
		t.Fatalf("非法参数不应发请求，收到 %d", got)
	}
	mw.On("PUT", "/v1/accounts/S1/status", mwtest.OK(`{"account_no":"S1","out_account_no":"ACX"}`))
	mw.On("GET", "/v1/accounts/page", mwtest.Reply{Body: fmt.Sprintf(twoAccounts, "DISABLED")})
	a, err := s.SetSubAccountStatus(ctx, "S1", "DISABLED", "暂停使用", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "DISABLED" || accountRow(t, "S1").Status != "DISABLED" {
		t.Fatalf("账户 = %+v", a)
	}
	reqs := mw.Requests("PUT", "/v1/accounts/S1/status")
	if len(reqs) != 1 || string(reqs[0].Body) != `{"account_status":"DISABLED","remark":"暂停使用"}` {
		t.Fatalf("PUT 请求 = %+v", reqs)
	}
	if got := len(mw.Requests("GET", "/v1/accounts/page")); got != 2 {
		t.Fatalf("同步次数 = %d，期望 2", got)
	}
}

// 变更前开始的那一轮同步（返回旧状态）还在进行时，启停不能共享它的旧数据。
func TestSetSubAccountStatusIgnoresStaleSharedSync(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	mw.On("GET", "/v1/accounts/page", mwtest.Reply{Body: fmt.Sprintf(twoAccounts, "ACTIVE")})
	if err := s.SyncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	stale := mwtest.Reply{Body: fmt.Sprintf(twoAccounts, "ACTIVE"), Hang: 300 * time.Millisecond}
	mw.On("GET", "/v1/accounts/page", stale)
	mw.On("PUT", "/v1/accounts/S1/status", mwtest.OK(`{"account_no":"S1","out_account_no":"ACX"}`))
	done := make(chan error, 1)
	go func() { done <- s.SyncAccounts(ctx) }()
	time.Sleep(60 * time.Millisecond) // 让旧同步先拿到锁并发出请求
	a, err := s.SetSubAccountStatus(ctx, "S1", "DISABLED", "暂停使用", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if a.Status != "DISABLED" || accountRow(t, "S1").Status != "DISABLED" {
		t.Fatalf("返回 %q、本地 %q，期望都是 DISABLED", a.Status, accountRow(t, "S1").Status)
	}
}

func TestAccountStatusWebhook(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	mw.On("GET", "/v1/accounts/page", accountPage(1, 1, 1))
	if err := s.SyncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	id := enqueueEvent(t, marswallex.TopicAccountStatusChanged,
		`{"account_no":"A001","out_account_no":"ACZ","account_status":"DISABLED","account_type":"SUB_ACCOUNT"}`)
	mustProcessEvents(t, s)
	mustBeProcessed(t, id, "")
	if a := accountRow(t, "A001"); a.Status != "DISABLED" || a.OutAccountNo != "ACZ" {
		t.Fatalf("账户 = %+v", a)
	}

	bad := enqueueEvent(t, marswallex.TopicAccountStatusChanged, `{"account_status":"DISABLED"}`)
	mustProcessEvents(t, s)
	if e := getEvent(t, bad); !e.processed || e.note != "数据无法解析" {
		t.Fatalf("缺 account_no: %+v", e)
	}

	before := len(mw.Requests("GET", "/v1/accounts/page"))
	unknown := enqueueEvent(t, marswallex.TopicAccountStatusChanged, `{"account_no":"ZZ9","account_status":"ACTIVE"}`)
	mustProcessEvents(t, s)
	mustBeProcessed(t, unknown, "已安排同步")
	lg := &loopLog{now: s.now, last: map[string]time.Time{}}
	s.maintainRound(ctx, lg)
	if got := len(mw.Requests("GET", "/v1/accounts/page")); got != before+1 {
		t.Fatalf("维护轮同步次数 = %d，期望 %d", got, before+1)
	}
	if s.accountsDirty.Load() {
		t.Fatal("同步成功后应清除标记")
	}
	s.maintainRound(ctx, lg)
	if got := len(mw.Requests("GET", "/v1/accounts/page")); got != before+1 {
		t.Fatal("标记清除后不应再同步")
	}
}

func TestReceivingAddresses(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	mw.On("GET", "/v1/receiving/receiving-address", mwtest.OK(`[
		{"currencies":["USDT"],"address":"0xabc","chain_type":"ETH"},
		{"currencies":["USDT","USDC"],"address":"Txyz","chain_type":"TRX"}]`))
	got, err := s.ReceivingAddresses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Address != "0xabc" || got[1].ChainType != "TRX" || len(got[1].Currencies) != 2 {
		t.Fatalf("地址 = %+v", got)
	}
	mw.On("GET", "/v1/receiving/receiving-address", mwtest.OK(`[]`))
	got, err = s.ReceivingAddresses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("空数据应返回空切片，得到 %#v", got)
	}
	// R13：data 为 null 同样读作空列表
	mw.On("GET", "/v1/receiving/receiving-address", mwtest.Reply{Body: `{"data":null}`})
	if got, err = s.ReceivingAddresses(ctx); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("data 为 null 应返回空切片: %#v %v", got, err)
	}
}

func TestAccountSerialsValidation(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	ctx := context.Background()
	start := clock.Now().Unix() - 3600
	end := clock.Now().Unix()
	bad := []SerialsQuery{
		{Currency: "USD", Start: start, End: end},
		{AccountNo: "A1", Start: start, End: end},
		{AccountNo: "A1", Currency: "USD", Start: end, End: end},
		{AccountNo: "A1", Currency: "USD", Start: end, End: start},
		{AccountNo: "A1", Currency: "USD", Start: end - 90*86400 - 1, End: end},
	}
	for i, q := range bad {
		if _, _, err := s.AccountSerials(ctx, q); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("用例 %d 应为 ErrBadRequest，得到 %v", i, err)
		}
	}
	if n := len(mw.Requests("GET", "/v1/accounts/serials")); n != 0 {
		t.Fatalf("校验失败不应请求平台，请求数 %d", n)
	}
	mw.On("GET", "/v1/accounts/serials", mwtest.Reply{Body: `{"count":"1","data":[{"serial_id":"S1","currency":"USD","amount":"1.00","flag_dc":"C","transaction_type":"X","create_time":1700000000}]}`})
	rows, total, err := s.AccountSerials(ctx, SerialsQuery{AccountNo: "A1", Currency: "USD", Start: end - 90*86400, End: end, PageSize: 500})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].SerialID != "S1" {
		t.Fatalf("rows=%+v total=%d", rows, total)
	}
	reqs := mw.Requests("GET", "/v1/accounts/serials")
	if len(reqs) != 1 {
		t.Fatalf("请求数 = %d", len(reqs))
	}
	q := reqs[0].RawQuery
	for _, want := range []string{"page_size=100", "page_no=1", fmt.Sprintf("start_time=%d", end-90*86400), fmt.Sprintf("end_time=%d", end), "account_no=A1", "currency=USD"} {
		if !strings.Contains(q, want) {
			t.Fatalf("查询串 %q 缺少 %s", q, want)
		}
	}
}

// 平台不返回 count 时总数未知：total 报 -1，由前端按「本页满页才有下一页」翻页。
func TestAccountSerialsUnknownTotal(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	ctx := context.Background()
	end := clock.Now().Unix()
	q := SerialsQuery{AccountNo: "A1", Currency: "USD", Start: end - 3600, End: end, PageSize: 20}
	mw.On("GET", "/v1/accounts/serials", mwtest.Reply{Body: `{"data":[{"serial_id":"S1","currency":"USD","amount":"1.00","flag_dc":"CREDIT","transaction_type":"X","create_time":1700000000}]}`})
	rows, total, err := s.AccountSerials(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if total != -1 || len(rows) != 1 {
		t.Fatalf("count 缺失: rows=%d total=%d，期望 total=-1", len(rows), total)
	}
	mw.On("GET", "/v1/accounts/serials", mwtest.Reply{Body: `{"count":"7","data":[]}`})
	if _, total, err = s.AccountSerials(ctx, q); err != nil || total != 7 {
		t.Fatalf("有 count 时照常返回: total=%d err=%v", total, err)
	}
}
