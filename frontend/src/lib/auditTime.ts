import { fmtBeijing, parseServerTimeMs } from './mwwebhook'

/** 审计日志时间显示为北京时间（YYYY-MM-DD HH:MM:SS）。空值显示「-」，解析不了原样返回。 */
export function fmtAuditTime(value?: string): string {
  const raw = (value || '').trim()
  if (!raw) return '-'
  const ms = parseServerTimeMs(raw)
  if (Number.isNaN(ms)) return raw
  return fmtBeijing(Math.floor(ms / 1000))
}
