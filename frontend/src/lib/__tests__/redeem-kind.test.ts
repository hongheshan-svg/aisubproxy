// 用户兑换页：本站付款的订阅开通码（开通一个月、到期不续费）与 Codex 点数码（兑换前须勾选须知）。
import { describe, expect, it } from 'vitest'
import {
  isCreditCode, isOneMonthCode, withCreditTerms,
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
  })

  it('点数码勾了须知才在请求体里带 agree_credit_terms', () => {
    const base = { redemption_token: 't', preflight_token: 'p', client_request_id: 'c' }
    expect(withCreditTerms(base, credit, true)).toEqual({ ...base, agree_credit_terms: true })
    expect(withCreditTerms(base, credit, false)).toEqual(base)
    expect(withCreditTerms(base, sub, true)).toEqual(base)
  })

  it('点数档的显示名，且永远不判「已满足」', () => {
    expect(planLabel('credit250')).toBe('Codex 点数 250')
    expect(planLabel('credit25000')).toBe('Codex 点数 25000')
    expect(planSatisfied('pro', 'credit250')).toBe(false)
    expect(planSatisfied('plus', 'credit1000')).toBe(false)
  })
})
