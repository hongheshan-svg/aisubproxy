package cardpool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

const backfillStateKey = "mw_txn_backfill_state"

const (
	backfillInterval  = 15 * time.Minute   // 定时补拉间隔
	backfillOverlap   = 2 * time.Hour      // 起点比 cursor 提前 2 小时，补上平台延迟入账的交易
	backfillMaxWindow = 7 * 24 * time.Hour // 首次窗口与每轮上限
	backfillSegment   = 24 * time.Hour     // 按 24 小时切段
	backfillPageSize  = 50
	backfillMaxPages  = 200 // 每段每个接口最多 200 页，平台 count 异常时不死循环
	backfillErrLen    = 200
)

// BackfillState 为交易补拉状态，以 JSON 存在 site_settings。时间均为 Unix 秒。
type BackfillState struct {
	Cursor    *int64 `json:"cursor"`      // 已完整拉取到的时间点
	LastRunAt *int64 `json:"last_run_at"` // 上一轮结束时间
	LastAdded int    `json:"last_added"`  // 上一轮新插入的交易条数
	LastError string `json:"last_error"`  // 上一轮的错误，成功为空
}

// advance 段 [a, b) 成功后推进 cursor：只有段起点不晚于 cursor（或尚无 cursor）时才推进，避免跳过空档；只增不减。
func (st *BackfillState) advance(a, b int64) bool {
	if st.Cursor != nil && (a > *st.Cursor || b <= *st.Cursor) {
		return false
	}
	st.Cursor = &b
	return true
}

func loadBackfillState() (BackfillState, error) {
	var st BackfillState
	v, err := db.GetSetting(backfillStateKey)
	if err != nil || v == "" {
		return st, err
	}
	if err := json.Unmarshal([]byte(v), &st); err != nil {
		return BackfillState{}, fmt.Errorf("补拉状态无法解析: %w", err)
	}
	return st, nil
}

func saveBackfillState(st BackfillState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return db.SetSetting(backfillStateKey, string(b))
}

// BackfillStatus 返回补拉状态；不等待进行中的补拉。
func (s *Service) BackfillStatus(ctx context.Context) (BackfillState, error) {
	return loadBackfillState()
}

// backfillOnce 定时补拉一轮：起点 cursor − 2h（首次为 now − 7d），终点 now，单轮最多处理起点后的 7 天。
func (s *Service) backfillOnce(ctx context.Context) error {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	c, err := s.opts.Client()
	if err != nil {
		return err
	}
	st, err := loadBackfillState()
	if err != nil {
		return err
	}
	now := s.now().Unix()
	start := now - secs(backfillMaxWindow)
	if st.Cursor != nil {
		start = *st.Cursor - secs(backfillOverlap)
	}
	_, err = s.backfillWindow(ctx, c, st, start, min(now, start+secs(backfillMaxWindow)), now)
	return err
}

// BackfillRecent 手动补拉最近 days 天（只能 1 / 3 / 7）：窗口 [now − days×24h, now]，与定时补拉串行。返回补拉后的状态。
func (s *Service) BackfillRecent(ctx context.Context, days int) (BackfillState, error) {
	if days != 1 && days != 3 && days != 7 {
		return BackfillState{}, badRequest("补拉天数只能是 1、3 或 7")
	}
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	c, err := s.opts.Client()
	if err != nil {
		return BackfillState{}, err
	}
	st, err := loadBackfillState()
	if err != nil {
		return BackfillState{}, err
	}
	now := s.now().Unix()
	return s.backfillWindow(ctx, c, st, now-int64(days)*secs(24*time.Hour), now, now)
}

// backfillWindow 把 [start, end] 按 24 小时切段补拉，每段先授权后结算。一段成功即按 advance 推进 cursor 并保存；
// 任一页失败则本轮停止并记 last_error。结束时写 last_run_at、last_added。调用方须持有 backfillMu。
func (s *Service) backfillWindow(ctx context.Context, c *marswallex.Client, st BackfillState, start, end, now int64) (BackfillState, error) {
	added := 0
	var runErr error
	for a := start; a < end; {
		b := min(a+secs(backfillSegment), end)
		n, err := s.pullSegment(ctx, c, a, b)
		added += n
		if err != nil {
			runErr = err
			break
		}
		if st.advance(a, b) {
			if err := saveBackfillState(st); err != nil {
				runErr = err
				break
			}
		}
		a = b
	}
	st.LastRunAt, st.LastAdded, st.LastError = &now, added, ""
	if runErr != nil {
		st.LastError = truncate(runErr.Error(), backfillErrLen)
	}
	if err := saveBackfillState(st); err != nil && runErr == nil {
		runErr = err
	}
	return st, runErr
}

// pullSegment 拉取 [a, b) 内的授权与结算交易并写入，返回新插入的条数。
func (s *Service) pullSegment(ctx context.Context, c *marswallex.Client, a, b int64) (int, error) {
	label := time.Unix(a, 0).In(beijing).Format("01-02 15:04")
	n, err := pullPages(ctx, c.ListAuthTransactions, a, b, func(x marswallex.AuthTransaction) (bool, error) {
		return s.saveTxn(ctx, pulledAuthTxn(x), true)
	})
	if err != nil {
		return n, fmt.Errorf("拉取授权交易（北京时间 %s 起）%w", label, err)
	}
	m, err := pullPages(ctx, c.ListSettlementTransactions, a, b, func(x marswallex.SettlementTransaction) (bool, error) {
		return s.saveTxn(ctx, pulledSettleTxn(x), false)
	})
	if err != nil {
		return n + m, fmt.Errorf("拉取结算交易（北京时间 %s 起）%w", label, err)
	}
	return n + m, nil
}

// pullPages 分页拉取 [a, b) 的交易，逐条交给 save（返回是否新增），返回新增条数。
// 停在 data 为空、累计条数达到 count 或第 200 页。
func pullPages[T any](ctx context.Context, list func(context.Context, marswallex.CardTransactionsQuery) (*marswallex.Page[T], error),
	a, b int64, save func(T) (bool, error)) (int, error) {
	added, got := 0, 0
	for page := 1; page <= backfillMaxPages; page++ {
		p, err := list(ctx, marswallex.CardTransactionsQuery{PageNo: page, PageSize: backfillPageSize, StartTime: a, EndTime: b})
		if err != nil {
			return added, fmt.Errorf("第 %d 页: %w", page, err)
		}
		for _, x := range p.Data {
			isNew, err := save(x)
			if err != nil {
				return added, err
			}
			if isNew {
				added++
			}
		}
		got += len(p.Data)
		if len(p.Data) == 0 || int64(got) >= int64(p.Count) {
			return added, nil
		}
	}
	log.Printf("[cardpool] 交易补拉：[%d, %d) 已拉满 %d 页仍未达到平台 count，其余不再拉取", a, b, backfillMaxPages)
	return added, nil
}

// saveTxn 写入一条拉取到的交易，返回是否为新交易；auth 为真时再执行「未交卡号出现授权」规则（只认入池后的交易）。
// 缺交易号的无法去重，跳过（推送路径同样不收）。
func (s *Service) saveTxn(ctx context.Context, t Transaction, auth bool) (bool, error) {
	if strings.TrimSpace(t.TransactionID) == "" {
		return false, nil
	}
	var exists bool
	if err := db.DB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM mw_card_transactions WHERE transaction_id = ?)`,
		t.TransactionID).Scan(&exists); err != nil {
		return false, err
	}
	if err := s.upsertTxn(ctx, t); err != nil {
		return false, err
	}
	if auth {
		if _, err := s.disableIfUnusedAuth(ctx, t, true); err != nil {
			return false, err
		}
	}
	return !exists, nil
}

// pulledAuthTxn 把拉取到的授权交易转成推送结构后沿用 authTxn；验证码（otp）丢弃，不进验证码表。
func pulledAuthTxn(x marswallex.AuthTransaction) Transaction {
	return authTxn(marswallex.AuthTransactionWebhook{
		TransactionID: x.TransactionID, OriginTransactionID: x.OriginTransactionID, TransactionType: x.TransactionType,
		CardID: x.CardID, Status: x.Status, TransactionAmount: x.TransactionAmount, TransactionCurrency: x.TransactionCurrency,
		BillingAmount: x.BillingAmount, BillingCurrency: x.BillingCurrency, AuthCode: x.AuthCode, Merchant: x.Merchant,
		DeclineReason: x.DeclineReason, TransactionTime: x.TransactionTime,
	})
}

// pulledSettleTxn 把拉取到的结算交易转成推送结构后沿用 settleTxn。
func pulledSettleTxn(x marswallex.SettlementTransaction) Transaction {
	m := x.Merchant
	return settleTxn(marswallex.SettlementTransactionWebhook{
		TransactionID: x.TransactionID, TransactionType: x.TransactionType, OriginTransactionID: x.OriginTransactionID,
		AuthTransactionID: x.AuthTransactionID, CardID: x.CardID, Status: x.Status,
		TransactionAmount: x.TransactionAmount, TransactionCurrency: x.TransactionCurrency,
		BillingAmount: x.BillingAmount, BillingCurrency: x.BillingCurrency, AuthCode: x.AuthCode, ARN: x.ARN,
		Merchant: &m, TransactionTime: x.TransactionTime,
	})
}
