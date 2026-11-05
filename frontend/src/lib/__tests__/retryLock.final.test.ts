// F1 复审（Fable）后的修改：
// 1. 归属不符只「看不到」，不删；丢弃只在 bind（核对登录身份）时做。
// 2. 锁定的「重读 + 写入」：有 Web Locks 就互斥；写后读回（含稍等片刻再读）发现 key 不是自己的 → 不发请求。
// 3. 调账在发出前被别的标签页抢先锁定：run 抛 IdempotencyLockedError、不发请求，页面据此显示锁定。
// 4. 管理员退出前从存储判断有没有未决调账（不只看本页内存）。
import { describe, expect, it, vi } from 'vitest'
import { IdempotencyLockedError } from '../idempotency'
import { ADJUST_RETRY_PREFIX, ORDER_RETRY_KEY, type Mutex, type RetryStore } from '../retryLock'
import { OrderRetry, type PendingOrder } from '../agentViews'
import { AdjustRetry, type PendingAdjust } from '../agentAdmin'

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

const P: PendingOrder = { product_code: 'gpt-plus', quantity: 3, note: '', expected_unit_price_cents: 1200 }
const P2: PendingOrder = { product_code: 'gpt-pro', quantity: 1, note: '另一个标签页', expected_unit_price_cents: 20000 }
const slot = (owner: string, key: string, payload: unknown) => JSON.stringify({ owner, key, payload, at: Date.now() })

describe('复审 1：旧标签页不删别的代理商刚写入的记录', () => {
  it('A 的下单页收到 B 写入的 storage 事件（recheck / refresh）后，B 的记录仍在，B 刷新能找回', () => {
    const s = memStore()
    const a = new OrderRetry(s)
    a.bind('alice') // A 的标签页停在下单页
    const b = new OrderRetry(s)
    b.bind('bob') // 另一个标签页用 B 登录
    const kb = b.begin(P)
    // A 的标签页：storage 事件 → recheck；之后各种读取
    expect(a.recheck(null)).toEqual({ kind: 'go' })
    expect(a.recheck(P2)).toEqual({ kind: 'settled' })
    expect(a.refresh()).toBeNull()
    expect(JSON.parse(s.data[ORDER_RETRY_KEY])).toMatchObject({ owner: 'bob', key: kb })
    // B 刷新、原样重试
    expect(new OrderRetry(s).bind('bob')).toEqual(P)
    expect(b.begin(P)).toBe(kb)
  })

  it('A 的旧标签页要下新单：不覆盖 B 的记录（拒绝锁定）', () => {
    const s = memStore()
    const a = new OrderRetry(s)
    a.bind('alice')
    const b = new OrderRetry(s)
    b.bind('bob')
    const kb = b.begin(P)
    expect(() => a.begin(P2)).toThrow(IdempotencyLockedError)
    expect(() => a.lock.keyFor(P2, 'alice')).toThrow(IdempotencyLockedError)
    expect(JSON.parse(s.data[ORDER_RETRY_KEY]).key).toBe(kb)
  })

  it('丢弃只在核对登录身份（bind）时做', () => {
    const s = memStore()
    s.data[ORDER_RETRY_KEY] = slot('alice', 'ka', P)
    const t = new OrderRetry(s)
    t.bind('bob')
    expect(s.data[ORDER_RETRY_KEY]).toBeUndefined()
    // 归属不符的 current 只返回 null
    s.data[ORDER_RETRY_KEY] = slot('alice', 'ka', P)
    expect(t.lock.current('bob')).toBeNull()
    expect(s.data[ORDER_RETRY_KEY]).toBeTruthy()
  })
})

describe('复审 2：两个标签页同时锁定，写后读回不一致 → 不发请求', () => {
  it('写入后立刻读回的是别人的记录：抛 IdempotencyLockedError，不退回内存、不覆盖', () => {
    const s = memStore()
    s.setItem = (k) => { s.data[k] = slot('alice', 'k-other', P2) } // 模拟写入被别的标签页覆盖
    const t = new OrderRetry(s)
    t.bind('alice')
    expect(() => t.begin(P)).toThrow(IdempotencyLockedError)
    expect(t.lock.persistent).toBe(true)
    expect(t.recheck(null)).toEqual({ kind: 'locked', pending: P2 })
  })

  it('稍等片刻再读回，发现被别的标签页同一瞬间的锁定覆盖：acquire 失败，不发请求', async () => {
    const s = memStore()
    const t = new OrderRetry(s, undefined, Date.now, { settleMs: 5, sleep: async () => { s.data[ORDER_RETRY_KEY] = slot('alice', 'k-other', P2) } })
    t.bind('alice')
    await expect(t.acquire(P)).rejects.toThrow(IdempotencyLockedError)
    expect(JSON.parse(s.data[ORDER_RETRY_KEY]).key).toBe('k-other')
    expect(t.recheck(null)).toEqual({ kind: 'locked', pending: P2 })
  })

  it('等待期间记录被清掉：补写回去，照常发', async () => {
    const s = memStore()
    const t = new OrderRetry(s, undefined, Date.now, { settleMs: 5, sleep: async () => { delete s.data[ORDER_RETRY_KEY] } })
    t.bind('alice')
    const key = await t.acquire(P)
    expect(JSON.parse(s.data[ORDER_RETRY_KEY]).key).toBe(key)
  })

  it('原样重试（同一个请求号）读回一致：照常发，请求号不变', async () => {
    const s = memStore()
    const t = new OrderRetry(s, undefined, Date.now, { settleMs: 1, sleep: async () => {} })
    t.bind('alice')
    const k1 = await t.acquire(P)
    expect(await t.acquire(P)).toBe(k1)
  })

  it('有 Web Locks：重读 + 写入 + 读回都在互斥锁里做，锁名是存储键', async () => {
    const s = memStore()
    const names: string[] = []
    const mutex: Mutex = async (name, fn) => {
      names.push(name)
      expect(s.data[ORDER_RETRY_KEY]).toBeUndefined() // 拿到锁之前还没读写
      return fn()
    }
    const t = new OrderRetry(s, undefined, Date.now, { mutex, settleMs: 1, sleep: async () => {} })
    t.bind('alice')
    const key = await t.acquire(P)
    expect(names).toEqual([ORDER_RETRY_KEY])
    expect(JSON.parse(s.data[ORDER_RETRY_KEY]).key).toBe(key)
  })

  it('存储不可用：不用互斥锁、不等待，退回内存', async () => {
    const mutex = vi.fn()
    const sleep = vi.fn(async () => {})
    const t = new OrderRetry(undefined, undefined, Date.now, { mutex: mutex as unknown as Mutex, settleMs: 50, sleep })
    t.bind('alice')
    const key = await t.acquire(P)
    expect(await t.acquire(P)).toBe(key)
    expect(mutex).not.toHaveBeenCalled()
    expect(sleep).not.toHaveBeenCalled()
  })

  it('调账：发出前读回发现被别的标签页锁定了另一笔 → 不发请求、清在途，能读到那一笔', async () => {
    const s = memStore()
    const other: PendingAdjust = { agent_id: 5, amount_cents: -100, note: '另一个标签页' }
    const tab = new AdjustRetry(s, undefined, Date.now, { settleMs: 5, sleep: async () => { s.data[ADJUST_RETRY_PREFIX + '5'] = slot('', 'k-other', other) } })
    const send = vi.fn(async () => ({ status: 200, data: {} }))
    tab.begin(5)
    await expect(tab.run(5, { agent_id: 5, amount_cents: 10000, note: '充值' }, send)).rejects.toThrow(IdempotencyLockedError)
    expect(send).not.toHaveBeenCalled()
    expect(tab.inflight.has(5)).toBe(false)
    expect(tab.pendingFor(5)).toEqual(other)
    expect(tab.recheck(5, other, true)).toEqual({ kind: 'go' })
  })
})

describe('复审 4：管理员退出前从存储判断有没有未决调账', () => {
  const rec = (id: number, extra: Record<string, unknown> = {}) => JSON.stringify({ owner: '', key: 'k' + id, at: Date.now(), payload: { agent_id: id, amount_cents: 100, note: 'x' }, ...extra })

  it('别的标签页留下的未决（本页内存里没有）也算', () => {
    const s = memStore()
    s.data[ADJUST_RETRY_PREFIX + '7'] = rec(7)
    const fresh = new AdjustRetry(s)
    expect(fresh.pendings.size).toBe(0)
    expect(fresh.hasAnyPending()).toBe(true)
    fresh.clear()
    expect(fresh.hasAnyPending()).toBe(false)
  })

  it('过期、损坏、存放位置和 agent_id 不符、键名不是数字的不算；别的存储键不看', () => {
    const s = memStore()
    s.data[ADJUST_RETRY_PREFIX + '8'] = '{坏的'
    s.data[ADJUST_RETRY_PREFIX + '9'] = rec(10)
    s.data[ADJUST_RETRY_PREFIX + 'x'] = rec(1)
    s.data[ADJUST_RETRY_PREFIX + '11'] = rec(11, { at: Date.now() - 25 * 3600 * 1000 })
    s.data[ORDER_RETRY_KEY] = slot('alice', 'ka', P)
    expect(new AdjustRetry(s).hasAnyPending()).toBe(false)
  })

  it('存储不可用：看本页内存', async () => {
    const tab = new AdjustRetry(undefined)
    expect(tab.hasAnyPending()).toBe(false)
    tab.begin(5)
    await tab.run(5, { agent_id: 5, amount_cents: 100, note: 'x' }, async () => { throw new TypeError('Failed to fetch') })
    expect(tab.hasAnyPending()).toBe(true)
  })
})
