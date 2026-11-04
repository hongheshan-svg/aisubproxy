import { describe, expect, it } from 'vitest'
import {
  CODE_STATUS_LABELS, LEDGER_KIND_LABELS, orderPreview, validQuantity, beijingRange,
  codesQuery, exportBody, EXPORT_MAX, ledgerQuery, defaultLedgerRange, ymdBeijing,
} from '../agentViews'

describe('agentViews', () => {
  it('标签', () => {
    expect(CODE_STATUS_LABELS.used).toBe('已开通')
    expect(LEDGER_KIND_LABELS.topup).toBe('充值')
  })
  it('orderPreview', () => {
    expect(orderPreview(500, 3, 1000)).toEqual({ totalCents: 1500, afterCents: -500, enough: false })
    expect(orderPreview(500, 2, 1000)).toEqual({ totalCents: 1000, afterCents: 0, enough: true })
  })
  it('validQuantity', () => {
    expect(validQuantity('3')).toBe(3)
    expect(validQuantity(0)).toBeNull()
    expect(validQuantity(201)).toBeNull()
    expect(validQuantity(200)).toBe(200)
    expect(validQuantity(1.5)).toBeNull()
    expect(validQuantity('')).toBeNull()
    expect(validQuantity('abc')).toBeNull()
  })
  it('beijingRange', () => {
    expect(beijingRange('2026-11-01', '2026-11-04')).toEqual({ from: 1793462400, to: 1793808000 })
    expect(beijingRange('2026-11-05', '2026-11-04')).toBeNull()
    expect(beijingRange('bad', '2026-11-04')).toBeNull()
  })
  it('codesQuery 只带非空条件', () => {
    expect(codesQuery({ product: 'a', status: '', orderId: '', from: '', to: '' }, 2, 50)).toBe('?product=a&page=2&page_size=50')
    expect(codesQuery({ product: '', status: 'used', orderId: '12', from: '2026-11-01', to: '2026-11-04' }, 1, 50))
      .toBe('?status=used&order_id=12&from=1793462400&to=1793808000&page=1&page_size=50')
  })
  it('exportBody 勾选与筛选两种', () => {
    expect(exportBody({ ids: [3, 4] })).toEqual({ ids: [3, 4] })
    expect(exportBody({ filter: { product: 'a', status: '', orderId: '7', from: '2026-11-01', to: '2026-11-04' } }))
      .toEqual({ filter: { product: 'a', status: '', order_id: 7, from: 1793462400, to: 1793808000 } })
    expect(EXPORT_MAX).toBe(10000)
  })
  it('流水查询串与默认范围', () => {
    expect(ledgerQuery(100, 200)).toBe('from=100&to=200')
    const r = defaultLedgerRange(1793808000 + 3600)
    expect(r.to).toBe(ymdBeijing(1793808000 + 3600))
    expect(r.from).toBe('2026-10-06')
  })
})
