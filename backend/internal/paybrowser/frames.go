package paybrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// 跨 frame 查找与输入的底层操作。
//
// 卡号框在 js.stripe.com 的 iframe 里，3DS 框还会再嵌一层发卡行页面。Launch 关掉了站点隔离，
// 这些跨站 iframe 与主页面同属一个 target，可按 frame 取执行环境。每个 frame 里建一个本包专用的
// 隔离执行环境（不受页面脚本改写原型的影响），元素以对象句柄在 CDP 命令间传递。

const (
	worldName   = "cdk-pay" // 本包在各 frame 里的隔离执行环境名
	objectGroup = "cdk-pay" // 本包取得的页面对象句柄所属分组，每次操作后释放
)

// frameRef 是一个可见的 frame 及本包在其中的执行环境。
type frameRef struct {
	id, parent cdp.FrameID
	ctx        runtime.ExecutionContextID
}

// elem 是某个 frame 里的一个元素。
type elem struct {
	frame frameRef
	obj   runtime.RemoteObjectID
	sel   string // 命中的选择器，只用于填表报告
}

// visibleJS 判断元素是否可见（自身与祖先都未隐藏、透明度不为 0、有面积）。
const visibleJS = `(el => {
	if (!el || !el.isConnected) return false
	if (el.checkVisibility && !el.checkVisibility({ opacityProperty: true, visibilityProperty: true })) return false
	const r = el.getBoundingClientRect()
	return r.width > 0 && r.height > 0
})`

// findJS 按 sels 顺序找第一个可见元素；needText 时还要求有文字。选择器写错时跳过。
const findJS = `((sels, visible, needText) => {
	for (const s of sels) {
		let list
		try { list = document.querySelectorAll(s) } catch (e) { continue }
		for (const el of list) {
			if (!visible(el)) continue
			if (needText && !(el.innerText || '').trim()) continue
			return el
		}
	}
	return null
})`

// findAnyJS 按 sels 顺序找第一个存在的元素，不管是否可见。选择器写错时跳过。
const findAnyJS = `(sels => {
	for (const s of sels) {
		try {
			const el = document.querySelector(s)
			if (el) return el
		} catch (e) {}
	}
	return null
})`

// visibleTextsJS 返回 sels 命中的全部可见元素的文字（空白已归一），没有文字的跳过。选择器写错时跳过。
const visibleTextsJS = `((sels, visible) => {
	const out = []
	for (const s of sels) {
		let list
		try { list = document.querySelectorAll(s) } catch (e) { continue }
		for (const el of list) {
			if (!visible(el)) continue
			const t = (el.innerText || '').replace(/\s+/g, ' ').trim()
			if (t) out.push(t)
		}
	}
	return out
})`

// findTextJS 找可见文字含任一 needles（不分大小写）的元素，返回离它最近的可点击祖先。
const findTextJS = `((needles, visible) => {
	const clickable = 'button, a, label, summary, [role="button"], [role="radio"], [role="tab"], [role="option"], [role="link"], [role="menuitem"]'
	const root = document.body || document.documentElement
	for (const n of needles) {
		const want = n.toLowerCase()
		const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
		for (let t = walker.nextNode(); t; t = walker.nextNode()) {
			if (!t.data.replace(/\s+/g, ' ').toLowerCase().includes(want)) continue
			const el = t.parentElement
			const target = el && (el.closest(clickable) || el)
			if (target && visible(target)) return target
		}
	}
	return null
})`

// do 在标签页上执行一组 CDP 操作，结束后释放这期间取得的页面对象句柄。
func (b *Browser) do(ctx context.Context, f func(ctx context.Context) error) error {
	return b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		defer releaseObjects(ctx)
		return f(ctx)
	}))
}

// releaseObjects 释放本包取得的页面对象句柄。轮询时每轮没找到就释放一次，免得长时间等待中越积越多。
func releaseObjects(ctx context.Context) { _ = runtime.ReleaseObjectGroup(objectGroup).Do(ctx) }

// frames 返回主页面与所有可见的各级 iframe（深度优先），第一个总是主页面。所在 iframe 元素不可见的子树整个跳过。
func (b *Browser) frames(ctx context.Context) ([]frameRef, error) {
	tree, err := page.GetFrameTree().Do(ctx)
	if err != nil {
		return nil, err
	}
	var out []frameRef
	var walk func(t *page.FrameTree, parent *frameRef)
	walk = func(t *page.FrameTree, parent *frameRef) {
		f := frameRef{id: t.Frame.ID}
		if parent != nil {
			f.parent = parent.id
			if !b.ownerVisible(ctx, *parent, f.id) {
				return
			}
		}
		c, err := b.world(ctx, f.id)
		if err != nil {
			return
		}
		f.ctx = c
		out = append(out, f)
		for _, ch := range t.ChildFrames {
			walk(ch, &f)
		}
	}
	walk(tree, nil)
	if len(out) == 0 {
		return nil, errors.New("页面还没有可用的 frame")
	}
	return out, nil
}

// ownerVisible 判断子 frame 所在的 iframe 元素在父 frame 里是否可见。
func (b *Browser) ownerVisible(ctx context.Context, parent frameRef, child cdp.FrameID) bool {
	owner, _, err := dom.GetFrameOwner(child).Do(ctx)
	if err != nil {
		return false
	}
	obj, err := dom.ResolveNode().WithBackendNodeID(owner).WithExecutionContextID(parent.ctx).WithObjectGroup(objectGroup).Do(ctx)
	if err != nil {
		b.forgetWorld(parent.id)
		return false
	}
	var ok bool
	return callOn(ctx, obj.ObjectID, fmt.Sprintf("function () { return %s(this) }", visibleJS), &ok) == nil && ok
}

// world 返回 frame 里本包的隔离执行环境，按 frame 缓存；frame 换了文档后旧环境失效，用到出错时丢弃重建。
func (b *Browser) world(ctx context.Context, id cdp.FrameID) (runtime.ExecutionContextID, error) {
	b.worldMu.Lock()
	c, ok := b.worlds[id]
	b.worldMu.Unlock()
	if ok {
		return c, nil
	}
	c, err := page.CreateIsolatedWorld(id).WithWorldName(worldName).Do(ctx)
	if err != nil {
		return 0, err
	}
	b.worldMu.Lock()
	if b.worlds == nil {
		b.worlds = map[cdp.FrameID]runtime.ExecutionContextID{}
	}
	b.worlds[id] = c
	b.worldMu.Unlock()
	return c, nil
}

func (b *Browser) forgetWorld(id cdp.FrameID) {
	b.worldMu.Lock()
	delete(b.worlds, id)
	b.worldMu.Unlock()
}

// eval 在 frame 的执行环境里求值，对象结果以句柄返回。
func (b *Browser) eval(ctx context.Context, f frameRef, expr string) (*runtime.RemoteObject, error) {
	r, exc, err := runtime.Evaluate(expr).WithContextID(f.ctx).WithObjectGroup(objectGroup).Do(ctx)
	if err != nil {
		b.forgetWorld(f.id)
		return nil, err
	}
	if exc != nil {
		return nil, fmt.Errorf("页面脚本出错: %s", exc.Text)
	}
	return r, nil
}

// evalElem 求值并把非空对象结果当作元素返回。
func (b *Browser) evalElem(ctx context.Context, frames []frameRef, expr string) *elem {
	for _, f := range frames {
		if r, err := b.eval(ctx, f, expr); err == nil && r.ObjectID != "" {
			return &elem{frame: f, obj: r.ObjectID}
		}
	}
	return nil
}

// find 按 sels 顺序逐个选择器在各 frame 中找第一个可见元素；找不到返回 nil。
// 选择器顺序优先于 frame 顺序：不能让前面 frame 里只命中宽泛选择器的元素，压过后面 frame 里命中精确选择器的元素。
func (b *Browser) find(ctx context.Context, frames []frameRef, sels []string, needText bool) *elem {
	for _, s := range sels {
		if e := b.evalElem(ctx, frames, fmt.Sprintf("%s(%s, %s, %t)", findJS, mustJSON([]string{s}), visibleJS, needText)); e != nil {
			e.sel = s
			return e
		}
	}
	return nil
}

// findAny 同 find，但不要求可见：透明、盖在样式元素上的勾选框按可见规则找不到。
func (b *Browser) findAny(ctx context.Context, frames []frameRef, sels []string) *elem {
	for _, s := range sels {
		if e := b.evalElem(ctx, frames, fmt.Sprintf("%s(%s)", findAnyJS, mustJSON([]string{s}))); e != nil {
			e.sel = s
			return e
		}
	}
	return nil
}

// visibleTexts 按 sels 顺序逐个选择器在各 frame 中收集命中元素的文字，相同文字只留一条。读不出的 frame 跳过。
func (b *Browser) visibleTexts(ctx context.Context, frames []frameRef, sels []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range sels {
		expr := fmt.Sprintf("%s(%s, %s)", visibleTextsJS, mustJSON([]string{s}), visibleJS)
		for _, f := range frames {
			r, exc, err := runtime.Evaluate(expr).WithContextID(f.ctx).WithReturnByValue(true).Do(ctx)
			if err != nil {
				b.forgetWorld(f.id)
				continue
			}
			var list []string
			if exc != nil || json.Unmarshal(r.Value, &list) != nil {
				continue
			}
			for _, t := range list {
				if !seen[t] {
					seen[t] = true
					out = append(out, t)
				}
			}
		}
	}
	return out
}

// findText 按 needles 顺序逐条文字在各 frame 中找可点击元素（NewCardOption 用），同样文字顺序优先于 frame 顺序。
func (b *Browser) findText(ctx context.Context, frames []frameRef, needles []string) *elem {
	for _, n := range needles {
		if e := b.evalElem(ctx, frames, fmt.Sprintf("%s(%s, %s)", findTextJS, mustJSON([]string{n}), visibleJS)); e != nil {
			return e
		}
	}
	return nil
}

// poll 每 250 毫秒扫描一次 frame 并调用 step，直到 step 返回 true 或超过 limit（超时返回 false）。
// 至少扫描一次；每轮没成功就释放这一轮取得的对象句柄。
func (b *Browser) poll(ctx context.Context, limit time.Duration, step func([]frameRef) bool) (bool, error) {
	end := time.Now().Add(limit)
	for {
		if frames, err := b.frames(ctx); err == nil && step(frames) {
			return true, nil
		}
		releaseObjects(ctx)
		if !time.Now().Before(end) {
			return false, nil
		}
		if err := sleep(ctx, pollInterval); err != nil {
			return false, err
		}
	}
}

// waitFind 轮询直到 look 找到元素或超过 limit；超时返回 nil。
func (b *Browser) waitFind(ctx context.Context, limit time.Duration, look func([]frameRef) *elem) (*elem, error) {
	var found *elem
	_, err := b.poll(ctx, limit, func(frames []frameRef) bool {
		found = look(frames)
		return found != nil
	})
	return found, err
}

// subtree 返回 frame id 及其各级子 frame。
func subtree(frames []frameRef, id cdp.FrameID) []frameRef {
	in := map[cdp.FrameID]bool{id: true}
	var out []frameRef
	for _, f := range frames { // frames 按深度优先排列，父 frame 总在子 frame 之前
		if in[f.id] || in[f.parent] {
			in[f.id] = true
			out = append(out, f)
		}
	}
	return out
}

// contentFrame 返回 iframe 元素承载的 frame。
func contentFrame(ctx context.Context, e *elem) (cdp.FrameID, error) {
	n, err := dom.DescribeNode().WithObjectID(e.obj).Do(ctx)
	if err != nil {
		return "", err
	}
	if n.FrameID == "" {
		return "", errors.New("不是 iframe 元素")
	}
	return n.FrameID, nil
}

// callOn 以元素为 this 调用函数，结果按 JSON 解到 res（可为 nil）。
func callOn(ctx context.Context, obj runtime.RemoteObjectID, fn string, res any, args ...any) error {
	p := runtime.CallFunctionOn(fn).WithObjectID(obj).WithReturnByValue(true)
	if len(args) > 0 {
		cargs := make([]*runtime.CallArgument, len(args))
		for i, a := range args {
			cargs[i] = &runtime.CallArgument{Value: mustJSON(a)}
		}
		p = p.WithArguments(cargs)
	}
	r, exc, err := p.Do(ctx)
	if err != nil {
		return err
	}
	if exc != nil {
		return fmt.Errorf("页面脚本出错: %s", exc.Text)
	}
	if res == nil || len(r.Value) == 0 {
		return nil
	}
	return json.Unmarshal(r.Value, res)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // 只用于字符串与字符串切片，不会失败
	}
	return b
}

// bounds 返回元素在主页面可视区域中的外接矩形。
func bounds(ctx context.Context, e *elem) (x0, y0, x1, y1 float64, err error) {
	quads, err := dom.GetContentQuads().WithObjectID(e.obj).Do(ctx)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	if len(quads) == 0 || len(quads[0]) < 8 {
		return 0, 0, 0, 0, errors.New("元素不在可视区域内")
	}
	q := quads[0]
	x0, y0, x1, y1 = q[0], q[1], q[0], q[1]
	for i := 2; i < 8; i += 2 {
		x0, x1 = math.Min(x0, q[i]), math.Max(x1, q[i])
		y0, y1 = math.Min(y0, q[i+1]), math.Max(y1, q[i+1])
	}
	return x0, y0, x1, y1, nil
}

// click 把元素滚进视野，用鼠标点它的中心：焦点与用户激活都和真人点击一致，跨站 iframe 里也一样。
func click(ctx context.Context, e *elem) error {
	if err := dom.ScrollIntoViewIfNeeded().WithObjectID(e.obj).Do(ctx); err != nil {
		return err
	}
	x0, y0, x1, y1, err := bounds(ctx, e)
	if err != nil {
		return err
	}
	return chromedp.MouseClickXY((x0+x1)/2, (y0+y1)/2).Do(ctx)
}

const (
	landSettle    = 50 * time.Millisecond // 落点检查时两次读位置的间隔
	landTolerance = 1.0                   // 两次读到的中心点相差不超过这么多像素，才算没在移动
	landEdge      = 10.0                  // 中心点离主页面可视区边缘不足这么多像素，就先用脚本滚主页面
)

// scrollMainJS 在主页面上把点 (x, y) 挪到可视区中部：点在可视区外、或离边缘不足 edge 像素时，用 window.scrollBy 滚主页面
// （只是脚本滚动，不发鼠标、键盘事件），返回滚了没有。可视区宽高取 innerWidth/innerHeight 与 documentElement 的
// clientWidth/clientHeight（不含滚动条）中较小的。
const scrollMainJS = `((x, y, edge) => {
	const de = document.documentElement
	const w = Math.min(innerWidth, de.clientWidth || innerWidth)
	const h = Math.min(innerHeight, de.clientHeight || innerHeight)
	const dx = x < edge || x >= w - edge ? x - w / 2 : 0
	const dy = y < edge || y >= h - edge ? y - h / 2 : 0
	if (!dx && !dy) return false
	window.scrollBy({ left: dx, top: dy, behavior: 'instant' })
	return true
})`

// landsInOwnFrame 把元素滚进视野，求它中心点在主页面可视区里的坐标，确认鼠标点下去会落在它自己所在的 iframe 上，返回这个坐标。
//   - 先 ScrollIntoViewIfNeeded。中心点仍在主页面可视区外、或离边缘不足 landEdge 时，再用 scrollMainJS 把主页面滚到让它居中：
//     2026-11-04 实测卡片 iframe 在首屏下方时，iframe 里元素的滚入没有带动主页面，坐标在主页面视口以外，落点检查一律不过。
//     滚完仍在视口外，下面的 elementFromPoint 读不到元素，照旧不通过。
//   - 隔 landSettle 读两次位置，相差超过 landTolerance 说明还在移动，不通过。
//   - 在主页面上用 elementFromPoint 看那一点：必须是 iframe 元素，而且是元素所在 frame 或它的上层。主页面上的元素一律不通过。
//
// 内嵌页的 iframe 可能只露出一部分（2026-11-04 实测地址 iframe 带 scrolling="no"、内文档比 iframe 高），
// getContentQuads 不按 iframe 裁剪，算出的坐标可能在 iframe 外面、落到主页面别的元素上，而支付按钮就在主页面。
// frames 是 b.frames 的结果，第一个是主页面。只在内嵌页（PageEmbedded）的路径上用。
func (b *Browser) landsInOwnFrame(ctx context.Context, frames []frameRef, e *elem) (x, y float64, err error) {
	if err := dom.ScrollIntoViewIfNeeded().WithObjectID(e.obj).Do(ctx); err != nil {
		return 0, 0, err
	}
	center := func() (float64, float64, error) {
		x0, y0, x1, y1, err := bounds(ctx, e)
		return (x0 + x1) / 2, (y0 + y1) / 2, err
	}
	x0, y0, err := center()
	if err != nil {
		return 0, 0, err
	}
	r, err := b.eval(ctx, frames[0], fmt.Sprintf("%s(%f, %f, %f)", scrollMainJS, x0, y0, landEdge))
	if err != nil {
		return 0, 0, err
	}
	if string(r.Value) == "true" {
		if x0, y0, err = center(); err != nil {
			return 0, 0, err
		}
	}
	if err := sleep(ctx, landSettle); err != nil {
		return 0, 0, err
	}
	if x, y, err = center(); err != nil {
		return 0, 0, err
	}
	if math.Abs(x-x0) > landTolerance || math.Abs(y-y0) > landTolerance {
		return 0, 0, errors.New("位置还在变，没有点击")
	}

	if r, err = b.eval(ctx, frames[0], fmt.Sprintf("document.elementFromPoint(%f, %f)", x, y)); err != nil {
		return 0, 0, err
	}
	if r.ObjectID == "" {
		return 0, 0, errors.New("点击落点不在页面上，没有点击")
	}
	hit := &elem{frame: frames[0], obj: r.ObjectID}
	var tag string
	if err := callOn(ctx, hit.obj, `function () { return this.tagName }`, &tag); err != nil {
		return 0, 0, err
	}
	if tag != "IFRAME" {
		return 0, 0, fmt.Errorf("点击落点是主页面上的 %s，不是 iframe，没有点击", strings.ToLower(tag))
	}
	id, err := contentFrame(ctx, hit)
	if err != nil {
		return 0, 0, err
	}
	if !slices.ContainsFunc(subtree(frames, id), func(f frameRef) bool { return f.id == e.frame.id }) {
		return 0, 0, errors.New("点击落点是别的 iframe，没有点击")
	}
	return x, y, nil
}

// clickOption 点 iframe 里的下拉选项（地址联想）。先确认选项本身不是提交控件（同 clickReveal），
// 再过 landsInOwnFrame，落点检查紧挨着鼠标点击；任一不过就不点、返回错误。主页面上的选项一律不点。
func (b *Browser) clickOption(ctx context.Context, frames []frameRef, e *elem) error {
	var isSubmit bool
	if err := callOn(ctx, e.obj, submitControlJS, &isSubmit); err != nil {
		return err
	}
	if isSubmit {
		return errSubmitControl
	}
	return b.clickLanded(ctx, frames, e)
}

// clickLanded 过 landsInOwnFrame 后用鼠标点它算出的坐标；不过就不发任何鼠标事件，返回原因。
func (b *Browser) clickLanded(ctx context.Context, frames []frameRef, e *elem) error {
	x, y, err := b.landsInOwnFrame(ctx, frames, e)
	if err != nil {
		return err
	}
	return chromedp.MouseClickXY(x, y).Do(ctx)
}

// clickBox 用鼠标点勾选框。托管页同 click；内嵌页上先过 landsInOwnFrame，不通过就不点、返回原因，由调用方改用脚本点并核对。
func (b *Browser) clickBox(ctx context.Context, e *elem) error {
	if b.pageKind() != PageEmbedded {
		return click(ctx, e)
	}
	frames, err := b.frames(ctx)
	if err != nil {
		return err
	}
	return b.clickLanded(ctx, frames, e)
}

// clickEntry 点按文字找到的入口（新卡、Pay without Link、手动地址）。托管页同 clickReveal；
// 内嵌页同 clickOption：不是提交控件、落点检查也通过才点，不通过就不点、返回原因。
func (b *Browser) clickEntry(ctx context.Context, frames []frameRef, e *elem) error {
	if b.pageKind() != PageEmbedded {
		return clickReveal(ctx, e)
	}
	return b.clickOption(ctx, frames, e)
}

// focus 点击输入框；点完焦点仍不在它上面时直接设焦点。
func focus(ctx context.Context, e *elem) error {
	if err := click(ctx, e); err != nil {
		return err
	}
	_, err := ensureFocus(ctx, e)
	return err
}

// focusField 选中要填写的输入框。托管页同 focus。内嵌页上先过 landsInOwnFrame，通过才用鼠标点；
// 不通过（落点不在它自己的 iframe 上、还在移动、读不出位置）就不发任何鼠标事件，直接用 DOM.focus 设焦点。
// 内嵌页上用了直接设焦点时，direct 是原因（落点检查的错误文字，去掉「，没有点击」，不含任何值），由调用方写进填表报告；托管页总是空。
func (b *Browser) focusField(ctx context.Context, e *elem) (direct string, err error) {
	if b.pageKind() != PageEmbedded {
		return "", focus(ctx, e)
	}
	frames, lerr := b.frames(ctx)
	if lerr == nil {
		var x, y float64
		if x, y, lerr = b.landsInOwnFrame(ctx, frames, e); lerr == nil {
			if err := chromedp.MouseClickXY(x, y).Do(ctx); err != nil {
				return "", err
			}
		}
	}
	set, err := ensureFocus(ctx, e)
	switch {
	case lerr != nil:
		direct = strings.TrimSuffix(lerr.Error(), "，没有点击")
	case set:
		direct = "鼠标点了，焦点没过去"
	}
	return direct, err
}

// ensureFocus 确认焦点在输入框上，不在就用 DOM.focus 直接设（不发鼠标事件）。set 表示用了 DOM.focus。
func ensureFocus(ctx context.Context, e *elem) (set bool, err error) {
	const focused = `function () { return this.ownerDocument.hasFocus() && this.ownerDocument.activeElement === this }`
	var ok bool
	if err := callOn(ctx, e.obj, focused, &ok); err != nil || ok {
		return false, err
	}
	if err := dom.Focus().WithObjectID(e.obj).Do(ctx); err != nil {
		return true, err
	}
	if err := callOn(ctx, e.obj, focused, &ok); err != nil {
		return true, err
	}
	if !ok {
		return true, errors.New("焦点不在输入框上")
	}
	return true, nil
}

// clearInput 选中输入框全部内容后按退格清空（真实按键，受控组件能感知）。
func clearInput(ctx context.Context, e *elem) error {
	var empty bool
	if err := callOn(ctx, e.obj, `function () { if (this.select) this.select(); return this.value === '' }`, &empty); err != nil || empty {
		return err
	}
	if err := chromedp.KeyEvent(kb.Backspace).Do(ctx); err != nil {
		return err
	}
	if err := callOn(ctx, e.obj, `function () { return this.value === '' }`, &empty); err != nil {
		return err
	}
	if !empty {
		return errors.New("输入框清不空")
	}
	return nil
}

// typeKeys 逐字发送真实按键，每字间隔 delay。
func typeKeys(ctx context.Context, s string, delay time.Duration) error {
	for _, r := range s {
		if err := chromedp.KeyEvent(string(r)).Do(ctx); err != nil {
			return err
		}
		if err := sleep(ctx, delay); err != nil {
			return err
		}
	}
	return nil
}

func inputValue(ctx context.Context, e *elem) (string, error) {
	var v string
	err := callOn(ctx, e.obj, `function () { return String(this.value) }`, &v)
	return v, err
}

// innerText 返回元素文字，空白归一。
func innerText(ctx context.Context, e *elem) string {
	var t string
	_ = callOn(ctx, e.obj, `function () { return (this.innerText || this.textContent || '').replace(/\s+/g, ' ').trim() }`, &t)
	return t
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
