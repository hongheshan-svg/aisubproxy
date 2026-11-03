// 本机没有 DOM 环境，沿用 AdminLayout.spec 的做法：用 vue/server-renderer 渲染真实组件并断言 HTML。
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import { ID_INJECTION_KEY } from 'element-plus/es/hooks/use-id/index.mjs'
import { ZINDEX_INJECTION_KEY } from 'element-plus/es/hooks/use-z-index/index.mjs'

vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = v },
    removeItem: (k: string) => { delete data[k] },
  }
})

import { registryFixture, readinessFixture } from '../../../lib/__tests__/registryFixture'

const responses: Record<string, unknown> = {}
vi.mock('../../../lib/api', () => ({
  authFetch: async (url: string) => {
    const path = String(url).replace('/api/v1/admin', '').split('?')[0]
    if (path in responses) return { ok: true, status: 200, json: async () => responses[path] }
    return { ok: false, status: 404, json: async () => ({ error: 'nf' }) }
  },
}))

import { installElement } from '../../../element'
import { registry, setRegistry } from '../../../lib/provider'
import CardProviders from '../CardProviders.vue'
import IssueProviderBar from '../components/IssueProviderBar.vue'

async function render(comp: any, props: Record<string, unknown> = {}) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { template: '<div />' } }] })
  router.push('/')
  await router.isReady()
  const app = createSSRApp(defineComponent({ render: () => h(comp, props) }))
  app.use(router)
  installElement(app)
  app.provide(ID_INJECTION_KEY, { prefix: 1024, current: 0 })
  app.provide(ZINDEX_INJECTION_KEY, { current: 0 })
  return renderToString(app)
}

beforeEach(() => {
  registry.value = null
  responses['/providers'] = registryFixture()
  responses['/providers/readiness'] = readinessFixture()
})

describe('卡服务商页', () => {
  it('按接口渲染三家：显示名、类型标签、检查项、extras、warnings、combos，且不按名字写死', async () => {
    const html = await render(CardProviders)
    for (const n of ['zovocard', 'marswallex', 'testcard']) expect(html).toContain(`data-provider="${n}"`)
    expect((html.match(/data-testid="kind-tag"/g) || []).length).toBe(3)
    expect(html).toContain('上游发码')
    expect(html).toContain('供卡·本站付款')
    expect(html).toContain('TestCard')
    // extras 来自接口
    expect(html).toContain('卡台余额 123.45 USD')
    expect(html).toContain('测试额度 9 张')
    // 假服务商的自定义检查项也渲染（未知 key 退回 key 本身）
    expect(html).toContain('zz_custom_check')
    // warnings、combos 只出现在给出它们的那家
    expect(html).toContain('主钱包余额偏低')
    expect((html.match(/data-testid="combos"/g) || []).length).toBe(1)
    // 首笔真实付款提示由检查项数据触发
    expect((html.match(/data-testid="first-pay"/g) || []).length).toBe(1)
    expect(html).toContain('还差一笔系统自动确认成功的真实付款')
  })

  it('按产品表格：每行一个产品，选项只含支持该产品的服务商，不支持的档位用小字列出', async () => {
    const html = await render(CardProviders)
    for (const k of ['gpt', 'x', 'grok', 'claude']) expect(html).toContain(`data-product="${k}"`)
    const row = (k: string) => html.split(`data-product="${k}"`)[1].split('</tr>')[0]
    // gpt：三家都可选，已选 marswallex；生效 MarsWallex，不支持 Codex 点数
    const gpt = row('gpt')
    expect(gpt).toContain('跟随全局默认（ZovoCard）')
    expect(gpt).toMatch(/<option value="marswallex"[^>]*selected/)
    expect(gpt).toContain('value="testcard"')
    expect(gpt).toContain('不支持：')
    expect(gpt).toContain('Codex 点数')
    // x：marswallex 不在选项里
    const x = row('x')
    expect(x).toContain('value="testcard"')
    expect(x).not.toContain('value="marswallex"')
    // grok：只有 zovocard
    expect(row('grok')).not.toContain('value="testcard"')
  })

  it('全局默认单选来自接口，保存按钮在无修改时禁用', async () => {
    const html = await render(CardProviders)
    expect(html).toContain('全局默认')
    expect(html).toMatch(/data-testid="save"[^>]*disabled|disabled[^>]*data-testid="save"/)
  })
})

describe('发码服务商选择条', () => {
  const reg = () => { setRegistry(registryFixture()); return registry.value }
  it('选项来自注册表；该产品不支持的服务商置灰；显示当前默认', async () => {
    const html = await render(IssueProviderBar, {
      reg: reg(), products: registry.value!.products, provider: 'marswallex', product: 'x', notice: '',
    })
    expect(html).toContain('ZovoCard')
    expect(html).toContain('MarsWallex')
    expect(html).toContain('TestCard')
    expect(html).toContain('X Premium当前默认：ZovoCard')
    expect(html).toMatch(/is-disabled[^>]*>[\s\S]*?MarsWallex/)
    expect(html).not.toContain('provider-notice')
  })
  it('预选改选后显示提示文案', async () => {
    const html = await render(IssueProviderBar, {
      reg: reg(), products: registry.value!.products, provider: 'zovocard', product: 'gpt',
      notice: 'ChatGPT 配置为用 MarsWallex，它暂不支持 Codex 点数，已改用 ZovoCard',
    })
    expect(html).toContain('data-testid="provider-notice"')
    expect(html).toContain('已改用 ZovoCard')
    expect(html).toContain('ChatGPT当前默认：MarsWallex')
  })
})
