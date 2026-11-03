package paybrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
)

// chatgpt.com 内嵌结账页（设计 2026-11-03 §5.5、§5.6、§5.8）专用的脚本与函数。
//
// 卡片字段在 js.stripe.com 的 Payment Element iframe 里，选择器与托管页共用 Sel。
// 金额只认 iframe 里给读屏软件的英文说明「Payment amount: $100.00 Currency: usd …」：
// 主页面右栏的「今日应付」随账号语言变、没有稳定的 id，不参与判断。
// 支付按钮是主页面上的 button[type="submit"]，文字随账号语言变，只能按个数认，必须正好一个。

// ErrCFChallenge 表示内嵌页打不开、停在 Cloudflare 挑战页；引擎据此记 cf_challenge。
var ErrCFChallenge = errors.New("支付页停在 Cloudflare 挑战页")

const (
	// embeddedPayButtonSel 是内嵌页的支付按钮，只在主页面找，不进 iframe。
	embeddedPayButtonSel = `button[type="submit"]`
	// cfCheckWait 是打开超时后读一次页面、判断是不是 Cloudflare 挑战页的时限。
	// 这时调用方的时限通常也到了，所以这一步不沿用调用方的时限，只受 cfCheckWait 约束；只读页面，不操作。
	cfCheckWait = 5 * time.Second
)

// embeddedAmountRe 从读屏文字里取金额原文和币种代码。币种代码后面不能再连着字母（如 usdt），宁可读不出也不误认。
// 金额原文里只能有一段数字，见 normalizeEmbeddedAmount。
var embeddedAmountRe = regexp.MustCompile(`Payment amount:\s*(.+?)\s+Currency:\s*([A-Za-z]{3})\b`)

// embeddedAmountJS 在本 frame 里找以「Payment amount:」开头的文字，返回它所在元素的父元素里的全部文字；找不到返回空串。
// 这段文字视觉上隐藏，所以不要求可见。各段文字之间补一个空格再归一空白：相邻的 <p> 之间可能没有空白，
// innerText 对没渲染的元素也不分段，直接拼会把「$100.00」和「Currency:」粘在一起。
const embeddedAmountJS = `(() => {
	const root = document.body || document.documentElement
	if (!root) return ''
	const textOf = node => {
		const parts = []
		const w = document.createTreeWalker(node, NodeFilter.SHOW_TEXT)
		for (let t = w.nextNode(); t; t = w.nextNode()) parts.push(t.data)
		return parts.join(' ').replace(/\s+/g, ' ').trim()
	}
	const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
	for (let t = w.nextNode(); t; t = w.nextNode()) {
		if (!/^\s*Payment amount:/.test(t.data)) continue
		const el = t.parentElement
		if (!el || el.closest('script, style')) continue
		return textOf(el.parentElement || el)
	}
	return ''
})()`

// countVisibleJS 数本 frame 里 sel 命中的可见元素个数。
const countVisibleJS = `((sel, visible) => Array.from(document.querySelectorAll(sel)).filter(visible).length)`

// uniquePayButtonJS 在一次求值里看本 frame：路径不以 prefix 开头时返回 'left'（已离开结账页，按钮一律不认）；
// 否则数 sel 命中的可见元素，正好一个且可用时返回它，正好一个但不可用时返回 'disabled'，个数不是 1 时返回个数。
// 核对路径、数和取都在同一次求值里，中间页面插不进别的脚本。
const uniquePayButtonJS = `((prefix, sel, visible, enabled) => {
	if (!location.pathname.startsWith(prefix)) return 'left'
	const list = Array.from(document.querySelectorAll(sel)).filter(visible)
	if (list.length !== 1) return list.length
	return enabled.call(list[0]) ? list[0] : 'disabled'
})`

// checkoutPrefix 是内嵌结账页的路径前缀；主页面路径不再以它开头，就是已经离开结账页。
const checkoutPrefix = "/checkout/"

// leftCheckoutMsg 是离开结账页时 WaitOutcome 返回的说明。
const leftCheckoutMsg = "页面已离开结账页"

// embeddedSeen 是一轮里看到的内嵌页就绪情况，三样同时成立才算打开成功。
type embeddedSeen struct {
	card    bool // 某个 frame 里有可见的卡号框
	amount  bool // 某个 frame 里有 Payment amount 读屏文字
	buttons int  // 主页面可见的支付按钮个数；读不出时为 -1
}

func (s embeddedSeen) ready() bool { return s.card && s.amount && s.buttons == 1 }

// problem 说明哪几样没成立。
func (s embeddedSeen) problem() string {
	var p []string
	if !s.card {
		p = append(p, "支付页上没有找到卡号框")
	}
	if !s.amount {
		p = append(p, "支付页上没有找到 Payment amount")
	}
	switch {
	case s.buttons < 0:
		p = append(p, "支付页上的支付按钮读不出")
	case s.buttons != 1:
		p = append(p, fmt.Sprintf("支付页上的支付按钮不唯一（找到 %d 个）", s.buttons))
	}
	return strings.Join(p, "；")
}

// waitEmbedded 在 ctx 的时限内等内嵌页就绪（见 embeddedSeen）。
// 等不到时读一次页面：是 Cloudflare 挑战页就返回包着 ErrCFChallenge 的错误，否则说明哪一样没成立。
func (b *Browser) waitEmbedded(ctx context.Context) error {
	seen := embeddedSeen{buttons: -1}
	ready := false
	err := b.do(ctx, func(c context.Context) error {
		var err error
		ready, err = b.poll(c, pageLoadTimeout, func(frames []frameRef) bool {
			s := b.seeEmbedded(c, frames)
			if c.Err() == nil { // 时限到了的那一轮读不全，不能拿来说明原因
				seen = s
			}
			return s.ready()
		})
		return err
	})
	if ready {
		return nil
	}
	if err == nil {
		err = context.DeadlineExceeded
	}
	// 调用方主动取消（不是超时）时不再读页面
	if errors.Is(err, context.DeadlineExceeded) {
		if title, ok := b.cfChallenge(ctx); ok {
			return fmt.Errorf("%w（标题 %q）", ErrCFChallenge, title)
		}
	}
	return openErr(seen.problem(), err)
}

// seeEmbedded 看一轮内嵌页：卡号框、金额读屏文字、主页面的支付按钮个数。
func (b *Browser) seeEmbedded(ctx context.Context, frames []frameRef) embeddedSeen {
	n, err := b.countVisible(ctx, frames[0], embeddedPayButtonSel)
	if err != nil {
		n = -1
	}
	return embeddedSeen{
		card:    b.find(ctx, frames, Sel.CardNumber, false) != nil,
		amount:  b.embeddedAmountText(ctx, frames) != "",
		buttons: n,
	}
}

// cfChallenge 读一次当前页面，判断是不是 Cloudflare 挑战页，是就返回页面标题。只读。
// 打开超时时调用方的时限通常也已经到了，所以不沿用 ctx 的时限，另给 cfCheckWait。
func (b *Browser) cfChallenge(ctx context.Context) (string, bool) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfCheckWait)
	defer cancel()
	p, err := b.ProbeRead(cctx)
	if err != nil || !IsCFChallengePage(p) {
		return "", false
	}
	return p.Title, true
}

// countVisible 数 frame 里 sel 命中的可见元素个数。
func (b *Browser) countVisible(ctx context.Context, f frameRef, sel string) (int, error) {
	expr := fmt.Sprintf("%s(%s, %s)", countVisibleJS, mustJSON(sel), visibleJS)
	r, exc, err := runtime.Evaluate(expr).WithContextID(f.ctx).WithReturnByValue(true).Do(ctx)
	if err != nil {
		b.forgetWorld(f.id)
		return 0, err
	}
	if exc != nil {
		return 0, fmt.Errorf("页面脚本出错: %s", exc.Text)
	}
	var n int
	if err := json.Unmarshal(r.Value, &n); err != nil {
		return 0, err
	}
	return n, nil
}

// embeddedPayButton 记在 *n 里的两种特殊状态；其余取值是主页面可见的提交按钮个数。
const (
	payButtonUnreadable = -1 // 读不出
	payButtonLeft       = -2 // 主页面路径已不在 /checkout/ 下
)

// embeddedPayButton 在主页面找唯一可见、可用的支付按钮，找不到返回 nil。主页面已离开结账页时按钮一律不认。
// *n 记下这次看到的可见按钮个数（唯一但不可用时为 1），或 payButtonUnreadable、payButtonLeft。
func (b *Browser) embeddedPayButton(ctx context.Context, main frameRef, n *int) *elem {
	*n = payButtonUnreadable
	expr := fmt.Sprintf("%s(%s, %s, %s, %s)", uniquePayButtonJS, mustJSON(checkoutPrefix), mustJSON(embeddedPayButtonSel), visibleJS, payEnabledJS)
	r, err := b.eval(ctx, main, expr)
	switch {
	case err != nil:
		return nil
	case r.ObjectID != "":
		*n = 1
		return &elem{frame: main, obj: r.ObjectID, sel: embeddedPayButtonSel}
	case r.Type == runtime.TypeString:
		var s string
		if json.Unmarshal(r.Value, &s) == nil {
			switch s {
			case "left":
				*n = payButtonLeft
			case "disabled": // 唯一但不可用
				*n = 1
			}
		}
		return nil
	}
	var count int
	if json.Unmarshal(r.Value, &count) == nil {
		*n = count
	}
	return nil
}

// embeddedPayButtonErr 说明内嵌页为什么没点支付。n 是最后一次看到的状态（见 embeddedPayButton）。
func embeddedPayButtonErr(n int) error {
	switch {
	case n == payButtonLeft:
		return errors.New(leftCheckoutMsg + "，没有点击")
	case n < 0:
		return errors.New("支付按钮读不出，没有点击")
	case n == 1:
		return errors.New("支付按钮不可点击（可能有字段未通过页面校验），没有点击")
	}
	return fmt.Errorf("支付按钮不唯一（找到 %d 个），没有点击", n)
}

// leftCheckout 读主页面的 location.pathname，不再以 /checkout/ 开头时返回 true。
// 读不出（比如页面正在跳转、旧文档的执行环境已失效）时按还在结账页处理，下一轮再读。
func (b *Browser) leftCheckout(ctx context.Context, main frameRef) bool {
	r, exc, err := runtime.Evaluate(`location.pathname`).WithContextID(main.ctx).WithReturnByValue(true).Do(ctx)
	if err != nil {
		b.forgetWorld(main.id)
		return false
	}
	var path string
	if exc != nil || json.Unmarshal(r.Value, &path) != nil {
		return false
	}
	return !strings.HasPrefix(path, checkoutPrefix)
}

// embeddedAmountText 在各 frame 里找 Payment amount 读屏文字，返回所在父元素的文字；找不到返回空串。读不出的 frame 跳过。
func (b *Browser) embeddedAmountText(ctx context.Context, frames []frameRef) string {
	for _, f := range frames {
		r, exc, err := runtime.Evaluate(embeddedAmountJS).WithContextID(f.ctx).WithReturnByValue(true).Do(ctx)
		if err != nil {
			b.forgetWorld(f.id)
			continue
		}
		var s string
		if exc == nil && json.Unmarshal(r.Value, &s) == nil && s != "" {
			return s
		}
	}
	return ""
}

// waitCardVisible 等卡号框可见，最多 max；等不到返回 false。
func (b *Browser) waitCardVisible(ctx context.Context, max time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	ok := false
	_ = b.do(ctx, func(c context.Context) error {
		var err error
		ok, err = b.poll(c, max, func(frames []frameRef) bool {
			return b.find(c, frames, Sel.CardNumber, false) != nil
		})
		return err
	})
	return ok
}

// readEmbeddedAmount 读内嵌页的 Payment amount 读屏文字，返回最小单位金额与规范化原文（如 "100.00 USD"）。
func (b *Browser) readEmbeddedAmount(ctx context.Context, currency string) (int64, string, error) {
	var raw string
	err := b.do(ctx, func(ctx context.Context) error {
		frames, err := b.frames(ctx)
		if err != nil {
			return err
		}
		raw = b.embeddedAmountText(ctx, frames)
		return nil
	})
	if err != nil {
		return 0, "", fmt.Errorf("读取金额失败: %w", err)
	}
	if raw == "" {
		return 0, "", errors.New("支付页上没有找到 Payment amount")
	}
	text, err := normalizeEmbeddedAmount(raw)
	if err != nil {
		return 0, "", err
	}
	minor, err := ParseAmount(text, currency)
	if err != nil {
		return 0, text, err
	}
	return minor, text, nil
}

// normalizeEmbeddedAmount 从读屏文字里取出金额和币种代码，规范成「金额里的那一段数字 + 空格 + 大写币种代码」，
// 如 "100.00 USD"、"1,120.00 PHP"。原来的货币符号丢掉，币种只由 Currency 后面的代码决定；读不到 Currency 就报错，不猜。
// 金额原文按 amountRe 的口径必须正好一段数字，多段时报错。
func normalizeEmbeddedAmount(raw string) (string, error) {
	m := embeddedAmountRe.FindStringSubmatch(raw)
	if m == nil {
		return "", errors.New("读不到 Payment amount 的币种")
	}
	// 夹着税费、折扣之类的第二个金额时，取哪一段都可能读错（比如读成税额，绕过最低金额），宁可读不出
	nums := amountRe.FindAllString(m[1], -1)
	switch {
	case len(nums) == 0:
		return "", errors.New("Payment amount 里没有金额数字")
	case len(nums) > 1:
		return "", errors.New("Payment amount 里有多个金额，无法确定应付金额")
	}
	return nums[0] + " " + strings.ToUpper(m[2]), nil
}
