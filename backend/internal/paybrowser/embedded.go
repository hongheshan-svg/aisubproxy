package paybrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/page"
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
	// payFocusSettle 是点支付前把焦点移到主页面之后固定等的时间，让页面对 iframe 失焦的异步反应（校验、重算）先跑完。
	payFocusSettle = 400 * time.Millisecond
)

// payReadyJS 判断内嵌页的支付按钮能不能点：可用（同 payEnabledJS），而且不在忙（aria-busy 不是 "true"）。
// 2026-11-04 实测：地址改了，页面重算时按钮同时 disabled、aria-busy="true"。
const payReadyJS = `function () {
	return !this.disabled && this.getAttribute('aria-disabled') !== 'true' && this.getAttribute('aria-busy') !== 'true'
}`

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

// onCheckoutJS 判断本 frame 是否还在内嵌结账页：主机名是 host，而且路径以 prefix 开头。
// 只看路径不够：主页面跳到别的域名、路径恰好也以 /checkout/ 开头时，会被当成仍在结账页。
const onCheckoutJS = `((host, prefix) => location.hostname === host && location.pathname.startsWith(prefix))`

// uniquePayButtonJS 在一次求值里看本 frame：不在结账页（onCheckoutJS）时返回 'left'（已离开结账页，按钮一律不认）；
// 否则数 sel 命中的可见元素，正好一个且可用（enabled，即 payReadyJS）时返回它，正好一个但不可用时返回 'disabled'，个数不是 1 时返回个数。
// 核对主机名和路径、数和取都在同一次求值里，中间页面插不进别的脚本。
const uniquePayButtonJS = `((host, prefix, sel, visible, enabled) => {
	if (!` + onCheckoutJS + `(host, prefix)) return 'left'
	const list = Array.from(document.querySelectorAll(sel)).filter(visible)
	if (list.length !== 1) return list.length
	return enabled.call(list[0]) ? list[0] : 'disabled'
})`

// checkoutPrefix 是内嵌结账页的路径前缀；主页面路径不再以它开头，就是已经离开结账页。
const checkoutPrefix = "/checkout/"

// checkoutHost 是内嵌结账页的主机名；主页面主机名不是它，也算已经离开结账页。引擎的白名单只放行这个主机名的内嵌页链接。
// 是变量只为测试：仿造页在 127.0.0.1 上。
var checkoutHost = "chatgpt.com"

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

// embeddedPayButton 在主页面找唯一可见、可用（payReadyJS：不 disabled、不 aria-busy）的支付按钮，找不到返回 nil。
// 主页面已离开结账页时按钮一律不认。
// *n 记下这次看到的可见按钮个数（唯一但不可用时为 1），或 payButtonUnreadable、payButtonLeft。
func (b *Browser) embeddedPayButton(ctx context.Context, main frameRef, n *int) *elem {
	*n = payButtonUnreadable
	expr := fmt.Sprintf("%s(%s, %s, %s, %s, %s)", uniquePayButtonJS, mustJSON(checkoutHost), mustJSON(checkoutPrefix), mustJSON(embeddedPayButtonSel), visibleJS, payReadyJS)
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

// errPayNotReady 表示内嵌页的支付按钮唯一但不可点击（disabled、aria-disabled 或 aria-busy）。
var errPayNotReady = errors.New("支付按钮不可点击（可能有字段未通过页面校验），没有点击")

// embeddedPayButtonErr 说明内嵌页为什么没点支付。n 是最后一次看到的状态（见 embeddedPayButton）。
func embeddedPayButtonErr(n int) error {
	switch {
	case n == payButtonLeft:
		return errors.New(leftCheckoutMsg + "，没有点击")
	case n < 0:
		return errors.New("支付按钮读不出，没有点击")
	case n == 1:
		return errPayNotReady
	}
	return fmt.Errorf("支付按钮不唯一（找到 %d 个），没有点击", n)
}

// waitPayButton 最多等 fieldWait，直到主页面上有唯一可见、可用的支付按钮（embeddedPayButton），看到已离开结账页就不再等。
// 等不到返回 nil，*n 是最后一次看到的状态。
func (b *Browser) waitPayButton(ctx context.Context, n *int) (*elem, error) {
	var btn *elem
	_, err := b.poll(ctx, fieldWait, func(frames []frameRef) bool {
		btn = b.embeddedPayButton(ctx, frames[0], n)
		return btn != nil || *n == payButtonLeft
	})
	return btn, err
}

// readyAfterFocus 把焦点移到主页面的支付按钮上（DOM.focus：只设焦点，不点击、不发按键），固定等 payFocusSettle，
// 再用 waitPayButton 等按钮重新唯一可用，返回那时的按钮和这段等待（不含固定的 payFocusSettle）用了多久；等不到返回 nil。
//
// 不先移焦点就点，按下鼠标那一刻焦点才离开 Stripe 的 iframe：页面要是在 iframe 失焦时校验、重算、把按钮短暂设成不可用，
// 按钮在松开之前就不可用了，点击丢失，而 submitted 已经置位，这一单只能走确认、回收。先移焦点、等页面反应完，这一下就不会再触发。
//
// 焦点此后就在支付按钮上：从这里到点击，绝不能再发任何按键（回车、空格都会直接触发支付按钮，绕过落点检查和先置位再点的顺序）。
// 之后只许读页面、滚动（landPoint）和最后那一下鼠标点击。
func (b *Browser) readyAfterFocus(ctx context.Context, btn *elem, n *int) (*elem, time.Duration, error) {
	if err := dom.Focus().WithObjectID(btn.obj).Do(ctx); err != nil {
		return nil, 0, fmt.Errorf("把焦点移到支付按钮上失败: %w", err)
	}
	if err := sleep(ctx, payFocusSettle); err != nil {
		return nil, 0, err
	}
	start := time.Now()
	btn, err := b.waitPayButton(ctx, n)
	return btn, time.Since(start), err
}

// visualOnlyProps 是只改样子、不改位置和大小的动画属性（Web Animations 关键帧里的驼峰写法，另含关键帧自带的 offset、easing、composite）。
// 支付按钮或它的上级上正在跑的动画，只要动了这之外的属性（transform、translate、left、margin、width 等），就当作按钮还在动。
const visualOnlyProps = `/^(offset|computedOffset|easing|composite|opacity|visibility|color|caretColor|accentColor|background.*|border.*Color|outline.*|boxShadow|textShadow|textDecoration.*|filter|backdropFilter|fill|stroke.*)$/`

// payClickJS 以支付按钮为 this，在同一次求值里看点它之前的最后状态，依次是：
//   - 'left'：主页面已不在结账页（onCheckoutJS：主机名、路径）；
//   - 'count:N'：主页面可见的提交按钮（sel）不是正好一个，N 是个数；
//   - 'busy'：按钮不可点（payReadyJS）；
//   - 'iframe'：主页面的焦点还在 iframe 上；
//   - 'offset'：中心点偏了——此刻的中心点和 landPoint 读到的 (x, y) 差了 tol 以上；
//   - 'anim:<属性>@<标签名>'：按钮或它的上级上有正在跑的、改位置或大小的动画（CSS 动画、过渡、Web Animations，见 visualOnlyProps），
//     属性写成 CSS 的连字符写法，标签名是挂动画的元素（小写），不写值、class、文本。这一条不能省：transform 动画由合成线程驱动，
//     2026-11-04 本机复现过 CDP 隔 50ms 读两次位置读到同一个值（主线程的动画时钟没走），按钮却在屏幕上以 200px/s 移动。
//     哪些动画不算，见下文；
//   - 'offpage'：主页面可视区里 (x, y) 那一点没有元素；
//   - 别的元素的标签名（小写，iframe 就是 iframe）：那一点不是按钮自己或它的后代。
//
// 动画判断里不算的（终审二 Minor-3）：
//   - 伪元素（::before、::after）上的：不挪按钮本身的框；
//   - 时间线不是 DocumentTimeline 的（animation-timeline: scroll()/view() 这类滚动驱动的，或没有时间线的）：只随滚动走，
//     落点检查停稳之后、点击之前不再有人滚动，它就不动；playState 却一直是 running，算上会把每一单都拒掉。
//     页面自己还在滚动时，前面两次读位置、中心点核对会看出来；
//   - 关键帧里的 CSS 自定义属性（--x）：自己不改位置和大小。经 var() 带动位置的，这种动画在主线程上跑，
//     前面两次读位置、中心点核对看得出来（TestEmbeddedPayAnimationKinds）。
//
// 全部通过返回空串。离开结账页、按钮个数放在同一次求值里查，scanBeforeClick 之后那次重数到这里的滚动、等稳期间页面变了也能拦住。
const payClickJS = `function (x, y, tol, host, prefix, sel) {
	const ready = ` + payReadyJS + `
	const visible = ` + visibleJS + `
	const visualOnly = ` + visualOnlyProps + `
	const doc = this.ownerDocument
	if (!` + onCheckoutJS + `(host, prefix)) return 'left'
	const n = Array.from(doc.querySelectorAll(sel)).filter(visible).length
	if (n !== 1) return 'count:' + n
	if (!ready.call(this)) return 'busy'
	if (doc.activeElement && doc.activeElement.tagName === 'IFRAME') return 'iframe'
	const r = this.getBoundingClientRect()
	if (Math.abs(r.left + r.width / 2 - x) > tol || Math.abs(r.top + r.height / 2 - y) > tol) return 'offset'
	const timeDriven = typeof DocumentTimeline === 'function' ? (a => a.timeline instanceof DocumentTimeline) : (a => true)
	for (const a of doc.getAnimations ? doc.getAnimations() : []) {
		const t = a.effect && a.effect.target
		if (a.playState !== 'running' || !t || !t.contains || !t.contains(this) || a.effect.pseudoElement || !timeDriven(a)) continue
		for (const k of a.effect.getKeyframes ? a.effect.getKeyframes() : [{ transform: '' }]) {
			for (const p of Object.keys(k)) {
				if (p.startsWith('--') || visualOnly.test(p)) continue
				return 'anim:' + p.replace(/[A-Z]/g, c => '-' + c.toLowerCase()) + '@' + String(t.tagName).toLowerCase()
			}
		}
	}
	const hit = doc.elementFromPoint(x, y)
	if (!hit) return 'offpage'
	return this.contains(hit) ? '' : hit.tagName.toLowerCase()
}`

// pageChanged 是点支付前最后检查时发现的页面变化：已离开结账页、可见的提交按钮不是正好一个、填表之后整页重新加载过、
// 卡片 iframe 换了。它不是落点问题，payCheckErr 原样上报，试运行的按钮状态用 state() 的写法。
type pageChanged struct {
	n   int    // msg 为空时：payButtonLeft，或可见提交按钮的个数（不是 1）
	msg string // 填表之后页面或卡片 iframe 换了时的说明，不含「，没有点击」
}

func (e pageChanged) Error() string {
	if e.msg != "" {
		return e.msg + "，没有点击"
	}
	return embeddedPayButtonErr(e.n).Error()
}

// state 是试运行按钮状态里的写法，和 embeddedPayButtonState 等不到按钮时的措辞一致。
func (e pageChanged) state() string {
	switch {
	case e.msg != "":
		return e.msg
	case e.n == payButtonLeft:
		return leftCheckoutMsg
	case e.n > 1:
		return fmt.Sprintf("不唯一（%d 个）", e.n)
	}
	return "未找到"
}

// payClickPoint 是内嵌页点支付前的最后检查（设计 §5.8），Submit 和 PayButtonState 共用，只检查、不点击：
// 先把按钮滚进视野、等它停稳（landPoint），再核对填表之后页面和卡片 iframe 没换过（checkFillMark），
// 最后用 payClickJS 在同一次求值里查结账页、按钮个数、可点、焦点、按钮没在动、落点。全部通过才返回中心点坐标，
// 调用方就点这个坐标，不再重新滚动、求中心；检查和点击之间不再有等待。
// 按钮不可点返回 errPayNotReady；页面变了返回 pageChanged；落点是别的元素返回「落点是 <标签名>，没有点击」；其余原因也以「，没有点击」结尾。
func (b *Browser) payClickPoint(ctx context.Context, btn *elem) (x, y float64, err error) {
	if x, y, err = b.landPoint(ctx, []frameRef{btn.frame}, btn); err != nil {
		return 0, 0, err
	}
	if b.beforePayCheck != nil {
		if err := b.beforePayCheck(ctx); err != nil {
			return 0, 0, err
		}
	}
	if err := b.checkFillMark(ctx); err != nil {
		return 0, 0, err
	}
	var why string
	if err := callOn(ctx, btn.obj, payClickJS, &why, x, y, landTolerance, checkoutHost, checkoutPrefix, embeddedPayButtonSel); err != nil {
		return 0, 0, err
	}
	switch why {
	case "":
		return x, y, nil
	case "left":
		return 0, 0, pageChanged{n: payButtonLeft}
	case "busy":
		return 0, 0, errPayNotReady
	case "iframe":
		return 0, 0, errors.New("焦点还在 iframe 里，没有点击")
	case "offset":
		return 0, 0, errors.New("位置还在变，中心点偏了，没有点击")
	case "offpage":
		return 0, 0, errors.New("点击落点不在页面上，没有点击")
	}
	if c, ok := strings.CutPrefix(why, "count:"); ok {
		n, _ := strconv.Atoi(c)
		return 0, 0, pageChanged{n: n}
	}
	if a, ok := strings.CutPrefix(why, "anim:"); ok {
		return 0, 0, fmt.Errorf("位置还在变，检测到动画：%s，没有点击", animNote(a))
	}
	return 0, 0, fmt.Errorf("落点是 %s，没有点击", why)
}

// animNote 整理 payClickJS 报的「属性@标签名」：只留小写字母、数字、连字符和 @，过长的截断。
// 两样都来自页面（标签名可以是页面自定义的元素名），整理一下免得说明里混进别的东西。
func animNote(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '@' {
			return r
		}
		return -1
	}, s)
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// payCheckErr 把 payClickPoint 的错误写成 Submit 的错误：按钮不可点、页面变了（pageChanged）照原样；
// 落点是别的元素写「支付按钮被挡住（落点是 …），没有点击」；
// 其余（位置还在变、落点不在页面上、焦点还在 iframe 里、读不出位置）写「支付按钮没过落点检查（原因），没有点击」。
// 位置还在变分三种：landPoint 两次读到的位置不同（「位置还在变」）、最后检查时中心点偏了（「位置还在变，中心点偏了」）、
// 检测到改位置的动画（「位置还在变，检测到动画：transform@button」）。
func payCheckErr(err error) error {
	var pc pageChanged
	if errors.Is(err, errPayNotReady) || errors.As(err, &pc) {
		return err
	}
	return fmt.Errorf("支付按钮%s，没有点击", payCheckNote(err))
}

// payCheckNote 是 payClickPoint 不通过（按钮不可点除外）的短说明，Submit 的错误和试运行的按钮状态共用：
// 落点是别的元素时是「被挡住（落点是 …）」，其余是「没过落点检查（原因）」。
func payCheckNote(err error) string {
	why := landReason(err)
	if strings.HasPrefix(why, "落点是") {
		return "被挡住（" + why + "）"
	}
	return "没过落点检查（" + why + "）"
}

// fillMark 是内嵌页 Fill 成功时的页面快照：主页面的文档（loaderId）、装着卡号框的 iframe 和它的文档。
// 填表之后整页重新加载、Payment Element 重新挂载（换了新 iframe）或卡片 iframe 自己重新加载，表单就是空的：
// 这时点支付会被 Stripe 前端校验拦下、不会扣款，但 submitted 已经置位，这一单要白占一张卡、走完确认才释放，还计入熔断。
// 所以点之前核对（checkFillMark），变了就不点、不置位。托管页不记、不核对。
type fillMark struct {
	mainLoader cdp.LoaderID
	cardFrame  cdp.FrameID
	cardLoader cdp.LoaderID
}

// readFillMark 读此刻的主页面文档，和装着可见卡号框的 iframe 及其文档；找不到卡号框时 cardFrame 为空。只读页面。
func (b *Browser) readFillMark(ctx context.Context) (fillMark, error) {
	tree, err := page.GetFrameTree().Do(ctx)
	if err != nil {
		return fillMark{}, err
	}
	m := fillMark{mainLoader: tree.Frame.LoaderID}
	frames, err := b.frames(ctx)
	if err != nil {
		return fillMark{}, err
	}
	if e := b.find(ctx, frames, Sel.CardNumber, false); e != nil {
		m.cardFrame, m.cardLoader = e.frame.id, loaderOf(tree, e.frame.id)
	}
	return m, nil
}

// loaderOf 在 frame 树里找 id 那个 frame 当前文档的 loaderId，找不到返回空。
func loaderOf(t *page.FrameTree, id cdp.FrameID) cdp.LoaderID {
	if t.Frame.ID == id {
		return t.Frame.LoaderID
	}
	for _, ch := range t.ChildFrames {
		if l := loaderOf(ch, id); l != "" {
			return l
		}
	}
	return ""
}

// markFill 是内嵌页 Fill 的最后一步：记下 fillMark。卡号框都找不到时报错（刚核对过字段，不该发生），Fill 失败、不点支付。
func (b *Browser) markFill(ctx context.Context) error {
	m, err := b.readFillMark(ctx)
	if err != nil {
		return fmt.Errorf("记下填表时的页面失败: %w", err)
	}
	if m.cardFrame == "" {
		return errors.New("记下填表时的页面失败：找不到卡号输入框")
	}
	b.mu.Lock()
	b.fillMark = m
	b.mu.Unlock()
	return nil
}

// checkFillMark 核对此刻的页面还是 Fill 时那一份（fillMark）：主页面文档没换、卡号框还在同一个 iframe 的同一个文档里。
// 变了返回 pageChanged。没记过（没填过表、只读按钮状态）时不核对；内嵌页 Fill 成功就一定记下了，Submit 要求先 Fill 成功。
func (b *Browser) checkFillMark(ctx context.Context) error {
	b.mu.Lock()
	want := b.fillMark
	b.mu.Unlock()
	if want.mainLoader == "" {
		return nil
	}
	got, err := b.readFillMark(ctx)
	switch {
	case err != nil:
		return fmt.Errorf("核对填表之后的页面失败: %w", err)
	case got.mainLoader != want.mainLoader:
		return pageChanged{msg: "页面在填表之后重新加载过"}
	case got.cardFrame != want.cardFrame || got.cardLoader != want.cardLoader:
		return pageChanged{msg: "卡号输入框在填表之后换过（卡片 iframe 重新挂载或重新加载）"}
	}
	return nil
}

// leftCheckout 看主页面还在不在结账页（onCheckoutJS：主机名是 chatgpt.com、路径以 /checkout/ 开头），不在时返回 true。
// 读不出（比如页面正在跳转、旧文档的执行环境已失效）时按还在结账页处理，下一轮再读。
func (b *Browser) leftCheckout(ctx context.Context, main frameRef) bool {
	expr := fmt.Sprintf("%s(%s, %s)", onCheckoutJS, mustJSON(checkoutHost), mustJSON(checkoutPrefix))
	r, exc, err := runtime.Evaluate(expr).WithContextID(main.ctx).WithReturnByValue(true).Do(ctx)
	if err != nil {
		b.forgetWorld(main.id)
		return false
	}
	var on bool
	if exc != nil || json.Unmarshal(r.Value, &on) != nil {
		return false
	}
	return !on
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
		//nolint:staticcheck // ST1005：Payment amount 是 Stripe 读屏文字里的原词，照原样写便于对照页面
		return "", errors.New("Payment amount 里没有金额数字")
	case len(nums) > 1:
		//nolint:staticcheck // ST1005：同上
		return "", errors.New("Payment amount 里有多个金额，无法确定应付金额")
	}
	return nums[0] + " " + strings.ToUpper(m[2]), nil
}
