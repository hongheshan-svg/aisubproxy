import { describe, expect, it } from 'vitest'
import {
  canFreeze, canUnfreeze, importProductOptions, previewMatchesInput, cancelWhen, importSweepWarning, releaseConfirmText, releaseDoneText, releaseWillBeUsedUp, usedStateNote,
} from '../mwcardtext'

const policy = { max_uses_per_card: 3, cancel_after_days: 7 }

describe('mwcardtext lib', () => {
  it('cancelWhen: 0 天写立即注销，否则 N 天后注销', () => {
    expect(cancelWhen(0)).toBe('立即注销')
    expect(cancelWhen(7)).toBe('7 天后注销')
  })

  it('releaseWillBeUsedUp: 取过卡号按 use_count + 1 比上限，没取过按 use_count', () => {
    expect(releaseWillBeUsedUp({ use_count: 2, lease_sensitive: true }, policy)).toBe(true)
    expect(releaseWillBeUsedUp({ use_count: 1, lease_sensitive: true }, policy)).toBe(false)
    expect(releaseWillBeUsedUp({ use_count: 2, lease_sensitive: false }, policy)).toBe(false)
    expect(releaseWillBeUsedUp({ use_count: 5, lease_sensitive: true }, policy)).toBe(true)
  })

  it('releaseWillBeUsedUp: 策略没加载时不下结论', () => {
    expect(releaseWillBeUsedUp({ use_count: 9, lease_sensitive: true }, null)).toBeNull()
  })

  it('releaseConfirmText: 会回到可用', () => {
    const t = releaseConfirmText({ card_id: 'c1', last4: '1234', use_count: 0, lease_sensitive: true }, policy)
    expect(t).toContain('c1')
    expect(t).toContain('1234')
    expect(t).toContain('本次租约已取过卡号')
    expect(t).toContain('余额清零后回到可用')
    expect(t).not.toContain('已用满')
  })
  it('releaseConfirmText: 没取过卡号', () => {
    const t = releaseConfirmText({ card_id: 'c1', last4: '1234', use_count: 2, lease_sensitive: false }, policy)
    expect(t).toContain('本次租约没有取过卡号')
    expect(t).toContain('余额清零后回到可用')
  })
  it('releaseConfirmText: 会用满', () => {
    const t = releaseConfirmText({ card_id: 'c1', last4: '1234', use_count: 2, lease_sensitive: true }, policy)
    expect(t).toContain('已用满，将清零、冻结，并在 7 天后注销')
    expect(t).not.toContain('回到可用')
    expect(releaseConfirmText({ card_id: 'c1', last4: '1234', use_count: 2, lease_sensitive: true }, { ...policy, cancel_after_days: 0 }))
      .toContain('已用满，将清零、冻结，并立即注销')
  })
  it('releaseConfirmText: 策略没加载时说明去向取决于使用次数', () => {
    const t = releaseConfirmText({ card_id: 'c1', last4: '1234', use_count: 2, lease_sensitive: true }, null)
    expect(t).toContain('取决于使用次数')
  })

  it('releaseDoneText: 提示卡的去向', () => {
    expect(releaseDoneText({ use_count: 0, lease_sensitive: true }, policy)).toBe('已释放，卡会清零后回到可用')
    expect(releaseDoneText({ use_count: 2, lease_sensitive: true }, policy)).toBe('已释放，卡已用满，将清零、冻结，并在 7 天后注销')
    expect(releaseDoneText({ use_count: 2, lease_sensitive: true }, null)).toBe('已释放')
  })

  it('usedStateNote: used 且有 cancel_after 显示北京时间注销时间', () => {
    // 1793694600 = 2026-11-03 08:30 UTC = 16:30 北京
    expect(usedStateNote({ pool_state: 'used', cancel_after: 1793694600 })).toBe('已冻结，11-03 16:30 注销')
    expect(usedStateNote({ pool_state: 'used', cancel_after: 1793736309 })).toBe('已冻结，11-04 04:05 注销')
  })
  it('usedStateNote: used 但没冻结显示等待清零、冻结；其他状态不显示', () => {
    expect(usedStateNote({ pool_state: 'used', cancel_after: null })).toBe('等待清零、冻结')
    expect(usedStateNote({ pool_state: 'used' })).toBe('等待清零、冻结')
    expect(usedStateNote({ pool_state: 'available', cancel_after: 1793694600 })).toBe('')
    expect(usedStateNote({ pool_state: 'leased', cancel_after: null })).toBe('')
  })

  it('importSweepWarning: 有余额才警告', () => {
    expect(importSweepWarning(0)).toBe('')
    expect(importSweepWarning(1234)).toBe('导入后，卡上 12.34 USD 会自动转回主钱包（清零），然后卡才变成可用。')
  })

  it('canFreeze / canUnfreeze 只按平台状态', () => {
    expect(canFreeze('ACTIVE')).toBe(true)
    expect(canFreeze('REVOKED')).toBe(false)
    expect(canFreeze('CANCELED')).toBe(false)
    expect(canFreeze('')).toBe(false)
    expect(canUnfreeze('REVOKED')).toBe(true)
    expect(canUnfreeze('ACTIVE')).toBe(false)
    expect(canUnfreeze('CANCELED')).toBe(false)
  })

  it('退役原因非空时按用满处理（后端：retire_reason <> \'\' 或用满都转 used）', () => {
    const c = { card_id: 'c1', last4: '1234', use_count: 0, lease_sensitive: false, retire_reason: '健康度' }
    expect(releaseWillBeUsedUp(c, policy)).toBe(true)
    expect(releaseWillBeUsedUp(c, null)).toBe(true)
    expect(releaseWillBeUsedUp({ ...c, retire_reason: '' }, policy)).toBe(false)
    const t = releaseConfirmText(c, policy)
    expect(t).toContain('已退役（原因 健康度），将清零、冻结，并在 7 天后注销')
    expect(t).not.toContain('回到可用')
    expect(releaseConfirmText(c, { ...policy, cancel_after_days: 0 })).toContain('已退役（原因 健康度），将清零、冻结，并立即注销')
    expect(releaseDoneText(c, policy)).toBe('已释放，卡已退役（原因 健康度），将清零、冻结，并在 7 天后注销')
    expect(releaseDoneText(c, null)).toContain('已退役（原因 健康度），将清零、冻结')
  })

  it('importProductOptions: 列出全部产品，候选排前面并标「（匹配）」，原有顺序保持', () => {
    const ps = [{ product_id: 'a' }, { product_id: 'b' }, { product_id: 'c' }, { product_id: 'd' }]
    const out = importProductOptions(ps, ['c', 'a'])
    expect(out.map((o) => o.product_id)).toEqual(['a', 'c', 'b', 'd'])
    expect(out.map((o) => o.matched)).toEqual([true, true, false, false])
    expect(importProductOptions(ps, []).map((o) => o.product_id)).toEqual(['a', 'b', 'c', 'd'])
    expect(importProductOptions(ps, undefined).every((o) => !o.matched)).toBe(true)
  })

  it('previewMatchesInput: 查询结果只在卡 ID 输入仍是查询时的那个才有效', () => {
    expect(previewMatchesInput('c1', ' c1 ')).toBe(true)
    expect(previewMatchesInput('c1', 'c2')).toBe(false)
    expect(previewMatchesInput('c1', '')).toBe(false)
    expect(previewMatchesInput(undefined, 'c1')).toBe(false)
  })
})
