import { describe, expect, it } from 'vitest'
import { csvRow, type LookupResult } from '../aftersale'

function row(over: Partial<LookupResult>): LookupResult {
  return { cdk_code: 'GPTD-AAAA', status: 'used', used: true, ...over } as LookupResult
}

describe('售后 CSV 防公式注入（F-9）', () => {
  it('文本单元格以 = + - @ 开头时前面加单引号', () => {
    expect(csvRow(row({ cdk_code: '=HYPERLINK("http://x")' }))).toMatch(/^"'=HYPERLINK/)
    expect(csvRow(row({ cdk_code: 'GPTD-1', account_email: '+1@evil.com' }))).toContain(`"'+1@evil.com"`)
    expect(csvRow(row({ cdk_code: 'GPTD-1', plan: '-2+3' }))).toContain(`"'-2+3"`)
    expect(csvRow(row({ cdk_code: 'GPTD-1', account_email: '@SUM(A1)' }))).toContain(`"'@SUM(A1)"`)
  })
  it('制表符和回车开头也要加单引号', () => {
    expect(csvRow(row({ cdk_code: '\t=cmd' }))).toMatch(/^"'\t=cmd"/)
    expect(csvRow(row({ cdk_code: '\r=cmd' }))).toMatch(/^"'\r=cmd"/)
  })
  it('普通文本和数字列不变', () => {
    const line = csvRow(row({ cdk_code: 'GPTD-AAAA', plan: 'plus', can_resubmit: true }))
    expect(line.split(',')[0]).toBe('"GPTD-AAAA"')
    expect(line.split(',')[2]).toBe('"1"')
    expect(csvRow(row({ cdk_code: 'GPTD-1', can_resubmit: false })).split(',')[2]).toBe('"0"')
  })
  it('内部引号照常转义，不影响注入判断', () => {
    expect(csvRow(row({ cdk_code: 'GPTD-1', plan: 'a"b' }))).toContain(`"a""b"`)
  })
})
