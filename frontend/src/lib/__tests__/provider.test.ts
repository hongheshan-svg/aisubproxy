import { describe, expect, it } from 'vitest'
import {
  selectionPage, canDisableLocal, canEnableLocal, canResolveSwap, centsToUsd, classifyIssueFailure, localCdkStatusText, localCdkTagType,
  needsFirstRealPayment, planNote, swapFailure, usdToCents,
} from '../provider'

describe('usdToCents', () => {
  it('美元换算成分，不出浮点误差', () => {
    expect(usdToCents('19.99')).toEqual({ ok: true, cents: 1999 })
    expect(usdToCents('0.29')).toEqual({ ok: true, cents: 29 })
    expect(usdToCents(500)).toEqual({ ok: true, cents: 50000 })
    expect(usdToCents('2000.5')).toEqual({ ok: true, cents: 200050 })
  })
  it('拒绝 0、负数、空、非数字、超过两位小数', () => {
    for (const bad of ['0', '0.00', '-1', '-0.5', '', '  ', 'abc', '1.234', null, undefined]) {
      expect(usdToCents(bad as any).ok).toBe(false)
    }
  })
  it('centsToUsd 两位小数', () => {
    expect(centsToUsd(50000)).toBe('500.00')
    expect(centsToUsd(5)).toBe('0.05')
    expect(centsToUsd(undefined)).toBe('0.00')
  })
})

describe('classifyIssueFailure', () => {
  it('409 already_issued：码拿不回来，要走禁用后重发', () => {
    const f = classifyIssueFailure(409, { already_issued: true, first_id: 7, last_id: 9, count: 3 })
    expect(f.kind).toBe('already_issued')
    expect(f).toMatchObject({ firstId: 7, lastId: 9, count: 3 })
  })
  it('未就绪、不支持、结果不明、其他', () => {
    expect(classifyIssueFailure(400, { error: 'x', provider: 'marswallex', problems: ['a'] }).kind).toBe('not_ready')
    expect(classifyIssueFailure(400, { error: 'x', provider: 'marswallex', plan: 'claude_pro' }).kind).toBe('unsupported')
    expect(classifyIssueFailure(0, { error: '网络错误' }).kind).toBe('uncertain')
    expect(classifyIssueFailure(502, { error: 'up' }).kind).toBe('uncertain')
    expect(classifyIssueFailure(400, { error: 'bad' }).kind).toBe('other')
  })
})

describe('本地码状态', () => {
  it('派生状态文字与标签', () => {
    expect(localCdkStatusText('failed', 'needs_review')).toBe('已失败（待人工核对）')
    expect(localCdkStatusText('unused', '')).toBe('未使用')
    expect(localCdkTagType('consumed', '')).toBe('success')
    expect(localCdkTagType('failed', 'needs_review')).toBe('warning')
  })
  it('禁用与启用的条件', () => {
    expect(canDisableLocal('unused')).toBe(true)
    expect(canDisableLocal('consumed')).toBe(false)
    expect(canEnableLocal('disabled', 'manual')).toBe(true)
    expect(canEnableLocal('disabled', 'exchanged')).toBe(false)
    expect(canEnableLocal('unused', '')).toBe(false)
  })
})

describe('就绪与说明', () => {
  it('首笔真实付款这一条未通过才提示', () => {
    const base = { provider: 'marswallex', ok: false, problems: [] }
    expect(needsFirstRealPayment({ ...base, checks: [{ key: 'first_real_payment', ok: false, detail: '' }] })).toBe(true)
    expect(needsFirstRealPayment({ ...base, checks: [{ key: 'first_real_payment', ok: true, detail: '' }] })).toBe(false)
    expect(needsFirstRealPayment(null)).toBe(false)
  })
  it('pro_20x 标注仅限无订阅账号', () => {
    expect(planNote('pro_20x')).toBe('仅限无订阅账号')
    expect(planNote('plus', '可选')).toBe('可选')
  })
  it('只有 unknown 的换码记录能核对', () => {
    expect(canResolveSwap('unknown')).toBe(true)
    expect(canResolveSwap('disabling')).toBe(false)
  })
})

describe('swapFailure', () => {
  it('发码结果不明、网络异常、处理中都锁住并提示勿重试', () => {
    expect(swapFailure(502, { outcome: 'unknown', error: '发码结果不明，已转人工核对，请勿重试' }).locked).toBe(true)
    expect(swapFailure(0, {}).message).toContain('请勿重试')
    expect(swapFailure(409, { state: 'pending', error: 'x' }).locked).toBe(true)
  })
  it('明确失败不锁', () => {
    expect(swapFailure(400, { error: '次数用完' })).toEqual({ locked: false, message: '次数用完' })
    expect(swapFailure(429, { error: '今日已达上限' }).locked).toBe(false)
    expect(swapFailure(502, { error: '卡台不可用' }).locked).toBe(false)
  })
})

describe('selectionPage', () => {
  it('两家内置服务商各有选卡规则页，其他服务商没有', () => {
    expect(selectionPage('zovocard')).toBe('/ops/card-selection')
    expect(selectionPage('marswallex')).toBe('/ops/marswallex-selection')
    expect(selectionPage('testup')).toBeUndefined()
  })
})
