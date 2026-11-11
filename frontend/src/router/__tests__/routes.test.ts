import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
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

describe('旧地址 /admin/... 重定向（F1-06）', () => {
  const legacy = () => routes.find((r) => r.path === '/admin/:pathMatch(.*)*')
  const go = (pathMatch: string | string[]) => {
    const redirect = legacy()?.redirect as (to: { params: Record<string, unknown> }) => string
    return redirect({ params: { pathMatch } })
  }
  it('多段地址用斜杠拼，不是逗号', () => expect(go(['agents', '5'])).toBe('/ops/agents/5'))
  it('单段地址', () => expect(go(['cdkeys'])).toBe('/ops/cdkeys'))
  it('空', () => expect(go('')).toBe('/ops/'))
  it('字符串形式也行', () => expect(go('cdkeys')).toBe('/ops/cdkeys'))
})

describe('路由守卫只在后台页面等 /setup/status（F1-03）', () => {
  const src = readFileSync(fileURLToPath(new URL('../index.ts', import.meta.url)), 'utf8')
  it('isAdminPath 的提前返回出现在 ensureSetupStatus() 调用之前', () => {
    const guard = src.slice(src.indexOf('router.beforeEach'))
    const early = guard.indexOf('if (!isAdminPath(to.path)) return')
    const call = guard.indexOf('await ensureSetupStatus()')
    expect(early).toBeGreaterThan(-1)
    expect(call).toBeGreaterThan(early)
  })
  it('请求失败不把 setupChecked 置真（下次跳转再问），也不看 JSON 前先看 r.ok', () => {
    const fn = src.slice(src.indexOf('async function ensureSetupStatus'), src.indexOf('const isLoginPath'))
    const catchBlock = fn.slice(fn.indexOf('catch'), fn.indexOf('\n  }\n', fn.indexOf('catch')))
    expect(catchBlock).not.toContain('setupChecked = true')
    expect(fn).toContain('r.ok')
  })
  it('需要管理员、安装页、登录页的路由路径都在 /ops 下', () => {
    const flat: Array<{ path: string; meta?: Record<string, unknown>; name?: unknown }> = []
    const walk = (list: typeof routes, base: string, inherited: Record<string, unknown>) => {
      for (const r of list) {
        const path = r.path.startsWith('/') ? r.path : `${base}/${r.path}`
        const meta = { ...inherited, ...(r.meta || {}) }
        flat.push({ path, meta, name: r.name })
        if (r.children) walk(r.children, path, meta)
      }
    }
    walk(routes, '', {})
    const guarded = flat.filter((r) => r.meta?.requiresAdmin || r.meta?.setupPage || r.name === 'Login')
    expect(guarded.length).toBeGreaterThan(5)
    for (const r of guarded) expect(isAdminPath(r.path), r.path).toBe(true)
  })
})
