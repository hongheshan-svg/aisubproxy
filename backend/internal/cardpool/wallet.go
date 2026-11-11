package cardpool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

const (
	accountSyncPageSize = 50
	accountSyncMaxPages = 200 // 防止平台 count 异常时死循环
	accountTypeMain     = "MAIN"
	accountTypeSub      = "SUB_ACCOUNT"
	accountActive       = "ACTIVE"
	accountDisabled     = "DISABLED"
)

// SyncAccounts 拉取平台全部账户并整体写入本地：全部页成功后才在一个事务里 upsert，
// 并把本次没出现的标 gone = 1；中途失败或平台一个账户都不返回时不改本地表。并发调用共享同一轮结果。
func (s *Service) SyncAccounts(ctx context.Context) error {
	gen := s.syncGen.Load()
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if s.syncGen.Load() != gen { // 等锁期间别人已同步成功
		return nil
	}
	c, err := s.opts.Client()
	if err != nil {
		return err
	}
	var all []marswallex.Account
	for page := 1; page <= accountSyncMaxPages; page++ {
		p, err := c.ListAccounts(ctx, marswallex.ListAccountsQuery{PageNo: page, PageSize: accountSyncPageSize})
		if err != nil {
			return err
		}
		if len(p.Data) == 0 {
			break
		}
		all = append(all, p.Data...)
		if int64(len(all)) >= int64(p.Count) {
			break
		}
	}
	// 商户至少有主钱包：平台一个账户都不返回按异常处理，不把本地账户全部标为消失
	if len(all) == 0 {
		log.Printf("[cardpool] 同步账户：平台未返回任何账户，本次不写入")
		return ErrNoAccounts
	}
	// 分页接口没带 balance_list（字段缺失，不是空数组）的账户，用详情接口兜底取余额。
	// 兜底也失败就整体报错、本地不变：不能把「没拿到余额」当成余额 0 写进去，否则余额校验会基于假数据放行或拒绝。
	for i := range all {
		if all[i].BalanceList != nil {
			continue
		}
		d, err := c.GetAccount(ctx, all[i].AccountNo)
		if err != nil {
			return fmt.Errorf("账户 %s 的分页结果没有余额，用详情接口取余额也失败: %w", all[i].AccountNo, err)
		}
		all[i].BalanceList = d.BalanceList // 详情接口也没带余额时保持 nil，写成空列表（平台明确答复没有余额）
	}

	now := s.now().Unix()
	tx, err := db.DB.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE mw_accounts SET gone = 1`); err != nil {
		return err
	}
	for _, a := range all {
		balances, err := json.Marshal(a.BalanceList)
		if err != nil || a.BalanceList == nil {
			balances = []byte("[]")
		}
		purpose := ""
		if a.Purpose != nil {
			purpose = *a.Purpose
		}
		created := now
		if !a.CreateTime.IsZero() {
			created = a.CreateTime.Unix()
		}
		if _, err := tx.Exec(`INSERT INTO mw_accounts (`+accountCols+`) VALUES (?, '', ?, ?, ?, ?, ?, 0, ?, ?)
			ON CONFLICT(account_no) DO UPDATE SET account_name = excluded.account_name, account_type = excluded.account_type,
			purpose = excluded.purpose, status = excluded.status, balances = excluded.balances, gone = 0, synced_at = excluded.synced_at`,
			a.AccountNo, a.AccountName, a.AccountType, purpose, a.AccountStatus, string(balances), now, created); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.syncGen.Add(1)
	return nil
}

// ListAccounts 读本地账户：未消失的在前，MAIN 在前，其余按创建时间。
func (s *Service) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := db.DB.QueryContext(ctx, `SELECT `+accountCols+` FROM mw_accounts
		ORDER BY gone, CASE account_type WHEN 'MAIN' THEN 0 ELSE 1 END, created_at, account_no`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Account{}
	for rows.Next() {
		var a Account
		if err := a.scan(rows); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, rows.Err()
}

// markAccountsDirty 标记本地账户可能落后于平台，下一轮维护时同步。
func (s *Service) markAccountsDirty() { s.accountsDirty.Store(true) }

// syncAccountsIfDirty 在有标记时同步账户；成功后清除标记，失败保留。
func (s *Service) syncAccountsIfDirty(ctx context.Context) error {
	if !s.accountsDirty.Swap(false) {
		return nil
	}
	if err := s.SyncAccounts(ctx); err != nil {
		s.accountsDirty.Store(true)
		return err
	}
	return nil
}

// CreateSubAccount 创建子账户。账户名去首尾空白后 1–50 字且不含空白字符。
// 平台出错时原样返回（结果未知时由前端稍后刷新）。
func (s *Service) CreateSubAccount(ctx context.Context, name, by string) (*Account, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 50 {
		return nil, badRequest("账户名需为 1–50 个字")
	}
	if strings.IndexFunc(name, unicode.IsSpace) >= 0 {
		return nil, badRequest("账户名不能包含空白字符")
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	now := s.now()
	out, err := c.CreateAccount(ctx, marswallex.CreateAccountRequest{AccountName: name, OutAccountNo: newOutID("AC", now)})
	if err != nil {
		return nil, err
	}
	status := out.Status
	if status == "" {
		status = accountActive
	}
	purpose := ""
	if out.Purpose != nil {
		purpose = *out.Purpose
	}
	created := now.Unix()
	if !out.CreateTime.IsZero() {
		created = out.CreateTime.Unix()
	}
	acctName := out.AccountName
	if acctName == "" {
		acctName = name
	}
	if _, err := db.DB.ExecContext(context.WithoutCancel(ctx), `INSERT INTO mw_accounts (`+accountCols+`) VALUES (?, ?, ?, ?, ?, ?, '[]', 0, ?, ?)
		ON CONFLICT(account_no) DO UPDATE SET out_account_no = excluded.out_account_no, account_name = excluded.account_name,
		status = excluded.status, gone = 0`,
		out.AccountNo, out.OutAccountNo, acctName, accountTypeSub, purpose, status, now.Unix(), created); err != nil {
		return nil, maybeApplied(err)
	}
	log.Printf("[cardpool] %s 创建子账户 %s", by, out.AccountNo)
	return getAccount(ctx, out.AccountNo)
}

// isEmojiRune 判断是否 emoji（U+1F000–U+1FAFF、U+2600–U+27BF、U+FE0F）。
func isEmojiRune(r rune) bool {
	return (r >= 0x1F000 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF) || r == 0xFE0F
}

// SetSubAccountStatus 启用或停用子账户（不含 MAIN）。调用后无论成败都尝试同步，返回最新本地账户；
// 接口出错时返回接口错误。
func (s *Service) SetSubAccountStatus(ctx context.Context, accountNo, status, remark, by string) (*Account, error) {
	if status != accountActive && status != accountDisabled {
		return nil, badRequest("状态只能是 ACTIVE 或 DISABLED")
	}
	acct, err := getAccount(ctx, accountNo)
	if err != nil {
		return nil, err
	}
	if acct.AccountType == accountTypeMain {
		return nil, badRequest("主钱包不能启停")
	}
	remark = strings.TrimSpace(remark)
	if n := utf8.RuneCountInString(remark); n < 2 || n > 100 {
		return nil, badRequest("原因需为 2–100 个字")
	}
	if strings.IndexFunc(remark, isEmojiRune) >= 0 {
		return nil, badRequest("原因不能包含 emoji")
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	_, apiErr := c.UpdateAccountStatus(ctx, accountNo, marswallex.UpdateAccountStatusRequest{AccountStatus: status, Remark: remark})
	syncErr := s.SyncAccounts(ctx)
	if apiErr != nil {
		return nil, apiErr
	}
	// 同步可能共享了更早开始、数据发生在变更之前的那一轮，所以接口成功后无条件写入新状态
	if _, err := db.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE mw_accounts SET status = ? WHERE account_no = ?`, status, accountNo); err != nil {
		return nil, maybeApplied(err)
	}
	if syncErr != nil {
		log.Printf("[cardpool] 子账户 %s 状态已变更，同步账户失败: %v", accountNo, syncErr)
		s.markAccountsDirty()
	}
	log.Printf("[cardpool] %s 将子账户 %s 置为 %s", by, accountNo, status)
	return getAccount(ctx, accountNo)
}

// onAccountStatusChanged 按 account_no 更新本地账户状态；本地没有该账户时安排同步。
func (s *Service) onAccountStatusChanged(ctx context.Context, ev *marswallex.WebhookEvent) (string, error) {
	var d marswallex.AccountStatusChangedData
	if err := decodeEventData(ev, &d); err != nil {
		return "", err
	}
	no := strings.TrimSpace(d.AccountNo)
	if no == "" {
		return "", dataError("缺少 account_no")
	}
	res, err := db.DB.ExecContext(ctx, `UPDATE mw_accounts SET status = CASE WHEN ? = '' THEN status ELSE ? END,
		out_account_no = CASE WHEN ? = '' THEN out_account_no ELSE ? END WHERE account_no = ?`,
		strings.TrimSpace(d.AccountStatus), strings.TrimSpace(d.AccountStatus),
		strings.TrimSpace(d.OutAccountNo), strings.TrimSpace(d.OutAccountNo), no)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		s.markAccountsDirty()
		return "账户未同步，已安排同步", nil
	}
	return "账户 " + no + " 状态：" + strings.TrimSpace(d.AccountStatus), nil
}

// ReceivingAddresses 查询平台数币收款地址；平台返回 null 时给空切片。
func (s *Service) ReceivingAddresses(ctx context.Context) ([]marswallex.ReceivingAddress, error) {
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	out, err := c.ListReceivingAddresses(ctx)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []marswallex.ReceivingAddress{}
	}
	return out, nil
}

// SerialsQuery 为账户流水查询条件；Start/End 为 Unix 秒。
type SerialsQuery struct {
	AccountNo, Currency string
	Start, End          int64
	Page, PageSize      int
}

const maxSerialsSpan = 90 * 86400

// AccountSerials 查询账户流水（只读透传）。账户号与币种必填，窗口最长 90 天；平台未给总数时 total 为 -1。
func (s *Service) AccountSerials(ctx context.Context, q SerialsQuery) ([]marswallex.AccountSerial, int, error) {
	if strings.TrimSpace(q.AccountNo) == "" || strings.TrimSpace(q.Currency) == "" {
		return nil, 0, badRequest("账户号与币种必填")
	}
	if q.End <= q.Start {
		return nil, 0, badRequest("结束时间必须晚于开始时间")
	}
	if q.End-q.Start > maxSerialsSpan {
		return nil, 0, badRequest("查询跨度不能超过 90 天")
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = defaultPageSize
	}
	if q.PageSize > maxPageSize {
		q.PageSize = maxPageSize
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, 0, err
	}
	p, err := c.ListAccountSerials(ctx, marswallex.AccountSerialsQuery{
		PageNo: q.Page, PageSize: q.PageSize, AccountNo: q.AccountNo, Currency: q.Currency,
		StartTime: q.Start, EndTime: q.End,
	})
	if err != nil {
		return nil, 0, err
	}
	rows := p.Data
	if rows == nil {
		rows = []marswallex.AccountSerial{}
	}
	total := int(p.Count)
	if total <= 0 {
		total = -1 // 平台未给总数：-1 表示未知，前端按本页是否满页判断有无下一页
	}
	return rows, total, nil
}
