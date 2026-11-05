// F1：管理员调账结果未知时，幂等键和金额、原因按代理商 ID 分开落 localStorage；发出前就落盘；
// 结算只做一次（Task 11：返回后更新界面出异常不再补记未决、不换键）；多标签页提交前重读；退出登录清掉。
import { describe, expect, it } from 'vitest'
import { IdempotencyLockedError } from '../idempotency'
import { ADJUST_RETRY_PREFIX, createLeaveGuard, type RetryStore } from '../retryLock'
import { AdjustRetry, adjustOpenState, type PendingAdjust } from '../agentAdmin'

function memStore(): RetryStore & { data: Record<string, string> } {
  const data: Record<string, string> = {}
  return {
    data,
    getItem: (k) => (k in data ? data[k] : null),
    setItem: (k, v) => { data[k] = String(v) },
    removeItem: (k) => { delete data[k] },
    get length() { return Object.keys(data).length },
    key: (i) => Object.keys(data)[i] ?? null,
  }
}

const P5: PendingAdjust = { agent_id: 5, amount_cents: 10000, note: '充值' }
const P6: PendingAdjust = { agent_id: 6, amount_cents: -2050, note: '补差' }
const ok = async () => ({ status: 200, data: { replayed: false } })
const netDown = async (): Promise<{ status: number; data: any }> => { throw new TypeError('Failed to fetch') }

describe('F1 调账结果未知：刷新后找回', () => {
  it('请求抛异常 → 结果未知 → 模拟刷新 → 锁定恢复，原样重试沿用同一个幂等键', async () => {
    const s = memStore()
    const tab = new AdjustRetry(s)
    const keys: string[] = []
    expect(tab.begin(5)).toBe(true)
    const o = await tab.run(5, P5, async (k) => { keys.push(k); return netDown() })
    expect(o.kind).toBe('unknown')
    expect(tab.inflight.has(5)).toBe(false)
    // 刷新
    const again = new AdjustRetry(s)
    expect(again.pendingFor(5)).toEqual(P5)
    expect(adjustOpenState(again.pendingFor(5)).locked).toBe(true)
    expect(again.keeperFor(5).keyFor(P5)).toBe(keys[0])
    expect(() => again.keeperFor(5).keyFor({ ...P5, amount_cents: 1 })).toThrow(IdempotencyLockedError)
    again.begin(5)
    await again.run(5, P5, async (k) => { keys.push(k); return ok() })
    expect(keys[1]).toBe(keys[0])
    expect(again.pendingFor(5)).toBeNull()
    expect(Object.keys(s.data)).toHaveLength(0)
  })

  it('发出后、返回前刷新（在途）：也能找回同一个幂等键', async () => {
    const s = memStore()
    const tab = new AdjustRetry(s)
    let sent = ''
    tab.begin(5)
    void tab.run(5, P5, (k) => { sent = k; return new Promise(() => {}) }) // 永不返回
    const again = new AdjustRetry(s)
    expect(again.pendingFor(5)).toEqual(P5)
    expect(again.keeperFor(5).keyFor(P5)).toBe(sent)
  })

  it('5xx 结果未知、明确失败、成功：分别保留、清掉、清掉', async () => {
    const s = memStore()
    const tab = new AdjustRetry(s)
    tab.begin(5)
    expect((await tab.run(5, P5, async () => ({ status: 503, data: {} }))).kind).toBe('unknown')
    expect(new AdjustRetry(s).pendingFor(5)).toEqual(P5)
    tab.begin(5)
    expect((await tab.run(5, P5, async () => ({ status: 400, data: { error: 'insufficient_balance' } }))).kind).toBe('failed')
    expect(new AdjustRetry(s).pendingFor(5)).toBeNull()
  })

  it('放弃：清掉存储，下一笔用新键', async () => {
    const s = memStore()
    const tab = new AdjustRetry(s)
    const keys: string[] = []
    tab.begin(5)
    await tab.run(5, P5, async (k) => { keys.push(k); return netDown() })
    tab.abandon(5)
    expect(new AdjustRetry(s).pendingFor(5)).toBeNull()
    tab.begin(5)
    await tab.run(5, P5, async (k) => { keys.push(k); return ok() })
    expect(keys[1]).not.toBe(keys[0])
  })
})

describe('F1 调账按代理商 ID 隔离', () => {
  it('每个代理商单独一条记录，互不影响', async () => {
    const s = memStore()
    const tab = new AdjustRetry(s)
    const keys: Record<number, string> = {}
    tab.begin(5)
    await tab.run(5, P5, async (k) => { keys[5] = k; return netDown() })
    tab.begin(6)
    await tab.run(6, P6, async (k) => { keys[6] = k; return netDown() })
    expect(Object.keys(s.data).sort()).toEqual([ADJUST_RETRY_PREFIX + '5', ADJUST_RETRY_PREFIX + '6'])
    expect(keys[5]).not.toBe(keys[6])
    const again = new AdjustRetry(s)
    expect(again.pendingFor(5)).toEqual(P5)
    expect(again.pendingFor(6)).toEqual(P6)
    expect(again.pendingFor(7)).toBeNull()
    again.abandon(5)
    expect(again.pendingFor(6)).toEqual(P6)
    // 记录里的 agent_id 和存放位置不一致（被改过）：不认
    s.data[ADJUST_RETRY_PREFIX + '8'] = s.data[ADJUST_RETRY_PREFIX + '6']
    expect(new AdjustRetry(s).pendingFor(8)).toBeNull()
  })

  it('存储里只有幂等键、金额和原因', async () => {
    const s = memStore()
    const tab = new AdjustRetry(s)
    tab.begin(5)
    await tab.run(5, P5, netDown)
    const v = JSON.parse(s.data[ADJUST_RETRY_PREFIX + '5'])
    expect(Object.keys(v).sort()).toEqual(['at', 'key', 'owner', 'payload'])
    expect(Object.keys(v.payload).sort()).toEqual(['agent_id', 'amount_cents', 'note'])
  })
})

describe('F1 Task 11：结算只做一次', () => {
  it('调账成功后，返回后的界面更新抛异常：不再补记未决、不锁定，下一笔用新键', async () => {
    const s = memStore()
    const tab = new AdjustRetry(s)
    const keys: string[] = []
    tab.begin(5)
    await expect(tab.run(5, P5, async (k) => { keys.push(k); return ok() }, () => { throw new Error('toast 出错') })).rejects.toThrow('toast 出错')
    expect(tab.pendingFor(5)).toBeNull()
    expect(tab.pendings.has(5)).toBe(false)
    expect(tab.inflight.has(5)).toBe(false)
    expect(Object.keys(s.data)).toHaveLength(0)
    tab.begin(5)
    await tab.run(5, P5, async (k) => { keys.push(k); return ok() })
    expect(keys[1]).not.toBe(keys[0])
  })

  it('结果未知后界面更新抛异常：未决只记一次，键不变', async () => {
    const s = memStore()
    const tab = new AdjustRetry(s)
    const keys: string[] = []
    tab.begin(5)
    await expect(tab.run(5, P5, async (k) => { keys.push(k); return netDown() }, () => { throw new Error('x') })).rejects.toThrow('x')
    expect(tab.keeperFor(5).keyFor(P5)).toBe(keys[0])
  })
})

describe('F1 调账多标签页', () => {
  it('另一个标签页锁定 → 本标签页提交前重读到锁定；处理完 → 本标签页的锁定作废', async () => {
    const s = memStore()
    const tab1 = new AdjustRetry(s)
    const tab2 = new AdjustRetry(s)
    expect(tab2.recheck(5, P5, false)).toEqual({ kind: 'go' })
    tab1.begin(5)
    let key = ''
    await tab1.run(5, P5, async (k) => { key = k; return netDown() })
    // 标签页 2 以为没有未决，要发一笔新的（哪怕内容一样）：先改显示锁定的那笔
    expect(tab2.recheck(5, { ...P5, amount_cents: 1 }, false)).toEqual({ kind: 'locked', pending: P5 })
    expect(tab2.recheck(5, P5, false)).toEqual({ kind: 'locked', pending: P5 })
    // 显示的就是锁定的那笔：原样重试
    expect(tab2.recheck(5, P5, true)).toEqual({ kind: 'go' })
    expect(tab2.keeperFor(5).keyFor(P5)).toBe(key)
    tab2.begin(5)
    await tab2.run(5, P5, ok)
    // 标签页 1 还停在锁定界面：再点原样重试前重读，发现已处理完
    expect(tab1.recheck(5, P5, true)).toEqual({ kind: 'settled' })
  })
})

describe('F1 调账：退出登录、存储不可用、离开提示', () => {
  it('退出登录：清掉所有代理商的调账记录（包括本页面没打开过的）', async () => {
    const s = memStore()
    const tab = new AdjustRetry(s)
    tab.begin(5)
    await tab.run(5, P5, netDown)
    s.data[ADJUST_RETRY_PREFIX + '6'] = JSON.stringify({ owner: '', key: 'k6', at: Date.now(), payload: P6 })
    s.data['other'] = 'keep'
    tab.clear()
    expect(Object.keys(s.data)).toEqual(['other'])
    expect(tab.pendingFor(5)).toBeNull()
    expect(tab.pendings.size).toBe(0)
  })

  it('存储读写都抛异常：不报错，本页面内照样锁定', async () => {
    const boom = () => { throw new Error('denied') }
    const tab = new AdjustRetry({ getItem: boom, setItem: boom, removeItem: boom })
    const keys: string[] = []
    tab.begin(5)
    await tab.run(5, P5, async (k) => { keys.push(k); return netDown() })
    expect(tab.pendingFor(5)).toEqual(P5)
    expect(tab.keeperFor(5).keyFor(P5)).toBe(keys[0])
    expect(() => tab.clear()).not.toThrow()
    expect(tab.pendingFor(5)).toBeNull()
  })

  it('有未决调账时注册 beforeunload，清掉后注销；离开页面提示照旧', async () => {
    const handlers = new Set<unknown>()
    const target = { addEventListener: (_: string, h: unknown) => { handlers.add(h) }, removeEventListener: (_: string, h: unknown) => { handlers.delete(h) } }
    const s = memStore()
    const tab = new AdjustRetry(s, createLeaveGuard(target))
    tab.begin(5)
    const p = tab.run(5, P5, netDown)
    expect(handlers.size).toBe(1) // 在途就已注册
    await p
    expect(handlers.size).toBe(1)
    expect(tab.leaveWarning(5)).not.toBe('')
    expect(tab.leaveWarning(6)).toBe('')
    tab.abandon(5)
    expect(handlers.size).toBe(0)
    // 刷新后恢复出未决也注册
    tab.begin(5)
    await tab.run(5, P5, netDown)
    const h2 = new Set<unknown>()
    const again = new AdjustRetry(s, createLeaveGuard({ addEventListener: (_: string, h: unknown) => { h2.add(h) }, removeEventListener: (_: string, h: unknown) => { h2.delete(h) } }))
    expect(h2.size).toBe(0)
    again.pendingFor(5)
    expect(h2.size).toBe(1)
  })
})
