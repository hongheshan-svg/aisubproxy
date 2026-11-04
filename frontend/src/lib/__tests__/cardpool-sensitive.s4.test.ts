import { describe, expect, it } from 'vitest'
import { toSensitiveView } from '../cardpool-sensitive'

describe('toSensitiveView（S4 / C-3）', () => {
  it('只取打码卡号，丢弃其他字段（含 CVV2 与完整卡号）', () => {
    const v = toSensitiveView({ card_no_masked: '411111******1234', card_no: '4111111111111234', cvv2: '987', expiry_date: '1228' })
    expect(v).toEqual({ cardNoMasked: '411111******1234' })
    expect(JSON.stringify(v)).not.toMatch(/987|4111111111111234|1228/)
  })
  it('后端误给了完整卡号时自己再打码', () => {
    expect(toSensitiveView({ card_no_masked: '4111111111111234' }).cardNoMasked).toBe('411111******1234')
  })
  it('缺字段返回空串', () => {
    expect(toSensitiveView({}).cardNoMasked).toBe('')
    expect(toSensitiveView(null).cardNoMasked).toBe('')
  })
})
