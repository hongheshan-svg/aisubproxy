// Claude 卡密：兑换时填目标组织的 Organization ID（两次一致、UUID），并确认目标账号目前是免费账号。
import { describe, expect, it } from 'vitest'
import {
  claudeOrgCheck, claudeCredential, claudePlanLabel, claudeRedeemBody, claudeTargetPlan, isClaudePlan, isClaudePreview, normalizeOrgId,
} from '../claude'
import { planLabel, planSatisfied } from '../plan'

const ORG = '3f2a9c4e-1b7d-4e8a-9c0f-5d6e7f8a9b0c'

describe('Claude 档位识别', () => {
  it('预览带 product=claude 时补上 claude_ 前缀', () => {
    expect(claudeTargetPlan('pro', 'claude')).toBe('claude_pro')
    expect(claudeTargetPlan('max_5x', 'Claude')).toBe('claude_max_5x')
    expect(claudeTargetPlan('claude_max_20x', '')).toBe('claude_max_20x')
  })

  it('没有 product 时只认 Claude 独有的裸键，裸 pro 不当成 Claude（旧 GPT 码也用过 pro）', () => {
    expect(claudeTargetPlan('max_5x', '')).toBe('claude_max_5x')
    expect(claudeTargetPlan('max_20x', '')).toBe('claude_max_20x')
    expect(claudeTargetPlan('pro', '')).toBe('')
    expect(claudeTargetPlan('pro_20x', '')).toBe('')
    expect(claudeTargetPlan('plus', 'gpt')).toBe('')
  })

  it('isClaudePlan 只认三档', () => {
    expect(isClaudePlan('claude_pro')).toBe(true)
    expect(isClaudePlan('claude_max_5x')).toBe(true)
    expect(isClaudePlan('claude_max_20x')).toBe(true)
    expect(isClaudePlan('claude_team')).toBe(false)
    expect(isClaudePlan('pro')).toBe(false)
    expect(isClaudePlan('')).toBe(false)
  })

  it('预览响应是不是 Claude 码（批量页用）', () => {
    expect(isClaudePreview({ plan: 'pro', product: 'claude' })).toBe(true)
    expect(isClaudePreview({ plan: 'claude_max_5x' })).toBe(true)
    expect(isClaudePreview({ plan_type: 'max_20x' })).toBe(true)
    expect(isClaudePreview({ plan: 'pro_20x', product: 'gpt' })).toBe(false)
    expect(isClaudePreview({ plan: 'premium_monthly', product: 'grok' })).toBe(false)
    expect(isClaudePreview(null)).toBe(false)
  })

  it('套餐名：显示成 Claude Pro / Max，不能落进 GPT 的「Pro 20x」', () => {
    expect(claudePlanLabel('claude_pro')).toBe('Claude Pro')
    expect(claudePlanLabel('claude_max_5x')).toBe('Claude Max 5x')
    expect(claudePlanLabel('claude_max_20x')).toBe('Claude Max 20x')
    expect(planLabel('claude_pro')).toBe('Claude Pro')
    expect(planLabel('claude_max_20x')).toBe('Claude Max 20x')
    expect(planLabel('pro_20x')).toBe('Pro 20x')
  })

  it('Claude 码不做「已满足」拦截：账号是不是免费由持码人确认，兑换时再核对', () => {
    expect(planSatisfied('claude_max_20x', 'claude_pro')).toBe(false)
    expect(planSatisfied('pro_20x', 'claude_pro')).toBe(false)
  })
})

describe('Organization ID', () => {
  it('去掉首尾空白、转小写；不是完整 UUID 返回空串', () => {
    expect(normalizeOrgId(`  ${ORG.toUpperCase()} `)).toBe(ORG)
    expect(normalizeOrgId(ORG.slice(0, 35))).toBe('')
    expect(normalizeOrgId(ORG.replace(/-/g, ''))).toBe('')
    expect(normalizeOrgId('user_' + ORG)).toBe('')
    expect(normalizeOrgId('someone@example.com')).toBe('')
    expect(normalizeOrgId('')).toBe('')
  })

  it('两次一致、UUID 格式、确认免费账号，三条都满足才通过', () => {
    expect(claudeOrgCheck(ORG, ORG.toUpperCase(), true)).toBe('')
    expect(claudeOrgCheck('', '', true)).toBe('format')
    expect(claudeOrgCheck('not-a-uuid', 'not-a-uuid', true)).toBe('format')
    expect(claudeOrgCheck(ORG, '', true)).toBe('mismatch')
    expect(claudeOrgCheck(ORG, '4f2a9c4e-1b7d-4e8a-9c0f-5d6e7f8a9b0c', true)).toBe('mismatch')
    expect(claudeOrgCheck(ORG, ORG, false)).toBe('free')
  })

  it('凭证与 Grok / X 同形：mode=session，session 放规范化后的 Organization ID', () => {
    expect(claudeCredential(` ${ORG.toUpperCase()}`)).toEqual({ mode: 'session', session: ORG })
  })

  it('兑换请求：预检给了票就带票；没给票（与 Grok 一样）就直接带凭证', () => {
    expect(claudeRedeemBody('rt', 'pt', ORG, 'cid')).toEqual({ redemption_token: 'rt', preflight_token: 'pt', client_request_id: 'cid' })
    expect(claudeRedeemBody('rt', '', ORG, 'cid')).toEqual({
      redemption_token: 'rt', credential: { mode: 'session', session: ORG }, client_request_id: 'cid',
    })
  })
})
