import { describe, expect, it } from 'vitest'
import { failureLabel, formatMinor, fromMinor, isTerminal, stateLabel, toMinor } from '../pay'

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
