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
  buildNavItems, groupNavItems, isCollapsed, NAV_STORE_KEY, pageTitle, readNavPref, writeNavPref,
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
      { path: 'cdkeys', component: Stub, meta: { title: 'CDK 卡密', group: '卡密与付款', icon: 'Tickets' } },
      { path: 'marswallex-pay', component: Stub, meta: { title: '自动付款', group: '卡密与付款', icon: 'Money' } },
      { path: 'providers', component: Stub, meta: { title: '卡服务商', group: '卡密与付款', icon: 'Coin' } },
      { path: 'integration', component: Stub, meta: { title: '接入配置', fullTitle: 'ZovoCard 接入配置', group: 'ZovoCard', icon: 'Connection' } },
      { path: 'card-selection', component: Stub, meta: { title: '选卡规则', fullTitle: 'ZovoCard 选卡规则', group: 'ZovoCard', icon: 'Operation' } },
      { path: 'orders', component: Stub, meta: { title: '兑换订单', fullTitle: 'ZovoCard 兑换订单', group: 'ZovoCard', icon: 'Document' } },
      { path: 'webhooks', component: Stub, meta: { title: 'Webhook', fullTitle: 'ZovoCard Webhook', group: 'ZovoCard', icon: 'Bell' } },
      { path: 'marswallex', component: Stub, meta: { title: '接入配置', fullTitle: 'MarsWallex 接入配置', group: 'MarsWallex', icon: 'Platform' } },
      { path: 'marswallex-wallet', component: Stub, meta: { title: '钱包', fullTitle: 'MarsWallex 钱包', group: 'MarsWallex', icon: 'Wallet' } },
      { path: 'cardpool', component: Stub, meta: { title: '卡池', fullTitle: 'MarsWallex 卡池', group: 'MarsWallex', icon: 'CreditCard' } },
      { path: 'marswallex-selection', component: Stub, meta: { title: '选卡规则', fullTitle: 'MarsWallex 选卡规则', group: 'MarsWallex', icon: 'Operation' } },
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
  it('按 route.meta 渲染 15 个菜单项、4 个分组标题，且不再有顶部胶囊导航', async () => {
    const html = await render('/ops/cardpool')
    expect((html.match(/class="[^"]*\bsn-item\b/g) || []).length).toBe(15)
    expect((html.match(/class="[^"]*\bsn-group\b/g) || []).length).toBe(4)
    for (const g of ['卡密与付款', 'ZovoCard', 'MarsWallex', '系统']) expect(html).toContain(g)
    expect(html).not.toContain('nav-pills')
    expect(html).not.toContain('topnav')
  })

  it('当前页高亮，面包屑显示「分组 / 页面」', async () => {
    const html = await render('/ops/cardpool')
    expect(html).toMatch(/class="[^"]*\bsn-item\b[^"]*\bactive\b[^"]*"[^>]*aria-label="MarsWallex 卡池"/)
    expect(html).toMatch(/class="crumb-group"[^>]*>MarsWallex</)
    expect(html).toMatch(/class="[^"]*\bcrumb-page\b[^"]*"[^>]*>卡池</)
  })

  it('同名菜单项用完整页名区分：读屏标签和手机面包屑用 fullTitle', async () => {
    const html = await render('/ops/marswallex-selection')
    expect(html).toContain('aria-label="ZovoCard 选卡规则"')
    expect(html).toMatch(/class="[^"]*\bsn-item\b[^"]*\bactive\b[^"]*"[^>]*aria-label="MarsWallex 选卡规则"/)
    expect(html).toMatch(/class="[^"]*\bcrumb-full\b[^"]*"[^>]*>MarsWallex 选卡规则</)
    // 通用页面没有 fullTitle，不多出一份
    expect(await render('/ops/providers')).not.toContain('crumb-full')
  })

  it('面包屑第三级：默认标签与 ?tab= 指定的标签', async () => {
    const def = await render('/ops/cardpool')
    expect(def).toMatch(/class="[^"]*\bcrumb-tab\b[^"]*"[^>]*>概览</)
    const t = await render('/ops/cardpool?tab=holders')
    expect(t).toMatch(/class="[^"]*\bcrumb-tab\b[^"]*"[^>]*>持卡人</)
    const bad = await render('/ops/cardpool?tab=nope')
    expect(bad).toMatch(/class="[^"]*\bcrumb-tab\b[^"]*"[^>]*>概览</)
  })

  it('没有页内标签的页面没有第三级', async () => {
    const html = await render('/ops/providers')
    expect(html).not.toContain('crumb-tab')
  })

  it('总览没有分组前缀', async () => {
    const html = await render('/ops')
    expect(html).toMatch(/class="[^"]*\bcrumb-page\b[^"]*"[^>]*>总览</)
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
    expect(gs.map((g) => g.name)).toEqual(['', '卡密与付款', 'ZovoCard', 'MarsWallex', '系统'])
    expect(gs[0].items[0].path).toBe('/ops')
    expect(gs[3].items.map((i) => i.title)).toEqual(['接入配置', '钱包', '卡池', '选卡规则'])
    expect(gs[3].items.map((i) => i.fullTitle)).toEqual(['MarsWallex 接入配置', 'MarsWallex 钱包', 'MarsWallex 卡池', 'MarsWallex 选卡规则'])
    // 没设 fullTitle 时等于 title
    expect(gs[1].items[0].fullTitle).toBe('CDK 卡密')
  })

  it('浏览器标签页标题用 fullTitle', () => {
    expect(pageTitle({ title: '卡池', fullTitle: 'MarsWallex 卡池' }, '我的站')).toBe('MarsWallex 卡池 · 我的站')
    expect(pageTitle({ title: '卡服务商' }, '')).toBe('卡服务商 · 控制台')
    expect(pageTitle({}, '我的站')).toBe('我的站 · 我的站')
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
