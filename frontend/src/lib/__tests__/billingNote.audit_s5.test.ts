import { describe, expect, it } from 'vitest'
import { pickBillingNote } from '../billingNote'

describe('账单查询说明字段', () => {
  it('取非空字符串并去掉首尾空白', () => {
    expect(pickBillingNote({ note: ' 凭卡密查询不提供账单链接 ' })).toBe('凭卡密查询不提供账单链接')
  })
  it('没有、空值、非字符串都当没有', () => {
    expect(pickBillingNote({})).toBe('')
    expect(pickBillingNote(null)).toBe('')
    expect(pickBillingNote({ note: 5 })).toBe('')
    expect(pickBillingNote({ note: '  ' })).toBe('')
  })
})
