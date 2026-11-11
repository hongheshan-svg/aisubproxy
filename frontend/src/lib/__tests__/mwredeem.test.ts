import { describe, expect, it } from 'vitest'
import { isCardSupply, redeemRejectedBeforeOrder } from '../mwredeem'

describe('按 provider_kind 判断供卡型通道', () => {
  it('只有 card_supply 启用本站付款界面，上游或缺失一律按上游', () => {
    expect(isCardSupply('card_supply')).toBe(true)
    expect(isCardSupply('upstream')).toBe(false)
    expect(isCardSupply(undefined)).toBe(false)
    expect(isCardSupply('')).toBe(false)
  })
  it('不再按服务商名判断：假的 testcard 供卡型同样启用，marswallex 名但没有 kind 不启用', () => {
    expect(isCardSupply({ provider: 'testcard', provider_kind: 'card_supply' }.provider_kind)).toBe(true)
    expect(isCardSupply({ provider: 'marswallex' }['provider_kind' as never])).toBe(false)
  })
})

describe('redeemRejectedBeforeOrder', () => {
  it('熔断 / 未就绪 503、预检失效 400 没建单：停在确认页', () => {
    expect(redeemRejectedBeforeOrder(503, { error: '兑换通道暂时不可用，请稍后再试' })).toBe(true)
    expect(redeemRejectedBeforeOrder(400, { error: '预检已失效' })).toBe(true)
    expect(redeemRejectedBeforeOrder(429, { error: 'x' })).toBe(true)
  })
  it('带 order 的响应、成功响应：去结果页', () => {
    expect(redeemRejectedBeforeOrder(400, { order: { status: 'failed' } })).toBe(false)
    expect(redeemRejectedBeforeOrder(200, { order: {} })).toBe(false)
  })
})
