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

/**
 * 下载文件（如流水 CSV）：用 fetch 拿二进制，带 cookie。
 * 401 和普通 API 一样走统一的会话失效处理；非 2xx 返回 JSON 错误体，不返回 blob。
 */
export async function agentDownload(path: string): Promise<AgentResult<any> & { blob?: Blob }> {
  const res = await fetch(AGENT_API_BASE + path, { method: 'GET', credentials: 'include' })
  if (res.status === 401 && !path.startsWith('/auth/')) onAgentUnauthorized()
  if (!res.ok) {
    let data: any = {}
    try { data = await res.json() } catch { data = {} }
    return { ok: false, status: res.status, data }
  }
  return { ok: true, status: res.status, data: {}, blob: await res.blob() }
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
  invalid_credentials: '登录名或密码错误',
  totp_invalid: '动态码错误',
  pre_expired: '登录已过期，请重新输入密码',
  weak_password: '新密码不符合要求',
  unauthorized: '登录已失效，请重新登录',
  csrf_invalid: '页面已过期，请刷新后重试',
  price_changed: '价格已变化，请刷新后重新下单',
  totp_bound: '已绑定两步验证，请直接登录',
  internal: '系统出错，请稍后再试',
  not_found: '内容不存在',
  invalid_request: '请求参数不正确',
  export_too_large: '一次最多导出 10000 张，请缩小筛选范围',
  range_too_large: '日期范围不能超过 366 天',
}

const FALLBACK = '操作失败，请稍后再试'

export function agentErrorText(data: any, status?: number): string {
  const code = data && typeof data.error === 'string' ? data.error : ''
  // 余额不足固定用 shortfall_cents 拼，不看 message
  if (code === 'insufficient_balance') {
    const s = Number(data.shortfall_cents)
    return Number.isFinite(s) && s > 0 ? `余额不足，还差 ${fmtUsd(s)}` : AGENT_ERROR_TEXT[code]
  }
  if (data && typeof data.message === 'string' && data.message) return data.message
  if (AGENT_ERROR_TEXT[code]) return AGENT_ERROR_TEXT[code]
  // LookupGuard 旧格式 {"error":"<中文文本>"}：429 用 rate_limited 文案，其他状态直接显示这段文本
  if (code && !/^[a-z0-9_]+$/.test(code)) return status === 429 ? AGENT_ERROR_TEXT.rate_limited : code
  if (status === 429) return AGENT_ERROR_TEXT.rate_limited
  return FALLBACK
}

/** 登录后的跳转目标：只接受 /agent 开头的站内路径，其余一律回 /agent（防开放跳转）。 */
export function safeAgentRedirect(v: unknown): string {
  const s = Array.isArray(v) ? v[0] : v
  if (typeof s !== 'string') return '/agent'
  if (!/^\/agent([/?#]|$)/.test(s) || s.includes('\\') || s.startsWith('/agent//')) return '/agent'
  if (/[\u0000-\u001f]/.test(s)) return '/agent'
  return s
}
