import { describe, expect, it } from 'vitest'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'
import { GUIDE_CODES, afterSaleView, type LookupResult } from '../aftersale'

function flat(o: unknown, prefix = ''): string[] {
  if (o && typeof o === 'object') {
    return Object.entries(o as Record<string, unknown>).flatMap(([k, v]) => flat(v, prefix ? `${prefix}.${k}` : k))
  }
  return [prefix]
}

describe('aftersale 语言包', () => {
  const zk = flat((zh as any).aftersale).sort()
  const ek = flat((en as any).aftersale).sort()

  it('中英文键一致且非空', () => {
    expect(zk.length).toBeGreaterThan(0)
    expect(zk).toEqual(ek)
  })

  it('包含 11 个 guide 编号', () => {
    for (const g of GUIDE_CODES) expect(zk).toContain(`guide.${g}`)
  })

  it('所有文案是非空字符串，且不含 vue-i18n 特殊字符', () => {
    const walk = (o: any): string[] => Object.values(o).flatMap((v) => (typeof v === 'string' ? [v] : walk(v)))
    for (const t of [...walk((zh as any).aftersale), ...walk((en as any).aftersale)]) {
      expect(t.trim()).not.toBe('')
      expect(t).not.toMatch(/[@|{}]/)
    }
  })

  it('afterSaleView 产出的所有键在中英文语言包里都存在', () => {
    const statuses = ['unused', 'used', 'processing', 'failed', 'replaced', 'unsupported', 'disabled', 'expired', 'unknown', 'weird']
    const b = (o: Partial<LookupResult>): LookupResult => ({ cdk_code: 'C', status: 'unused', used: false, ...o })
    const inputs: LookupResult[] = [
      b({}),
      b({ can_resubmit: true, guide: 'retry_later' }),
      b({ status: 'used', used: true, plan: 'P', plan_kind: 'subscription', used_at: '2026-03-04T05:06:07Z', expires_at: '2026-04-04T05:06:07Z', account_email: 'a***@b.com', renewal: 'off' }),
      b({ status: 'used', used: true, renewal: 'closing' }),
      b({ status: 'used', used: true, plan_kind: 'credit', plan: 'P', used_at: '2026-03-04T05:06:07Z' }),
      b({ status: 'failed', can_resubmit: true, guide: 'retry_later' }),
      b({ status: 'failed', can_resubmit: true }),
      b({ status: 'failed', can_resubmit: false }),
      b({ status: 'replaced', replaced_unknown: true }),
      b({ status: 'replaced' }),
      ...statuses.map((st) => b({ status: 'replaced', replacement: { code: 'N', status: st, depth: 1 } })),
      ...statuses.map((st) => b({ status: st })),
      ...GUIDE_CODES.map((g) => b({ status: 'failed', guide: g })),
    ]
    const keys = new Set<string>()
    for (const r of inputs) {
      const v = afterSaleView(r)
      keys.add(v.titleKey)
      if (v.guideKey) keys.add(v.guideKey)
      if (v.renewalKey) keys.add(v.renewalKey)
      if (v.replacement) keys.add(v.replacement.statusKey)
      v.fields.forEach((f) => keys.add(f.labelKey))
    }
    const zset = new Set(zk.map((k) => `aftersale.${k}`))
    const eset = new Set(ek.map((k) => `aftersale.${k}`))
    for (const k of keys) {
      expect(zset.has(k), `zh 缺 ${k}`).toBe(true)
      expect(eset.has(k), `en 缺 ${k}`).toBe(true)
    }
  })
})
