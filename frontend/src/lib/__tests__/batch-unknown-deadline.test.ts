// 结果未知的 90 秒时限按墙钟算（FE2 审查次要 1）：页面按最早一张的时限另排计时器，查询间隔、429 退避、挂住的请求都拖不长它。
import { describe, expect, it } from 'vitest'
import { POLL_BACKOFF_MAX_MS, nextPollPlan } from '../batch-poll'
import { UNKNOWN_QUERY_MS, msUntilUnknownDeadline, unknownExpired } from '../batch-redeem'

const since = 1_000_000

describe('离最早一张结果未知的时限还有多久（FE2 审查次要 1）', () => {
  it('没有在查的结果未知：不用排计时器', () => {
    expect(msUntilUnknownDeadline({}, since)).toBeNull()
    // 正常受理的、已经查到过记录的（没有 unknownSince）
    expect(msUntilUnknownDeadline({ a: { token: 'x', terminal: false } }, since)).toBeNull()
    // 已经到终态的
    expect(msUntilUnknownDeadline({ a: { token: 'x', terminal: true, unknownSince: since } }, since)).toBeNull()
  })
  it('一张：从标结果未知那一刻起算 90 秒', () => {
    const t = { a: { token: 'x', terminal: false, unknownSince: since } }
    expect(msUntilUnknownDeadline(t, since)).toBe(UNKNOWN_QUERY_MS)
    expect(msUntilUnknownDeadline(t, since + 80_000)).toBe(UNKNOWN_QUERY_MS - 80_000)
  })
  it('几张：取最早到时限的那一张', () => {
    const t = {
      a: { token: 'x', terminal: false, unknownSince: since + 20_000 },
      b: { token: 'y', terminal: false, unknownSince: since },
      c: { token: 'z', terminal: true, unknownSince: since - 50_000 },
      d: { token: 'w', terminal: false },
    }
    expect(msUntilUnknownDeadline(t, since + 1_000)).toBe(UNKNOWN_QUERY_MS - 1_000)
  })
  it('已经过了时限（计时器晚到）：马上检查，不返回负数', () => {
    const t = { a: { token: 'x', terminal: false, unknownSince: since } }
    expect(msUntilUnknownDeadline(t, since + UNKNOWN_QUERY_MS + 5_000)).toBe(0)
  })
  it('80 秒时被限流、下一轮要等 2 分钟：时限计时器 10 秒后就到，到点时 unknownExpired 为真', () => {
    const t = { a: { token: 'x', terminal: false, unknownSince: since } }
    const now = since + 80_000
    const plan = nextPollPlan({ rateLimited: true, failed: false }, 1, POLL_BACKOFF_MAX_MS)
    expect(plan.delayMs).toBe(POLL_BACKOFF_MAX_MS)
    const wait = msUntilUnknownDeadline(t, now)!
    expect(wait).toBe(10_000)
    expect(wait).toBeLessThan(plan.delayMs)
    expect(unknownExpired(since, now + wait)).toBe(true)
  })
})
