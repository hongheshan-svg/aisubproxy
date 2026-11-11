// 批量兑换页的提交编排（lib/batch-redeem）。数据都是假的。
import { describe, expect, it } from 'vitest'
import { UNKNOWN_NOT_FOUND_MS, batchStats, redeemOutcome, runSubmitPool, successRows, unknownLookupVerdict } from '../batch-redeem'

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

describe('结果未知的那一张查不到兑换记录（F2-03）', () => {
  const since = 1_000_000
  it('等够时间之前一直查：服务端建单最多 30 秒，留足余量', () => {
    expect(UNKNOWN_NOT_FOUND_MS).toBeGreaterThanOrEqual(60_000)
    expect(unknownLookupVerdict(404, since, since + 5_000)).toBe('keep')
    expect(unknownLookupVerdict(404, since, since + UNKNOWN_NOT_FOUND_MS - 1)).toBe('keep')
  })
  it('超过等待时间仍是 404：判定没有兑换记录，停下提示核对', () => {
    expect(unknownLookupVerdict(404, since, since + UNKNOWN_NOT_FOUND_MS)).toBe('not_found')
  })
  it('暂时出错（5xx、429）不算查不到，一直查下去', () => {
    for (const s of [500, 503, 429]) expect(unknownLookupVerdict(s, since, since + 10 * UNKNOWN_NOT_FOUND_MS)).toBe('keep')
  })
  it('不是结果未知的条目（正常受理的）查到 404 照旧继续查', () => {
    expect(unknownLookupVerdict(404, 0, since + 10 * UNKNOWN_NOT_FOUND_MS)).toBe('keep')
  })
})

const row = (status: string, email = 'buyer@example.com') => ({
  status, email, gptPassword: 'gp', emailPassword: 'ep', accessToken: 'at',
})

describe('统计和导出跟着「处理中」走（F2-03）', () => {
  it('结果未知标成处理中：算在处理中，不算失败', () => {
    const s = batchStats([row('processing'), row('success'), row('failed'), row('verify_fail'), row('waiting_session'), row('verify_ok'), row('submitting'), row('pending_verify')])
    expect(s).toEqual({ total: 8, ok: 1, fail: 2, run: 2, wait: 2 })
  })
  it('查到开通成功之后：计入成功，也进导出表', () => {
    expect(successRows([row('processing')])).toEqual([])
    expect(batchStats([row('success')]).ok).toBe(1)
    expect(successRows([row('success'), row('failed'), row('processing')])).toEqual([['buyer@example.com', 'gp', 'ep', 'at']])
  })
  it('导出的值去掉换行', () => {
    expect(successRows([{ ...row('success'), gptPassword: 'a\r\nb' }])[0][1]).toBe('a b')
  })
})
