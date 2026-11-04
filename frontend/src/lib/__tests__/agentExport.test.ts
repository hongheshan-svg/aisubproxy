import { describe, expect, it } from 'vitest'
import { codesCsv, codesTxt } from '../agentExport'

describe('codesTxt', () => {
  it('一行一张，跳过取不回的并计数', () => {
    expect(codesTxt([{ id: 1, code: 'A' }, { id: 2, code: '', unavailable: true }])).toEqual({ text: 'A\n', skipped: 1 })
  })
  it('全部跳过时文本为空', () => {
    expect(codesTxt([{ id: 2, code: '', unavailable: true }])).toEqual({ text: '', skipped: 1 })
  })
})

describe('codesCsv', () => {
  it('BOM、表头、转义、无法取回', () => {
    const csv = codesCsv([
      { id: 1, code: 'A', product_code: 'p', plan: 'plus', region: 'US', status: 'unused', created_at: 1, order_id: 7, note: 'a,"b"' },
      { id: 2, code: '', unavailable: true },
    ])
    expect(csv.startsWith('﻿')).toBe(true)
    const lines = csv.slice(1).split('\n')
    expect(lines[0]).toBe('code,product_code,plan,region,status,purchased_at,order_id,note')
    expect(lines[1]).toBe('A,p,plus,US,unused,1970-01-01 08:00:01,7,"a,""b"""')
    expect(lines[2].startsWith('无法取回，请联系管理员,')).toBe(true)
  })
})
