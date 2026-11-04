import { describe, expect, it } from 'vitest'
import { BATCH_MAX_KEYS } from '../batch-session'
import {
  POLL_BACKOFF_MAX_MS, POLL_BATCH_MAX, POLL_RATE_LIMIT_NOTICE,
  chunkTokens, nextBackoffMs, nextPollPlan, parsePollResponse, pollIntervalMs,
} from '../batch-poll'

describe('批量轮询（S4 / F-4）', () => {
  it('批量上限降到 100，且不超过一次批量查询的令牌数', () => {
    expect(BATCH_MAX_KEYS).toBe(100)
    expect(BATCH_MAX_KEYS).toBeLessThanOrEqual(POLL_BATCH_MAX)
  })
  it('在途越多间隔越长，且按每小时 6000 次的额度算不会超', () => {
    const seq = [1, 10, 11, 30, 31, 60, 61, 100].map(pollIntervalMs)
    for (let i = 1; i < seq.length; i++) expect(seq[i]).toBeGreaterThanOrEqual(seq[i - 1])
    expect(pollIntervalMs(1)).toBe(3000)
    expect(pollIntervalMs(100)).toBeGreaterThan(pollIntervalMs(1))
    // 合并成一次请求后，每小时的查询次数 = 3600000 / 间隔，任何档位都远低于 6000
    for (const n of [1, 10, 30, 60, 100]) expect(3600000 / pollIntervalMs(n)).toBeLessThan(6000)
  })
  it('429 退避：15 秒起翻倍，封顶 2 分钟', () => {
    expect(nextBackoffMs(0)).toBe(15000)
    expect(nextBackoffMs(15000)).toBe(30000)
    expect(nextBackoffMs(90000)).toBe(POLL_BACKOFF_MAX_MS)
    expect(nextBackoffMs(POLL_BACKOFF_MAX_MS)).toBe(POLL_BACKOFF_MAX_MS)
  })
  it('429 时有明确提示并退避，不静默；恢复后清掉提示', () => {
    const limited = parsePollResponse(429, { error: '请求过于频繁' })
    expect(limited.rateLimited).toBe(true)
    const p1 = nextPollPlan(limited, 5, 0)
    expect(p1.notice).toBe(POLL_RATE_LIMIT_NOTICE)
    expect(p1.notice).toContain('稍后自动重试')
    expect(p1.delayMs).toBeGreaterThan(3000)
    const p2 = nextPollPlan(limited, 5, p1.backoffMs)
    expect(p2.delayMs).toBeGreaterThan(p1.delayMs)
    const ok = parsePollResponse(200, { results: [] })
    const p3 = nextPollPlan(ok, 5, p2.backoffMs)
    expect(p3).toEqual({ delayMs: 3000, backoffMs: 0, notice: '' })
  })
  it('5xx / 坏响应当作整体失败重试，不改条目', () => {
    expect(parsePollResponse(502, null)).toEqual({ rateLimited: false, failed: true, items: [] })
    expect(parsePollResponse(200, { oops: 1 }).failed).toBe(true)
  })
  it('解析逐个令牌的结果', () => {
    const o = parsePollResponse(200, { results: [{ token: 'a', status: 200, data: { status: 'completed' } }, { token: 'b', status: 404 }, null] })
    expect(o.items).toEqual([
      { token: 'a', status: 200, data: { status: 'completed' } },
      { token: 'b', status: 404, data: null },
    ])
  })
  it('按 100 个一组切分', () => {
    expect(chunkTokens(Array.from({ length: 250 }, (_, i) => i)).map((c) => c.length)).toEqual([100, 100, 50])
  })
})
