import { describe, expect, it, vi } from 'vitest'
import { hardNavigate } from '../pageReload'

function loc(href: string) {
  return { href, reload: vi.fn(), assign: vi.fn() }
}

describe('hardNavigate', () => {
  it('目标等于当前地址：reload', () => {
    const l = loc('http://h/ops/cdkeys?a=1')
    hardNavigate('/ops/cdkeys?a=1', l)
    expect(l.reload).toHaveBeenCalledTimes(1)
    expect(l.assign).not.toHaveBeenCalled()
  })
  it('路径不同：assign', () => {
    const l = loc('http://h/ops/cdkeys')
    hardNavigate('/ops/audit', l)
    expect(l.assign).toHaveBeenCalledWith('/ops/audit')
    expect(l.reload).not.toHaveBeenCalled()
  })
  it('后退到 /#help：地址栏已是 /#help，目标也是 /#help，走 reload', () => {
    const l = loc('http://h/#help')
    hardNavigate('/#help', l)
    expect(l.reload).toHaveBeenCalledTimes(1)
    expect(l.assign).not.toHaveBeenCalled()
  })
  it('只差 hash（当前没有 hash）：assign 不会重新加载文档，但这里不是「相同地址」，按 assign', () => {
    const l = loc('http://h/')
    hardNavigate('/#help', l)
    expect(l.assign).toHaveBeenCalledTimes(1)
  })
})
