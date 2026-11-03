// 用户兑换页：本站付款的订阅开通码（开通一个月、到期不续费）与 Codex 点数码（兑换前须勾选须知）。
import { describe, expect, it } from 'vitest'
import {
  CREDIT_TERMS, ONE_MONTH_TEXT, creditTermsBlock, isCreditCode, isOneMonthCode, mwOutcome, withCreditTerms,
} from '../mwredeem'
import { planLabel, planSatisfied } from '../plan'

describe('用户兑换页：订阅开通码与点数码', () => {
  const credit = { kind: 'credit', credit_quantity: 250, requires_credit_terms: true, requires_subscription: true }
  const sub = { kind: 'subscription', auto_renew: false, requires_no_subscription: true }

  it('按预览字段区分两种码；上游码两者都不是', () => {
    expect(isCreditCode(credit)).toBe(true)
    expect(isCreditCode({ requires_credit_terms: true })).toBe(true)
    expect(isCreditCode(sub)).toBe(false)
    expect(isOneMonthCode(sub)).toBe(true)
    expect(isOneMonthCode({ auto_renew: false })).toBe(true)
    expect(isOneMonthCode(credit)).toBe(false)
    expect(isCreditCode({ plan: 'plus' })).toBe(false)
    expect(isOneMonthCode({ plan: 'plus' })).toBe(false)
    expect(isOneMonthCode(null)).toBe(false)
    expect(ONE_MONTH_TEXT).toBe('本码开通一个月，到期不自动续费')
  })

  it('点数码没勾须知不能兑换，勾了才在请求体里带 agree_credit_terms', () => {
    expect(creditTermsBlock(credit, false)).not.toBe('')
    expect(creditTermsBlock(credit, true)).toBe('')
    expect(creditTermsBlock(sub, false)).toBe('')
    const base = { redemption_token: 't', preflight_token: 'p', client_request_id: 'c' }
    expect(withCreditTerms(base, credit, true)).toEqual({ ...base, agree_credit_terms: true })
    expect(withCreditTerms(base, credit, false)).toEqual(base)
    expect(withCreditTerms(base, sub, true)).toEqual(base)
  })

  it('须知三条：订阅要求、币种与地区、过期不退款', () => {
    expect(CREDIT_TERMS).toHaveLength(3)
    expect(CREDIT_TERMS.join('；')).toContain('仅限已订阅 Plus 或 Pro 的账号')
    expect(CREDIT_TERMS.join('；')).toContain('码上的地区必须与之一致')
    expect(CREDIT_TERMS.join('；')).toContain('12 个月过期，不退款')
  })

  it('点数码成功的结论文字不说「开通」', () => {
    expect(mwOutcome('completed', null, true).text).toContain('点数')
    expect(mwOutcome('completed').text).toContain('开通完成')
  })

  it('点数档的显示名，且永远不判「已满足」', () => {
    expect(planLabel('credit250')).toBe('Codex 点数 250')
    expect(planLabel('credit25000')).toBe('Codex 点数 25000')
    expect(planSatisfied('pro', 'credit250')).toBe(false)
    expect(planSatisfied('plus', 'credit1000')).toBe(false)
  })
})
