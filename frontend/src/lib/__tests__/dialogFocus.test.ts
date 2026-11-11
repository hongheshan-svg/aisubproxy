// 统一确认框的焦点（审查修复，F2-18 次要）：原生确认框会挡住整个页面；换成统一确认框后焦点还留在底下的按钮上，
// 用键盘能 Tab 到底下的「原样重试」，或对着原按钮再按一次回车开出第二个框。
// 现在弹出后焦点移到框内（确认、选择默认在「取消」上，提示在「确定」上，输入框在输入框上），Tab 在框内循环，
// 关闭后焦点回到原来的按钮。回车不在框上处理，只作用于获得焦点的元素（见 components/__tests__/dialog-keys.spec.ts）。
// 本机没有 DOM 环境：逻辑在 lib/dialogFocus.ts 里用假元素测，DialogHost 的接线按源码断言。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it, vi } from 'vitest'
import { createFocusReturn, initialFocus, nextFocusIndex } from '../dialogFocus'

describe('initialFocus：弹出后焦点放在哪', () => {
  it('确认、选择：放在「取消」上（误按回车不会确认危险操作）', () => {
    expect(initialFocus('confirm')).toBe('cancel')
    expect(initialFocus('select')).toBe('cancel')
  })
  it('提示：放在「确定」上；输入：放在输入框上', () => {
    expect(initialFocus('alert')).toBe('ok')
    expect(initialFocus('prompt')).toBe('input')
  })
})

describe('nextFocusIndex：Tab 在框内循环', () => {
  it('往后到最后一个之后回到第一个，往前到第一个之前回到最后一个', () => {
    expect(nextFocusIndex(2, 0, false)).toBe(1)
    expect(nextFocusIndex(2, 1, false)).toBe(0)
    expect(nextFocusIndex(2, 0, true)).toBe(1)
    expect(nextFocusIndex(3, 2, true)).toBe(1)
  })
  it('焦点不在框内（-1）：往后到第一个，往前到最后一个', () => {
    expect(nextFocusIndex(3, -1, false)).toBe(0)
    expect(nextFocusIndex(3, -1, true)).toBe(2)
  })
  it('框里没有能聚焦的：-1', () => {
    expect(nextFocusIndex(0, -1, false)).toBe(-1)
  })
})

describe('createFocusReturn：关闭后焦点回到原来的按钮', () => {
  const el = (connected = true) => ({ isConnected: connected, focus: vi.fn() })
  it('记下弹出前的焦点，关闭后还回去', () => {
    const btn = el()
    const r = createFocusReturn(() => btn as any)
    r.save()
    r.restore()
    expect(btn.focus).toHaveBeenCalledTimes(1)
  })
  it('一个框换成另一个框：只记第一次（原来的按钮），不记框里的按钮', () => {
    const btn = el()
    const inner = el()
    let active: any = btn
    const r = createFocusReturn(() => active)
    r.save()
    active = inner
    r.save()
    r.restore()
    expect(btn.focus).toHaveBeenCalledTimes(1)
    expect(inner.focus).not.toHaveBeenCalled()
  })
  it('原来的按钮已经不在页面上：不动；还回去之后再 restore 不重复', () => {
    const gone = el(false)
    const r = createFocusReturn(() => gone as any)
    r.save()
    r.restore()
    expect(gone.focus).not.toHaveBeenCalled()
    const btn = el()
    const r2 = createFocusReturn(() => btn as any)
    r2.save()
    r2.restore()
    r2.restore()
    expect(btn.focus).toHaveBeenCalledTimes(1)
  })
})

describe('DialogHost 接线', () => {
  const src = readFileSync(resolve(__dirname, '../../components/DialogHost.vue'), 'utf8')
  it('弹出时记下原焦点并把焦点移到框内，关闭时还回去', () => {
    expect(src).toContain("from '../lib/dialogFocus'")
    expect(src).toMatch(/if \(val && !old\) focusReturn\.save\(\)/)
    expect(src).toMatch(/const target = initialFocus\(val\.kind\)/)
    expect(src).toMatch(/else if \(old\) focusReturn\.restore\(\)/)
    expect(src).toContain('ref="cancelEl"')
    expect(src).toContain('ref="okEl"')
  })
  it('Tab 在框内循环', () => {
    expect(src).toMatch(/@keydown\.tab="onTab"/)
  })
})
