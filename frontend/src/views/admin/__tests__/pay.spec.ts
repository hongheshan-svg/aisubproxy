// 自动付款相关组件：发起付款的服务商预选、卡服务商页的「去自动付款做真实付款」链接、点数档配置、关闭续费面板。
// 本机没有 DOM 环境，沿用 providers.spec 的做法：用 vue/server-renderer 渲染真实组件并断言 HTML。
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
import { planToForm, type PlanConf } from '../../../lib/pay'
import { registry, setRegistry } from '../../../lib/provider'
import MarsWallexPay from '../MarsWallexPay.vue'
import LocalCdkPanel from '../components/LocalCdkPanel.vue'
import PayCreditPlans from '../components/PayCreditPlans.vue'
import PaySubscriptionPlans from '../components/PaySubscriptionPlans.vue'
import PayRenewalPanel from '../components/PayRenewalPanel.vue'
import ProviderCard from '../components/ProviderCard.vue'

async function render(comp: any, props: Record<string, unknown> = {}, url = '/', stubTable = false) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { template: '<div />' } }] })
  router.push(url)
  await router.isReady()
  const app = createSSRApp(defineComponent({ render: () => h(comp, props) }))
  app.use(router)
  installElement(app)
  // SSR 下 el-table 不渲染表头；要看列标题时，把表格换成直接输出各列 label 的替身
  if (stubTable) {
    // 先删掉 installElement 注册的真组件，再注册替身，避免重复注册警告（不能用 warnHandler 吞警告）
    delete (app._context.components as any).ElTable
    delete (app._context.components as any).ElTableColumn
    app.component('ElTable', { render() { return h('div', (this as any).$slots.default?.()) } })
    app.component('ElTableColumn', { props: ['label'], render() { return h('span', { class: 'col' }, (this as any).label) } })
  }
  app.provide(ID_INJECTION_KEY, { prefix: 1024, current: 0 })
  app.provide(ZINDEX_INJECTION_KEY, { current: 0 })
  return renderToString(app)
}

// 取出替身表格输出的列标题
const colLabels = (html: string) => [...html.matchAll(/class="col"[^>]*>([^<]*)</g)].map((m) => m[1])

beforeEach(() => {
  registry.value = null
  for (const k of Object.keys(responses)) delete responses[k]
  responses['/providers'] = registryFixture()
  responses['/providers/readiness'] = readinessFixture()
})

describe('「去自动付款做真实付款」带上服务商', () => {
  it('卡服务商卡片的链接带 ?provider=', async () => {
    setRegistry(registryFixture())
    const rd = readinessFixture().providers.marswallex
    const html = await render(ProviderCard, { info: registry.value!.providers[1], readiness: rd })
    expect(html).toContain('data-testid="first-pay-link"')
    expect(html).toContain('href="/ops/marswallex-pay?provider=marswallex"')
  })

  it('本地码面板的链接带 ?provider=，跟着面板的服务商走', async () => {
    setRegistry(registryFixture())
    const fx = readinessFixture()
    responses['/providers/readiness'] = {
      ...fx,
      providers: {
        ...fx.providers,
        testcard: { provider: 'testcard', ok: false, problems: ['还没有真实付款'], checks: [{ key: 'first_real_payment', ok: false, detail: '还没有' }], combos: [] },
      },
    }
    const html = await render(LocalCdkPanel, { provider: 'testcard' })
    expect(html).toContain('data-testid="first-pay-link"')
    expect(html).toContain('href="/ops/marswallex-pay?provider=testcard"')
  })
})

describe('发起付款的服务商选择', () => {
  const selected = (html: string) => /data-testid="pay-provider" data-selected="([^"]*)"/.exec(html)?.[1]

  it('读取 ?provider= 预选供卡型服务商', async () => {
    setRegistry(registryFixture())
    const html = await render(MarsWallexPay, {}, '/ops/marswallex-pay?provider=testcard')
    expect(selected(html)).toBe('testcard')
    expect(html).not.toContain('data-testid="pay-provider-notice"')
  })

  it('没有参数默认 MarsWallex', async () => {
    setRegistry(registryFixture())
    const html = await render(MarsWallexPay, {}, '/ops/marswallex-pay')
    expect(selected(html)).toBe('marswallex')
  })

  it('参数是上游型服务商：不预选，改选 MarsWallex 并提示', async () => {
    setRegistry(registryFixture())
    const html = await render(MarsWallexPay, {}, '/ops/marswallex-pay?provider=acme')
    expect(selected(html)).toBe('marswallex')
    expect(html).toContain('data-testid="pay-provider-notice"')
    expect(html).toContain('不是供卡型服务商')
  })
})

describe('付款配置：Codex 点数档', () => {
  const REGIONS = ['PH', 'US', 'JP', 'CL', 'EG']
  const credit = (key: string, q: number): PlanConf => ({
    key, label: `Codex 点数 ${q}`, chatgpt_plan_name: '', expect_plan_type: '', kind: 'credit', credit_quantity: q,
    regions: { PH: { enabled: false, currency: 'PHP', expect_minor: 56500, cap_minor: 63280, fund_cents: 0 } },
  })
  const plus: PlanConf = { key: 'plus', label: 'Plus', chatgpt_plan_name: 'chatgptplusplan', expect_plan_type: 'plus', kind: 'subscription', regions: {} }

  const builtin = {
    plus: { US: { currency: 'USD', expect_minor: 2000, floor_minor: 2000, cap_minor: 2200 }, PH: { currency: 'PHP', expect_minor: 99900, floor_minor: 87412, cap_minor: 112200 } },
    credit250: { US: { currency: 'USD', expect_minor: 1000, floor_minor: 1000, cap_minor: 1100 }, PH: { currency: 'PHP', expect_minor: 56500, floor_minor: 49437, cap_minor: 63280 } },
    credit500: { US: { currency: 'USD', expect_minor: 2000, floor_minor: 2000, cap_minor: 2200 }, PH: { currency: 'PHP', expect_minor: 113000, floor_minor: 98875, cap_minor: 126560 } },
  }

  it('只渲染点数档，有内置价的 US、PH 行各有一个恢复内置价格按钮，并写明内置说明', async () => {
    const plans = [plus, credit('credit250', 250), credit('credit500', 500)].map((p) => planToForm(p, REGIONS))
    const html = await render(PayCreditPlans, { modelValue: plans, builtin })
    expect(html).toContain('data-testid="credit-plans"')
    expect(html).toContain('data-plan="credit250"')
    expect(html).toContain('data-plan="credit500"')
    expect(html).not.toContain('data-plan="plus"')
    expect(html).toContain('data-testid="builtin-credit250-PH"')
    expect(html).toContain('data-testid="builtin-credit250-US"')
    expect(html).not.toContain('data-testid="builtin-credit250-JP"')
    expect(html).toContain('恢复内置价格')
    expect(html).toContain('美国、菲律宾的预期金额、最低金额、上限已按官方价格内置（可改）；其他地区要自己填。')
    expect(html).not.toContain('ph-default-')
  })

  it('订阅表有恢复内置价格入口和内置说明；接口没给 builtin 时没有入口', async () => {
    const forms = [planToForm(plus, REGIONS), planToForm(credit('credit250', 250), REGIONS)]
    const html = await render(PaySubscriptionPlans, { modelValue: forms, builtin })
    expect(html).toContain('data-testid="builtin-plus-PH"')
    expect(html).toContain('data-testid="builtin-plus-US"')
    expect(html).not.toContain('data-testid="builtin-plus-JP"')
    expect(html).toContain('恢复内置价格')
    expect(html).toContain('美国、菲律宾的预期金额、最低金额、上限已按官方价格内置（可改）；其他地区要自己填。')
    const none = await render(PaySubscriptionPlans, { modelValue: forms })
    expect(none).not.toContain('恢复内置价格')
  })

  it('点数表格有最低金额列', async () => {
    const html = await render(PayCreditPlans, { modelValue: [planToForm(credit('credit250', 250), REGIONS)] }, '/', true)
    expect(colLabels(html)).toEqual(expect.arrayContaining(['预期金额', '最低金额', '应付金额上限']))
  })

  it('订阅套餐表格有预期金额和最低金额列', async () => {
    const html = await render(PaySubscriptionPlans, { modelValue: [planToForm(plus, REGIONS), planToForm(credit('credit250', 250), REGIONS)] }, '/', true)
    expect(colLabels(html)).toEqual(expect.arrayContaining(['预期金额', '最低金额', '应付金额上限']))
    expect(html).not.toContain('credit250')
  })

  it('订阅表有「允许 0 元开通」列和提示，点数表没有', async () => {
    const html = await render(PaySubscriptionPlans, { modelValue: [planToForm(plus, REGIONS)] }, '/', true)
    expect(colLabels(html)).toContain('允许 0 元开通')
    expect(html).toContain('结账页显示 0 元（首月免费等优惠）时照样开通；不勾则放弃这一单，不注资。')
    const creditHtml = await render(PayCreditPlans, { modelValue: [planToForm(credit('credit250', 250), REGIONS)] }, '/', true)
    expect(creditHtml).not.toContain('允许 0 元开通')
  })

  it('没有点数档时不渲染', async () => {
    const html = await render(PayCreditPlans, { modelValue: [planToForm(plus, REGIONS)] })
    expect(html).not.toContain('data-testid="credit-plans"')
  })
})

describe('订单详情：关闭续费', () => {
  const base = {
    order_no: 'PO1', state: 'succeeded', dry_run: false, kind: 'subscription',
    renewal_state: 'failed', renewal_attempts: 3, renewal_checked_at: null, renewal_next_at: null, has_session: true,
  }
  // 只认 disabled 属性本身（el-button 还会输出 aria-disabled="false"）
  const retryDisabled = (html: string) => /\sdisabled(=""|\s|>)/.test(/<button[^>]*data-testid="renewal-retry"[^>]*>/.exec(html)?.[0] || '')

  it('关闭失败且会话还在：显示失败、提醒找用户、可以重试', async () => {
    const html = await render(PayRenewalPanel, { order: base })
    expect(html).toContain('关闭续费失败')
    expect(html).toContain('data-testid="renewal-manual"')
    expect(html).toContain('续费未关闭')
    expect(html).toContain('请让用户自行在 ChatGPT 设置里关闭续费')
    expect(retryDisabled(html)).toBe(false)
  })

  it('会话已清空：按钮禁用并写明原因', async () => {
    const html = await render(PayRenewalPanel, { order: { ...base, has_session: false } })
    expect(retryDisabled(html)).toBe(true)
    expect(html).toContain('会话已清空')
  })

  it('已关闭：显示已关闭，按钮禁用，不提醒', async () => {
    const html = await render(PayRenewalPanel, { order: { ...base, renewal_state: 'cancelled' } })
    expect(html).toContain('已关闭续费')
    expect(retryDisabled(html)).toBe(true)
    expect(html).not.toContain('data-testid="renewal-manual"')
  })

  it('点数单和试运行单不显示', async () => {
    expect(await render(PayRenewalPanel, { order: { ...base, kind: 'credit', renewal_state: '' } })).not.toContain('data-testid="renewal-panel"')
    expect(await render(PayRenewalPanel, { order: { ...base, dry_run: true, renewal_state: '' } })).not.toContain('data-testid="renewal-panel"')
  })
})
