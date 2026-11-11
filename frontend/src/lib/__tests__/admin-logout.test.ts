// 管理员退出看服务端结果（F1-02，用户选 1）：退出请求失败时留在后台并提示重试，本地登录状态不动；
// 服务端会话本来就没了（401）才照常回登录页。否则共用电脑上会「以为退了其实没退」：HttpOnly 的会话 cookie 还在。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = String(v) },
    removeItem: (k: string) => { delete data[k] },
  }
})

import { useAuthStore } from '../../stores/auth'
import { serverLogout } from '../api'

const future = () => new Date(Date.now() + 3600_000).toISOString()

function login() {
  const auth = useAuthStore()
  auth.save({ username: 'admin', name: 'A', expiresAt: future() })
  return auth
}

beforeEach(() => { setActivePinia(createPinia()) })
afterEach(() => { vi.unstubAllGlobals() })

describe('serverLogout（F1-02）', () => {
  it('2xx：清本地状态，返回 true', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200 })))
    const auth = login()
    expect(await serverLogout()).toBe(true)
    expect(auth.isLoggedIn).toBe(false)
  })

  it('401：服务端会话本来就没了，照常清本地状态，返回 true', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 401 })))
    const auth = login()
    expect(await serverLogout()).toBe(true)
    expect(auth.isLoggedIn).toBe(false)
  })

  for (const status of [403, 500, 502]) {
    it(`${status}：服务端会话可能还在，本地状态不动，返回 false`, async () => {
      vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status })))
      const auth = login()
      expect(await serverLogout()).toBe(false)
      expect(auth.isLoggedIn).toBe(true)
      expect(auth.username).toBe('admin')
    })
  }

  it('网络错误：本地状态不动，返回 false', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch') }))
    const auth = login()
    expect(await serverLogout()).toBe(false)
    expect(auth.isLoggedIn).toBe(true)
  })
})

describe('后台布局的退出（F1-02）', () => {
  const src = readFileSync(resolve(__dirname, '../../layouts/AdminLayout.vue'), 'utf8')
  it('退出失败时提示重试并留在后台：不清调账记录、不跳登录页', () => {
    const at = src.indexOf("cmd === 'logout'")
    const branch = src.slice(at, src.indexOf("router.push('/ops/login')", at) + 30)
    expect(branch).toContain("if (!(await serverLogout())) {")
    const fail = branch.slice(branch.indexOf('if (!(await serverLogout())) {'))
    const failBlock = fail.slice(0, fail.indexOf('}'))
    expect(failBlock).toContain("dialog.toast('退出失败，服务器上的登录状态还在，请重试', 'err')")
    expect(failBlock).toContain('return')
    // 失败分支之后才清调账记录、跳登录页
    expect(branch.indexOf('clearAdjustRetries()')).toBeGreaterThan(branch.indexOf('if (!(await serverLogout())) {'))
  })
})
