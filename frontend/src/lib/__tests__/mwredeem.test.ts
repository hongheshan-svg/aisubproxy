import { describe, expect, it } from 'vitest'
import { channelLabel, isCardSupply, mwOutcome, mwStatusText, redeemRejectedBeforeOrder } from '../mwredeem'

describe('mwOutcome', () => {
  it('成功、处理中', () => {
    expect(mwOutcome('completed').kind).toBe('success')
    expect(mwOutcome('queued').kind).toBe('running')
    expect(mwOutcome('running').kind).toBe('running')
  })
  it('未扣款失败且还有次数：可重兑，写出剩余次数', () => {
    const o = mwOutcome('failed_precharge', { can_retry: true, attempts_left: 2 })
    expect(o.kind).toBe('retry')
    expect(o.text).toContain('还剩 2 次')
    expect(mwOutcome('failed_precharge', {}).kind).toBe('retry')
  })
  it('次数用完或明确不可重试：转人工', () => {
    expect(mwOutcome('failed_precharge', { can_retry: true, attempts_left: 0 }).kind).toBe('manual')
    expect(mwOutcome('failed_precharge', { can_retry: false }).kind).toBe('manual')
  })
  it('被拒、待核对转人工，不可重兑', () => {
    expect(mwOutcome('declined').kind).toBe('manual')
    expect(mwOutcome('review').kind).toBe('manual')
    expect(mwOutcome('failed').kind).toBe('failed')
  })
  it('状态中文，未知原样', () => {
    expect(mwStatusText('failed_precharge')).toBe('未扣款，兑换失败')
    expect(mwStatusText('weird')).toBe('weird')
  })
})

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
  it('通道标签优先显示名，缺失退回 provider', () => {
    expect(channelLabel({ provider: 'testcard', provider_display_name: 'TestCard' })).toBe('TestCard')
    expect(channelLabel({ provider: 'testcard' })).toBe('testcard')
    expect(channelLabel(null, { provider: 'testcard', provider_display_name: 'TestCard' })).toBe('TestCard')
    expect(channelLabel(undefined, null)).toBe('本站')
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
