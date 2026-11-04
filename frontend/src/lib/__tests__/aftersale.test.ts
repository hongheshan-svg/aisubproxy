import { describe, expect, it } from 'vitest'
import { afterSaleView, GUIDE_CODES, type LookupResult } from '../aftersale'

const base = (o: Partial<LookupResult>): LookupResult => ({ cdk_code: 'CDK-1', status: 'unused', used: false, ...o })

describe('afterSaleView', () => {
  it('unused 没有 guide：可兑换', () => {
    const v = afterSaleView(base({}))
    expect(v.titleKey).toBe('aftersale.state.unused')
    expect(v.actions).toEqual(['redeem'])
    expect(v.guideKey).toBeUndefined()
    expect(v.fields).toEqual([])
  })

  it('unused 且 can_resubmit 有 guide：失败码已恢复，可重新提交', () => {
    const v = afterSaleView(base({ can_resubmit: true, guide: 'retry_later' }))
    expect(v.titleKey).toBe('aftersale.state.failed_restored')
    expect(v.guideKey).toBe('aftersale.guide.retry_later')
    expect(v.actions).toEqual(['resubmit'])
  })

  it('failed 且 can_resubmit 有 guide：失败码已恢复，可重新提交，带服务商原文', () => {
    const v = afterSaleView(base({ status: 'failed', can_resubmit: true, guide: 'provider_retry', provider_note: 'oops' }))
    expect(v.titleKey).toBe('aftersale.state.failed_restored')
    expect(v.guideKey).toBe('aftersale.guide.provider_retry')
    expect(v.note).toBe('oops')
    expect(v.actions).toEqual(['resubmit'])
    expect(v.tone).toBe('warn')
  })

  it('failed 且 !can_resubmit：已转人工，没有按钮', () => {
    const v = afterSaleView(base({ status: 'failed', can_resubmit: false, guide: 'manual_review' }))
    expect(v.titleKey).toBe('aftersale.state.failed_manual')
    expect(v.guideKey).toBe('aftersale.guide.manual_review')
    expect(v.actions).toEqual([])
  })

  it('processing：没有按钮', () => {
    const v = afterSaleView(base({ status: 'processing' }))
    expect(v.titleKey).toBe('aftersale.state.processing')
    expect(v.actions).toEqual([])
    expect(v.tone).toBe('info')
  })

  it('used：套餐、开通时间、到期时间、邮箱按顺序出现，时间为北京时间', () => {
    const v = afterSaleView(base({
      status: 'used', used: true, plan: 'ChatGPT Plus', plan_kind: 'subscription',
      used_at: '2026-03-04T05:06:07Z', expires_at: '2026-04-04T05:06:07Z', account_email: 'a***@g***.com', renewal: 'off',
    }))
    expect(v.titleKey).toBe('aftersale.state.used')
    expect(v.tone).toBe('ok')
    expect(v.fields).toEqual([
      { labelKey: 'aftersale.field.plan', value: 'ChatGPT Plus' },
      { labelKey: 'aftersale.field.usedAt', value: '2026-03-04 13:06:07' },
      { labelKey: 'aftersale.field.expiresAt', value: '2026-04-04 13:06:07' },
      { labelKey: 'aftersale.field.email', value: 'a***@g***.com' },
    ])
    expect(v.renewalKey).toBe('aftersale.renewal.off')
    expect(v.actions).toEqual([])
  })

  it('used 的 renewal=closing', () => {
    expect(afterSaleView(base({ status: 'used', used: true, renewal: 'closing' })).renewalKey).toBe('aftersale.renewal.closing')
  })

  it('used 没有值的字段不出现，renewal 为空或未知不给 renewalKey', () => {
    const v = afterSaleView(base({ status: 'used', used: true, renewal: 'weird' }))
    expect(v.fields).toEqual([])
    expect(v.renewalKey).toBeUndefined()
  })

  it('点数码：显示点数已到账，不放到期时间', () => {
    const v = afterSaleView(base({
      status: 'used', used: true, plan_kind: 'credit', plan: 'Credits',
      used_at: '2026-03-04T05:06:07Z', expires_at: '2026-04-04T05:06:07Z',
    }))
    expect(v.titleKey).toBe('aftersale.state.credit_used')
    expect(v.fields.map((f) => f.labelKey)).toEqual(['aftersale.field.plan', 'aftersale.field.usedAt'])
  })

  it('时间无法解析：该字段不出现，也不显示 Invalid Date 或 NaN', () => {
    const v = afterSaleView(base({ status: 'used', used: true, used_at: 'not-a-time', expires_at: '' }))
    expect(v.fields).toEqual([])
    expect(JSON.stringify(v)).not.toMatch(/Invalid|NaN/)
  })

  it('replaced 有 replacement：可复制，新码 unused 时可去兑换', () => {
    const v = afterSaleView(base({ status: 'replaced', replacement: { code: 'NEW-1', status: 'unused', depth: 1 } }))
    expect(v.titleKey).toBe('aftersale.state.replaced')
    expect(v.replacement).toEqual({ code: 'NEW-1', statusKey: 'aftersale.state.unused' })
    expect(v.actions).toEqual(['copy_replacement', 'redeem_replacement'])
  })

  it('replaced 的新码已使用：只能复制', () => {
    const v = afterSaleView(base({ status: 'replaced', replacement: { code: 'NEW-1', status: 'used', depth: 2 } }))
    expect(v.replacement?.statusKey).toBe('aftersale.state.used')
    expect(v.actions).toEqual(['copy_replacement'])
  })

  it('replaced 的新码状态不认得：statusKey 用 unknown', () => {
    const v = afterSaleView(base({ status: 'replaced', replacement: { code: 'NEW-1', status: 'weird', depth: 1 } }))
    expect(v.replacement?.statusKey).toBe('aftersale.state.unknown')
    expect(v.actions).toEqual(['copy_replacement'])
  })

  it('replaced 且 replaced_unknown：请联系售卖方，没有按钮', () => {
    const v = afterSaleView(base({ status: 'replaced', replaced_unknown: true }))
    expect(v.titleKey).toBe('aftersale.state.replaced_unknown')
    expect(v.replacement).toBeUndefined()
    expect(v.actions).toEqual([])
  })

  it('replaced 既无 replacement 也无 replaced_unknown：新码暂时查不到，没有按钮', () => {
    const v = afterSaleView(base({ status: 'replaced' }))
    expect(v.titleKey).toBe('aftersale.state.replaced_pending')
    expect(v.actions).toEqual([])
  })

  it('unsupported：暂不支持在线查询', () => {
    const v = afterSaleView(base({ status: 'unsupported' }))
    expect(v.titleKey).toBe('aftersale.state.unsupported')
    expect(v.actions).toEqual([])
  })

  it('disabled 与 expired 是正常状态，没有按钮', () => {
    const d = afterSaleView(base({ status: 'disabled' }))
    expect(d.titleKey).toBe('aftersale.state.disabled')
    expect(d.actions).toEqual([])
    const e = afterSaleView(base({ status: 'expired' }))
    expect(e.titleKey).toBe('aftersale.state.expired')
    expect(e.actions).toEqual([])
  })

  it('unknown 状态与 afterSaleView 未知状态兜底：不抛错', () => {
    const v = afterSaleView(base({ status: 'weird' }))
    expect(v.titleKey).toBe('aftersale.state.unknown')
    expect(v.actions).toEqual([])
    expect(afterSaleView(base({ status: 'unknown', message: '暂时查不到，请稍后再试' })).titleKey).toBe('aftersale.state.unknown')
  })

  it('guide 的 11 个编号逐个产出 aftersale.guide.<编号>', () => {
    expect(GUIDE_CODES).toHaveLength(11)
    for (const g of GUIDE_CODES) {
      expect(afterSaleView(base({ status: 'failed', guide: g })).guideKey).toBe(`aftersale.guide.${g}`)
    }
  })

  it('guide 不在编号里：不给 guideKey', () => {
    expect(afterSaleView(base({ status: 'failed', guide: 'nope' })).guideKey).toBeUndefined()
    expect(afterSaleView(base({ status: 'failed', guide: '' })).guideKey).toBeUndefined()
  })

  it('provider_note 原样带出', () => {
    expect(afterSaleView(base({ status: 'failed', provider_note: 'card ***' })).note).toBe('card ***')
  })
})
