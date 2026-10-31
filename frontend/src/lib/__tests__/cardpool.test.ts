import { describe, expect, it } from 'vitest'
import { hasEmoji, isUnknownOutcome, kindLabel, parseAmountCents } from '../cardpool'

describe('cardpool lib', () => {
  it('parseAmountCents', () => {
    expect(parseAmountCents('12.5')).toBe(1250)
    expect(parseAmountCents('0.01')).toBe(1)
    expect(parseAmountCents(' 3 ')).toBe(300)
    expect(parseAmountCents('0')).toBeNull()
    expect(parseAmountCents('1.005')).toBeNull()
    expect(parseAmountCents('-1')).toBeNull()
    expect(parseAmountCents('abc')).toBeNull()
    expect(parseAmountCents('1234567890123')).toBeNull()
  })

  it('kindLabel covers wallet kinds', () => {
    expect(kindLabel('conversion')).toBe('兑换')
    expect(kindLabel('wallet_transfer')).toBe('钱包转账')
    expect(kindLabel('apply')).toBe('开卡')
    expect(kindLabel('x')).toBe('x')
  })

  it('isUnknownOutcome', () => {
    expect(isUnknownOutcome({ ok: false, status: 502, data: { outcome: 'unknown' } })).toBe(true)
    expect(isUnknownOutcome({ ok: false, status: 502, data: { outcome: 'rejected' } })).toBe(false)
    expect(isUnknownOutcome({ ok: true, status: 200, data: {} })).toBe(false)
  })

  it('hasEmoji', () => {
    expect(hasEmoji('停用😀')).toBe(true)
    expect(hasEmoji('停用原因')).toBe(false)
  })
})
