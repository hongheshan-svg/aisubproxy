// 批量兑换页的提交编排（lib/batch-redeem）。数据都是假的。
import { describe, expect, it } from 'vitest'
import { runSubmitPool } from '../batch-redeem'

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
