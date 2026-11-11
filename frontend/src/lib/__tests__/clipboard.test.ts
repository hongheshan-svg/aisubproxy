// 复制的传统回退（审查修复，F2-09 次要）：安装向导的明文账号密码也走这里。
// 回退时临时放进页面的隐藏文本框必须在任何情况下都移除，包括 execCommand 抛异常——否则密码留在一个看不见的节点里，直到离开页面。
// 本机没有 DOM 环境，用假的 document 测。
import { afterEach, describe, expect, it, vi } from 'vitest'
import { copyToClipboard } from '../clipboard'

function fakeDom(exec: () => boolean) {
  const body: any[] = []
  const ta: any = {
    value: '',
    style: {},
    parentNode: null as any,
    setAttribute: vi.fn(),
    focus: vi.fn(),
    select: vi.fn(),
    setSelectionRange: vi.fn(),
  }
  const bodyEl = {
    appendChild: (el: any) => { body.push(el); el.parentNode = bodyEl },
    removeChild: (el: any) => { const i = body.indexOf(el); if (i >= 0) body.splice(i, 1); el.parentNode = null },
  }
  vi.stubGlobal('document', { createElement: () => ta, body: bodyEl, execCommand: vi.fn(exec) })
  vi.stubGlobal('navigator', {}) // 没有 Clipboard API（HTTP 下）
  vi.stubGlobal('window', { isSecureContext: false })
  return { body, ta }
}

afterEach(() => { vi.unstubAllGlobals() })

describe('copyToClipboard 的回退', () => {
  it('execCommand 成功：返回 true，文本框已移除', async () => {
    const { body } = fakeDom(() => true)
    expect(await copyToClipboard('admin / s3cret')).toBe(true)
    expect(body).toHaveLength(0)
  })

  it('execCommand 返回 false：返回 false，文本框已移除', async () => {
    const { body } = fakeDom(() => false)
    expect(await copyToClipboard('admin / s3cret')).toBe(false)
    expect(body).toHaveLength(0)
  })

  it('execCommand 抛异常：返回 false，带密码的文本框也要移除、内容清空', async () => {
    const { body, ta } = fakeDom(() => { throw new Error('NotAllowed') })
    expect(await copyToClipboard('admin / s3cret')).toBe(false)
    expect(body).toHaveLength(0)
    expect(ta.value).toBe('')
  })
})
