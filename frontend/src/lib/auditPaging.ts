// 审计日志分页（审计 B-8）：后端按 id 倒序，用 before_id 游标翻页。

export interface AuditPageResponse<T extends { id: number }> {
  logs?: T[]
  has_more?: boolean
  next_before_id?: number
}

export const AUDIT_PAGE_SIZE = 200

export function auditLogsUrl(limit: number, beforeId?: number): string {
  const q = new URLSearchParams({ limit: String(limit) })
  if (beforeId && beforeId > 0) q.set('before_id', String(beforeId))
  return `/api/v1/admin/audit-logs?${q.toString()}`
}

/** 把新一页接在已有记录后面，按 id 去重（刷新期间有新记录写入也不会重复）。 */
export function appendAuditPage<T extends { id: number }>(existing: T[], incoming: T[]): T[] {
  const seen = new Set(existing.map((x) => x.id))
  return existing.concat(incoming.filter((x) => !seen.has(x.id)))
}

/** 下一页的游标；没有更多时返回 0。 */
export function nextAuditCursor<T extends { id: number }>(page: AuditPageResponse<T>): number {
  return page.has_more && page.next_before_id && page.next_before_id > 0 ? page.next_before_id : 0
}
