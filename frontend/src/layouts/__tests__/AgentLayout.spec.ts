// 无 DOM 环境：用 vue/server-renderer 渲染真实的 AgentLayout，断言侧栏结构。
// SSR 不执行 onMounted，所以不会真的请求代理商接口；仍然打桩，防止以后有人改成同步调用。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { createPinia } from 'pinia'
import { renderToString } from 'vue/server-renderer'
import { ID_INJECTION_KEY } from 'element-plus/es/hooks/use-id/index.mjs'
import { ZINDEX_INJECTION_KEY } from 'element-plus/es/hooks/use-z-index/index.mjs'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// theme.ts 在模块顶层读 localStorage，必须在 import 之前就位
vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = v },
    removeItem: (k: string) => { delete data[k] },
  }
})

vi.mock('../../lib/agentApi', () => ({
  agentApi: vi.fn(async () => ({ ok: false, status: 401, data: {} })),
  setAgentUnauthorizedHandler: vi.fn(),
}))

import { installElement } from '../../element'
import AgentLayout from '../AgentLayout.vue'

const Stub = { template: '<div>page</div>' }
const routes = [
  {
    path: '/agent',
    component: AgentLayout,
    children: [
      { path: '', component: Stub },
      { path: 'order', component: Stub },
      { path: 'codes', component: Stub },
      { path: 'orders', component: Stub },
      { path: 'orders/:id', component: Stub },
      { path: 'ledger', component: Stub },
      { path: 'integration', component: Stub },
      { path: 'account', component: Stub },
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
  app.provide(ID_INJECTION_KEY, { prefix: 1024, current: 0 })
  app.provide(ZINDEX_INJECTION_KEY, { current: 0 })
  return renderToString(app)
}

const labels = ['概览', '下单', '我的码', '订单', '流水', '对接异次元', '账号']
const paths = ['/agent', '/agent/order', '/agent/codes', '/agent/orders', '/agent/ledger', '/agent/integration', '/agent/account']

describe('AgentLayout 侧栏', () => {
  it('7 个菜单链接按顺序出现，路径正确', async () => {
    const html = await render('/agent/orders')
    const links = [...html.matchAll(/<a[^>]*class="[^"]*\bsn-item\b[^"]*"[^>]*>/g)].map((m) => m[0])
    expect(links.length).toBe(7)
    links.forEach((a, i) => {
      expect(a).toContain(`href="${paths[i]}"`)
      expect(a).toContain(`aria-label="${labels[i]}"`)
    })
  })

  it('当前页（订单）带 active，其他项没有；订单详情页也算订单', async () => {
    for (const p of ['/agent/orders', '/agent/orders/5']) {
      const html = await render(p)
      const active = [...html.matchAll(/<a[^>]*class="[^"]*\bsn-item\b[^"]*\bactive\b[^"]*"[^>]*>/g)].map((m) => m[0])
      expect(active.length, p).toBe(1)
      expect(active[0]).toContain('aria-label="订单"')
    }
  })

  it('概览精确匹配：在 /agent/order 时概览不高亮', async () => {
    const html = await render('/agent/order')
    const active = [...html.matchAll(/<a[^>]*class="[^"]*\bsn-item\b[^"]*\bactive\b[^"]*"[^>]*>/g)].map((m) => m[0])
    expect(active.length).toBe(1)
    expect(active[0]).toContain('aria-label="下单"')
  })

  it('侧栏 id、退出登录、副标题；无名分组不渲染分组标题', async () => {
    const html = await render('/agent/orders')
    expect(html).toContain('id="agent-sidenav"')
    expect(html).toContain('退出登录')
    expect(html).toContain('代理商后台')
    expect(html).toContain('aria-label="代理商导航"')
    expect((html.match(/class="[^"]*\bsn-group\b/g) || []).length).toBe(0)
    expect(html).not.toContain('sn-sep')
    expect(html).not.toContain('nav-pills')
  })

  it('顶栏显示当前菜单项文字', async () => {
    const html = await render('/agent/ledger')
    expect(html).toMatch(/class="[^"]*\bagent-topbar-title\b[^"]*"[^>]*>流水</)
  })

  it('折叠偏好键是 agent-sidenav，退出前的确认逻辑还在', () => {
    const src = readFileSync(fileURLToPath(new URL('../AgentLayout.vue', import.meta.url)), 'utf8')
    expect(src).toContain('pref-key="agent-sidenav"')
    expect(src).toContain('nav-id="agent-sidenav"')
    expect(src).toContain('window.confirm')
  })

  it('侧栏底部按钮在深色侧栏上用侧栏变量，压住皮肤的背景和边框', () => {
    const src = readFileSync(fileURLToPath(new URL('../AgentLayout.vue', import.meta.url)), 'utf8')
    const m = src.match(/\.sn-tools :deep\(\.btn-ghost\)\s*\{([^}]*)\}/)
    expect(m).toBeTruthy()
    expect(m![1]).toContain('var(--side-ink)')
    expect(m![1]).toContain('background: transparent')
    expect(m![1]).toContain('border-color: transparent')
  })
})

// 无 DOM 环境下 AgentLayout 的 onMounted 不跑，拿不到余额；所以直接渲染侧栏底部的 AgentSideMe，再用源文件断言 AgentLayout 在折叠时也把它放出来
import AgentSideMe from '../../components/shell/AgentSideMe.vue'

describe('AgentLayout 侧栏折叠后仍能看到余额', () => {
  const mount = (rail: boolean) => renderToString(createSSRApp(AgentSideMe, { name: '张三', balanceCents: 123456, rail }))
  it('展开：名称 + 余额', async () => {
    const html = await mount(false)
    expect(html).toContain('张三')
    expect(html).toContain('余额 $1,234.56')
  })
  it('折叠：名称不显示，小号金额和 title 都在', async () => {
    const html = await mount(true)
    expect(html).not.toContain('张三')
    expect(html).toContain('$1,234.56')
    expect(html).toContain('title="余额 $1,234.56"')
  })
  it('AgentLayout 不再用 v-if="me && !rail" 把折叠态的余额藏掉', () => {
    const src = readFileSync(fileURLToPath(new URL('../AgentLayout.vue', import.meta.url)), 'utf8')
    expect(src).not.toContain('me && !rail')
    expect(src).toMatch(/<AgentSideMe v-if="me"[^>]*:rail="rail"/)
  })
})
