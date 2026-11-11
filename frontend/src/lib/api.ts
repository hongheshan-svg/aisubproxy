import { useAuthStore } from '../stores/auth'
import { isLocalSessionAuthFailure } from './session'

const UNSAFE_METHODS = ['POST', 'PUT', 'PATCH', 'DELETE']

function getCookie(name: string): string {
  if (typeof document === 'undefined') return ''
  const match = document.cookie.match(new RegExp('(?:^|; )' + name.replace(/([.$?*|{}()[\]\\/+^])/g, '\\$1') + '=([^;]*)'))
  return match ? decodeURIComponent(match[1]) : ''
}

function requestUrl(input: RequestInfo | URL): string {
  if (typeof input === 'string') return input
  if (input instanceof URL) return input.href
  try {
    return String((input as Request).url || '')
  } catch {
    return ''
  }
}

export const authFetch = async (input: RequestInfo | URL, init: RequestInit = {}) => {
  const authStore = useAuthStore()
  const headers = new Headers(init.headers || {})
  const url = requestUrl(input)

  // 认证只靠 HttpOnly cookie（credentials: 'include'），不带 Authorization 头：令牌不经过前端 JS

  // 基于 cookie 的写操作需要 CSRF 双提交：把可读的 csrf_token cookie 放进请求头
  const method = (init.method || 'GET').toUpperCase()
  if (UNSAFE_METHODS.includes(method)) {
    const csrf = getCookie('csrf_token')
    if (csrf) headers.set('X-CSRF-Token', csrf)
  }

  // FormData 必须由浏览器自动带 multipart boundary，不能手设 Content-Type
  if (init.body && !(init.body instanceof FormData) && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  const response = await fetch(input, {
    ...init,
    headers,
    credentials: 'include',
  })

  if (response.status === 401 || response.status === 403) {
    let body: any = null
    try {
      body = await response.clone().json()
    } catch {
      body = null
    }
    if (isLocalSessionAuthFailure(url, response.status, body)) {
      authStore.logout()
      if (typeof window !== 'undefined' && window.location.pathname.startsWith('/ops')) {
        window.location.href = '/ops/login'
      }
    }
  }

  return response
}

/**
 * 主动登出：先请求后端清除 HttpOnly cookie，服务端确认后才清本地状态（F1-02）。
 * - done：已退出（2xx；或 401，会话本来就没了），本地状态已清；
 * - csrf：403 csrf_failed。后端只比 X-CSRF-Token 头和 csrf_token cookie，cookie 只在登录时下发、没有补发接口，
 *   cookie 丢了重试也不会成功，由调用方提示清除本站 Cookie；
 * - failed：网络错误、5xx、其他 403，服务端会话可能还在，由调用方提示重试。
 * 后两种本地状态都不动。
 */
export const serverLogout = async (): Promise<'done' | 'csrf' | 'failed'> => {
  let r: Response
  try {
    const csrf = getCookie('csrf_token')
    const headers = new Headers()
    if (csrf) headers.set('X-CSRF-Token', csrf)
    r = await fetch('/api/v1/auth/admin/logout', { method: 'POST', headers, credentials: 'include' })
  } catch {
    return 'failed'
  }
  if ((r.status >= 200 && r.status < 300) || r.status === 401) {
    useAuthStore().logout()
    return 'done'
  }
  if (r.status === 403) {
    let body: any = null
    try { body = await r.json() } catch { body = null }
    if (body?.error_code === 'csrf_failed') return 'csrf'
  }
  return 'failed'
}
