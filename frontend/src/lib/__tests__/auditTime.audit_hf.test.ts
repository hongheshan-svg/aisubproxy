import { describe, expect, it } from 'vitest'
import { fmtAuditTime } from '../auditTime'

describe('fmtAuditTime：后端 UTC 无时区字符串按北京时间显示', () => {
  it('SQLite CURRENT_TIMESTAMP 格式（UTC）转北京时间，不是原样显示', () => {
    expect(fmtAuditTime('2026-11-04 03:00:00')).toBe('2026-11-04 11:00:00')
    expect(fmtAuditTime('2026-11-04 20:30:05')).toBe('2026-11-05 04:30:05')
  })
  it('带 Z 或偏移的 RFC3339 同样转北京时间', () => {
    expect(fmtAuditTime('2026-11-04T03:00:00Z')).toBe('2026-11-04 11:00:00')
    expect(fmtAuditTime('2026-11-04T11:00:00+08:00')).toBe('2026-11-04 11:00:00')
  })
  it('空值显示横线，解析不了原样返回，不出现 Invalid Date', () => {
    expect(fmtAuditTime('')).toBe('-')
    expect(fmtAuditTime(undefined)).toBe('-')
    expect(fmtAuditTime('not-a-time')).toBe('not-a-time')
  })
})
