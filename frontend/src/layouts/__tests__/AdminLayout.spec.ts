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
import { SKINS, siteBrand } from '../../theme'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

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
  it('按 route.meta 渲染 11 个菜单项、3 个分组标题，且不再有顶部胶囊导航', async () => {
    const html = await render('/ops/cardpool')
    expect((html.match(/class="[^"]*\bsn-item\b/g) || []).length).toBe(11)
    expect((html.match(/class="[^"]*\bsn-group\b/g) || []).length).toBe(3)
    for (const g of ['卡密与付款', 'MarsWallex', '系统']) expect(html).toContain(g)
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
    expect(html).not.toContain('ZovoCard')
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

const shellSrc = readFileSync(fileURLToPath(new URL('../../components/shell/SideNavShell.vue', import.meta.url)), 'utf8')

describe('侧栏外壳 SideNavShell', () => {
  it('选中项带 active 类，侧栏用侧栏变量', async () => {
    const html = await render('/ops/cardpool')
    expect((html.match(/class="[^"]*\bsn-item\b[^"]*\bactive\b/g) || []).length).toBe(1)
    expect(shellSrc).toContain('var(--side-bg)')
    expect(shellSrc).toContain('var(--side-ink)')
    expect(shellSrc).toContain('var(--side-muted)')
    expect(shellSrc).toContain('var(--side-active)')
    expect(shellSrc).toContain('var(--side-active-on)')
    expect(shellSrc).toContain('inset 3px 0 0 #FFFFFF')
    expect(shellSrc).toContain('font-weight: 700')
  })

  it('有 Logo：白底小块里显示图片，带加载失败处理', async () => {
    siteBrand.value = { ...siteBrand.value, logoVersion: 'abc' }
    try {
      const html = await render('/ops/cardpool')
      expect(html).toContain('sn-logo-tile')
      expect(html).toContain('/api/v1/public/site/logo?v=abc')
      expect(shellSrc).toMatch(/<img[^>]*@error/)
    } finally {
      siteBrand.value = { ...siteBrand.value, logoVersion: undefined }
    }
  })

  it('没 Logo：不显示菱形图标，只显示站名', async () => {
    siteBrand.value = { ...siteBrand.value, logoVersion: undefined }
    const html = await render('/ops/cardpool')
    expect(html).not.toContain('sn-logo-tile')
    expect(html).not.toContain('class="sn-logo"')
    expect(html).toContain('sn-brand-name')
  })

  it('侧栏底部主题按钮压住皮肤给 .btn-ghost 加的背景和边框', () => {
    const src = readFileSync(fileURLToPath(new URL('../AdminLayout.vue', import.meta.url)), 'utf8')
    for (const sel of ['.sn-tools :deep(.btn-ghost)', '.sn-tools :deep(.btn-ghost:hover)']) {
      const m = src.match(new RegExp(sel.replace(/[()]/g, '\\$&') + '\\s*\\{([^}]*)\\}'))
      expect(m, sel).toBeTruthy()
      if (sel.endsWith('hover)')) expect(m![1]).toContain('border-color: transparent')
      else {
        expect(m![1]).toContain('background: transparent')
        expect(m![1]).toContain('border-color: transparent')
      }
    }
  })

  it('侧栏分隔线只由侧栏变量推出（不回落到浅色的 --brd）', () => {
    expect(shellSrc).not.toContain('--side-brd')
    // 仅顶栏下边线（规格指定）可以用 --brd
    expect((shellSrc.match(/var\(--brd\)/g) || []).length).toBe(1)
    expect(shellSrc).toMatch(/\.topbar \{[^}]*var\(--brd\)/)
  })

  it('分组名为空串时不渲染分组标题', () => {
    expect(shellSrc).toMatch(/v-if="g\.name && !railMode"/)
  })
})

describe('菜单数据与折叠状态', () => {
  beforeEach(() => {
    for (const k of Object.keys(store)) delete store[k]
  })

  it('分组保持路由定义顺序', () => {
    const gs = groupNavItems(buildNavItems(routes as any))
    expect(gs.map((g) => g.name)).toEqual(['', '卡密与付款', 'MarsWallex', '系统'])
    expect(gs[0].items[0].path).toBe('/ops')
    expect(gs[2].items.map((i) => i.title)).toEqual(['接入配置', '钱包', '卡池', '选卡规则'])
    expect(gs[2].items.map((i) => i.fullTitle)).toEqual(['MarsWallex 接入配置', 'MarsWallex 钱包', 'MarsWallex 卡池', 'MarsWallex 选卡规则'])
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

  it('readNavPref/writeNavPref 可以换键，不传键时仍用 ops-sidenav', () => {
    writeNavPref('collapsed', 'agent-sidenav')
    expect(localStorage.getItem('agent-sidenav')).toBe('collapsed')
    expect(localStorage.getItem(NAV_STORE_KEY)).toBeNull()
    expect(readNavPref('agent-sidenav')).toBe('collapsed')
    expect(readNavPref()).toBe('')
    writeNavPref('expanded')
    expect(localStorage.getItem('ops-sidenav')).toBe('expanded')
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

  it('账号菜单下拉用 .sn-user-menu 限定整宽，不泄漏到其他后台下拉', () => {
    const src = readFileSync(fileURLToPath(new URL('../AdminLayout.vue', import.meta.url)), 'utf8')
    expect(src).not.toContain(':deep(.el-dropdown)')
    expect(src).toContain('class="sn-user-menu"')
    expect(src).toMatch(/\.sn-user-menu \{ width: 100%; \}/)
  })
})
