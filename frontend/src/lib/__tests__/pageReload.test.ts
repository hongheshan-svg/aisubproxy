import { describe, expect, it, vi } from 'vitest'
import { hardNavigate } from '../pageReload'

function loc(href: string) {
  return { href, reload: vi.fn(), assign: vi.fn(), replace: vi.fn() }
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
  it('只差 hash：先 replace 换掉 hash（不多压历史），再 reload', () => {
    const l = loc('http://h/')
    hardNavigate('/#help', l)
    expect(l.replace).toHaveBeenCalledWith('http://h/#help')
    expect(l.reload).toHaveBeenCalledTimes(1)
    expect(l.assign).not.toHaveBeenCalled()
  })
  it('当前有 hash、目标没有：同样 replace 后 reload', () => {
    const l = loc('http://h/a#x')
    hardNavigate('/a', l)
    expect(l.replace).toHaveBeenCalledWith('http://h/a')
    expect(l.reload).toHaveBeenCalledTimes(1)
  })
})
