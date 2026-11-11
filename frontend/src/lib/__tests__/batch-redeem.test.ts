// 批量兑换页的提交编排（lib/batch-redeem）。数据都是假的。
import { describe, expect, it } from 'vitest'
import { UNKNOWN_QUERY_MS, batchStats, hasUnknownQuerying, isSubmittable, redeemOutcome, runSubmitPool, successRows, unknownExpired, unknownRows } from '../batch-redeem'

/** 手动放行的提交：记下谁被提交了、同时在途几个，测试决定什么时候让它返回。 */
function controlledSubmit() {
  const started: string[] = []
  const pending: Array<() => void> = []
  let inFlight = 0
  let maxInFlight = 0
  const submit = (item: string) =>
    new Promise<void>((resolve) => {
      started.push(item)
      inFlight++
      maxInFlight = Math.max(maxInFlight, inFlight)
      pending.push(() => {
        inFlight--
        resolve()
      })
    })
  const releaseAll = async () => {
    while (pending.length) {
      pending.shift()!()
      await Promise.resolve()
      await Promise.resolve()
    }
  }
  return { submit, started, releaseAll, maxInFlight: () => maxInFlight }
}

describe('自动提交的并发循环（F2-04b）', () => {
  it('一直在当前批次：每张恰好提交一次，同时在途不超过并发数', async () => {
    const c = controlledSubmit()
    const items = ['A', 'B', 'C', 'D', 'E']
    const done = runSubmitPool(items, 2, () => true, c.submit)
    await c.releaseAll()
    await done
    expect(c.started).toEqual(items)
    expect(c.maxInFlight()).toBeLessThanOrEqual(2)
  })

  it('并发数大于张数：不会多取，也不会重复提交', async () => {
    const seen: string[] = []
    await runSubmitPool(['A', 'B'], 6, () => true, async (x) => { seen.push(x) })
    expect(seen).toEqual(['A', 'B'])
  })

  it('离开页面或重置之后：已经在提交的照常返回，剩下的一张都不再提交', async () => {
    const c = controlledSubmit()
    let live = true
    const done = runSubmitPool(['A', 'B', 'C', 'D', 'E'], 2, () => live, c.submit)
    // 两张已经发出去了，这时离开页面
    expect(c.started).toEqual(['A', 'B'])
    live = false
    await c.releaseAll()
    await done
    expect(c.started).toEqual(['A', 'B'])
  })

  it('一开始就不在当前批次：一张都不提交', async () => {
    const seen: string[] = []
    await runSubmitPool(['A', 'B'], 2, () => false, async (x) => { seen.push(x) })
    expect(seen).toEqual([])
  })
})

describe('兑换请求的结果归类（F2-03）', () => {
  it('发出后断线（0）或 5xx：结果未知，不当失败', () => {
    for (const s of [0, 500, 502, 503, 504]) expect(redeemOutcome(s, ''), String(s)).toBe('unknown')
  })
  it('4xx：服务器明确拒绝，没有用掉卡密', () => {
    for (const s of [400, 403, 404, 409, 429]) expect(redeemOutcome(s, ''), String(s)).toBe('rejected')
  })
  it('2xx 按单据状态', () => {
    expect(redeemOutcome(200, 'completed')).toBe('success')
    expect(redeemOutcome(200, 'DECLINED')).toBe('failed')
    expect(redeemOutcome(200, 'failed_precharge')).toBe('failed')
    expect(redeemOutcome(202, 'queued')).toBe('processing')
    expect(redeemOutcome(200, '')).toBe('processing')
  })
})

describe('结果未知的那一张查到时限还没有结论（F2-03 裁定 1）', () => {
  const since = 1_000_000
  it('时限之内一直查：服务端建单最多 30 秒，留足余量', () => {
    expect(UNKNOWN_QUERY_MS).toBeGreaterThanOrEqual(60_000)
    expect(unknownExpired(since, since + 5_000)).toBe(false)
    expect(unknownExpired(since, since + UNKNOWN_QUERY_MS - 1)).toBe(false)
  })
  it('到时限还没查到兑换记录（不管是 404、5xx 还是断线）：停止查询', () => {
    expect(unknownExpired(since, since + UNKNOWN_QUERY_MS)).toBe(true)
  })
  it('不是结果未知的条目（正常受理的、已经查到过记录的）永远不按时限停', () => {
    expect(unknownExpired(0, since + 10 * UNKNOWN_QUERY_MS)).toBe(false)
  })
})

const row = (status: string, email = 'buyer@example.com') => ({
  status, cardKey: 'SXC-AAAA-BBBB', email, gptPassword: 'gp', emailPassword: 'ep', accessToken: 'at',
})

describe('统计和导出（F2-03、裁定 1）', () => {
  it('查询中的结果未知算处理中；停查后的结果未知单独计，不算失败也不算处理中', () => {
    const s = batchStats([row('processing'), row('unknown'), row('success'), row('failed'), row('verify_fail'), row('waiting_session'), row('verify_ok'), row('submitting'), row('pending_verify')])
    expect(s).toEqual({ total: 9, ok: 1, fail: 2, run: 2, wait: 2, unknown: 1 })
  })
  it('查到开通成功之后：计入成功，也进成功表', () => {
    expect(successRows([row('processing')])).toEqual([])
    expect(batchStats([row('success')]).ok).toBe(1)
    expect(successRows([row('success'), row('failed'), row('processing'), row('unknown')])).toEqual([['buyer@example.com', 'gp', 'ep', 'at']])
  })
  it('导出的值去掉换行', () => {
    expect(successRows([{ ...row('success'), gptPassword: 'a\r\nb' }])[0][1]).toBe('a b')
  })
  it('结果未知的单独导出，带卡密并标明结果未知，值不打码', () => {
    const label = '结果未知，请到卡密查询页核对，不要重复提交'
    expect(unknownRows([row('unknown'), row('success'), row('processing'), row('failed')], label)).toEqual([
      ['SXC-AAAA-BBBB', 'buyer@example.com', 'gp', 'ep', 'at', label],
    ])
  })
})

describe('结果未知的卡密不会再被提交（裁定 1）', () => {
  it('只有「待凭证」「卡密有效」能提交，结果未知、处理中、失败、成功都不能', () => {
    expect(isSubmittable('waiting_session')).toBe(true)
    expect(isSubmittable('verify_ok')).toBe(true)
    for (const st of ['unknown', 'processing', 'submitting', 'failed', 'success', 'verify_fail', 'pending_verify']) {
      expect(isSubmittable(st), st).toBe(false)
    }
  })
})

describe('还有结果未知正在查询时不能重置（裁定 3）', () => {
  it('有没到终态、带 unknownSince 的查询目标：算正在查', () => {
    expect(hasUnknownQuerying({ a: { token: 'x', terminal: false, unknownSince: 1 } })).toBe(true)
    expect(hasUnknownQuerying({ a: { token: 'x', terminal: true }, b: { token: 'y', terminal: false, unknownSince: 5 } })).toBe(true)
  })
  it('停查了（终态）、查到过记录（没有 unknownSince）、或者根本没有：不算', () => {
    expect(hasUnknownQuerying({})).toBe(false)
    expect(hasUnknownQuerying({ a: { token: 'x', terminal: true, unknownSince: 1 } })).toBe(false)
    expect(hasUnknownQuerying({ a: { token: 'x', terminal: false } })).toBe(false)
  })
})
