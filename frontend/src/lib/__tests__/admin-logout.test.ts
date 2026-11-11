// 管理员退出看服务端结果（F1-02，用户选 1）：退出请求失败时留在后台并提示重试，本地登录状态不动；
// 服务端会话本来就没了（401）才照常回登录页。否则共用电脑上会「以为退了其实没退」：HttpOnly 的会话 cookie 还在。
// 审查修复：CSRF 校验失败（403 csrf_failed，csrf_token cookie 丢了）重试也不会成功，单独返回 'csrf'，页面给可操作的提示。
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
  it('2xx：清本地状态，返回 done', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200 })))
    const auth = login()
    expect(await serverLogout()).toBe('done')
    expect(auth.isLoggedIn).toBe(false)
  })

  it('401：服务端会话本来就没了，照常清本地状态，返回 done', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 401 })))
    const auth = login()
    expect(await serverLogout()).toBe('done')
    expect(auth.isLoggedIn).toBe(false)
  })

  for (const status of [403, 500, 502]) {
    it(`${status}：服务端会话可能还在，本地状态不动，返回 failed`, async () => {
      vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status })))
      const auth = login()
      expect(await serverLogout()).toBe('failed')
      expect(auth.isLoggedIn).toBe(true)
      expect(auth.username).toBe('admin')
    })
  }

  it('网络错误：本地状态不动，返回 failed', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch') }))
    const auth = login()
    expect(await serverLogout()).toBe('failed')
    expect(auth.isLoggedIn).toBe(true)
  })

  it('403 csrf_failed：返回 csrf（重试也不会成功），本地状态不动', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 403, json: async () => ({ error: 'CSRF 校验失败', error_code: 'csrf_failed' }) })))
    const auth = login()
    expect(await serverLogout()).toBe('csrf')
    expect(auth.isLoggedIn).toBe(true)
  })

  it('403 但响应体读不出：按一般失败处理', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 403, json: async () => { throw new SyntaxError('bad') } })))
    login()
    expect(await serverLogout()).toBe('failed')
  })
})

describe('后台布局的退出（F1-02）', () => {
  const src = readFileSync(resolve(__dirname, '../../layouts/AdminLayout.vue'), 'utf8')
  const at = src.indexOf("cmd === 'logout'")
  const branch = src.slice(at, src.indexOf("router.push('/ops/login')", at) + 30)
  it('退出失败时留在后台：不清调账记录、不跳登录页；一般失败提示重试', () => {
    expect(branch).toContain('const out = await serverLogout()')
    const fail = branch.slice(branch.indexOf("if (out !== 'done') {"))
    const failBlock = fail.slice(0, fail.indexOf('\n    }'))
    expect(failBlock).toContain("dialog.toast('退出失败，服务器上的登录状态还在，请重试', 'err')")
    expect(failBlock).toContain('return')
    // 失败分支之后才清调账记录、跳登录页
    expect(branch.indexOf('clearAdjustRetries()')).toBeGreaterThan(branch.indexOf("if (out !== 'done') {"))
  })
  it('CSRF 校验失败：不说「请重试」，说清楚要清除本站 Cookie（审查修复）', () => {
    const fail = branch.slice(branch.indexOf("if (out !== 'done') {"))
    expect(fail).toMatch(/if \(out === 'csrf'\) dialog\.toast\('退出失败：浏览器里本站的校验 Cookie 丢了[^']*清除本站的 Cookie[^']*', 'err', 10000\)/)
  })
})
