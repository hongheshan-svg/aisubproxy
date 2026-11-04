// 代理商后台的请求封装：只带 cookie，不碰管理员的 authStore 和令牌。
import { fmtUsd } from './agentMoney'

export const AGENT_API_BASE = '/api/v1/agent'

export type AgentResult<T = any> = { ok: boolean; status: number; data: T }

function defaultUnauthorized() {
  if (typeof location !== 'undefined') location.assign('/agent/login')
}
let onAgentUnauthorized: () => void = defaultUnauthorized

/** 替换 401 的处理方式（传 null 恢复默认：跳 /agent/login）。 */
export function setAgentUnauthorizedHandler(fn: (() => void) | null) {
  onAgentUnauthorized = fn || defaultUnauthorized
}

function csrfToken(): string {
  if (typeof document === 'undefined') return ''
  for (const part of String(document.cookie || '').split(';')) {
    const i = part.indexOf('=')
    if (i < 0) continue
    if (part.slice(0, i).trim() === 'agent_csrf') {
      const v = part.slice(i + 1).trim()
      try { return decodeURIComponent(v) } catch { return v }
    }
  }
  return ''
}

export async function agentApi<T = any>(path: string, method = 'GET', body?: unknown): Promise<AgentResult<T>> {
  const m = method.toUpperCase()
  const headers: Record<string, string> = {}
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (m !== 'GET' && m !== 'HEAD') {
    const t = csrfToken()
    if (t) headers['X-CSRF-Token'] = t
  }
  const res = await fetch(AGENT_API_BASE + path, {
    method: m,
    credentials: 'include',
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  let data: any = {}
  try { data = await res.json() } catch { data = {} }
  if (res.status === 401 && !path.startsWith('/auth/')) onAgentUnauthorized()
  return { ok: res.ok, status: res.status, data: data as T }
}

/** 设计文档第 9 节：错误编号 → 中文提示。 */
export const AGENT_ERROR_TEXT: Record<string, string> = {
  insufficient_balance: '余额不足',
  product_unavailable: '该商品暂不可售',
  product_not_ready: '该商品暂时无法出码，请稍后再试',
  maintenance: '系统维护中，暂不能下单',
  request_no_conflict: '请求号已用于另一笔订单',
  busy: '系统繁忙，请稍后重试',
  rate_limited: '操作太频繁，请稍后再试',
  price_changed: '价格已变化，请刷新后重新下单',
}

const FALLBACK = '操作失败，请稍后再试'

export function agentErrorText(data: any): string {
  if (data && typeof data.message === 'string' && data.message) return data.message
  const code = data && typeof data.error === 'string' ? data.error : ''
  if (code === 'insufficient_balance') {
    const s = Number(data.shortfall_cents)
    return Number.isFinite(s) && s > 0 ? `余额不足，还差 ${fmtUsd(s)}` : AGENT_ERROR_TEXT[code]
  }
  return AGENT_ERROR_TEXT[code] || FALLBACK
}

/** 登录后的跳转目标：只接受 /agent 开头的站内路径，其余一律回 /agent（防开放跳转）。 */
export function safeAgentRedirect(v: unknown): string {
  const s = Array.isArray(v) ? v[0] : v
  if (typeof s !== 'string') return '/agent'
  if (!/^\/agent([/?#]|$)/.test(s) || s.includes('\\') || s.startsWith('/agent//')) return '/agent'
  if (/[\u0000-\u001f]/.test(s)) return '/agent'
  return s
}
