// CDK 卡密页只剩页头、本站卡密、代理换码三部分：渲染各标签，并确认不再请求任何卡台接口。
import { beforeEach, describe, expect, it, vi } from 'vitest'
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
    if (String(url).endsWith('/api/v1/admin/providers')) {
      return { ok: true, status: 200, json: async () => ({
        providers: [{ name: 'marswallex', display_name: 'MarsWallex', kind: 'card_supply', kind_text: '供卡·本站付款', products: ['gpt'], code_list: 'local' }],
        products: [{ key: 'gpt', label: 'ChatGPT' }],
        default: 'marswallex', by_product: {}, effective: { gpt: 'marswallex' }, warnings: [],
        capabilities: [{ product: 'gpt', plan: 'plus', comment: 'Plus', support: { marswallex: true } }],
      }) }
    }
    return { ok: true, status: 200, json: async () => ({}) }
  },
}))

// 三个子面板各用一个带标记的占位，只验证页面挂了谁
vi.mock('../components/LocalCdkPanel.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return { default: defineComponent({ name: 'LocalCdkPanel', props: ['provider', 'product'], setup: (p: any) => () => h('div', { 'data-stub': 'local-cdk-panel', 'data-provider': p.provider }) }) }
})
vi.mock('../components/AgentSwapSettings.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return { default: defineComponent({ name: 'AgentSwapSettings', setup: () => () => h('div', { 'data-stub': 'agent-swap-settings' }) }) }
})
vi.mock('../components/AgentExchangePanel.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return { default: defineComponent({ name: 'AgentExchangePanel', setup: () => () => h('div', { 'data-stub': 'agent-exchange-panel' }) }) }
})

// SSR 不跑 onMounted，预选不会发生；直接给定「已预选 marswallex」，只看页面据此挂哪个面板
vi.mock('../../../lib/useIssueProvider', async () => {
  const { ref } = await import('vue')
  return { useIssueProvider: () => ({ provider: ref('marswallex'), notice: ref(''), apply: () => {} }) }
})

import { installElement } from '../../../element'
import { registry, setRegistry } from '../../../lib/provider'
import CDKeyManagement from '../CDKeyManagement.vue'
import { mountHeadless, settle } from './headless'

function routerAt(path: string) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { template: '<div />' } }] })
  router.push(path)
  return router
}

async function render(path: string) {
  const router = routerAt(path)
  await router.isReady()
  const app = createSSRApp(CDKeyManagement)
  app.use(router)
  installElement(app)
  app.provide(ID_INJECTION_KEY, { prefix: 1024, current: 0 })
  app.provide(ZINDEX_INJECTION_KEY, { current: 0 })
  return renderToString(app)
}

beforeEach(() => {
  registry.value = null
  calls.length = 0
})

describe('CDK 卡密页', () => {
  it('卡密标签渲染本站卡密面板；没有卡台那一类的任何入口', async () => {
    // 先让注册表进缓存，SSR 不跑 onMounted
    setRegistry({
      providers: [{ name: 'marswallex', display_name: 'MarsWallex', kind: 'card_supply', products: ['gpt'], code_list: 'local' }],
      products: [{ key: 'gpt', label: 'ChatGPT' }], default: 'marswallex', by_product: {}, effective: { gpt: 'marswallex' }, warnings: [], capabilities: [],
    })
    const html = await render('/ops/cdkeys')
    expect(html).toContain('data-stub="local-cdk-panel"')
    expect(html).not.toContain('agent-swap-settings')
    expect(html).not.toContain('agent-exchange-panel')
    for (const t of ['从卡台同步完整码', '卡台', '购买并生成', '去配置 API Key', '出口 IP', 'API 已配置', '未配置 Key']) expect(html).not.toContain(t)
    expect(html).toContain('代理换码')
  })

  it('代理换码标签渲染换码设置和换码核对面板，不渲染本站卡密面板', async () => {
    const html = await render('/ops/cdkeys?tab=swaps')
    expect(html).toContain('data-stub="agent-swap-settings"')
    expect(html).toContain('data-stub="agent-exchange-panel"')
    expect(html).not.toContain('local-cdk-panel')
    expect(html).not.toContain('卡台')
  })

  it('挂载后只读服务商注册表，不请求任何卡台、出口 IP、设置接口', async () => {
    const router = routerAt('/ops/cdkeys')
    await router.isReady()
    const unmount = mountHeadless(CDKeyManagement, (app) => app.use(router))
    await settle()
    unmount()
    expect(calls).toEqual(['/api/v1/admin/providers'])
    const src = readFileSync(fileURLToPath(new URL('../CDKeyManagement.vue', import.meta.url)), 'utf8')
    for (const t of ['cardplatform', 'network/egress', '卡台', '/ops/integration']) expect(src).not.toContain(t)
  })
})
