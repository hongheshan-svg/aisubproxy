import { describe, expect, it } from 'vitest'
import {
  applyBuiltinPrice, failureLabel, failureText, floorProblem, formatMinor, formToPlan, fromMinor, isTerminal, pageOutcomeLabel, payBlockReason, planToForm,
  proxyTestText, stateLabel, toMinor, type PlanConf,
} from '../pay'

describe('proxyTestText', () => {
  it('一致', () => {
    expect(proxyTestText({ region: 'PH', proxy_configured: true, ok: true, ip: '1.2.3.4', loc: 'PH', latency_ms: 320, matches_region: true }))
      .toEqual({ tone: 'ok', text: '出口 IP 1.2.3.4，OpenAI 识别为 PH（菲律宾），耗时 320 ms' })
  })
  it('不一致用警告色', () => {
    const r = proxyTestText({ region: 'PH', proxy_configured: false, ok: true, ip: '5.6.7.8', loc: 'SG', latency_ms: 90, matches_region: false })
    expect(r.tone).toBe('warn')
    expect(r.text).toContain('识别为 SG，与 PH 不一致，可能拿不到该地区价格，也更容易被风控')
  })
  it('失败显示中文原因', () => {
    expect(proxyTestText({ region: 'PH', proxy_configured: true, ok: false, error: '代理认证失败，请检查账号密码' }))
      .toEqual({ tone: 'err', text: '测试失败：代理认证失败，请检查账号密码' })
  })
})

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

  it('failureText：人工定论的单不显示系统判定的失败类别', () => {
    expect(failureText('declined', '')).toBe('提交后被拒')
    expect(failureText('not_submitted', '')).toBe('提交前放弃，未扣款')
    expect(failureText('declined', 'alice')).toBe('人工定论为失败')
    expect(failureText('x', '')).toBe('x')
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
  const region = { enabled: true, cap_minor: 120000, fund_cents: 3000, floor_minor: 100000 }
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
  it('真实付款要求最低金额大于 0，试运行不要求', () => {
    const noFloor = { ...plan, regions: { PH: { ...region, floor_minor: 0 } } }
    expect(payBlockReason(noFloor, 'PH', false)).toContain('最低金额')
    expect(payBlockReason({ ...plan, regions: { PH: { enabled: true, cap_minor: 120000, fund_cents: 3000 } } }, 'PH', false)).toContain('最低金额')
    expect(payBlockReason(noFloor, 'PH', true)).toBe('')
  })
})

describe('最低金额', () => {
  const REGIONS = ['PH', 'US', 'JP']
  const plan: PlanConf = {
    key: 'plus', label: 'Plus', chatgpt_plan_name: 'chatgptplusplan', expect_plan_type: 'plus', kind: 'subscription',
    regions: {
      PH: { enabled: true, currency: 'USD', expect_minor: 2000, floor_minor: 1850, cap_minor: 2400, fund_cents: 3000 },
      JP: { enabled: true, currency: 'JPY', expect_minor: 3000, floor_minor: 2800, cap_minor: 3600, fund_cents: 3000 },
    },
  }

  it('planToForm 再 formToPlan 往返后 floor_minor 不变（USD 与 JPY）', () => {
    const form = planToForm(plan, REGIONS)
    // 直接断言表单值：USD 2 位小数、JPY 0 位，抓得到两边对称算错的情况
    expect(form.rows.find((r) => r.region === 'PH')!.floor).toBe(18.5)
    expect(form.rows.find((r) => r.region === 'JP')!.floor).toBe(2800)
    const back = formToPlan(form)
    expect(back.regions.PH.floor_minor).toBe(1850)
    expect(back.regions.JP.floor_minor).toBe(2800)
    expect(back.regions.US.floor_minor).toBe(0)
  })

  it('applyBuiltinPrice 后 PH 点数行的最低金额低于预期金额，且能通过 floorProblem', () => {
    const credit: PlanConf = { ...plan, key: 'credit250', kind: 'credit', credit_quantity: 250, regions: {} }
    const f = planToForm(credit, REGIONS)
    const builtin = { credit250: { PH: { currency: 'PHP', expect_minor: 56500, floor_minor: 49437, cap_minor: 63280 } } }
    expect(applyBuiltinPrice(f, 'PH', builtin)).toBe(true)
    const ph = f.rows.find((r) => r.region === 'PH')!
    expect(ph.expect).toBe(565)
    expect(ph.floor).toBe(494.37)
    expect(formToPlan(f).regions.PH.floor_minor).toBe(49437)
    expect(floorProblem(49437, 56500, 63280)).toBe('')
  })

  it('floorProblem 与后端保存校验一致', () => {
    expect(floorProblem(12001, 0, 12000)).toContain('上限')
    expect(floorProblem(10001, 10000, 12000)).toContain('预期')
    expect(floorProblem(10000, 10000, 12000)).toBe('')
    expect(floorProblem(0, 10000, 12000)).toBe('')
    expect(floorProblem(5000, 0, 0)).toBe('')
  })
})
