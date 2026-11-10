import { beforeEach, describe, expect, it } from 'vitest'
import { nextTick, ref } from 'vue'
import {
  checkLink, checkTitle, normalizeRegistry, pickIssueProvider, providerCodeList, providerLabel, registry, setRegistry, supportsPlan, unsupportedPlans,
} from '../provider'
import { useIssueProvider } from '../useIssueProvider'
import { registryFixture } from './registryFixture'

const reg = () => normalizeRegistry(registryFixture())

beforeEach(() => { registry.value = null })

describe('服务商注册表', () => {
  it('显示名取自注册表，取不到显示 name 本身', () => {
    setRegistry(registryFixture())
    expect(providerLabel('testcard')).toBe('TestCard')
    expect(providerLabel('newone')).toBe('newone')
    expect(providerLabel('')).toBe('—')
  })
  it('码列表方式取自 code_list，未知或空为没有列表', () => {
    const r = reg()
    expect(providerCodeList(r, 'acme')).toBe('')
    expect(providerCodeList({ ...r, providers: [{ ...r.providers[0], code_list: 'cardplatform' }] }, 'acme')).toBe('')
    expect(providerCodeList(r, 'testcard')).toBe('local')
    expect(providerCodeList(r, 'nobody')).toBe('')
  })
  it('支持判断：产品不含则不支持；能力矩阵缺行时不在前端拦', () => {
    const r = reg()
    expect(supportsPlan(r, 'marswallex', 'x', 'premium')).toBe(false)
    expect(supportsPlan(r, 'marswallex', 'gpt', 'plus')).toBe(true)
    expect(supportsPlan(r, 'marswallex', 'gpt', 'credit250')).toBe(false)
    expect(supportsPlan(r, 'marswallex', 'gpt', 'unknown_plan')).toBe(true)
  })
  it('列出某家在某产品下不支持的档位', () => {
    expect(unsupportedPlans(reg(), 'marswallex', 'gpt')).toEqual([{ plan: 'credit*', comment: 'Codex 点数' }])
    expect(unsupportedPlans(reg(), 'acme', 'gpt')).toEqual([])
  })
})

describe('发码预选', () => {
  it('产品配置优先，没有配置用全局默认', () => {
    const r = reg()
    expect(pickIssueProvider(r, 'gpt', 'plus').provider).toBe('marswallex')
    expect(pickIssueProvider(r, 'x', 'premium').provider).toBe('acme')
  })
  it('预选那家不支持该档位：改选第一家支持的并给出提示', () => {
    const p = pickIssueProvider(reg(), 'gpt', 'credit250', (k) => (k === 'credit250' ? 'Codex 点数' : k))
    expect(p.provider).toBe('acme')
    expect(p.notice).toBe('ChatGPT 配置为用 MarsWallex，它暂不支持 Codex 点数，已改用 Acme')
  })
  it('没有产品配置而是全局默认不支持时，提示写「默认用」', () => {
    const r = reg()
    r.by_product = {}
    r.default = 'testcard'
    const p = pickIssueProvider(r, 'gpt', 'credit250')
    expect(p.provider).toBe('acme')
    expect(p.notice).toContain('默认用 TestCard')
  })
  it('能力矩阵只有 credit* 通配行：credit250 在 MarsWallex 不支持，改选 Acme 并提示', () => {
    const r = reg()
    r.capabilities = [{ product: 'gpt', plan: 'credit*', comment: 'Codex 点数', support: { acme: true, marswallex: false, testcard: false } }]
    const p = pickIssueProvider(r, 'gpt', 'credit250')
    expect(p.provider).toBe('acme')
    expect(p.notice).toBe('ChatGPT 配置为用 MarsWallex，它暂不支持 credit250，已改用 Acme')
  })
  it('通配行：* 匹配任意档位，精确行优先于通配行', () => {
    const r = reg()
    r.capabilities = [
      { product: 'gpt', plan: '*', support: { acme: true, marswallex: false } },
      { product: 'gpt', plan: 'plus', support: { acme: true, marswallex: true } },
      { product: 'gpt', plan: 'credit*', support: { acme: false, marswallex: true } },
    ]
    expect(supportsPlan(r, 'marswallex', 'gpt', 'plus')).toBe(true)
    expect(supportsPlan(r, 'marswallex', 'gpt', 'pro')).toBe(false)
    expect(supportsPlan(r, 'marswallex', 'gpt', 'credit500')).toBe(true)
    expect(supportsPlan(r, 'acme', 'gpt', 'credit500')).toBe(false)
  })
  it('预选那家不做这个产品：提示「它不做 X Premium」，不提档位', () => {
    const r = reg()
    r.by_product = {}
    r.default = 'marswallex'
    const p = pickIssueProvider(r, 'x', 'plus')
    expect(p.provider).toBe('acme')
    expect(p.notice).toBe('X Premium 默认用 MarsWallex，它不做 X Premium，已改用 Acme')
  })
  it('没有任何一家支持时保留预选，让服务端拒绝', () => {
    const r = reg()
    r.capabilities = [{ product: 'gpt', plan: 'x', support: { acme: false, marswallex: false, testcard: false } }]
    expect(pickIssueProvider(r, 'gpt', 'x')).toEqual({ provider: 'marswallex', notice: '' })
  })
  it('useIssueProvider：预选、产品变化重新预选、手动换到不支持的档位自动改选', async () => {
    setRegistry(registryFixture())
    const product = ref('gpt')
    const plan = ref('plus')
    const u = useIssueProvider(product, plan, (k) => (k === 'credit250' ? 'Codex 点数' : k))
    u.apply()
    expect(u.provider.value).toBe('marswallex')
    expect(u.notice.value).toBe('')

    plan.value = 'credit250'
    await nextTick()
    expect(u.provider.value).toBe('acme')
    expect(u.notice.value).toContain('已改用 Acme')

    // 当前服务商支持新档位时不打断人工选择
    plan.value = 'plus'
    await nextTick()
    expect(u.provider.value).toBe('acme')

    product.value = 'x'
    plan.value = 'premium'
    u.apply()
    expect(u.provider.value).toBe('acme')
    expect(u.notice.value).toBe('')
  })
})

describe('检查项标题', () => {
  it('有 title 用 title，没有退回按 key 查表，再没有显示 key', () => {
    expect(checkTitle('breaker', '付款熔断（接口）')).toBe('付款熔断（接口）')
    expect(checkTitle('breaker')).toBe('付款熔断')
    expect(checkTitle('breaker', '  ')).toBe('付款熔断')
    expect(checkTitle('zz_custom_check')).toBe('zz_custom_check')
  })
})

describe('切换产品的立即预选', () => {
  it('档位残留时切到别家才有的产品：立即改选支持该产品的服务商，不依赖套餐接口返回', () => {
    const r = registryFixture()
    r.by_product = {}
    r.default = 'marswallex'
    setRegistry(r)
    const product = ref('gpt')
    const plan = ref('plus')
    const u = useIssueProvider(product, plan)
    u.apply()
    expect(u.provider.value).toBe('marswallex')
    product.value = 'x'
    u.apply()
    expect(u.provider.value).toBe('acme')
    expect(u.notice.value).toBe('X Premium 默认用 MarsWallex，它不做 X Premium，已改用 Acme')
  })
})

describe('检查项跳转链接', () => {
  const bad = { key: 'config', ok: false, detail: 'x' }
  it('后端给了 link 就用它', () => {
    expect(checkLink('testcard', { ...bad, link: { to: '/ops/x', label: '去 X' } })).toEqual({ to: '/ops/x', label: '去 X' })
  })
  it('没有 link：只有 marswallex 退回按 key 查表，别的服务商不显示', () => {
    expect(checkLink('marswallex', bad)?.to).toBe('/ops/marswallex')
    expect(checkLink('testcard', bad)).toBeUndefined()
    expect(checkLink('acme', bad)).toBeUndefined()
  })
  it('通过的检查项不显示链接', () => {
    expect(checkLink('marswallex', { ...bad, ok: true })).toBeUndefined()
  })
})
