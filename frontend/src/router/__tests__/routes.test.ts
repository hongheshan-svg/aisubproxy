import { describe, expect, it } from 'vitest'
import { isAdminPath, routes } from '../routes'
import { buildNavItems, groupNavItems } from '../../lib/adminNav'

describe('isAdminPath', () => {
  it('后台路径', () => {
    expect(isAdminPath('/ops')).toBe(true)
    expect(isAdminPath('/ops/cdkeys')).toBe(true)
  })
  it('非后台路径', () => {
    for (const p of ['/', '/recharge', '/opsx']) expect(isAdminPath(p)).toBe(false)
  })
})

const opsChildren = () => (routes.find((r) => r.path === '/ops')?.children || []) as Array<{ path: string; redirect?: unknown; name?: unknown }>

describe('已删后台页面的旧地址', () => {
  it('旧的 7 个地址都一次直接重定向到「卡服务商」页，不经过别的中转', () => {
    const kids = opsChildren()
    for (const p of ['integration', 'card-selection', 'orders', 'webhooks', 'billing', 'reconcile', 'cards']) {
      const r = kids.find((c) => c.path === p)
      expect(r, p).toBeTruthy()
      expect(r?.redirect, p).toBe('/ops/providers')
    }
  })

  it('已删页面的路由名不再存在', () => {
    const names = opsChildren().map((c) => c.name)
    for (const n of ['CardIntegration', 'CardSelectionConfig', 'OrderReconcile', 'Webhooks']) expect(names).not.toContain(n)
  })
})

describe('后台菜单', () => {
  const items = () => buildNavItems(routes)

  it('没有 ZovoCard 的接入配置、选卡规则，也没有兑换订单和 Webhook 页', () => {
    const full = items().map((i) => i.fullTitle)
    for (const t of ['ZovoCard 接入配置', 'ZovoCard 选卡规则', 'ZovoCard 兑换订单', 'ZovoCard Webhook', '兑换订单', 'Webhook']) expect(full).not.toContain(t)
    expect(groupNavItems(items()).map((g) => g.name)).not.toContain('ZovoCard')
  })

  it('MarsWallex 的几项都在', () => {
    const full = items().map((i) => i.fullTitle)
    for (const t of ['MarsWallex 接入配置', 'MarsWallex 钱包', 'MarsWallex 卡池', 'MarsWallex 选卡规则', 'CDK 卡密', '自动付款', '卡服务商']) expect(full).toContain(t)
  })
})
