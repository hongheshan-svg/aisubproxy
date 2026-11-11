// 代理商侧栏余额（F2-15，用户选 1）：每次切换页面重新读一次 /me，下单后切到别的菜单余额就对得上。
// 用 headless 挂载真正执行 onMounted 和路由监听；侧栏的渲染由 AgentLayout.spec 的 SSR 用例检查。
import { describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { createMemoryHistory, createRouter } from 'vue-router'
import { createPinia } from 'pinia'

vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = v },
    removeItem: (k: string) => { delete data[k] },
  }
})

const calls: string[] = []
vi.mock('../../lib/agentApi', () => ({
  agentApi: vi.fn(async (path: string) => {
    calls.push(path)
    if (path === '/me') return { ok: true, status: 200, data: { username: 'ag', balance_cents: 100 * calls.filter((p) => p === '/me').length } }
    return { ok: true, status: 200, data: { enabled: true } }
  }),
  setAgentUnauthorizedHandler: vi.fn(),
}))

import AgentLayout from '../AgentLayout.vue'
import { mountHeadless, settle } from '../../views/admin/__tests__/headless'

const Stub = { template: '<div />' }
const routes = [
  { path: '/agent/login', component: Stub },
  { path: '/agent', component: Stub, children: [
    { path: '', component: Stub }, { path: 'order', component: Stub }, { path: 'ledger', component: Stub },
  ] },
]

describe('代理商侧栏余额随切换页面刷新（F2-15）', () => {
  it('进入时读一次 /me；切到别的页面再读一次；只改查询参数不算切换页面', async () => {
    calls.length = 0
    const router = createRouter({ history: createMemoryHistory(), routes })
    router.push('/agent/order')
    await router.isReady()
    const unmount = mountHeadless(AgentLayout, (app) => { app.use(createPinia()); app.use(router) })
    await settle()
    expect(calls.filter((p) => p === '/me')).toHaveLength(1)

    await router.push('/agent/ledger')
    await settle()
    expect(calls.filter((p) => p === '/me')).toHaveLength(2)

    await router.push('/agent/ledger?page=2')
    await settle()
    expect(calls.filter((p) => p === '/me')).toHaveLength(2)

    await router.push('/agent')
    await settle()
    expect(calls.filter((p) => p === '/me')).toHaveLength(3)
    unmount()
  })

  it('离开代理商后台（退出回登录页）不再读：会话已清，/me 回 401 会触发整页跳转', async () => {
    calls.length = 0
    const router = createRouter({ history: createMemoryHistory(), routes })
    router.push('/agent/order')
    await router.isReady()
    const unmount = mountHeadless(AgentLayout, (app) => { app.use(createPinia()); app.use(router) })
    await settle()
    await router.replace('/agent/login')
    await settle()
    expect(calls.filter((p) => p === '/me')).toHaveLength(1)
    unmount()
  })

  it('/me 防乱序：先发的请求后到不覆盖新的余额', () => {
    const src = readFileSync(fileURLToPath(new URL('../AgentLayout.vue', import.meta.url)), 'utf8')
    expect(src).toMatch(/const my = \+\+meSeq[\s\S]*?await agentApi\('\/me'\)[\s\S]*?if \(my !== meSeq\) return/)
  })
})
