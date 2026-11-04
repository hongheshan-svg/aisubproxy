import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { agentApi, agentErrorText, safeAgentRedirect, setAgentUnauthorizedHandler } from '../agentApi'

function stubFetch(status: number, body: unknown = {}) {
  const f = vi.fn(async () => ({ status, ok: status >= 200 && status < 300, json: async () => body }))
  vi.stubGlobal('fetch', f)
  return f
}

describe('agentApi', () => {
  let handler: ReturnType<typeof vi.fn>
  beforeEach(() => {
    vi.stubGlobal('document', { cookie: 'a=1; agent_csrf=tok123; b=2' })
    handler = vi.fn()
    setAgentUnauthorizedHandler(handler)
  })
  afterEach(() => { vi.unstubAllGlobals(); setAgentUnauthorizedHandler(null) })

  it('POST 带 CSRF 头，GET 不带，都带 cookie', async () => {
    const f = stubFetch(200, { x: 1 })
    await agentApi('/orders', 'POST', { a: 1 })
    let [url, init] = f.mock.calls[0] as any
    expect(url).toBe('/api/v1/agent/orders')
    expect(init.credentials).toBe('include')
    expect(init.headers['X-CSRF-Token']).toBe('tok123')
    expect(init.body).toBe(JSON.stringify({ a: 1 }))
    const r = await agentApi('/me')
    ;[, init] = f.mock.calls[1] as any
    expect(init.headers['X-CSRF-Token']).toBeUndefined()
    expect(r).toEqual({ ok: true, status: 200, data: { x: 1 } })
  })

  it('401 非 /auth 接口触发处理函数', async () => {
    stubFetch(401, { error: 'x' })
    await agentApi('/me')
    expect(handler).toHaveBeenCalledTimes(1)
  })

  it('401 的 /auth 接口不触发', async () => {
    stubFetch(401, { error: 'bad' })
    const r = await agentApi('/auth/login', 'POST', {})
    expect(handler).not.toHaveBeenCalled()
    expect(r.status).toBe(401)
  })

  it('响应不是 JSON 时 data 为空对象', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ status: 502, ok: false, json: async () => { throw new Error('x') } })))
    const r = await agentApi('/me')
    expect(r.data).toEqual({})
  })
})

describe('agentErrorText', () => {
  it('余额不足带差额', () => {
    expect(agentErrorText({ error: 'insufficient_balance', shortfall_cents: 250 })).toContain('$2.50')
  })
  it('优先用 message，其次编号表，最后兜底', () => {
    expect(agentErrorText({ message: '自定义' })).toBe('自定义')
    expect(agentErrorText({ error: 'rate_limited' })).toBe('操作太频繁，请稍后再试')
    expect(agentErrorText({ error: 'price_changed' })).toBe('价格已变化，请刷新后重新下单')
    expect(agentErrorText({ error: 'zzz' })).toBe('操作失败，请稍后再试')
    expect(agentErrorText(null)).toBe('操作失败，请稍后再试')
  })
})

describe('agentErrorText 补充', () => {
  it('登录相关编号', () => {
    expect(agentErrorText({ error: 'invalid_credentials' })).toBe('登录名或密码错误')
    expect(agentErrorText({ error: 'totp_invalid' })).toBe('动态码错误')
    expect(agentErrorText({ error: 'pre_expired' })).toBe('登录已过期，请重新输入密码')
    expect(agentErrorText({ error: 'weak_password' })).toBe('新密码不符合要求')
    expect(agentErrorText({ error: 'unauthorized' })).toBe('登录已失效，请重新登录')
    expect(agentErrorText({ error: 'csrf_invalid' })).toBe('页面已过期，请刷新后重试')
  })
  it('余额不足无论有没有 message 都用 shortfall_cents', () => {
    expect(agentErrorText({ error: 'insufficient_balance', message: 'insufficient', shortfall_cents: 250 })).toBe('余额不足，还差 $2.50')
  })
})

describe('safeAgentRedirect', () => {
  it('接受站内 /agent 路径', () => {
    expect(safeAgentRedirect('/agent')).toBe('/agent')
    expect(safeAgentRedirect('/agent/codes?x=1')).toBe('/agent/codes?x=1')
  })
  it('拒绝开放跳转和奇怪输入', () => {
    for (const bad of ['//evil.com', '/agent@evil.com', '/agentx', '/\\evil.com', '/agent//x', '/agent\t/x', '/agent\n/x', 'https://evil.com', '']) {
      expect(safeAgentRedirect(bad), JSON.stringify(bad)).toBe('/agent')
    }
  })
  it('数组取第一个，非字符串回 /agent', () => {
    expect(safeAgentRedirect(['/agent/ledger', '/x'])).toBe('/agent/ledger')
    expect(safeAgentRedirect(undefined)).toBe('/agent')
    expect(safeAgentRedirect(5)).toBe('/agent')
    expect(safeAgentRedirect(null)).toBe('/agent')
  })
})
