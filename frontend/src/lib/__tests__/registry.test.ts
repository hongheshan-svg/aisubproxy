import { beforeEach, describe, expect, it } from 'vitest'
import { nextTick, ref } from 'vue'
import {
  checkTitle, normalizeRegistry, pickIssueProvider, providerCodeList, providerLabel, registry, setRegistry, supportsPlan, unsupportedPlans,
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
    expect(providerCodeList(r, 'zovocard')).toBe('cardplatform')
    expect(providerCodeList(r, 'testcard')).toBe('local')
    expect(providerCodeList(r, 'nobody')).toBe('')
  })
  it('支持判断：产品不含则不支持；能力矩阵缺行时不在前端拦', () => {
    const r = reg()
    expect(supportsPlan(r, 'marswallex', 'x', 'premium')).toBe(false)
    expect(supportsPlan(r, 'marswallex', 'gpt', 'plus')).toBe(true)
    expect(supportsPlan(r, 'marswallex', 'gpt', 'codex_points')).toBe(false)
    expect(supportsPlan(r, 'marswallex', 'gpt', 'unknown_plan')).toBe(true)
  })
  it('列出某家在某产品下不支持的档位', () => {
    expect(unsupportedPlans(reg(), 'marswallex', 'gpt')).toEqual([{ plan: 'codex_points', comment: 'Codex 点数' }])
    expect(unsupportedPlans(reg(), 'zovocard', 'gpt')).toEqual([])
  })
})

describe('发码预选', () => {
  it('产品配置优先，没有配置用全局默认', () => {
    const r = reg()
    expect(pickIssueProvider(r, 'gpt', 'plus').provider).toBe('marswallex')
    expect(pickIssueProvider(r, 'x', 'premium').provider).toBe('zovocard')
  })
  it('预选那家不支持该档位：改选第一家支持的并给出提示', () => {
    const p = pickIssueProvider(reg(), 'gpt', 'codex_points', (k) => (k === 'codex_points' ? 'Codex 点数' : k))
    expect(p.provider).toBe('zovocard')
    expect(p.notice).toBe('ChatGPT 配置为用 MarsWallex，它暂不支持 Codex 点数，已改用 ZovoCard')
  })
  it('没有产品配置而是全局默认不支持时，提示写「默认用」', () => {
    const r = reg()
    r.by_product = {}
    r.default = 'testcard'
    const p = pickIssueProvider(r, 'gpt', 'codex_points')
    expect(p.provider).toBe('zovocard')
    expect(p.notice).toContain('默认用 TestCard')
  })
  it('没有任何一家支持时保留预选，让服务端拒绝', () => {
    const r = reg()
    r.capabilities = [{ product: 'gpt', plan: 'x', support: { zovocard: false, marswallex: false, testcard: false } }]
    expect(pickIssueProvider(r, 'gpt', 'x')).toEqual({ provider: 'marswallex', notice: '' })
  })
  it('useIssueProvider：预选、产品变化重新预选、手动换到不支持的档位自动改选', async () => {
    setRegistry(registryFixture())
    const product = ref('gpt')
    const plan = ref('plus')
    const u = useIssueProvider(product, plan, (k) => (k === 'codex_points' ? 'Codex 点数' : k))
    u.apply()
    expect(u.provider.value).toBe('marswallex')
    expect(u.notice.value).toBe('')

    plan.value = 'codex_points'
    await nextTick()
    expect(u.provider.value).toBe('zovocard')
    expect(u.notice.value).toContain('已改用 ZovoCard')

    // 当前服务商支持新档位时不打断人工选择
    plan.value = 'plus'
    await nextTick()
    expect(u.provider.value).toBe('zovocard')

    product.value = 'x'
    plan.value = 'premium'
    u.apply()
    expect(u.provider.value).toBe('zovocard')
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
