import { describe, expect, it } from 'vitest'
import { failureLabel, formatMinor, fromMinor, isTerminal, pageOutcomeLabel, payBlockReason, stateLabel, toMinor } from '../pay'

describe('pay lib', () => {
  it('stateLabel 13 个状态', () => {
    const want: Record<string, string> = {
      queued: '已受理', checking: '预检中', link_created: '已生成链接', opening: '打开支付页',
      funding: '备卡中', filling: '填表中', submitted: '已提交', requires_3ds: '等待 3DS',
      verifying: '确认中', succeeded: '成功', failed: '失败', needs_review: '待人工', previewed: '试运行完成',
    }
    for (const [k, v] of Object.entries(want)) expect(stateLabel(k)).toBe(v)
    expect(stateLabel('weird')).toBe('weird')
  })

  it('failureLabel', () => {
    expect(failureLabel('not_submitted')).toBe('提交前放弃，未扣款')
    expect(failureLabel('declined')).toBe('提交后被拒')
    expect(failureLabel('x')).toBe('x')
  })

  it('pageOutcomeLabel', () => {
    expect(pageOutcomeLabel('success')).toBe('支付页显示成功')
    expect(pageOutcomeLabel('declined')).toBe('支付页显示被拒')
    expect(pageOutcomeLabel('form_rejected')).toBe('表单校验未通过')
    expect(pageOutcomeLabel('unknown')).toBe('没有明确结果')
    expect(pageOutcomeLabel('')).toBe('—')
    expect(pageOutcomeLabel('x')).toBe('x')
  })

  it('formatMinor', () => {
    expect(formatMinor(112000, 'PHP')).toBe('1,120.00 PHP')
    expect(formatMinor(3000, 'JPY')).toBe('3,000 JPY')
    expect(formatMinor(19000, 'CLP')).toBe('19,000 CLP')
    expect(formatMinor(0, 'USD')).toBe('0.00 USD')
  })

  it('toMinor / fromMinor', () => {
    expect(toMinor(19.99, 'USD')).toBe(1999)
    expect(toMinor(3000, 'JPY')).toBe(3000)
    expect(toMinor(1120, 'PHP')).toBe(112000)
    expect(toMinor(0.29, 'USD')).toBe(29)
    expect(fromMinor(1999, 'USD')).toBe(19.99)
    expect(fromMinor(3000, 'CLP')).toBe(3000)
  })

  it('isTerminal', () => {
    for (const s of ['succeeded', 'failed', 'needs_review', 'previewed']) expect(isTerminal(s)).toBe(true)
    for (const s of ['queued', 'checking', 'link_created', 'opening', 'funding', 'filling', 'submitted', 'requires_3ds', 'verifying']) expect(isTerminal(s)).toBe(false)
  })
})

describe('payBlockReason', () => {
  const region = { enabled: true, cap_minor: 120000, fund_cents: 3000 }
  const plan = { chatgpt_plan_name: 'chatgptplusplan', regions: { PH: region } }

  it('配置齐全时真实付款和试运行都放行', () => {
    expect(payBlockReason(plan, 'PH', false)).toBe('')
    expect(payBlockReason(plan, 'PH', true)).toBe('')
  })
  it('没有套餐或套餐名时都不放行', () => {
    expect(payBlockReason(undefined, 'PH', true)).not.toBe('')
    expect(payBlockReason({ ...plan, chatgpt_plan_name: '' }, 'PH', true)).not.toBe('')
  })
  it('试运行只要求套餐名', () => {
    expect(payBlockReason({ chatgpt_plan_name: 'x', regions: {} }, 'PH', true)).toBe('')
  })
  it('真实付款要求地区已启用且金额都大于 0', () => {
    expect(payBlockReason(plan, 'JP', false)).not.toBe('')
    expect(payBlockReason({ ...plan, regions: { PH: { ...region, enabled: false } } }, 'PH', false)).not.toBe('')
    expect(payBlockReason({ ...plan, regions: { PH: { ...region, cap_minor: 0 } } }, 'PH', false)).not.toBe('')
    expect(payBlockReason({ ...plan, regions: { PH: { ...region, fund_cents: 0 } } }, 'PH', false)).not.toBe('')
  })
})
