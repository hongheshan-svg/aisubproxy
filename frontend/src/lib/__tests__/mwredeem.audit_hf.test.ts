import { describe, expect, it } from 'vitest'
import { redeemRejectedBeforeOrder } from '../mwredeem'

describe('兑换被拒且没有建单：停在确认页（F-5）', () => {
  it('码已禁用、已换码（400 带 status）没建单：停在确认页，不去轮询', () => {
    expect(redeemRejectedBeforeOrder(400, { error: '卡密已禁用', status: 'disabled' })).toBe(true)
    expect(redeemRejectedBeforeOrder(400, { error: '该卡密已换发新码', status: 'replaced' })).toBe(true)
  })
  it('账号互斥、锁冲突的 409（没有 status 和 order）：停在确认页', () => {
    expect(redeemRejectedBeforeOrder(409, { error: '该账号已有其他通道在处理，请稍后再试' })).toBe(true)
    expect(redeemRejectedBeforeOrder(409, { error: '处理中' })).toBe(true)
  })
  it('409 带进度状态（码在途、已使用、已转人工）：已有单，去结果页按码查进度', () => {
    expect(redeemRejectedBeforeOrder(409, { error: '处理中（已使用，可查询进度）', status: 'reserved' })).toBe(false)
    expect(redeemRejectedBeforeOrder(409, { error: '已使用', status: 'consumed' })).toBe(false)
    expect(redeemRejectedBeforeOrder(409, { error: '已转人工', status: 'failed' })).toBe(false)
  })
})
