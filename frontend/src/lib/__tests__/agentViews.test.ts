import { describe, expect, it } from 'vitest'
import {
  CODE_STATUS_LABELS, LEDGER_KIND_LABELS, orderPreview, validQuantity, beijingRange,
  codesQuery, exportBody, EXPORT_MAX, ledgerQuery, defaultLedgerRange, ymdBeijing,
  isDefiniteOrderFailure, orderFailureKind, orderLockState, orderRetry, orderKeeper,
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
  it('validQuantity 严格匹配字符串', () => {
    for (const bad of ['1e2', '0x10', ' 3', '3 ', '+3', '-1', '1.0', '٣']) expect(validQuantity(bad), bad).toBeNull()
    expect(validQuantity('007')).toBe(7)
    expect(validQuantity(5)).toBe(5)
  })
  it('isDefiniteOrderFailure', () => {
    for (const c of ['insufficient_balance', 'invalid_request', 'product_unavailable', 'product_not_ready', 'price_changed', 'request_no_conflict']) expect(isDefiniteOrderFailure(c), c).toBe(true)
    for (const c of ['busy', 'maintenance', 'rate_limited', '', 'whatever']) expect(isDefiniteOrderFailure(c), c).toBe(false)
  })
  it('orderFailureKind：429 不是明确失败也不锁定', () => {
    expect(orderFailureKind(429, 'rate_limited')).toBe('rate_limited')
    expect(orderFailureKind(429, '')).toBe('rate_limited')
    expect(orderFailureKind(409, 'price_changed')).toBe('definite')
    expect(orderFailureKind(503, 'busy')).toBe('unknown')
    expect(orderFailureKind(0, '')).toBe('unknown')
    expect(isDefiniteOrderFailure('rate_limited')).toBe(false)
  })
  it('orderLockState：结果未知时锁定', () => {
    expect(orderLockState({ busy: false, confirming: false, hasPending: false })).toEqual({ inputsDisabled: false, unknown: false })
    expect(orderLockState({ busy: true, confirming: false, hasPending: false }).inputsDisabled).toBe(true)
    expect(orderLockState({ busy: false, confirming: true, hasPending: false }).inputsDisabled).toBe(true)
    expect(orderLockState({ busy: true, confirming: false, hasPending: true })).toEqual({ inputsDisabled: true, unknown: false })
    expect(orderLockState({ busy: false, confirming: false, hasPending: true })).toEqual({ inputsDisabled: true, unknown: true })
  })
  it('模块级请求号与待重试内容', () => {
    const p = { product_code: 'a', quantity: 1, note: '', expected_unit_price_cents: 100 }
    const k1 = orderKeeper.keyFor({ product_code: 'a', quantity: 1, note: '' })
    orderRetry.pending = p
    expect(orderKeeper.keyFor({ product_code: 'a', quantity: 1, note: '' })).toBe(k1)
    orderKeeper.done(); orderRetry.pending = null
    expect(orderKeeper.keyFor({ product_code: 'a', quantity: 1, note: '' })).not.toBe(k1)
  })
})
