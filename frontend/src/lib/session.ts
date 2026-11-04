// 管理后台登录态的纯逻辑。
// 认证只靠后端下发的 HttpOnly cookie（auth_token）加 CSRF 双提交头；浏览器存储里只放展示用的
// 用户名、显示名、到期时间，不放令牌。是否已登录以服务端 /api/v1/auth/admin/me 的回答为准，本地记录只是提示。

export const AUTH_STORAGE_KEY = 'cdk-admin-auth'

export interface StoredAuth {
  username: string
  name: string
  expiresAt: string
}

/** 解析本地存的登录记录。旧版本把 JWT 也存在这里，这里只挑展示字段，令牌丢掉；坏数据返回 null。 */
export function parseStoredAuth(raw: string | null): StoredAuth | null {
  if (!raw) return null
  let v: any
  try {
    v = JSON.parse(raw)
  } catch {
    return null
  }
  return pickAuth(v?.username, v?.name, v?.expiresAt)
}

function pickAuth(username: unknown, name: unknown, expiresAt: unknown): StoredAuth | null {
  if (typeof username !== 'string' || !username || typeof expiresAt !== 'string' || !expiresAt) return null
  return { username, name: typeof name === 'string' && name ? name : username, expiresAt }
}

/** 本地记录是否还没到期（只是提示，真正的判断以服务端为准）。 */
export function storedAuthAlive(a: Pick<StoredAuth, 'username' | 'expiresAt'> | null, now = Date.now()): boolean {
  if (!a || !a.username || !a.expiresAt) return false
  const t = new Date(a.expiresAt).getTime()
  return Number.isFinite(t) && t > now
}

export type SessionProbe =
  | { kind: 'alive'; auth: StoredAuth }
  | { kind: 'dead' }
  | { kind: 'unreachable' }

const ME_URL = '/api/v1/auth/admin/me'

/**
 * 问服务端当前 cookie 是不是有效的管理员会话：鉴权中间件回的 401/403 算失效；
 * 网络错误、5xx、反代自己回的 401/403（不是本站鉴权的错误码）、回包不全都算问不到。
 */
export async function probeAdminSession(fetchImpl: typeof fetch = fetch): Promise<SessionProbe> {
  try {
    const r = await fetchImpl(ME_URL, { credentials: 'include' })
    if (r.status === 401 || r.status === 403) {
      const body = await r.json().catch(() => null)
      return isLocalSessionAuthFailure(ME_URL, r.status, body) ? { kind: 'dead' } : { kind: 'unreachable' }
    }
    if (!r.ok) return { kind: 'unreachable' }
    const d: any = await r.json()
    const auth = pickAuth(d?.username, d?.name, d?.expires_at)
    return auth ? { kind: 'alive', auth } : { kind: 'unreachable' }
  } catch {
    return { kind: 'unreachable' }
  }
}

/** 旧版本安装向导往 sessionStorage 写过的管理员用户名和明文密码（B-4）。 */
export const LEGACY_SETUP_KEYS = ['post_setup_user', 'post_setup_pass'] as const

/** 清掉旧版本安装向导留在 sessionStorage 里的用户名和明文密码。存储不可用时静默跳过。 */
export function clearSetupLeftovers(storage: Pick<Storage, 'removeItem'> | undefined): void {
  if (!storage) return
  for (const k of LEGACY_SETUP_KEYS) {
    try {
      storage.removeItem(k)
    } catch {
      /* ignore */
    }
  }
}

// 后端鉴权中间件（server/middleware.go）失败时回的错误码和固定文案。只有这些才算本站登录失效。
const AUTH_FAILURE_CODES = ['unauthorized', 'csrf_failed', 'admin_required']
const AUTH_FAILURE_TEXTS = [
  'missing authorization',
  'invalid authorization header',
  'invalid or expired token',
  'csrf 校验失败',
  'admin access required',
]

/**
 * 是否把 401/403 当成本站登录失效（会清本地登录态、跳登录页）。
 * 只认鉴权中间件的错误码或固定文案；其余 401/403 是业务结果（改密时旧密码错、在线更新未开启等），
 * 原样交给页面显示（审计 B-5）。卡台上游 Key 无效、网络探测失败也可能是 401/403，绝不能踢出会话。
 */
export function isLocalSessionAuthFailure(url: string, status: number, body: any): boolean {
  if (status !== 401 && status !== 403) return false
  if (/\/api\/v1\/admin\/cardplatform\//i.test(url)) return false
  if (/\/api\/v1\/admin\/network\//i.test(url)) return false

  const code = String(body?.error_code || '').toLowerCase()
  if (AUTH_FAILURE_CODES.includes(code)) return true
  const err = String(body?.error || '').trim().toLowerCase()
  return AUTH_FAILURE_TEXTS.includes(err)
}
