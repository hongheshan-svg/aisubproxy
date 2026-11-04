import { describe, expect, it } from 'vitest'
import { afterSaleView, batchSummary, fmtLookupTime, GUIDE_CODES, type LookupResult } from '../aftersale'

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

  it('used：套餐、开通时间、邮箱按顺序出现，时间为北京时间；后端给了到期时间也不显示', () => {
    const v = afterSaleView(base({
      status: 'used', used: true, plan: 'ChatGPT Plus', plan_kind: 'subscription',
      used_at: '2026-03-04T05:06:07Z', expires_at: '2026-04-04T05:06:07Z', account_email: 'a***@g***.com', renewal: 'off',
    }))
    expect(v.titleKey).toBe('aftersale.state.used')
    expect(v.tone).toBe('ok')
    expect(v.fields).toEqual([
      { labelKey: 'aftersale.field.plan', value: 'ChatGPT Plus' },
      { labelKey: 'aftersale.field.usedAt', value: '2026-03-04 13:06:07' },
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

  it('guide 的 11 个编号逐个产出 aftersale.guide.<编号>（带服务商原文时）', () => {
    expect(GUIDE_CODES).toHaveLength(11)
    for (const g of GUIDE_CODES) {
      expect(afterSaleView(base({ status: 'failed', guide: g, provider_note: 'x' })).guideKey).toBe(`aftersale.guide.${g}`)
    }
  })

  it('guide 不在编号里：不给 guideKey', () => {
    expect(afterSaleView(base({ status: 'failed', guide: 'nope' })).guideKey).toBeUndefined()
    expect(afterSaleView(base({ status: 'failed', guide: '' })).guideKey).toBeUndefined()
  })

  it('provider_note 原样带出', () => {
    expect(afterSaleView(base({ status: 'failed', provider_note: 'card ***' })).note).toBe('card ***')
  })

  it('failed 且 can_resubmit 但没有 guide：仍是码已恢复，可重新提交，不给 guideKey', () => {
    const v = afterSaleView(base({ status: 'failed', can_resubmit: true }))
    expect(v.titleKey).toBe('aftersale.state.failed_restored')
    expect(v.actions).toEqual(['resubmit'])
    expect(v.guideKey).toBeUndefined()
  })

  it('点数码不给 renewalKey', () => {
    const v = afterSaleView(base({ status: 'used', used: true, plan_kind: 'credit', renewal: 'off' }))
    expect(v.renewalKey).toBeUndefined()
  })

  it('processing 带 guide 时照给 guideKey', () => {
    expect(afterSaleView(base({ status: 'processing', guide: 'manual_review' })).guideKey).toBe('aftersale.guide.manual_review')
  })

  it('provider_retry、provider_wait 没有服务商原文：改用不带「如下」的文案键', () => {
    const r = afterSaleView(base({ status: 'failed', can_resubmit: true, guide: 'provider_retry' }))
    expect(r.guideKey).toBe('aftersale.guide.provider_retry_plain')
    expect(r.note).toBeUndefined()
    const w = afterSaleView(base({ status: 'failed', can_resubmit: false, guide: 'provider_wait', provider_note: '' }))
    expect(w.guideKey).toBe('aftersale.guide.provider_wait_plain')
  })

  it('provider_retry、provider_wait 有服务商原文：仍用「原因如下」的文案键', () => {
    expect(afterSaleView(base({ status: 'failed', can_resubmit: true, guide: 'provider_retry', provider_note: 'bad' })).guideKey).toBe('aftersale.guide.provider_retry')
    expect(afterSaleView(base({ status: 'failed', guide: 'provider_wait', provider_note: 'bad' })).guideKey).toBe('aftersale.guide.provider_wait')
  })

  it('其他编号没有原文时照旧用本编号的文案键', () => {
    expect(afterSaleView(base({ status: 'failed', guide: 'manual_review' })).guideKey).toBe('aftersale.guide.manual_review')
  })

  it('换码链停在又换过码的码上：不显示那张作废的码，不给复制、去兑换', () => {
    const v = afterSaleView(base({ status: 'replaced', replacement: { code: 'OLD-B', status: 'replaced', depth: 1 } }))
    expect(v.titleKey).toBe('aftersale.state.replaced_again')
    expect(v.replacement).toBeUndefined()
    expect(v.actions).toEqual([])
    expect(JSON.stringify(v)).not.toContain('OLD-B')
  })

  describe('按钮按设计 §4 指引表：标「无」的编号不给重新兑换', () => {
    const noButton = ['credit_region_mismatch', 'manual_review', 'provider_wait']
    for (const g of GUIDE_CODES) {
      const want = noButton.includes(g) ? [] : ['resubmit']
      it(`${g}：${want.length ? '重新兑换' : '无'}`, () => {
        // 码已恢复（can_resubmit=true）是最容易误给按钮的情形，unused 和 failed 两种状态都要看
        expect(afterSaleView(base({ status: 'unused', can_resubmit: true, guide: g })).actions).toEqual(want)
        expect(afterSaleView(base({ status: 'failed', can_resubmit: true, guide: g })).actions).toEqual(want)
      })
    }
  })

  it('credit_region_mismatch 码已恢复：标题照旧是码已恢复，指引是联系售卖方换码，没有按钮', () => {
    const v = afterSaleView(base({ status: 'unused', can_resubmit: true, guide: 'credit_region_mismatch' }))
    expect(v.titleKey).toBe('aftersale.state.failed_restored')
    expect(v.guideKey).toBe('aftersale.guide.credit_region_mismatch')
    expect(v.actions).toEqual([])
  })
})

describe('fmtLookupTime', () => {
  it('RFC3339 转北京时间', () => {
    expect(fmtLookupTime('2026-11-04T09:22:37Z')).toBe('2026-11-04 17:22:37')
    expect(fmtLookupTime('2026-11-04T17:22:37+08:00')).toBe('2026-11-04 17:22:37')
    expect(fmtLookupTime('2026-11-04T09:22:37.999Z')).toBe('2026-11-04 17:22:37')
  })

  it('空或解析不了返回 undefined', () => {
    expect(fmtLookupTime(undefined)).toBeUndefined()
    expect(fmtLookupTime('')).toBeUndefined()
    expect(fmtLookupTime('not-a-time')).toBeUndefined()
  })
})

describe('batchSummary', () => {
  const row = (status: string): LookupResult => ({ cdk_code: status, status, used: status === 'used' })

  it('replaced、unsupported 各有一栏，不算进「未找到」', () => {
    const s = batchSummary(['used', 'failed', 'unused', 'processing', 'replaced', 'replaced', 'unsupported', 'unknown', 'disabled', 'expired', 'weird'].map(row))
    expect(s).toEqual({ used: 1, failed: 1, unused: 1, processing: 1, replaced: 2, unsupported: 1, unknown: 4 })
  })

  it('空列表全是 0', () => {
    expect(batchSummary([])).toEqual({ used: 0, failed: 0, unused: 0, processing: 0, replaced: 0, unsupported: 0, unknown: 0 })
  })
})
