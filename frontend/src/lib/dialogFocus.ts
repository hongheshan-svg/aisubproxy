// 统一确认框（components/DialogHost.vue）的焦点：弹出后移到框内，Tab 在框内循环，关闭后回到原来的按钮。
// 原生确认框会挡住整个页面；不移焦点的话，用键盘能 Tab 到底下的按钮，或对着原按钮再按一次回车开出第二个框。

export type DialogKind = 'alert' | 'confirm' | 'prompt' | 'select'

/** 弹出后焦点放在哪：确认、选择放「取消」（误按回车不会确认危险操作），提示放「确定」，输入放输入框。 */
export function initialFocus(kind: DialogKind): 'cancel' | 'ok' | 'input' {
  if (kind === 'prompt') return 'input'
  if (kind === 'alert') return 'ok'
  return 'cancel'
}

/** Tab / Shift+Tab 时框内下一个能聚焦的元素的下标；current 为 -1 表示焦点不在框内；框里没有能聚焦的返回 -1。 */
export function nextFocusIndex(count: number, current: number, back: boolean): number {
  if (count <= 0) return -1
  if (current < 0) return back ? count - 1 : 0
  return (current + (back ? count - 1 : 1)) % count
}

/** 对话框上按回车是否当成「确定」：焦点在按钮上时交给按钮自己（否则在「取消」上按回车会变成确定）。 */
export function enterConfirms(target: { tagName?: string } | null): boolean {
  return !(target && target.tagName === 'BUTTON')
}

type Focusable = { focus: () => void; isConnected?: boolean }

/** 记下弹出前的焦点、关闭后还回去。一个框直接换成另一个框时只记第一次（原来的按钮）。 */
export function createFocusReturn(getActive: () => Focusable | null) {
  let saved: Focusable | null = null
  let armed = false
  return {
    save() {
      if (armed) return
      armed = true
      saved = getActive()
    },
    restore() {
      const el = saved
      saved = null
      armed = false
      if (el && el.isConnected !== false && typeof el.focus === 'function') el.focus()
    },
  }
}
