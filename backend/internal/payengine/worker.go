package payengine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

const (
	dispatchInterval  = 10 * time.Second // 没有 Kick 时也定期查一次排队单据
	errorMessageLimit = 500              // error_message 最多保留的字数
	sessionDomain     = ".chatgpt.com"
)

// Kick 非阻塞地通知工作协程立即派发一轮；已有待处理的通知时合并。
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Start 同步完成重启恢复后启动后台协程：工作协程（收到 Kick 或每 10 秒按建单顺序取 queued 单据处理，
// 同时处理的单据数不超过 LoadConcurrency()）、确认协程（每 5 秒扫描确认中的单据）、清理协程（截图每天一次，浏览器残留每小时一次）。
// 恢复没做完时不派发、不定论被拒单，工作协程每一轮先重试恢复，首次失败告警一次。ctx 结束时全部停止，处理中的单据随之中断，留待下次启动恢复。
func (s *Service) Start(ctx context.Context) {
	s.cleanProfiles() // 第一次派发之前先扫一遍，之后由清理协程每小时一次
	s.tryRecover(ctx)
	s.loops.Add(3)
	go s.loop(ctx)
	go s.verifyLoop(ctx)
	go s.cleanLoop(ctx)
	s.Kick()
}

// Wait 阻塞到后台协程与全部单据协程退出；调用前须先取消传给 Start 的 ctx。
func (s *Service) Wait() { s.loops.Wait() }

func (s *Service) loop(ctx context.Context) {
	defer s.loops.Done()
	ticker := time.NewTicker(dispatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		if !s.recovered.Load() && !s.tryRecover(ctx) {
			continue
		}
		s.dispatch(ctx)
	}
}

// dispatch 按 id 顺序为排队单据分配名额，每张单据一个协程。
func (s *Service) dispatch(ctx context.Context) {
	if s.breakerPaused(ctx) {
		return // 熔断：不派发新单；在途单由确认协程照常走完
	}
	limit := LoadConcurrency()
	s.mu.Lock()
	full := len(s.running) >= limit
	s.mu.Unlock()
	if full {
		return
	}
	queued, err := ordersInStates(ctx, StateQueued)
	if err != nil {
		log.Printf("[payengine] 读取排队单据失败: %v", err)
		return
	}
	for i := range queued {
		o := queued[i]
		s.mu.Lock()
		if len(s.running) >= limit {
			s.mu.Unlock()
			return
		}
		if s.running[o.OrderNo] {
			s.mu.Unlock()
			continue
		}
		s.running[o.OrderNo] = true
		s.mu.Unlock()
		s.loops.Add(1)
		go s.handle(ctx, &o)
	}
}

// handle 处理一张单据并归还名额；领取成功的单据处理完后踢醒工作协程，让排队的单据接上。
func (s *Service) handle(ctx context.Context, o *Order) {
	defer s.loops.Done()
	claimed := s.process(ctx, o)
	s.mu.Lock()
	delete(s.running, o.OrderNo)
	s.mu.Unlock()
	if claimed {
		s.Kick()
	}
}

// job 是处理中的一张单据及其运行期资源。
type job struct {
	o       *Order
	sess    chatgpt.Session
	proxy   string
	gpt     ChatGPT
	browser Browser
	shots   int            // 已截图张数，用作截图名序号
	pending map[string]any // 随下一次状态写入一并落库的列

	lease      *cardpool.Lease // 提交前放弃时要释放的租约；提交之后由确认阶段按单据字段释放
	holderName string          // 持卡人英文名，填表用
	secrets    []string        // 要从文字里抹掉的 CVV、验证码
	cardNo     *regexp.Regexp  // 匹配完整卡号（含空格或连字符分组），要从文字里抹掉
}

// note 记下一列，随下一次状态写入落库。
func (j *job) note(col string, v any) {
	if j.pending == nil {
		j.pending = map[string]any{}
	}
	j.pending[col] = v
}

func (j *job) merge(set map[string]any) map[string]any {
	out := make(map[string]any, len(j.pending)+len(set))
	for k, v := range j.pending {
		out[k] = v
	}
	for k, v := range set {
		out[k] = v
	}
	return out
}

func (j *job) closeBrowser() {
	if j.browser != nil {
		j.browser.Close()
		j.browser = nil
	}
}

// useSession 记下解开的会话。刷新会覆盖 AccessToken，原 token 单独登记，免得它夹在错误文字里漏过脱敏。
func (j *job) useSession(sess chatgpt.Session) {
	j.sess = sess
	j.secrets = append(j.secrets, sess.AccessToken)
}

// noteCardSecrets 登记完整卡号与 CVV：之后所有经过 scrub 的文字都会抹掉它们。
func (j *job) noteCardSecrets(cardNo, cvv string) {
	if digits := []rune(strings.TrimSpace(cardNo)); len(digits) > 0 {
		parts := make([]string, len(digits))
		for i, r := range digits {
			parts[i] = regexp.QuoteMeta(string(r))
		}
		j.cardNo = regexp.MustCompile(strings.Join(parts, `[\s-]*`))
	}
	j.secrets = append(j.secrets, cvv)
}

// scrub 去掉文字中可能夹带的完整卡号、CVV、验证码、代理地址、代理账号密码（含各种转义形式）、会话 token 与账号邮箱；长串先替换。
// 卡号最先处理：CVV 可能恰好是卡号的一段，先替换 CVV 会让卡号匹配不上。
func (j *job) scrub(s string) string {
	if j.cardNo != nil {
		s = j.cardNo.ReplaceAllString(s, "***")
	}
	secrets := append([]string{j.proxy, j.sess.SessionToken, j.sess.AccessToken}, j.secrets...)
	if j.o != nil { // 恢复阶段脱敏用的 job 没有单据
		secrets = append(secrets, j.o.AccountEmail) // 页面文字可能带账号邮箱，邮箱也可能被填进页面
	}
	if u, err := url.Parse(j.proxy); err == nil && u.User != nil {
		secrets = append(secrets, u.User.String())
		if pw, ok := u.User.Password(); ok {
			secrets = append(secrets, pw, url.QueryEscape(pw), url.PathEscape(pw))
		}
	}
	if i := strings.Index(j.proxy, "://"); i >= 0 {
		if at := strings.LastIndex(j.proxy, "@"); at > i {
			secrets = append(secrets, j.proxy[i+3:at])
		}
	}
	sort.SliceStable(secrets, func(a, b int) bool { return len(secrets[a]) > len(secrets[b]) })
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	return s
}

// stepFailure 表示提交前放弃：单据以 failed / not_submitted 结束。
type stepFailure struct{ code, msg string }

func (f *stepFailure) Error() string { return f.code + ": " + f.msg }

func abort(code, format string, a ...any) error {
	return &stepFailure{code: code, msg: fmt.Sprintf(format, a...)}
}

// notClickedError 表示支付按钮确实没被点击，以后也不会被点：由浏览器点击前置位的标志证明（见 Browser.LockSubmit）。
// 只有 submitAndWait 能造出它。
type notClickedError struct{ err error }

func (e *notClickedError) Error() string { return e.err.Error() }
func (e *notClickedError) Unwrap() error { return e.err }

// chatgptAbort 把 ChatGPT 调用失败转成放弃原因；未授权一律按会话失效处理。
func chatgptAbort(code, what string, err error) error {
	if errors.Is(err, chatgpt.ErrUnauthorized) {
		return abort("session_invalid", "%s：会话已失效，请重新获取会话", what)
	}
	return abort(code, "%s：%v", what, err)
}

// advance 把单据从当前状态推进到 to（非终态），一并写入 pending 与 set。
func (s *Service) advance(ctx context.Context, j *job, to, msg string, set map[string]any) error {
	if err := transition(ctx, j.o.OrderNo, j.o.State, to, "info", msg, j.merge(set)); err != nil {
		return err
	}
	j.o.State, j.pending = to, nil
	return nil
}

// end 先关闭浏览器，再把单据写入终态 to。
func (s *Service) end(ctx context.Context, j *job, to, level, msg string, set map[string]any) error {
	j.closeBrowser()
	if err := finish(ctx, j.o.OrderNo, j.o.State, to, level, msg, j.merge(set)); err != nil {
		return err
	}
	j.o.State, j.pending = to, nil
	return nil
}

func (s *Service) fail(ctx context.Context, j *job, f *stepFailure) error {
	msg := truncate(j.scrub(f.msg), errorMessageLimit)
	return s.end(ctx, j, StateFailed, "error", msg, map[string]any{
		"failure": FailureNotSubmitted, "error_code": f.code, "error_message": msg,
	})
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// event 追加一条时间线事件，失败只记日志。
func (s *Service) event(ctx context.Context, j *job, level, msg, screenshot string) {
	if err := addEvent(ctx, j.o.OrderNo, level, msg, screenshot); err != nil {
		log.Printf("[payengine] 单据 %s 写时间线失败: %v", j.o.OrderNo, err)
	}
}

// shot 截一张图记入时间线，文件名为「两位序号-步骤」；截图失败只记警告。
func (s *Service) shot(ctx context.Context, j *job, step string, scope paybrowser.ShotScope) {
	j.shots++
	file, err := j.browser.Shot(ctx, fmt.Sprintf("%02d-%s", j.shots, step), scope)
	if err != nil {
		s.event(ctx, j, "warn", "截图失败："+j.scrub(err.Error()), "")
		return
	}
	s.event(ctx, j, "info", "已截图", file)
}

// process 领取并处理一张排队单据；未能领取（状态已变或写库失败）返回 false。
func (s *Service) process(ctx context.Context, o *Order) bool {
	j := &job{o: o}
	if err := s.advance(ctx, j, StateChecking, "检查账号", nil); err != nil {
		if !errors.Is(err, ErrStateChanged) && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			log.Printf("[payengine] 单据 %s 领取失败: %v", o.OrderNo, err)
		}
		return false
	}
	defer j.closeBrowser()

	s.settle(ctx, j, s.run(ctx, j))
	return true
}

// settle 按 run 的结果收尾。一旦单据写入过 submitted_at，任何错误（放弃原因也好、意外错误也好）都转 verifying 交给确认协程：
// 已点支付的单子记成「未提交」，用户会重复付款。唯一的例外是 notClickedError：浏览器能严格证明按钮没被点击。
// 提交之前的错误释放租约并以 failed / not_submitted 结束，其中意外错误（如写库失败）记 internal_error，免得单据卡在中间状态。
// 写库再失败就告警：已提交的单由确认协程接管（adoptSubmitted），提交前的单要等重启恢复。
// 服务停止时不收尾：单据与租约都留给重启恢复。
func (s *Service) settle(ctx context.Context, j *job, err error) {
	var f *stepFailure
	var notClicked *notClickedError
	switch {
	case err == nil:
	case ctx.Err() != nil:
		log.Printf("[payengine] 单据 %s 在 %s 时服务停止，留待重启后处理", j.o.OrderNo, j.o.State)
	case errors.As(err, &notClicked):
		s.abandonNotClicked(ctx, j, notClicked)
	case j.o.SubmittedAt != nil:
		msg := truncate(j.scrub(err.Error()), errorMessageLimit)
		log.Printf("[payengine] 单据 %s 提交后在 %s 时出错，转确认: %s", j.o.OrderNo, j.o.State, msg)
		s.event(ctx, j, "warn", "提交后出错："+msg, "")
		if verr := s.beginVerify(ctx, j, paybrowser.OutcomeUnknown, msg, "页面结果不明，确认是否开通"); verr != nil {
			detail := j.scrub(verr.Error())
			log.Printf("[payengine] 单据 %s 转确认失败，等确认协程接管: %s", j.o.OrderNo, detail)
			if ctx.Err() == nil {
				s.opts.Alert(fmt.Sprintf("付款单 %s 已提交，但转确认连续失败，等确认协程接管：%s", j.o.OrderNo, detail))
			}
		}
	case errors.As(err, &f):
		s.abandon(ctx, j, f)
	default:
		msg := j.scrub(err.Error())
		log.Printf("[payengine] 单据 %s 在 %s 时中断: %s", j.o.OrderNo, j.o.State, msg)
		s.abandon(ctx, j, &stepFailure{code: "internal_error", msg: "内部错误：" + msg})
	}
}

// abandon 处理提交之前的放弃：关浏览器、Release(aborted)、写 failed / not_submitted。
func (s *Service) abandon(ctx context.Context, j *job, f *stepFailure) {
	j.closeBrowser()
	s.release(ctx, j, j.lease, cardpool.Outcome{Result: cardpool.ResultAborted})
	j.lease = nil
	if err := s.fail(ctx, j, f); err != nil {
		detail := j.scrub(err.Error())
		log.Printf("[payengine] 单据 %s 写入失败结果出错: %s", j.o.OrderNo, detail)
		if ctx.Err() == nil {
			s.opts.Alert(fmt.Sprintf("付款单 %s 放弃时写库失败，单据不收敛，该账号下单会一直报在途：%s", j.o.OrderNo, detail))
		}
	}
}

// abandonNotClicked 处理已记下提交时间、但按钮确实没被点击的单：和提交前放弃一样收尾，并清掉 submitted_at，
// 否则这单看起来像提交过。写库失败时单据还是 submitted，由确认协程接管，那是安全方向。
func (s *Service) abandonNotClicked(ctx context.Context, j *job, e *notClickedError) {
	if j.browser != nil {
		s.notePageDiag(ctx, j, j.browser, "支付按钮没有被点击")
	}
	j.note("submitted_at", nil)
	s.abandon(ctx, j, &stepFailure{code: "submit_not_clicked", msg: "支付按钮没有被点击：" + e.Error()})
}

// release 释放租约并上报用卡结果。租约已失效（到期被卡池收回等）或其它错误都只记 warn 事件，不重试：
// 卡池会自行回收到期租约，单据结果不受影响。
func (s *Service) release(ctx context.Context, j *job, l *cardpool.Lease, o cardpool.Outcome) {
	if l == nil {
		return
	}
	err := s.opts.Pool.Release(ctx, l, o)
	switch {
	case err == nil:
	case errors.Is(err, cardpool.ErrLeaseInvalid):
		s.event(ctx, j, "warn", "租约已失效，卡已由卡池收回", "")
	default:
		s.event(ctx, j, "warn", "释放租约失败："+j.scrub(err.Error()), "")
	}
}

// run 依次执行步骤 2–4；试运行在读完金额后以 previewed 结束（试运行到填表多走一步填表，见 previewFill），真实付款单交给 pay。
func (s *Service) run(ctx context.Context, j *job) error {
	if err := s.check(ctx, j); err != nil {
		return err
	}
	if err := s.createLink(ctx, j); err != nil {
		return err
	}
	if err := s.openPage(ctx, j); err != nil {
		return err
	}
	if j.o.DryRunFill {
		return s.previewFill(ctx, j)
	}
	if j.o.DryRun {
		return s.end(ctx, j, StatePreviewed, "info", "试运行完成，应付 "+j.o.AmountText, nil)
	}
	return s.pay(ctx, j)
}

// check 是步骤 2：解开会话、刷新 token、查账号；已有有效订阅则放弃（升级不在本流程）。
func (s *Service) check(ctx context.Context, j *job) error {
	sess, err := openSession(j.o.SessionSealed)
	if errors.Is(err, chatgpt.ErrBadSession) {
		return abort("session_invalid", "%v", err)
	}
	if err != nil {
		return abort("session_unreadable", "会话密文无法解开（主密钥缺失或已更换）")
	}
	j.useSession(sess)
	proxies, err := LoadProxies()
	if err != nil {
		return abort("proxy_unavailable", "读取地区代理失败：%v", err)
	}
	j.proxy = proxies[j.o.Region]
	if j.gpt, err = s.opts.ChatGPT(j.proxy); err != nil {
		return abort("chatgpt_failed", "创建 ChatGPT 客户端失败：%v", err)
	}
	if err := j.gpt.Refresh(ctx, &j.sess); err != nil {
		return chatgptAbort("chatgpt_failed", "刷新会话失败", err)
	}
	acc, err := j.gpt.Account(ctx, j.sess)
	if err != nil {
		return chatgptAbort("chatgpt_failed", "查询账号失败", err)
	}
	j.note("account_id", acc.AccountID)
	if acc.HasActiveSubscription {
		return abort("already_subscribed", "账号已有有效订阅（%s），不能直接开通", acc.PlanType)
	}
	return nil
}

// createLink 是步骤 3：按套餐表创建支付会话，记下会话号与支付链接。
func (s *Service) createLink(ctx context.Context, j *job) error {
	plan, _, err := ResolvePlan(j.o.PlanKey, j.o.Region, j.o.DryRun)
	if errors.Is(err, ErrPlanUnavailable) {
		return abort("plan_unavailable", "%v", err)
	}
	if err != nil {
		return abort("internal_error", "读取套餐配置失败：%v", err)
	}
	co, err := j.gpt.CreateCheckout(ctx, j.sess, plan.ChatGPTPlanName, j.o.Region, j.o.Currency)
	if err != nil {
		return chatgptAbort("checkout_failed", "创建支付会话失败", err)
	}
	j.note("checkout_session_id", co.SessionID)
	if co.URL == "" {
		return abort("checkout_failed", "创建支付会话未返回支付链接")
	}
	if err := s.advance(ctx, j, StateLinkCreated, "已生成支付链接", map[string]any{"checkout_url": co.URL}); err != nil {
		return err
	}
	j.o.CheckoutSessionID, j.o.CheckoutURL = co.SessionID, co.URL
	return nil
}

// openPage 是步骤 4：启动浏览器（走地区代理）、注入会话 cookie、打开支付页、截整页图、读应付金额；
// 读不出或真实付款超过上限则放弃。
func (s *Service) openPage(ctx context.Context, j *job) error {
	if err := s.advance(ctx, j, StateOpening, "启动浏览器，打开支付页", nil); err != nil {
		return err
	}
	b, err := s.opts.Browser(ctx, j.proxy, filepath.Join(s.opts.ShotDir, j.o.OrderNo))
	if err != nil {
		msg := j.scrub(err.Error())
		s.opts.Alert(fmt.Sprintf("付款单 %s 浏览器无法启动：%s", j.o.OrderNo, msg))
		return abort("browser_failed", "浏览器无法启动：%s", msg)
	}
	j.browser = b
	if j.o.DryRun {
		b.LockSubmit() // 试运行单：浏览器对象本身拒绝点击，不只靠流程分支
	}

	pctx, cancel := context.WithTimeout(ctx, s.t.PageLoad)
	defer cancel()
	if err := b.Open(pctx, j.o.CheckoutURL, sessionCookies(j.sess)); err != nil {
		s.notePageDiag(ctx, j, b, "打开支付页失败")
		sctx, cancelShot := context.WithTimeout(ctx, diagTimeout)
		defer cancelShot()
		s.shot(sctx, j, "open-failed", paybrowser.ShotPage) // 还没填卡，允许截整页；pctx 可能已经超时
		return abort("page_failed", "打开支付页失败：%v", err)
	}
	s.shot(pctx, j, "opening", paybrowser.ShotPage)
	minor, text, err := b.ReadAmount(pctx, j.o.Currency)
	if text != "" {
		j.note("amount_text", text)
	}
	if err != nil {
		return abort("amount_unreadable", "读不出应付金额：%v", err)
	}
	if !paybrowser.CurrencyMatches(text, j.o.Currency) {
		return abort("currency_mismatch", "支付页金额 %q 的币种与期望的 %s 不符", text, j.o.Currency)
	}
	j.o.AmountMinor, j.o.AmountText = minor, text
	j.note("amount_minor", minor)
	if !j.o.DryRun && minor > j.o.CapMinor {
		return abort("amount_over_cap", "应付金额 %s 超过上限 %s %s", text, formatMinor(j.o.CapMinor, j.o.Currency), j.o.Currency)
	}
	return nil
}

// sessionCookies 把会话转成注入 chatgpt.com 的 cookie。
func sessionCookies(sess chatgpt.Session) []paybrowser.Cookie {
	cs := chatgpt.SessionCookies(sess.SessionToken)
	out := make([]paybrowser.Cookie, 0, len(cs))
	for _, c := range cs {
		out = append(out, paybrowser.Cookie{Name: c.Name, Value: c.Value, Domain: sessionDomain})
	}
	return out
}

// formatMinor 按币种小数位把最小单位金额格式化为数字文本，如 120000 PHP → "1200.00"。
func formatMinor(v int64, currency string) string {
	exp := paybrowser.CurrencyExponent(currency)
	if exp == 0 {
		return strconv.FormatInt(v, 10)
	}
	div := int64(math.Pow10(exp))
	return fmt.Sprintf("%d.%0*d", v/div, exp, v%div)
}
