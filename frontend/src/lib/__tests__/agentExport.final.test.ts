import { describe, expect, it } from 'vitest'
import { codesCsv, isStaleCode, staleCodesNotice } from '../agentExport'

describe('订单码的状态提示（F2）', () => {
  it('N 张非未使用时提示文字和计数正确', () => {
    const codes = [
      { id: 1, code: 'A', status: 'unused', replaced_by: null },
      { id: 2, code: 'B', status: 'disabled', replaced_by: 9 },
      { id: 3, code: 'C', status: 'used' },
      { id: 4, code: 'D', status: 'disabled' },
    ]
    expect(staleCodesNotice(codes)).toBe('含 3 张已禁用/已使用/已换码，新码请到「我的码」查看')
    expect(codes.map(isStaleCode)).toEqual([false, true, true, true])
  })
  it('全部未使用或没有状态时不提示', () => {
    expect(staleCodesNotice([{ id: 1, code: 'A', status: 'unused' }, { id: 2, code: 'B' }])).toBe('')
    expect(staleCodesNotice([])).toBe('')
  })
  it('CSV 带状态列', () => {
    const csv = codesCsv([{ id: 1, code: 'A', status: 'disabled' }])
    expect(csv.split('\n')[0]).toContain(',status,')
    expect(csv.split('\n')[1]).toContain(',disabled,')
  })
})
