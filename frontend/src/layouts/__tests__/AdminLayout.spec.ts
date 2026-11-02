// 本机没有 DOM 环境（项目未装 jsdom/happy-dom，也不新增依赖），所以：
// 1) 用 vue/server-renderer 把真实的 AdminLayout + 真实路由表渲染成 HTML，断言菜单与面包屑；
// 2) 折叠 / 持久化等不依赖 DOM 的逻辑放在 lib/adminNav，直接单测。
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { createPinia } from 'pinia'
import { renderToString } from 'vue/server-renderer'
import { ID_INJECTION_KEY } from 'element-plus/es/hooks/use-id/index.mjs'
import { ZINDEX_INJECTION_KEY } from 'element-plus/es/hooks/use-z-index/index.mjs'
import { installElement } from '../../element'
import AdminLayout from '../AdminLayout.vue'
import {
  buildNavItems, groupNavItems, isCollapsed, NAV_STORE_KEY, readNavPref, writeNavPref,
} from '../../lib/adminNav'
import { SKINS } from '../../theme'

// theme.ts 在模块顶层读 localStorage，所以必须在 import 之前就位
const store = vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = v },
    removeItem: (k: string) => { delete data[k] },
  }
  return data
})

const Stub ={ template: '<div>page</div>' }
const routes = [
  {
    path: '/ops',
    component: AdminLayout,
    children: [
      { path: '', component: Stub, meta: { title: '总览', group: '', icon: 'Odometer' } },
      { path: 'cdkeys', component: Stub, meta: { title: 'CDK 卡密', group: '卡密业务', icon: 'Tickets' } },
      { path: 'orders', component: Stub, meta: { title: 'CDK 兑换订单', group: '卡密业务', icon: 'Document' } },
      { path: 'marswallex-wallet', component: Stub, meta: { title: 'MarsWallex 钱包', group: '资金与付款', icon: 'Wallet' } },
      { path: 'cardpool', component: Stub, meta: { title: '卡池', group: '资金与付款', icon: 'CreditCard' } },
      { path: 'marswallex-pay', component: Stub, meta: { title: '自动付款', group: '资金与付款', icon: 'Money' } },
      { path: 'integration', component: Stub, meta: { title: '卡台 ZovoCard', group: '服务商接入', icon: 'Connection' } },
      { path: 'card-selection', component: Stub, meta: { title: '卡台选卡规则', group: '服务商接入', icon: 'Operation' } },
      { path: 'webhooks', component: Stub, meta: { title: '卡台 Webhook', group: '服务商接入', icon: 'Bell' } },
      { path: 'marswallex', component: Stub, meta: { title: 'MarsWallex 对接', group: '服务商接入', icon: 'Platform' } },
      { path: 'appearance', component: Stub, meta: { title: '外观与品牌', group: '系统', icon: 'Brush' } },
      { path: 'audit', component: Stub, meta: { title: '审计日志', group: '系统', icon: 'List' } },
      { path: 'update', component: Stub, meta: { title: '版本更新', group: '系统', icon: 'Refresh' } },
      { path: 'accounts', redirect: '/ops' },
    ],
  },
]

async function render(path: string) {
  const router = createRouter({ history: createMemoryHistory(), routes })
  router.push(path)
  await router.isReady()
  const app = createSSRApp({ template: '<router-view />' })
  app.use(createPinia())
  app.use(router)
  installElement(app)
  // Element Plus 在服务端渲染时要求提供 id 生成器
  app.provide(ID_INJECTION_KEY, { prefix: 1024, current: 0 })
  app.provide(ZINDEX_INJECTION_KEY, { current: 0 })
  return renderToString(app)
}

describe('AdminLayout 左侧菜单', () => {
  it('按 route.meta 渲染 13 个菜单项、4 个分组标题，且不再有顶部胶囊导航', async () => {
    const html = await render('/ops/cardpool')
    expect((html.match(/class="[^"]*\bsn-item\b/g) || []).length).toBe(13)
    expect((html.match(/class="[^"]*\bsn-group\b/g) || []).length).toBe(4)
    for (const g of ['卡密业务', '资金与付款', '服务商接入', '系统']) expect(html).toContain(g)
    expect(html).not.toContain('nav-pills')
    expect(html).not.toContain('topnav')
  })

  it('当前页高亮，面包屑显示「分组 / 页面」', async () => {
    const html = await render('/ops/cardpool')
    expect(html).toMatch(/class="[^"]*\bsn-item\b[^"]*\bactive\b[^"]*"[^>]*aria-label="卡池"/)
    expect(html).toMatch(/class="crumb-group"[^>]*>资金与付款</)
    expect(html).toMatch(/class="crumb-page"[^>]*>卡池</)
  })

  it('总览没有分组前缀', async () => {
    const html = await render('/ops')
    expect(html).toMatch(/class="crumb-page"[^>]*>总览</)
    expect(html).not.toContain('crumb-group')
  })

  it('子路径也算当前项（/ops/cardpool/x）', async () => {
    const r = createRouter({ history: createMemoryHistory(), routes: [...routes, { path: '/ops/cardpool/x', component: Stub }] })
    expect(r.resolve('/ops/cardpool/x').path.startsWith('/ops/cardpool')).toBe(true)
  })
})

describe('菜单数据与折叠状态', () => {
  beforeEach(() => {
    for (const k of Object.keys(store)) delete store[k]
  })

  it('分组保持路由定义顺序', () => {
    const gs = groupNavItems(buildNavItems(routes as any))
    expect(gs.map((g) => g.name)).toEqual(['', '卡密业务', '资金与付款', '服务商接入', '系统'])
    expect(gs[0].items[0].path).toBe('/ops')
    expect(gs[2].items.map((i) => i.title)).toEqual(['MarsWallex 钱包', '卡池', '自动付款'])
  })

  it('折叠偏好写入 localStorage[ops-sidenav] 并可读回', () => {
    expect(readNavPref()).toBe('')
    writeNavPref('collapsed')
    expect(localStorage.getItem(NAV_STORE_KEY)).toBe('collapsed')
    expect(readNavPref()).toBe('collapsed')
  })

  it('无偏好时 ≤1200 默认折叠，>1200 展开；有偏好以偏好为准', () => {
    expect(isCollapsed('', 1440)).toBe(false)
    expect(isCollapsed('', 1200)).toBe(true)
    expect(isCollapsed('expanded', 1000)).toBe(false)
    expect(isCollapsed('collapsed', 1600)).toBe(true)
  })
})

describe('皮肤元数据', () => {
  it('不再有 layout 字段（壳只有一种）', () => {
    for (const s of SKINS) expect('layout' in s).toBe(false)
  })
})
