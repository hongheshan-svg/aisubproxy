import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { agentApi, agentErrorText, setAgentUnauthorizedHandler } from '../agentApi'

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
