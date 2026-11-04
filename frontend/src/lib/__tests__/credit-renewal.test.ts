// 后台自动付款页：Codex 点数档配置、关闭续费、点数单详情、发起付款选服务商相关的纯函数。
import { describe, expect, it } from 'vitest'
import {
  applyBuiltinPrice, capBelowExpect, formToPlan, isCreditPlan, payBlockReason, planToForm, type BuiltinPrices, type PlanConf,
} from '../pay'
import {
  buildPayBody, cardSavedLabel, cardSupplyProviders, evidenceNotice, orderKindLabel, payErrorText, pickPayProvider,
  renewalLabel, renewalNeedsUser, renewalRetryBlock, renewalTagType, wantsRenewalCancel,
} from '../payui'
import { normalizeRegistry } from '../provider'
import { registryFixture } from './registryFixture'

const REGIONS = ['PH', 'US', 'JP', 'CL', 'EG']

describe('发起付款带上服务商', () => {
  it('buildPayBody 传了服务商就带 provider，空串不带', () => {
    expect(buildPayBody('real', 'plus', 'PH', 's', 'testcard')).toEqual({ plan_key: 'plus', region: 'PH', session: 's', dry_run: false, provider: 'testcard' })
    expect(buildPayBody('dry_fill', 'credit250', 'PH', 's', 'marswallex')).toEqual({
      plan_key: 'credit250', region: 'PH', session: 's', dry_run: true, dry_run_fill: true, provider: 'marswallex',
    })
    expect('provider' in buildPayBody('real', 'plus', 'PH', 's', '  ')).toBe(false)
    expect('provider' in buildPayBody('real', 'plus', 'PH', 's')).toBe(false)
  })

  it('服务商下拉只列供卡型', () => {
    const reg = normalizeRegistry(registryFixture())
    expect(cardSupplyProviders(reg).map((p) => p.name)).toEqual(['marswallex', 'testcard'])
    expect(cardSupplyProviders(null)).toEqual([])
  })

  it('预选：链接带来的供卡型服务商优先，否则 MarsWallex，再否则第一家', () => {
    const list = cardSupplyProviders(normalizeRegistry(registryFixture()))
    expect(pickPayProvider(list, 'testcard')).toBe('testcard')
    expect(pickPayProvider(list, ['testcard'])).toBe('testcard')
    expect(pickPayProvider(list, undefined)).toBe('marswallex')
    expect(pickPayProvider(list, '')).toBe('marswallex')
    // 上游型、不存在的名字都不能被预选
    expect(pickPayProvider(list, 'zovocard')).toBe('marswallex')
    expect(pickPayProvider(list, 'nope')).toBe('marswallex')
    expect(pickPayProvider([{ name: 'testcard', kind: 'card_supply' }], undefined)).toBe('testcard')
    expect(pickPayProvider([], 'testcard')).toBe('')
  })
})

describe('点数档的付款配置', () => {
  const creditPlan: PlanConf = {
    key: 'credit250', label: 'Codex 点数 250', chatgpt_plan_name: '', expect_plan_type: '', kind: 'credit', credit_quantity: 250,
    regions: {
      PH: { enabled: true, currency: 'PHP', expect_minor: 56500, floor_minor: 56500, cap_minor: 63280, fund_cents: 1500 },
      US: { enabled: false, currency: 'USD', expect_minor: 0, floor_minor: 0, cap_minor: 0, fund_cents: 0 },
      JP: { enabled: false, currency: 'JPY', expect_minor: 0, floor_minor: 0, cap_minor: 0, fund_cents: 0 },
      CL: { enabled: false, currency: 'CLP', expect_minor: 0, floor_minor: 0, cap_minor: 0, fund_cents: 0 },
      EG: { enabled: false, currency: 'EGP', expect_minor: 0, floor_minor: 0, cap_minor: 0, fund_cents: 0 },
    },
  }

  it('点数档没有 ChatGPT 套餐名也能提交；真实付款仍要求启用且金额已配', () => {
    expect(isCreditPlan(creditPlan)).toBe(true)
    expect(payBlockReason(creditPlan, 'PH', true)).toBe('')
    expect(payBlockReason(creditPlan, 'PH', false)).toBe('')
    expect(payBlockReason(creditPlan, 'US', false)).toContain('未启用')
    const noFund = { ...creditPlan, regions: { ...creditPlan.regions, PH: { ...creditPlan.regions.PH, fund_cents: 0 } } }
    expect(payBlockReason(noFund, 'PH', false)).toContain('金额未配置')
    // 订阅套餐没有套餐名照旧拦下
    expect(payBlockReason({ ...creditPlan, kind: 'subscription' }, 'PH', true)).toContain('ChatGPT 套餐名')
  })

  it('表单来回转换保留预期金额、种类和点数数量，五个地区都写出', () => {
    const form = planToForm(creditPlan, REGIONS)
    expect(form.kind).toBe('credit')
    expect(form.credit_quantity).toBe(250)
    expect(form.rows.find((r) => r.region === 'PH')).toMatchObject({ currency: 'PHP', expect: 565, cap: 632.8, fund: 15 })
    const back = formToPlan(form)
    expect(back).toEqual(creditPlan)
    expect(Object.keys(back.regions)).toEqual(REGIONS)
  })

  it('订阅套餐的预期金额缺省为 0，保存时也带上 expect_minor', () => {
    const sub: PlanConf = {
      key: 'plus', label: 'Plus', chatgpt_plan_name: 'chatgptplusplan', expect_plan_type: 'plus',
      regions: { PH: { enabled: true, currency: 'PHP', cap_minor: 120000, fund_cents: 3000 } },
    }
    const back = formToPlan(planToForm(sub, REGIONS))
    expect(back.regions.PH).toEqual({ enabled: true, currency: 'PHP', expect_minor: 0, floor_minor: 0, cap_minor: 120000, fund_cents: 3000 })
    expect(back.regions.US).toEqual({ enabled: false, currency: '', expect_minor: 0, floor_minor: 0, cap_minor: 0, fund_cents: 0 })
    expect(back.kind).toBe('')
  })

  // 接口返回的 builtin（与后端 BuiltinRegionPrice 一致）
  const builtin: BuiltinPrices = {
    credit1000: {
      US: { currency: 'USD', expect_minor: 4000, floor_minor: 4000, cap_minor: 4400 },
      PH: { currency: 'PHP', expect_minor: 226000, floor_minor: 197750, cap_minor: 253120 },
    },
    plus: {
      US: { currency: 'USD', expect_minor: 2000, floor_minor: 2000, cap_minor: 2200 },
      PH: { currency: 'PHP', expect_minor: 99900, floor_minor: 87412, cap_minor: 112200 },
    },
  }

  it('恢复内置价格（点数档）：只改该地区的币种、预期、最低金额和上限，不动启用和注资', () => {
    const form = planToForm({
      ...creditPlan, key: 'credit1000', credit_quantity: 1000,
      regions: { ...creditPlan.regions, PH: { enabled: false, currency: 'USD', expect_minor: 1, floor_minor: 1, cap_minor: 2, fund_cents: 4500 } },
    }, REGIONS)
    expect(applyBuiltinPrice(form, 'PH', builtin)).toBe(true)
    const back = formToPlan(form)
    expect(back.regions.PH).toEqual({ enabled: false, currency: 'PHP', expect_minor: 226000, floor_minor: 197750, cap_minor: 253120, fund_cents: 4500 })
    expect(back.regions.US).toEqual(creditPlan.regions.US)
    expect(applyBuiltinPrice(form, 'US', builtin)).toBe(true)
    expect(formToPlan(form).regions.US).toMatchObject({ currency: 'USD', expect_minor: 4000, floor_minor: 4000, cap_minor: 4400 })
  })

  it('恢复内置价格（订阅套餐）也能用；没有内置价的地区或套餐返回 false 且不改表单', () => {
    const sub = planToForm({ ...creditPlan, key: 'plus', kind: 'subscription' }, REGIONS)
    expect(applyBuiltinPrice(sub, 'PH', builtin)).toBe(true)
    expect(formToPlan(sub).regions.PH).toMatchObject({ currency: 'PHP', expect_minor: 99900, floor_minor: 87412, cap_minor: 112200 })
    const before = JSON.stringify(sub)
    expect(applyBuiltinPrice(sub, 'JP', builtin)).toBe(false)
    expect(applyBuiltinPrice(planToForm({ ...creditPlan, key: 'go' }, REGIONS), 'PH', builtin)).toBe(false)
    expect(applyBuiltinPrice(sub, 'PH', undefined)).toBe(false)
    expect(applyBuiltinPrice(sub, 'XX', builtin)).toBe(false)
    expect(JSON.stringify(sub)).toBe(before)
  })

  it('上限低于预期金额（两者都大于 0）才提示', () => {
    expect(capBelowExpect(50000, 56500)).toBe(true)
    expect(capBelowExpect(56500, 56500)).toBe(false)
    expect(capBelowExpect(0, 56500)).toBe(false)
    expect(capBelowExpect(50000, 0)).toBe(false)
  })
})

describe('关闭续费', () => {
  const ok = { state: 'succeeded', dry_run: false, kind: 'subscription', renewal_state: 'failed', has_session: true }

  it('只有成功的真实订阅开通单要关闭续费（迁移前没有种类的旧单也算）', () => {
    expect(wantsRenewalCancel(ok)).toBe(true)
    expect(wantsRenewalCancel({ ...ok, kind: '' })).toBe(true)
    expect(wantsRenewalCancel({ ...ok, kind: 'credit' })).toBe(false)
    expect(wantsRenewalCancel({ ...ok, dry_run: true })).toBe(false)
    expect(wantsRenewalCancel({ ...ok, state: 'needs_review' })).toBe(false)
  })

  it('重试按钮的条件与后端 RetryRenewal 一致', () => {
    expect(renewalRetryBlock(ok)).toBe('')
    expect(renewalRetryBlock({ ...ok, renewal_state: 'pending' })).toBe('')
    expect(renewalRetryBlock({ ...ok, renewal_state: 'cancelled' })).toContain('已经关闭')
    expect(renewalRetryBlock({ ...ok, has_session: false })).toContain('请让用户自行在 ChatGPT 设置里关闭续费')
    expect(renewalRetryBlock({ ...ok, renewal_state: 'skipped' })).toContain('请让用户自行在 ChatGPT 设置里关闭续费')
    expect(renewalRetryBlock({ ...ok, kind: 'credit', renewal_state: '' })).not.toBe('')
    expect(renewalRetryBlock({ ...ok, dry_run: true })).not.toBe('')
  })

  it('没关掉且系统没法再关时提醒找用户', () => {
    expect(renewalNeedsUser(ok)).toBe(true)
    expect(renewalNeedsUser({ ...ok, renewal_state: 'skipped', has_session: false })).toBe(true)
    expect(renewalNeedsUser({ ...ok, renewal_state: 'pending' })).toBe(false)
    expect(renewalNeedsUser({ ...ok, renewal_state: 'cancelled' })).toBe(false)
    expect(renewalNeedsUser({ ...ok, kind: 'credit', renewal_state: '' })).toBe(false)
  })

  it('续费进度文字与标签颜色', () => {
    expect(renewalLabel('pending')).toBe('正在关闭续费')
    expect(renewalLabel('cancelled')).toBe('已关闭续费')
    expect(renewalLabel('failed')).toBe('关闭续费失败')
    expect(renewalLabel('skipped')).toContain('会话已清空')
    expect(renewalLabel('')).toBe('不适用')
    expect(renewalTagType('cancelled')).toBe('success')
    expect(renewalTagType('failed')).toBe('danger')
    expect(renewalTagType('')).toBe('info')
  })
})

describe('点数单详情', () => {
  it('种类与卡是否被存', () => {
    expect(orderKindLabel({ kind: 'credit', credit_quantity: 250 })).toBe('Codex 点数 250')
    expect(orderKindLabel({ kind: 'subscription' })).toBe('订阅开通')
    expect(orderKindLabel({ kind: '' })).toBe('订阅开通')
    expect(cardSavedLabel('not_saved')).toBe('没有存进用户账号')
    expect(cardSavedLabel('saved')).toContain('请联系用户删除')
    expect(cardSavedLabel('unknown')).toContain('没能核对')
    expect(cardSavedLabel('')).toBe('没有核对')
  })

  it('新错误码都有中文', () => {
    for (const c of [
      'checkout_url_untrusted', 'custom_checkout_unsupported', 'save_payment_unconfirmed', 'provider_unavailable',
      'credit_account_unknown', 'credit_no_subscription', 'credit_plan_ineligible', 'credit_not_personal',
      'credit_delinquent', 'credit_delinquency_unknown', 'credit_currency_mismatch',
    ]) expect(payErrorText(c), c).not.toBe('')
  })

  it('没有卡池的服务商：授权交易部分标明「这家服务商没有卡池」', () => {
    const n = evidenceNotice('这家服务商没有卡池，卡的交易记录无法查询')
    expect(n.title).toBe('这家服务商没有卡池')
    expect(n.description).toContain('时间线照常可看')
    expect(evidenceNotice('交易记录暂时无法获取').title).toBe('授权交易暂时读不出')
  })
})
