// 批量兑换页只读查询的超时（F2-03 裁定：查询超时）。请求挂着不返回时到点中止，算这一次查询失败，
// 下一轮再查；结果未知的照旧按 90 秒时限收尾，重置按钮不会被一次挂住的请求一直锁着。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { POLL_REQUEST_TIMEOUT_MS, withAbortTimeout } from '../batch-poll'
import { UNKNOWN_QUERY_MS } from '../batch-redeem'

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { vi.useRealTimers() })

/** 模拟一个不返回的请求：只有被中止时才以 AbortError 失败。 */
function hanging() {
  let signal: AbortSignal | undefined
  const run = (s: AbortSignal) => {
    signal = s
    return new Promise<never>((_, reject) => {
      s.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
    })
  }
  return { run, signal: () => signal! }
}

describe('只读查询的超时（F2-03 裁定）', () => {
  it('超时是 15 秒的常量，90 秒时限内至少能查几轮', () => {
    expect(POLL_REQUEST_TIMEOUT_MS).toBe(15_000)
    expect(POLL_REQUEST_TIMEOUT_MS * 3).toBeLessThanOrEqual(UNKNOWN_QUERY_MS)
  })

  it('请求一直不返回：到点前不中止，到点中止并按失败抛出', async () => {
    const h = hanging()
    const p = withAbortTimeout(POLL_REQUEST_TIMEOUT_MS, h.run)
    const failed = expect(p).rejects.toMatchObject({ name: 'AbortError' })
    await vi.advanceTimersByTimeAsync(POLL_REQUEST_TIMEOUT_MS - 1)
    expect(h.signal().aborted).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(h.signal().aborted).toBe(true)
    await failed
  })

  it('按时返回：结果照常，计时器清掉，之后也不会被中止', async () => {
    let seen: AbortSignal | undefined
    const v = await withAbortTimeout(POLL_REQUEST_TIMEOUT_MS, async (s) => {
      seen = s
      return 'ok'
    })
    expect(v).toBe('ok')
    expect(vi.getTimerCount()).toBe(0)
    await vi.advanceTimersByTimeAsync(POLL_REQUEST_TIMEOUT_MS * 2)
    expect(seen!.aborted).toBe(false)
  })

  it('请求自己先失败：原样抛出，计时器也清掉', async () => {
    await expect(withAbortTimeout(POLL_REQUEST_TIMEOUT_MS, async () => { throw new TypeError('Failed to fetch') })).rejects.toThrow('Failed to fetch')
    expect(vi.getTimerCount()).toBe(0)
  })
})
