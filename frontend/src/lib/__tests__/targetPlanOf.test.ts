// 码种判断只写一份：预览数据 → 带产品前缀的档位
import { describe, expect, it } from 'vitest'
import { targetPlanOf } from '../redeemAccount'

describe('targetPlanOf', () => {
  it('Claude 预览补 claude_ 前缀', () => {
    expect(targetPlanOf({ plan: 'max_5x' })).toBe('claude_max_5x')
    expect(targetPlanOf({ plan: 'pro', product: 'claude' })).toBe('claude_pro')
  })
  it('product 为 grok 时裸 premium_monthly 补成 grok_premium_monthly', () => {
    expect(targetPlanOf({ plan: 'premium_monthly', product: 'grok' })).toBe('grok_premium_monthly')
  })
  it('product 为 x 时得到 x_premium_monthly', () => {
    expect(targetPlanOf({ plan_type: 'premium_monthly', product: 'x' })).toBe('x_premium_monthly')
  })
  it('普通 plus 原样（小写）', () => {
    expect(targetPlanOf({ plan: 'Plus' })).toBe('plus')
  })
  it('没有预览得到空串', () => {
    expect(targetPlanOf(null)).toBe('')
  })
})
