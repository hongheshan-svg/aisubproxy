package paybrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/chromedp/cdproto/runtime"
)

// 「保存付款信息」勾选框（设计 2026-11-03 §5.2 filling）：买 Codex 点数时，页面上如果有「把这张卡存到账号里、以后购买用」的勾选框，
// 必须确认它没勾上，否则我们的卡会留在用户账号上，以后的续费、自动充值可能扣它（调研 §3）。
//
// 真实页面上的写法没有核实（调研 T4），chatgpt.com 前端包里的文案是 "Save payment details to {merchantName} for future purchases."。
// 所以不按 id 认，按勾选框的标签文字认：同时含「保存」类词和「付款、卡、以后购买」类词。Link 的勾选框（#enableStripePass）另由 disableLinkSave 处理。
// 读不出某个 frame 就算确认不了，报错放弃；页面没有这类勾选框不算错。

// savePaymentJS 在一个 frame 里找「保存付款信息」类勾选框。参数为 true 时返回第一个勾着的元素（没有返回 null），否则返回 {found, checked} 计数。
const savePaymentJS = `((wantChecked) => {
	const save = /save|remember|保存|記憶|guardar/i
	const pay = /payment|card|future purchase|付款|支付|银行卡|卡片|お支払い|カード|pago|tarjeta/i
	const textOf = el => {
		const parts = [el.getAttribute('aria-label') || '', el.name || '', el.id || '']
		if (el.labels) for (const l of el.labels) parts.push(l.innerText || l.textContent || '')
		const by = el.getAttribute('aria-labelledby')
		if (by) for (const id of by.split(/\s+/)) {
			const n = id && document.getElementById(id)
			if (n) parts.push(n.innerText || n.textContent || '')
		}
		const wrap = el.closest('label')
		if (wrap) parts.push(wrap.innerText || wrap.textContent || '')
		if (el.parentElement) parts.push(String(el.parentElement.innerText || el.parentElement.textContent || '').slice(0, 300))
		return parts.join(' ')
	}
	const out = { found: 0, checked: 0 }
	let first = null
	for (const el of document.querySelectorAll('input[type="checkbox"], [role="checkbox"]')) {
		if (el.matches('#enableStripePass, input[name="enableStripePass"]')) continue
		const t = textOf(el)
		if (!save.test(t) || !pay.test(t)) continue
		const on = el.matches('input') ? !!el.checked : el.getAttribute('aria-checked') === 'true'
		out.found++
		if (on) {
			out.checked++
			if (!first) first = el
		}
	}
	return wantChecked ? first : out
})`

// savePaymentRounds 是取消勾选最多试几轮：第一轮用鼠标点，之后用脚本点。
const savePaymentRounds = 3

// EnsureNoSavePayment 确认支付页上没有勾着的「保存付款信息」勾选框，勾着就取消。返回处理结果（页面没有、未勾选、已取消），
// 也写进 FillReport。读不出页面、取消不掉都报错：调用方按「未提交」放弃。只读写勾选框，绝不点支付按钮。
func (b *Browser) EnsureNoSavePayment(ctx context.Context) (string, error) {
	note := ""
	err := b.do(ctx, func(ctx context.Context) error {
		for round := 0; round < savePaymentRounds; round++ {
			found, checked, err := b.countSavePayment(ctx)
			if err != nil {
				return err
			}
			if checked == 0 {
				switch {
				case found == 0:
					note = "页面没有"
				case round == 0:
					note = "未勾选"
				default:
					note = "已取消"
				}
				return nil
			}
			frames, err := b.frames(ctx)
			if err != nil {
				return fmt.Errorf("读取页面失败: %w", err)
			}
			e := b.evalElem(ctx, frames, fmt.Sprintf("%s(true)", savePaymentJS))
			if e == nil {
				return errors.New("找到勾着的「保存付款信息」，但拿不到这个勾选框")
			}
			if round == 0 {
				_ = click(ctx, e) // 点不到也没关系，下一轮看结果，再用脚本点
			} else if err := callOn(ctx, e.obj, `function () { this.click() }`, nil); err != nil {
				return fmt.Errorf("取消「保存付款信息」失败: %w", err)
			}
			if err := sleep(ctx, settleDelay); err != nil {
				return err
			}
			releaseObjects(ctx)
		}
		return errors.New("取消不掉「保存付款信息」勾选")
	})
	if err != nil {
		note = "无法确认"
	}
	b.updateReport(func(r *FillReport) { r.SavePayment = note })
	if err != nil {
		return note, fmt.Errorf("无法确认「保存付款信息」没有勾选: %w", err)
	}
	return note, nil
}

// countSavePayment 统计各 frame 里「保存付款信息」类勾选框的个数与勾着的个数。任何一个 frame 读不出都报错：那里可能就有勾着的框。
func (b *Browser) countSavePayment(ctx context.Context) (found, checked int, err error) {
	frames, err := b.frames(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("读取页面失败: %w", err)
	}
	expr := fmt.Sprintf("%s(false)", savePaymentJS)
	for _, f := range frames {
		r, exc, err := runtime.Evaluate(expr).WithContextID(f.ctx).WithReturnByValue(true).Do(ctx)
		if err != nil {
			b.forgetWorld(f.id)
			return 0, 0, errors.New("有 frame 读不出")
		}
		if exc != nil {
			return 0, 0, fmt.Errorf("页面脚本出错: %s", exc.Text)
		}
		var st struct {
			Found   int `json:"found"`
			Checked int `json:"checked"`
		}
		if err := json.Unmarshal(r.Value, &st); err != nil {
			return 0, 0, errors.New("读不出勾选框状态")
		}
		found += st.Found
		checked += st.Checked
	}
	return found, checked, nil
}
