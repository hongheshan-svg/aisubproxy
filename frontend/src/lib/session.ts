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

/** 问服务端当前 cookie 是不是有效的管理员会话：401、403 算失效；网络错误、5xx、回包不全算问不到。 */
export async function probeAdminSession(fetchImpl: typeof fetch = fetch): Promise<SessionProbe> {
  try {
    const r = await fetchImpl('/api/v1/auth/admin/me', { credentials: 'include' })
    if (r.status === 401 || r.status === 403) return { kind: 'dead' }
    if (!r.ok) return { kind: 'unreachable' }
    const d: any = await r.json()
    const auth = pickAuth(d?.username, d?.name, d?.expires_at)
    return auth ? { kind: 'alive', auth } : { kind: 'unreachable' }
  } catch {
    return { kind: 'unreachable' }
  }
}
