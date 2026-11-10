// 总览不再有卡台数字：4 格统计卡（CDK 总数、可用 CDK、已兑换、兑换订单）和 /api/v1/stats/system 请求都已去掉。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import { ID_INJECTION_KEY } from 'element-plus/es/hooks/use-id/index.mjs'
import { ZINDEX_INJECTION_KEY } from 'element-plus/es/hooks/use-z-index/index.mjs'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = v },
    removeItem: (k: string) => { delete data[k] },
  }
})

const calls: string[] = []
vi.mock('../../../lib/api', () => ({
  authFetch: async (url: string) => {
    calls.push(String(url))
    return { ok: false, status: 404, json: async () => ({}) }
  },
}))

import { installElement } from '../../../element'
import AdminDashboard from '../AdminDashboard.vue'
import { mountHeadless, settle } from './headless'

const routerOf = () => createRouter({
  history: createMemoryHistory(),
  routes: [{ path: '/ops', component: { template: '<div />' }, children: [
    { path: 'cdkeys', component: { template: '<div />' }, meta: { title: 'CDK 卡密', group: '卡密与付款' } },
  ] }, { path: '/:p(.*)*', component: { template: '<div />' } }],
})

describe('AdminDashboard', () => {
  it('没有卡台的 4 格统计卡，也没有统计失败提示', async () => {
    const router = routerOf()
    router.push('/ops')
    await router.isReady()
    const app = createSSRApp(AdminDashboard)
    app.use(router)
    installElement(app)
    app.provide(ID_INJECTION_KEY, { prefix: 1024, current: 0 })
    app.provide(ZINDEX_INJECTION_KEY, { current: 0 })
    const html = await renderToString(app)
    for (const t of ['CDK 总数', '可用 CDK', '已兑换', '兑换订单', '卡台']) expect(html).not.toContain(t)
    // 待办汇总、快捷入口、版本信息还在
    for (const t of ['待办汇总', '快捷入口', '版本']) expect(html).toContain(t)
  })

  it('挂载后不请求 /api/v1/stats/system', async () => {
    calls.length = 0
    const router = routerOf()
    router.push('/ops')
    await router.isReady()
    const unmount = mountHeadless(AdminDashboard, (app) => app.use(router))
    await settle()
    unmount()
    expect(calls.length).toBeGreaterThan(0) // 待办和版本信息的请求照常发
    expect(calls.some((u) => u.includes('/stats/system'))).toBe(false)
    expect(readFileSync(fileURLToPath(new URL('../AdminDashboard.vue', import.meta.url)), 'utf8')).not.toContain('stats/system')
  })
})
