import { describe, expect, it } from 'vitest'
import { fmtUsd, parseSignedUsd } from '../agentMoney'

describe('fmtUsd', () => {
  it('整数运算格式化', () => {
    expect(fmtUsd(123456)).toBe('$1,234.56')
    expect(fmtUsd(-500)).toBe('-$5.00')
    expect(fmtUsd(5)).toBe('$0.05')
    expect(fmtUsd(0)).toBe('$0.00')
    expect(fmtUsd(100000000)).toBe('$1,000,000.00')
  })
})

describe('parseSignedUsd', () => {
  it('允许负号和一到两位小数', () => {
    expect(parseSignedUsd('-12.5')).toEqual({ ok: true, cents: -1250 })
    expect(parseSignedUsd('0.29')).toEqual({ ok: true, cents: 29 })
    expect(parseSignedUsd(' 3 ')).toEqual({ ok: true, cents: 300 })
  })
  it('拒绝 0、三位小数、空和非数字', () => {
    expect(parseSignedUsd('0').ok).toBe(false)
    expect(parseSignedUsd('-0.00').ok).toBe(false)
    expect(parseSignedUsd('1.234').ok).toBe(false)
    expect(parseSignedUsd('').ok).toBe(false)
    expect(parseSignedUsd('abc').ok).toBe(false)
  })
})
