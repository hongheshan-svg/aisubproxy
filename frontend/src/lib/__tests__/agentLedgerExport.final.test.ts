import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { agentDownload, setAgentUnauthorizedHandler } from '../agentApi'
import { ledgerExportPath } from '../agentViews'

describe('ledgerExportPath（审计 AG-B-5：导出 URL 拼装）', () => {
  it('按北京时间日期范围拼出相对路径，from 为当天 0 点、to 为结束日次日 0 点', () => {
    const p = ledgerExportPath('2026-11-01', '2026-11-05')
    expect(p.startsWith('/ledger/export?from=')).toBe(true)
    const q = new URLSearchParams(p.split('?')[1])
    expect(Number(q.get('to')) - Number(q.get('from'))).toBe(5 * 24 * 3600)
  })

  it('日期不合法或结束早于开始时返回空串，页面据此禁用导出', () => {
    expect(ledgerExportPath('', '2026-11-05')).toBe('')
    expect(ledgerExportPath('2026-11-05', '2026-11-01')).toBe('')
  })
})

describe('agentDownload（审计 AG-B-5：导出走 fetch，401 走统一会话失效处理）', () => {
  let handler: ReturnType<typeof vi.fn>
  beforeEach(() => {
    handler = vi.fn()
    setAgentUnauthorizedHandler(handler)
  })
  afterEach(() => { vi.unstubAllGlobals(); setAgentUnauthorizedHandler(null) })

  it('401 时调用统一的会话失效处理，不返回文件', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ status: 401, ok: false, json: async () => ({ error: 'unauthorized' }) })))
    const r = await agentDownload('/ledger/export?from=1&to=2')
    expect(handler).toHaveBeenCalledTimes(1)
    expect(r.ok).toBe(false)
    expect(r.status).toBe(401)
    expect(r.blob).toBeUndefined()
  })

  it('成功时返回 blob，带 cookie、走代理商 API 前缀', async () => {
    const f = vi.fn(async () => ({ status: 200, ok: true, blob: async () => new Blob(['x']) }))
    vi.stubGlobal('fetch', f)
    const r = await agentDownload('/ledger/export?from=1&to=2')
    expect(f.mock.calls[0][0]).toBe('/api/v1/agent/ledger/export?from=1&to=2')
    expect((f.mock.calls[0] as any)[1].credentials).toBe('include')
    expect(r.ok).toBe(true)
    expect(r.blob).toBeInstanceOf(Blob)
    expect(handler).not.toHaveBeenCalled()
  })

  it('非 2xx 且非 401 时返回 JSON 错误体，不触发会话失效', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ status: 400, ok: false, json: async () => ({ error: 'range_too_large' }) })))
    const r = await agentDownload('/ledger/export?from=1&to=2')
    expect(r).toMatchObject({ ok: false, status: 400, data: { error: 'range_too_large' } })
    expect(handler).not.toHaveBeenCalled()
  })
})
