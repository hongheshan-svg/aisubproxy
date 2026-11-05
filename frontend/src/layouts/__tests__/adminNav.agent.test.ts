// 代理商相关菜单：用真实路由表生成侧栏，检查分组和不进侧栏的详情页。
import { describe, expect, it, vi } from 'vitest'

vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = v },
    removeItem: (k: string) => { delete data[k] },
  }
})

import { routes } from '../../router/routes'
import { buildNavItems, groupNavItems, pageTitle } from '../../lib/adminNav'

describe('侧栏：代理商分组', () => {
  const items = buildNavItems(routes)
  it('含「代理商」「批发商品与价格」两项', () => {
    const g = groupNavItems(items).find((x) => x.name === '代理商')
    expect(g?.items.map((i) => [i.path, i.title])).toEqual([
      ['/ops/agents', '代理商'],
      ['/ops/agent-pricing', '批发商品与价格'],
    ])
  })
  it('详情页有自己的标题，但不进侧栏', () => {
    expect(items.some((i) => i.path.includes(':id'))).toBe(false)
    expect(items.some((i) => i.title === '代理商详情')).toBe(false)
    const root = routes.find((r) => r.path === '/ops')!
    const d = (root.children || []).find((c) => c.path === 'agents/:id')
    expect(d).toBeTruthy()
    const m = d!.meta as any
    expect(m?.title).toBe('代理商详情')
    expect(m?.group).toBe('代理商')
    expect(m?.hideInNav).toBe(true)
    expect(pageTitle(m, 'Recharge Portal')).toBe('代理商详情 · Recharge Portal')
  })
})
