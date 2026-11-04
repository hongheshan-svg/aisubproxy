import { describe, expect, it } from 'vitest'
import { appendAuditPage, auditLogsUrl, nextAuditCursor } from '../auditPaging'

describe('审计日志分页（B-8）', () => {
  it('第一页不带 before_id，翻页带游标', () => {
    expect(auditLogsUrl(200)).toBe('/api/v1/admin/audit-logs?limit=200')
    expect(auditLogsUrl(200, 55)).toBe('/api/v1/admin/audit-logs?limit=200&before_id=55')
    expect(auditLogsUrl(200, 0)).toBe('/api/v1/admin/audit-logs?limit=200')
  })
  it('追加时按 id 去重', () => {
    expect(appendAuditPage([{ id: 3 }, { id: 2 }], [{ id: 2 }, { id: 1 }]).map((x) => x.id)).toEqual([3, 2, 1])
  })
  it('没有更多时游标为 0', () => {
    expect(nextAuditCursor({ logs: [], has_more: false, next_before_id: 0 })).toBe(0)
    expect(nextAuditCursor({ logs: [], has_more: true, next_before_id: 9 })).toBe(9)
    expect(nextAuditCursor({})).toBe(0)
  })
})
