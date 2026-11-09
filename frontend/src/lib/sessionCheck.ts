// 兑换页粘贴登录信息的本地检查。只有一套严格规则，和后端 chatgpt.ParseSession 一致：
// 必须是以 { 开头的 JSON 对象，有 sessionToken 或 session_token，且有 user.email。
// 纯函数：不碰 DOM、存储和网络。

export type SessionProblem =
  | 'empty'
  | 'not_logged_in'
  | 'access_token'
  | 'not_json'
  | 'no_token'
  | 'no_email'
  | 'expired'

export type SessionCheck =
  | { ok: true; value: string; maskedEmail?: string }
  | { ok: false; problem: SessionProblem }

const fail = (problem: SessionProblem): SessionCheck => ({ ok: false, problem })

// 以 eyJ 开头的三段式（JWT 形状）
const JWT_SHAPE = /^eyJ[\w-]*\.[\w-]+\.[\w-]*$/

// 保留开头：长度不超过 short 只留 1 个字符，更长留 2 个。按字符截，不截出乱码。
function keepHead(chars: string[], short: number): string {
  if (chars.length > short) return chars.slice(0, 2).join('')
  return chars.length > 0 ? chars[0] : ''
}

// 与后端 maskEmailForAdmin 一致：本地部分不超过 2 个字符只留 1 个，否则留前 2 个，后接 ***@域名。
export function maskEmail(email: string): string {
  const em = email.trim()
  if (em === '' || em === 'unknown') return em
  const at = em.lastIndexOf('@')
  if (at < 0) return keepHead(Array.from(em), 3) + '***'
  return keepHead(Array.from(em.slice(0, at)), 2) + '***' + em.slice(at)
}

function nonEmptyString(v: unknown): boolean {
  return typeof v === 'string' && v.trim() !== ''
}

export function checkPastedSession(raw: string, opts: { now: number }): SessionCheck {
  const value = raw.trim()
  if (value === '') return fail('empty')
  if (JWT_SHAPE.test(value)) return fail('access_token')
  if (!value.startsWith('{')) return fail('not_json')

  let data: unknown
  try {
    data = JSON.parse(value)
  } catch {
    return fail('not_json')
  }
  if (typeof data !== 'object' || data === null || Array.isArray(data)) return fail('not_json')
  const obj = data as Record<string, unknown>
  if (Object.keys(obj).length === 0) return fail('not_logged_in')

  if (!nonEmptyString(obj.sessionToken) && !nonEmptyString(obj.session_token)) return fail('no_token')

  const user = obj.user
  const email =
    typeof user === 'object' && user !== null && typeof (user as { email?: unknown }).email === 'string'
      ? ((user as { email: string }).email).trim()
      : ''
  if (email === '') return fail('no_email')

  // expires 解析不了不当作过期
  if (typeof obj.expires === 'string') {
    const t = Date.parse(obj.expires)
    if (!Number.isNaN(t) && t < opts.now) return fail('expired')
  }

  return { ok: true, value, maskedEmail: maskEmail(email) }
}
