import { describe, expect, it } from 'vitest'
import { cardHeadMarks, cardHeadOptionLabel, cardHeadsByProduct, cardHeadTitle, triLabel, type CardHead } from '../cardheads'

function head(over: Partial<CardHead>): CardHead {
  return {
    provider: 'marswallex', product_id: 'P', source: 'api', name: '', card_heads: [], network: '', country: '', currency: '',
    listed: true, three_ds: '', apple_pay: '', note: '', synced_at: 0, updated_at: 0, updated_by: '', ...over,
  }
}

describe('triLabel', () => {
  it('三种状态', () => {
    expect(triLabel('')).toBe('未标注')
    expect(triLabel('yes')).toBe('支持')
    expect(triLabel('no')).toBe('不支持')
  })
})

describe('cardHeadTitle', () => {
  it('有卡头时显示卡头', () => {
    expect(cardHeadTitle(head({ card_heads: ['512998'], name: '美卡-泛场景-512998-充值卡' }))).toBe('512998')
  })
  it('多个卡头用斜杠连接', () => {
    expect(cardHeadTitle(head({ card_heads: ['411111', '422222'] }))).toBe('411111/422222')
  })
  it('没有卡头时显示产品名', () => {
    expect(cardHeadTitle(head({ name: '无卡头产品' }))).toBe('无卡头产品')
  })
  it('什么都没有时显示产品 ID', () => {
    expect(cardHeadTitle(head({ product_id: '2100050582268325889' }))).toBe('2100050582268325889')
  })
})

describe('cardHeadOptionLabel', () => {
  it('MarsWallex 四个卡头按标注生成说明', () => {
    expect(cardHeadOptionLabel(head({ card_heads: ['512998'], three_ds: 'no' }))).toBe('512998 · 3DS 不支持')
    expect(cardHeadOptionLabel(head({ card_heads: ['428820'], three_ds: 'yes' }))).toBe('428820 · 3DS 支持')
    expect(cardHeadOptionLabel(head({ card_heads: ['537100'], three_ds: 'yes', apple_pay: 'yes' }))).toBe('537100 · 3DS 支持 · Apple Pay 支持')
    expect(cardHeadOptionLabel(head({ card_heads: ['537872712'], three_ds: 'no' }))).toBe('537872712 · 3DS 不支持')
  })
  it('未标注的项不写', () => {
    expect(cardHeadOptionLabel(head({ card_heads: ['512998'] }))).toBe('512998')
    expect(cardHeadOptionLabel(head({ card_heads: ['512998'], apple_pay: 'no' }))).toBe('512998 · Apple Pay 不支持')
  })
  it('接口已不再返回的加说明', () => {
    expect(cardHeadOptionLabel(head({ card_heads: ['512998'], three_ds: 'no', listed: false }))).toBe('512998 · 3DS 不支持（接口已不再返回）')
  })
})

describe('cardHeadMarks', () => {
  it('只给标注部分，用于已经显示了卡头的地方', () => {
    expect(cardHeadMarks(head({ card_heads: ['537100'], three_ds: 'yes', apple_pay: 'yes' }))).toBe('3DS 支持 · Apple Pay 支持')
    expect(cardHeadMarks(head({ card_heads: ['512998'] }))).toBe('')
    expect(cardHeadMarks(head({ three_ds: 'no', listed: false }))).toBe('3DS 不支持（接口已不再返回）')
  })
})

describe('cardHeadsByProduct', () => {
  it('按产品 ID 建索引', () => {
    const a = head({ product_id: 'A' })
    const b = head({ product_id: 'B' })
    expect(cardHeadsByProduct([a, b])).toEqual({ A: a, B: b })
    expect(cardHeadsByProduct([])).toEqual({})
  })
})
