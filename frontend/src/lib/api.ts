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

// 主动登出：先请求后端清除 HttpOnly cookie，服务端确认后才清本地状态。返回是否已退出。
// 网络错误、5xx、403（如 CSRF 校验失败）时服务端会话还在：本地状态不动，由调用方提示重试；
// 401 说明会话本来就没了，照常清本地（F1-02）。
export const serverLogout = async (): Promise<boolean> => {
  let status = 0
  try {
    const csrf = getCookie('csrf_token')
    const headers = new Headers()
    if (csrf) headers.set('X-CSRF-Token', csrf)
    const r = await fetch('/api/v1/auth/admin/logout', { method: 'POST', headers, credentials: 'include' })
    status = r.status
  } catch {
    status = 0
  }
  const done = (status >= 200 && status < 300) || status === 401
  if (done) useAuthStore().logout()
  return done
}
