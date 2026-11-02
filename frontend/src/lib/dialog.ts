// 全局自定义弹窗 + Toast，替代浏览器原生 alert/confirm/prompt。
import { reactive } from 'vue'
import { mapApiError } from './errors'

export interface SelectOption { label: string; value: any; desc?: string }
interface ActiveDialog {
  kind: 'alert' | 'confirm' | 'prompt' | 'select'
  title?: string
  message?: string
  okText?: string
  cancelText?: string
  danger?: boolean
  value?: string
  placeholder?: string
  options?: SelectOption[]
  resolve: (v: any) => void
}
interface Toast { id: number; message: string; type: 'ok' | 'err' | 'warn' | 'info'; detail?: string }

export const dialogState = reactive<{ active: ActiveDialog | null; toasts: Toast[] }>({ active: null, toasts: [] })

let toastSeq = 0
function open(opts: Omit<ActiveDialog, 'resolve'>): Promise<any> {
  return new Promise((resolve) => { dialogState.active = { ...opts, resolve } as ActiveDialog })
}

export const dialog = {
  alert(message: string, { title = '提示', okText = '确定' } = {}) {
    return open({ kind: 'alert', title, message, okText })
  },
  confirm(message: string, { title = '确认', okText = '确定', cancelText = '取消', danger = false } = {}) {
    return open({ kind: 'confirm', title, message, okText, cancelText, danger })
  },
  prompt(message: string, { title = '输入', defaultValue = '', placeholder = '', okText = '确定', cancelText = '取消' } = {}) {
    return open({ kind: 'prompt', title, message, value: defaultValue, placeholder, okText, cancelText })
  },
  select(message: string, options: SelectOption[], { title = '选择', cancelText = '取消' } = {}) {
    return open({ kind: 'select', title, message, options, cancelText })
  },
  toast(message: string, type: 'ok' | 'err' | 'warn' | 'info' = 'ok', timeout = 2800) {
    // 错误类提示：已知错误码翻译成人话，原文留在 detail 里排障；同一条提示不重复弹
    let detail: string | undefined
    if (type === 'err' || type === 'warn') {
      const m = mapApiError(message)
      if (m.mapped) {
        message = m.text
        detail = m.raw
      }
    }
    if (dialogState.toasts.some((t) => t.message === message && t.type === type)) return
    const id = ++toastSeq
    dialogState.toasts.push({ id, message, type, detail })
    setTimeout(() => {
      const i = dialogState.toasts.findIndex((t) => t.id === id)
      if (i >= 0) dialogState.toasts.splice(i, 1)
    }, timeout)
  },
}

export function resolveActive(value: any) {
  const a = dialogState.active
  dialogState.active = null
  if (a) a.resolve(value)
}
