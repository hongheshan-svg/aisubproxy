package payengine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

const (
	shotRetention  = 7 * 24 * time.Hour // 截图保留时长
	shotCleanEvery = 24 * time.Hour     // 截图清理间隔
	txnPageSize    = 100                // 翻交易时每页条数；卡池的分页上限更小时按实际读到的条数继续翻
	restartMessage = "服务重启，未提交"
)

// orderNoName 是单号格式，截图清理只动名字符合它的目录。
var orderNoName = regexp.MustCompile(`^PO\d{12}[0-9a-f]{8}$`)

// beginVerify 关闭浏览器，把已提交的单据转入确认阶段（步骤 9）。text 是页面提示或出错说明，记入 error_message；
// 被拒时只等 DeclineGrace（让授权推送到达），其余等满 Verify。
func (s *Service) beginVerify(ctx context.Context, j *job, out paybrowser.Outcome, text, note string) error {
	j.closeBrowser()
	now := s.now()
	until := now.Add(s.t.Verify)
	page := string(paybrowser.OutcomeUnknown)
	switch out {
	case paybrowser.OutcomeSuccess:
		page = string(out)
	case paybrowser.OutcomeDeclined:
		page = string(out)
		until = now.Add(s.t.DeclineGrace)
	}
	set := map[string]any{"page_outcome": page, "verify_until": until.Unix(), "next_check_at": now.Unix()}
	if text != "" {
		text = truncate(j.scrub(text), errorMessageLimit)
		set["error_message"] = text
		j.o.ErrorMessage = text
	}
	return s.advance(ctx, j, StateVerifying, note, set)
}

// verifyLoop 是确认协程：每隔 VerifyTick 扫描一次到期的确认中单据。不占浏览器名额。
func (s *Service) verifyLoop(ctx context.Context) {
	defer s.loops.Done()
	ticker := time.NewTicker(s.t.VerifyTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		s.verifyDue(ctx)
	}
}

func (s *Service) verifyDue(ctx context.Context) {
	list, err := dueVerifying(ctx, s.now().Unix())
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[payengine] 读取确认中单据失败: %v", err)
		}
		return
	}
	for i := range list {
		if ctx.Err() != nil {
			return
		}
		s.verifyOrder(ctx, &list[i])
	}
}

// verifyOrder 检查一张确认中的单据，定论规则见 spec §6。账号已开通即成功；到期前的任何错误都只记事件、等下次检查。
func (s *Service) verifyOrder(ctx context.Context, o *Order) {
	j := &job{o: o}
	opened, accErr := s.verifyAccount(ctx, j)
	if ctx.Err() != nil {
		return // 停机：下次启动接着查
	}
	if opened {
		s.verifyConclude(ctx, j, StateSucceeded, "info", "账号已开通，付款成功",
			cardpool.Outcome{Result: cardpool.ResultSuccess}, map[string]any{"error_code": "", "error_message": ""})
		return
	}
	if accErr != nil {
		s.event(ctx, j, "warn", "查询账号失败，等下次检查："+j.scrub(accErr.Error()), "")
	}
	now := s.now().Unix()
	var until int64
	if o.VerifyUntil != nil {
		until = *o.VerifyUntil
	}
	switch {
	case now < until:
		s.verifyReschedule(ctx, j)
	case o.PageOutcome == string(paybrowser.OutcomeDeclined):
		s.verifyDeclined(ctx, j, accErr, now >= until+int64(s.t.Verify/time.Second))
	default:
		s.verifyEscalate(ctx, j, "verify_timeout", "确认超时：账号未开通，支付页也没有明确拒绝")
	}
}

// verifyDeclined 定论支付页显示被拒、宽限期已过的单据。只有确认了账号未开通、翻完交易没有成功授权也没有处理中的授权，才算 failed / declined。
// 证据不全（查询出错、授权还在处理中）绝不定成被拒——把已扣款的单子定成 declined 是最贵的错误：
// 继续等下次检查，等到 exhausted（verify_until 之后再过一个 Verify）仍不全就转人工。
func (s *Service) verifyDeclined(ctx context.Context, j *job, accErr error, exhausted bool) {
	approved, pending, err := s.verifyAuth(ctx, j.o)
	if ctx.Err() != nil {
		return
	}
	switch {
	case err != nil:
		s.event(ctx, j, "warn", "查询授权交易失败，等下次检查："+j.scrub(err.Error()), "")
	case approved:
		s.verifyEscalate(ctx, j, "evidence_conflict", "证据冲突：支付页显示被拒，但该卡有成功授权")
		return
	case !pending && accErr == nil:
		reason := j.o.ErrorMessage
		if reason == "" {
			reason = "支付页显示被拒"
		}
		s.verifyConclude(ctx, j, StateFailed, "error", "支付被拒："+reason,
			cardpool.Outcome{Result: cardpool.ResultFailed, ErrorCode: truncate(reason, 200)},
			map[string]any{"failure": FailureDeclined, "error_code": "card_declined", "error_message": reason})
		return
	}
	if exhausted {
		s.verifyEscalate(ctx, j, "evidence_unavailable", "支付页显示被拒，但一直无法确认账号与授权交易")
		return
	}
	s.verifyReschedule(ctx, j)
}

// verifyAccount 解开单据里的会话（终态才清空）查账号，判断是否已是目标套餐。
// 期望套餐类型直接读套餐表：下单后管理员停用该地区不影响确认。
func (s *Service) verifyAccount(ctx context.Context, j *job) (bool, error) {
	tbl, err := LoadPlanTable()
	if err != nil {
		return false, err
	}
	expect := ""
	for i := range tbl {
		if tbl[i].Key == j.o.PlanKey {
			expect = strings.TrimSpace(tbl[i].ExpectPlanType)
		}
	}
	if expect == "" {
		return false, fmt.Errorf("套餐 %s 未配置期望套餐类型", j.o.PlanKey)
	}
	sess, err := openSession(j.o.SessionSealed)
	if err != nil {
		return false, errors.New("会话密文无法解开")
	}
	j.useSession(sess)
	proxies, err := LoadProxies()
	if err != nil {
		return false, fmt.Errorf("读取地区代理失败：%w", err)
	}
	j.proxy = proxies[j.o.Region]
	gpt, err := s.opts.ChatGPT(j.proxy)
	if err != nil {
		return false, fmt.Errorf("创建 ChatGPT 客户端失败：%w", err)
	}
	if err := gpt.Refresh(ctx, &j.sess); err != nil {
		return false, fmt.Errorf("刷新会话失败：%w", err)
	}
	acc, err := gpt.Account(ctx, j.sess)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(acc.PlanType), expect), nil
}

// verifyAuth 翻完该卡在 submitted_at 之后的交易，看有没有成功授权、处理中的授权。
// 平台状态原样入库（文档是小写，实际可能有大写），比较时不区分大小写。
func (s *Service) verifyAuth(ctx context.Context, o *Order) (approved, pending bool, err error) {
	if o.CardID == "" {
		return false, false, errors.New("单据没有记录卡")
	}
	var since int64
	if o.SubmittedAt != nil {
		since = *o.SubmittedAt
	}
	list, err := s.txnsSince(ctx, o.CardID, since)
	if err != nil {
		return false, false, err
	}
	for _, t := range list {
		if !strings.EqualFold(t.Kind, "AUTH") {
			continue
		}
		switch {
		case strings.EqualFold(t.Status, "approved"):
			approved = true
		case strings.EqualFold(t.Status, "pending"):
			pending = true
		}
	}
	return approved, pending, nil
}

// txnsSince 返回该卡在 since 之后的交易（新的在前）。交易按时间倒序分页，翻到全部读完或出现早于 since 的记录为止。
// 时间只精确到秒，与 since 同一秒的算在内：宁可多算一笔，也不能漏掉这次付款的授权。
func (s *Service) txnsSince(ctx context.Context, cardID string, since int64) ([]cardpool.Transaction, error) {
	out := []cardpool.Transaction{}
	seen := 0
	for page := 1; ; page++ {
		list, total, err := s.opts.Pool.ListTransactions(ctx, cardpool.TxnFilter{CardID: cardID, Page: page, PageSize: txnPageSize})
		if err != nil {
			return nil, err
		}
		for _, t := range list {
			if txnTime(t) >= since {
				out = append(out, t)
			}
		}
		seen += len(list)
		if len(list) == 0 || seen >= total || txnTime(list[len(list)-1]) < since {
			return out, nil
		}
	}
}

// txnTime 取交易时间；没有发生时间时用更新时间，与卡池的排序口径一致。
func txnTime(t cardpool.Transaction) int64 {
	if t.OccurredAt != nil {
		return *t.OccurredAt
	}
	return t.UpdatedAt
}

func (s *Service) verifyReschedule(ctx context.Context, j *job) {
	if err := setNextCheck(ctx, j.o.OrderNo, s.now().Add(s.t.VerifyEvery).Unix()); err != nil && ctx.Err() == nil {
		log.Printf("[payengine] 单据 %s 更新下次检查时间失败: %v", j.o.OrderNo, err)
	}
}

// verifyEscalate 转待人工：Release(aborted)，告警。
func (s *Service) verifyEscalate(ctx context.Context, j *job, code, reason string) {
	msg := reason
	if j.o.ErrorMessage != "" {
		msg += "；页面或浏览器提示：" + j.o.ErrorMessage
	}
	msg = truncate(j.scrub(msg), errorMessageLimit)
	if s.verifyConclude(ctx, j, StateNeedsReview, "error", msg, cardpool.Outcome{Result: cardpool.ResultAborted},
		map[string]any{"error_code": code, "error_message": msg}) {
		s.opts.Alert(fmt.Sprintf("付款单 %s 转待人工：%s", j.o.OrderNo, msg))
	}
}

// verifyConclude 先按单据字段重建租约并释放，再写终态。写终态失败时单据留在 verifying，下次检查重新定论（再释放得到 ErrLeaseInvalid，只记 warn）。
func (s *Service) verifyConclude(ctx context.Context, j *job, to, level, msg string, oc cardpool.Outcome, set map[string]any) bool {
	s.release(ctx, j, leaseFromOrder(j.o), oc)
	if err := s.end(ctx, j, to, level, msg, set); err != nil {
		if ctx.Err() == nil {
			log.Printf("[payengine] 单据 %s 写入 %s 失败，下次检查重试: %s", j.o.OrderNo, to, j.scrub(err.Error()))
		}
		return false
	}
	return true
}

// leaseFromOrder 按单据字段重建租约：重启后或确认阶段没有内存中的 Lease。单据没有租约时返回 nil。
func leaseFromOrder(o *Order) *cardpool.Lease {
	if o.LeaseToken == "" || o.CardID == "" {
		return nil
	}
	return &cardpool.Lease{Token: o.LeaseToken, CardID: o.CardID, PlanKey: o.PlanKey, OrderRef: o.OrderNo, AccountEmail: o.AccountEmail}
}

// recoverOrders 收敛上次运行遗留的单据，必须在第一次派发之前完成：
// 提交之前的中间状态判 failed / not_submitted 并释放租约；submitted、requires_3ds 转 verifying 继续确认，绝不再点支付。
// queued 留给工作协程，verifying 留给确认协程。有单据没收敛时返回错误，由工作协程重试。
func (s *Service) recoverOrders(ctx context.Context) error {
	stale, err := ordersInStates(ctx, StateChecking, StateLinkCreated, StateOpening, StateFunding, StateFilling)
	if err != nil {
		return err
	}
	var errs []error
	for i := range stale {
		j := &job{o: &stale[i]}
		s.release(ctx, j, leaseFromOrder(j.o), cardpool.Outcome{Result: cardpool.ResultAborted})
		if err := s.fail(ctx, j, &stepFailure{code: "service_restart", msg: restartMessage}); err != nil {
			errs = append(errs, fmt.Errorf("单据 %s: %w", j.o.OrderNo, err))
		}
	}
	submitted, err := ordersInStates(ctx, StateSubmitted, StateRequires3DS)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for i := range submitted {
		j := &job{o: &submitted[i]}
		if err := s.beginVerify(ctx, j, paybrowser.OutcomeUnknown, "", "服务重启，继续确认是否开通"); err != nil {
			errs = append(errs, fmt.Errorf("单据 %s: %w", j.o.OrderNo, err))
		}
	}
	return errors.Join(errs...)
}

// shotLoop 启动时清理一次截图，之后每天一次。
func (s *Service) shotLoop(ctx context.Context) {
	defer s.loops.Done()
	s.cleanShots()
	ticker := time.NewTicker(shotCleanEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanShots()
		}
	}
}

// cleanShots 删除 ShotDir 下名字符合单号格式、修改时间早于 7 天的目录。
// ReadDir 不跟随符号链接（链接的 IsDir 为 false，直接跳过），RemoveAll 也不会穿过目录里的链接。删除失败只打日志。
func (s *Service) cleanShots() {
	entries, err := os.ReadDir(s.opts.ShotDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("[payengine] 读取截图目录失败: %v", err)
		}
		return
	}
	cutoff := s.now().Add(-shotRetention)
	for _, e := range entries {
		if !e.IsDir() || !orderNoName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			log.Printf("[payengine] 读取截图目录 %s 失败: %v", e.Name(), err)
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.opts.ShotDir, e.Name())); err != nil {
			log.Printf("[payengine] 删除截图目录 %s 失败: %v", e.Name(), err)
		}
	}
}
