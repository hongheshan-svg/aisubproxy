import { describe, expect, it } from 'vitest'
import {
  PROBE_CHECKS, probeBlockReason, probeCounts, probeErrorText, probeNeedsBrowser, probeOrderChecks, probeStatusLabel, probeToText,
} from '../payprobe'
import { PAGE_TABS } from '../adminui'

describe('payprobe lib', () => {
  it('探测项与后端 key 一一对应，共 9 项', () => {
    expect(PROBE_CHECKS.map((c) => c.key)).toEqual([
      'account', 'subscriptions', 'checkout_modes', 'credit_checkout', 'payment_methods',
      'upgrade_preview', 'customer_portal', 'chatgpt_browser', 'credit_balance',
    ])
  })

  it('提交前检查：没有会话、没选探测项、正在运行都不能提交', () => {
    expect(probeBlockReason('', ['account'], false)).toContain('会话')
    expect(probeBlockReason('   ', ['account'], false)).toContain('会话')
    expect(probeBlockReason('{"a":1}', [], false)).toContain('探测项')
    expect(probeBlockReason('{"a":1}', ['account'], true)).toContain('运行')
    expect(probeBlockReason('{"a":1}', ['account'], false)).toBe('')
  })

  it('浏览器探测项识别', () => {
    expect(probeNeedsBrowser(['account'])).toBe(false)
    expect(probeNeedsBrowser(['account', 'customer_portal'])).toBe(true)
    expect(probeNeedsBrowser(['chatgpt_browser'])).toBe(true)
  })

  it('probeOrderChecks 按固定顺序', () => {
    expect(probeOrderChecks(['credit_balance', 'account'])).toEqual(['account', 'credit_balance'])
  })

  it('状态文字与计数', () => {
    expect(probeStatusLabel('ok')).toBe('成功')
    expect(probeStatusLabel('fail')).toBe('失败')
    expect(probeStatusLabel('skipped')).toBe('已跳过')
    const rs: any[] = [{ status: 'ok' }, { status: 'ok' }, { status: 'fail' }, { status: 'skipped' }]
    expect(probeCounts(rs)).toEqual({ ok: 2, fail: 1, skipped: 1 })
  })

  it('409 提示已有探测在运行', () => {
    expect(probeErrorText(409, { error: '已有探测正在运行，请等它结束再试' })).toContain('已有探测')
    expect(probeErrorText(409, {})).toContain('已有探测')
    expect(probeErrorText(0, {})).toBe('网络错误')
    expect(probeErrorText(400, { error: '会话无效' })).toBe('会话无效')
  })

  it('复制为文本：包含各项结论与详情，不含多余内容', () => {
    const txt = probeToText({
      id: 7, created_at: 1700000000, region: 'PH',
      results: [
        { key: 'account', title: '账号概况', status: 'ok', summary: 'plan_type=plus', details: { email: 'r***@e***.com' } },
        { key: 'checkout_modes', title: '开通结账会话形态', status: 'skipped', summary: '账号已有生效订阅' },
      ],
    })
    expect(txt).toContain('免付款探测 #7')
    expect(txt).toContain('地区 PH')
    expect(txt).toContain('[成功] account 账号概况')
    expect(txt).toContain('r***@e***.com')
    expect(txt).toContain('[已跳过] checkout_modes')
  })

  it('页内标签里有免付款探测', () => {
    const tabs = PAGE_TABS['/ops/marswallex-pay']
    expect(tabs.map((t) => t.name)).toContain('probe')
    expect(tabs.find((t) => t.name === 'probe')?.label).toBe('免付款探测')
  })
})
