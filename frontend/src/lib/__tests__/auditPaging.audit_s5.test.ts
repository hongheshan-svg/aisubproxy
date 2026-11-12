import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { appendAuditPage, auditLogsUrl, auditPageResult, nextAuditCursor } from '../auditPaging'

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

// 接口报错时不能显示成「暂无审计记录」（F2-08）
describe('审计日志读取失败', () => {
  it('非 2xx 算失败，不给出空列表', () => {
    expect(auditPageResult(false, { error: 'boom' })).toEqual({ failed: true })
    expect(auditPageResult(false, { logs: [] })).toEqual({ failed: true })
  })
  it('2xx 但响应体不是对象（解析失败）也算失败', () => {
    expect(auditPageResult(true, null)).toEqual({ failed: true })
    expect(auditPageResult(true, 'oops')).toEqual({ failed: true })
  })
  it('2xx 且列表为空：是真的没有记录，不算失败', () => {
    expect(auditPageResult(true, { logs: [], has_more: false })).toEqual({ failed: false, logs: [], cursor: 0 })
    expect(auditPageResult(true, {})).toEqual({ failed: false, logs: [], cursor: 0 })
  })
  it('2xx 有记录：带上游标', () => {
    expect(auditPageResult(true, { logs: [{ id: 9 }], has_more: true, next_before_id: 9 })).toEqual({ failed: false, logs: [{ id: 9 }], cursor: 9 })
  })
  it('页面用 loadError 区分「读取失败」和「暂无记录」，失败时不清空已有记录', () => {
    const src = readFileSync(resolve(__dirname, '../../views/admin/AuditLogs.vue'), 'utf8')
    expect(src).toContain('const loadError = ref(false)')
    // 卡片列表和表格共用 statusText
    expect(src).toContain("if (logs.value.length === 0) return loadError.value ? '读取失败，请点刷新重试' : '暂无审计记录'")
    expect(src.match(/\{\{ statusText \}\}/g)?.length).toBe(2)
    expect(src).toContain("dialog.toast('读取审计日志失败', 'err')")
    expect(src).toContain("dialog.toast('读取失败，请再点一次', 'err')")
    expect(src).not.toContain('logs.value = []')
  })
})
