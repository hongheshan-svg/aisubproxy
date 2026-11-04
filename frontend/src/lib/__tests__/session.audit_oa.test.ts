// 审计 OA 组（鉴权与会话）：后台登录态只靠 HttpOnly cookie + CSRF 头，浏览器存储里不放令牌；
// 是否已登录以服务端 /auth/admin/me 为准。本机没有 DOM 环境，localStorage 用内存假实现。
import { readFileSync } from 'node:fs'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const store = vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = String(v) },
    removeItem: (k: string) => { delete data[k] },
  }
  return data
})

import { useAuthStore } from '../../stores/auth'
import { authFetch } from '../api'
import {
  AUTH_STORAGE_KEY, LEGACY_SETUP_KEYS, clearSetupLeftovers, isLocalSessionAuthFailure, parseStoredAuth, probeAdminSession,
  type SessionProbe,
} from '../session'

const future = () => new Date(Date.now() + 3600_000).toISOString()
const past = () => new Date(Date.now() - 1000).toISOString()

function resp(status: number, body: unknown): Response {
  const r: any = { ok: status >= 200 && status < 300, status, json: async () => body }
  r.clone = () => r
  return r as Response
}

beforeEach(() => {
  for (const k of Object.keys(store)) delete store[k]
  setActivePinia(createPinia())
})

describe('parseStoredAuth', () => {
  it('旧版本存过的令牌被丢掉，只留展示字段', () => {
    const raw = JSON.stringify({ token: 'eyJhbGciOi.xxx.yyy', username: 'admin', name: 'A', expiresAt: '2030-01-01T00:00:00Z' })
    const got = parseStoredAuth(raw)
    expect(got).toEqual({ username: 'admin', name: 'A', expiresAt: '2030-01-01T00:00:00Z' })
    expect(JSON.stringify(got)).not.toContain('token')
  })
  it('空值、坏 JSON、缺字段返回 null', () => {
    expect(parseStoredAuth(null)).toBeNull()
    expect(parseStoredAuth('{')).toBeNull()
    expect(parseStoredAuth(JSON.stringify({ username: 'a' }))).toBeNull()
  })
})

describe('auth store', () => {
  it('save 不把令牌写进 localStorage', () => {
    const auth = useAuthStore()
    auth.save({ username: 'admin', name: 'A', expiresAt: future(), token: 'leak' } as any)
    expect(store[AUTH_STORAGE_KEY]).toBeDefined()
    expect(store[AUTH_STORAGE_KEY]).not.toContain('leak')
    expect(store[AUTH_STORAGE_KEY]).not.toContain('token')
    expect((auth as any).token).toBeUndefined()
  })

  it('restore 把旧版本存的令牌从 localStorage 抹掉', () => {
    store[AUTH_STORAGE_KEY] = JSON.stringify({ token: 'old.jwt.value', username: 'admin', name: 'A', expiresAt: future() })
    const auth = useAuthStore()
    auth.restore()
    expect(auth.isLoggedIn).toBe(true)
    expect(store[AUTH_STORAGE_KEY]).not.toContain('old.jwt.value')
  })

  it('刷新后本地没有记录、cookie 仍有效：问过服务端后算已登录', async () => {
    const auth = useAuthStore()
    auth.restore()
    expect(auth.isLoggedIn).toBe(false)
    const probe = vi.fn(async (): Promise<SessionProbe> => ({ kind: 'alive', auth: { username: 'admin', name: 'System Admin', expiresAt: future() } }))
    await auth.ensureSession(probe)
    expect(probe).toHaveBeenCalledTimes(1)
    expect(auth.isLoggedIn).toBe(true)
    expect(auth.username).toBe('admin')
    // 同一次页面加载里不再重复问
    await auth.ensureSession(probe)
    expect(probe).toHaveBeenCalledTimes(1)
  })

  it('本地显示已登录但服务端说会话失效：清掉本地状态', async () => {
    store[AUTH_STORAGE_KEY] = JSON.stringify({ username: 'admin', name: 'A', expiresAt: future() })
    const auth = useAuthStore()
    auth.restore()
    await auth.ensureSession(async () => ({ kind: 'dead' }))
    expect(auth.isLoggedIn).toBe(false)
    expect(store[AUTH_STORAGE_KEY]).toBeUndefined()
  })

  it('服务端暂时不可达：沿用本地记录，不踢人', async () => {
    store[AUTH_STORAGE_KEY] = JSON.stringify({ username: 'admin', name: 'A', expiresAt: future() })
    const auth = useAuthStore()
    auth.restore()
    await auth.ensureSession(async () => ({ kind: 'unreachable' }))
    expect(auth.isLoggedIn).toBe(true)
  })

  it('退出后不再问服务端，也不会显示成已登录', async () => {
    const auth = useAuthStore()
    auth.save({ username: 'admin', name: 'A', expiresAt: future() })
    auth.logout()
    const probe = vi.fn(async (): Promise<SessionProbe> => ({ kind: 'alive', auth: { username: 'admin', name: 'A', expiresAt: future() } }))
    await auth.ensureSession(probe)
    expect(probe).not.toHaveBeenCalled()
    expect(auth.isLoggedIn).toBe(false)
    expect(store[AUTH_STORAGE_KEY]).toBeUndefined()
  })

  it('本地记录已过期：restore 后算未登录并删掉记录', () => {
    store[AUTH_STORAGE_KEY] = JSON.stringify({ username: 'admin', name: 'A', expiresAt: past() })
    const auth = useAuthStore()
    auth.restore()
    expect(auth.isLoggedIn).toBe(false)
    expect(store[AUTH_STORAGE_KEY]).toBeUndefined()
  })
})

describe('probeAdminSession', () => {
  it('200 返回用户名、显示名、到期时间', async () => {
    const exp = future()
    const f = vi.fn(async () => resp(200, { username: 'admin', name: 'System Admin', expires_at: exp, is_admin: true }))
    const got = await probeAdminSession(f as any)
    expect(got).toEqual({ kind: 'alive', auth: { username: 'admin', name: 'System Admin', expiresAt: exp } })
    expect(f).toHaveBeenCalledWith('/api/v1/auth/admin/me', expect.objectContaining({ credentials: 'include' }))
  })
  it('401、403 算会话失效', async () => {
    expect(await probeAdminSession((async () => resp(401, { error: 'Invalid or expired token' })) as any)).toEqual({ kind: 'dead' })
    expect(await probeAdminSession((async () => resp(403, { error: 'Admin access required' })) as any)).toEqual({ kind: 'dead' })
  })
  it('反代自己回的 403（不是本站鉴权错误）算问不到，不清本地登录态', async () => {
    const html = { ok: false, status: 403, json: async () => { throw new Error('not json') } } as unknown as Response
    expect(await probeAdminSession((async () => html) as any)).toEqual({ kind: 'unreachable' })
  })
  it('网络错误、5xx 算问不到', async () => {
    expect(await probeAdminSession((async () => { throw new Error('offline') }) as any)).toEqual({ kind: 'unreachable' })
    expect(await probeAdminSession((async () => resp(502, {})) as any)).toEqual({ kind: 'unreachable' })
  })
})

describe('authFetch', () => {
  it('不带 Authorization 头，靠 cookie', async () => {
    // 模拟旧版本留下的本地记录
    store[AUTH_STORAGE_KEY] = JSON.stringify({ token: 'old.jwt.value', username: 'admin', name: 'A', expiresAt: future() })
    useAuthStore().restore()
    const seen: Headers[] = []
    const f = vi.fn(async (_u: any, init: RequestInit) => {
      seen.push(new Headers(init.headers))
      expect(init.credentials).toBe('include')
      return resp(200, {})
    })
    vi.stubGlobal('fetch', f)
    try {
      await authFetch('/api/v1/admin/settings')
    } finally {
      vi.unstubAllGlobals()
    }
    expect(seen).toHaveLength(1)
    expect(seen[0].has('Authorization')).toBe(false)
  })
})

describe('安装向导不在浏览器存储里留明文密码（B-4）', () => {
  it('clearSetupLeftovers 清掉旧版本写过的用户名和密码', () => {
    const data: Record<string, string> = { post_setup_user: 'admin', post_setup_pass: 'Secret123456', other: 'keep' }
    clearSetupLeftovers({ removeItem: (k: string) => { delete data[k] } })
    expect(data).toEqual({ other: 'keep' })
  })
  it('没有存储（隐私模式抛错、SSR）时不报错', () => {
    expect(() => clearSetupLeftovers(undefined)).not.toThrow()
    expect(() => clearSetupLeftovers({ removeItem: () => { throw new Error('denied') } })).not.toThrow()
  })
  it('安装页、登录页源码不再写入或读取这些键', () => {
    for (const rel of ['../../views/admin/SetupView.vue', '../../views/auth/LoginView.vue']) {
      const src = readFileSync(new URL(rel, import.meta.url), 'utf8')
      for (const k of LEGACY_SETUP_KEYS) {
        expect(src, rel).not.toMatch(new RegExp(`(setItem|getItem)\\(\\s*['"]${k}['"]`))
      }
    }
  })
})

describe('isLocalSessionAuthFailure（B-5）：只有鉴权中间件的失败才算登录失效', () => {
  const admin = '/api/v1/admin/settings'
  it('鉴权中间件的错误码和固定文案算失效', () => {
    expect(isLocalSessionAuthFailure(admin, 401, { error: 'whatever', error_code: 'unauthorized' })).toBe(true)
    expect(isLocalSessionAuthFailure(admin, 403, { error: 'CSRF 校验失败', error_code: 'csrf_failed' })).toBe(true)
    expect(isLocalSessionAuthFailure(admin, 403, { error: 'Admin access required', error_code: 'admin_required' })).toBe(true)
    for (const error of ['Missing authorization', 'Invalid authorization header', 'Invalid or expired token', 'CSRF 校验失败', 'Admin access required']) {
      expect(isLocalSessionAuthFailure(admin, 401, { error }), error).toBe(true)
    }
  })
  it('业务性的 401/403 不算失效', () => {
    expect(isLocalSessionAuthFailure('/api/v1/admin/system/update', 403, { error: 'UPDATE_ENABLED=0，未开启一键更新' })).toBe(false)
    expect(isLocalSessionAuthFailure('/api/v1/auth/admin/change-password', 401, { error: '旧密码不正确' })).toBe(false)
    expect(isLocalSessionAuthFailure(admin, 403, null)).toBe(false)
  })
  it('卡台上游、网络探测的 401/403 不算失效', () => {
    expect(isLocalSessionAuthFailure('/api/v1/admin/cardplatform/cdks', 401, { error: 'Invalid or expired token' })).toBe(false)
    expect(isLocalSessionAuthFailure('/api/v1/admin/network/probe', 403, { error_code: 'unauthorized' })).toBe(false)
  })
  it('其他状态码不算', () => {
    expect(isLocalSessionAuthFailure(admin, 500, { error_code: 'unauthorized' })).toBe(false)
  })
})

describe('authFetch 遇到业务性 403 不登出（B-5）', () => {
  it('改密时旧密码错（401）不会把管理员踢出去', async () => {
    const auth = useAuthStore()
    auth.save({ username: 'admin', name: 'A', expiresAt: future() })
    vi.stubGlobal('fetch', vi.fn(async () => resp(401, { error: '旧密码不正确' })))
    try {
      const r = await authFetch('/api/v1/auth/admin/change-password', { method: 'POST', body: '{}' })
      expect(r.status).toBe(401)
    } finally {
      vi.unstubAllGlobals()
    }
    expect(auth.isLoggedIn).toBe(true)
  })
  it('会话失效（401 unauthorized）照常登出', async () => {
    const auth = useAuthStore()
    auth.save({ username: 'admin', name: 'A', expiresAt: future() })
    vi.stubGlobal('fetch', vi.fn(async () => resp(401, { error: 'Invalid or expired token', error_code: 'unauthorized' })))
    try {
      await authFetch('/api/v1/admin/settings')
    } finally {
      vi.unstubAllGlobals()
    }
    expect(auth.isLoggedIn).toBe(false)
  })
})
