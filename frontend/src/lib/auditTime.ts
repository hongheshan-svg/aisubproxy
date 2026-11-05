import { fmtBeijing } from './mwwebhook'

// 后端审计时间来自 SQLite CURRENT_TIMESTAMP：UTC，`YYYY-MM-DD HH:MM:SS`，没有时区标记。
// 浏览器对这种带空格的格式解析不一致（Safari 返回 Invalid Date），所以先按 UTC 手动拆开。
const SQLITE_UTC = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2}):(\d{2})(?:\.\d+)?$/

/** 审计日志时间显示为北京时间（YYYY-MM-DD HH:MM:SS）。空值显示「-」，解析不了原样返回。 */
export function fmtAuditTime(value?: string): string {
  const raw = (value || '').trim()
  if (!raw) return '-'
  const m = SQLITE_UTC.exec(raw)
  const ms = m
    ? Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]), Number(m[4]), Number(m[5]), Number(m[6]))
    : Date.parse(raw)
  if (Number.isNaN(ms)) return raw
  return fmtBeijing(Math.floor(ms / 1000))
}
