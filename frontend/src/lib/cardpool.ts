// 卡池 / 钱包后台页面的公共函数。
import { authFetch } from './api'

export interface Resp<T = any> { ok: boolean; status: number; data: T }

/** 调用 /api/v1/admin/cardpool 下的接口；FormData 原样传，其余 JSON.stringify。 */
export async function poolApi<T = any>(path: string, method = 'GET', body?: unknown): Promise<Resp<T>> {
  try {
    const r = await authFetch('/api/v1/admin/cardpool' + path, {
      method,
      body: body === undefined ? undefined : body instanceof FormData ? body : JSON.stringify(body),
    })
    const data = await r.json().catch(() => ({}))
    return { ok: r.ok, status: r.status, data }
  } catch {
    return { ok: false, status: 0, data: { error: '网络错误' } as any }
  }
}

export function errText(r: Resp, fallback: string): string {
  return (r.data && r.data.error) || fallback
}

/** 502 且 outcome=unknown：平台是否已执行不确定，需稍后刷新确认。 */
export function isUnknownOutcome(r: Resp): boolean {
  return r.status === 502 && r.data?.outcome === 'unknown'
}

export function money(cents?: number | null): string {
  return ((cents || 0) / 100).toFixed(2)
}

export function fmtTs(ts?: number | null): string {
  if (!ts) return '—'
  return new Date(ts * 1000).toLocaleString()
}

export const ORDER_KINDS: { value: string; label: string }[] = [
  { value: 'apply', label: '开卡' },
  { value: 'recharge', label: '转入' },
  { value: 'withdraw', label: '转出' },
  { value: 'status', label: '改状态' },
  { value: 'conversion', label: '兑换' },
  { value: 'wallet_transfer', label: '钱包转账' },
]

export function kindLabel(k: string): string {
  return ORDER_KINDS.find((x) => x.value === k)?.label || k
}

/** 金额字符串（最多 2 位小数、整数部分最多 12 位、必须大于 0）转分；不合法返回 null。 */
export function parseAmountCents(s: string): number | null {
  const t = String(s ?? '').trim()
  const m = /^(\d{1,12})(?:\.(\d{1,2}))?$/.exec(t)
  if (!m) return null
  const cents = Number(m[1]) * 100 + Number((m[2] || '').padEnd(2, '0'))
  return cents > 0 ? cents : null
}

export function hasEmoji(s: string): boolean {
  return /\p{Extended_Pictographic}/u.test(s)
}
