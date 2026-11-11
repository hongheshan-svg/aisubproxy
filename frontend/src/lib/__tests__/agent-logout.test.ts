// 代理商退出看服务端结果（终审后端次要 1，照管理员端 F1-02）：
// 接口成功或 401（会话本来就没了）才算已退出；403（校验 Cookie 丢了）提示清除本站 Cookie；断网、5xx 留在页面提示重试。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { agentLogout } from '../agentApi'

afterEach(() => { vi.unstubAllGlobals() })

describe('agentLogout', () => {
  for (const status of [200, 204, 401]) {
    it(`${status}：已退出，返回 done`, async () => {
      vi.stubGlobal('fetch', vi.fn(async () => ({ ok: status < 300, status, json: async () => ({}) })))
      expect(await agentLogout()).toBe('done')
    })
  }
  it('403：返回 csrf（重试也不会成功）', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 403, json: async () => ({}) })))
    expect(await agentLogout()).toBe('csrf')
  })
  for (const status of [500, 502]) {
    it(`${status}：返回 failed`, async () => {
      vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status, json: async () => ({}) })))
      expect(await agentLogout()).toBe('failed')
    })
  }
  it('网络错误：返回 failed', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch') }))
    expect(await agentLogout()).toBe('failed')
  })
})

describe('代理商布局的退出', () => {
  const src = readFileSync(resolve(__dirname, '../../layouts/AgentLayout.vue'), 'utf8')
  const at = src.indexOf('async function logout()')
  const body = src.slice(at, src.indexOf("router.replace('/agent/login')", at) + 40)
  it('没退成时留在页面：先判断结果，失败分支 return 在清状态和跳转之前', () => {
    expect(body).toContain('const out = await agentLogout()')
    const failAt = body.indexOf("if (out !== 'done') {")
    expect(failAt).toBeGreaterThan(-1)
    expect(body.indexOf('return', failAt)).toBeLessThan(body.indexOf('resetAgentSession()'))
    expect(body.indexOf('orderRetry.clear()')).toBeGreaterThan(failAt)
    expect(body).toContain("dialog.toast('退出失败，服务器上的登录状态还在，请重试', 'err')")
    expect(body).toMatch(/if \(out === 'csrf'\) dialog\.toast\('退出失败：浏览器里本站的校验 Cookie 丢了[^']*清除本站的 Cookie[^']*', 'err', 10000\)/)
  })
})
