// F1：结果未知的下单落 localStorage，刷新、关标签页重开、新标签页都能找回；绑定代理商身份；多标签页提交前重读。
// 另含「最近一笔订单」的重复提醒（纯函数）。
import { afterEach, describe, expect, it, vi } from 'vitest'
import { IdempotencyLockedError } from '../idempotency'
import { ORDER_RETRY_KEY, createLeaveGuard, needsLeaveGuard, type RetryStore } from '../retryLock'
import {
  OrderRetry, orderLockState, pickLatestOrder, recentOrderWarning, RECENT_ORDER_WINDOW_SEC, type PendingOrder,
} from '../agentViews'

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

function throwingStore(): RetryStore {
  const boom = () => { throw new Error('存储被禁用') }
  return { getItem: boom, setItem: boom, removeItem: boom }
}

function fakeTarget() {
  const handlers = new Set<unknown>()
  return {
    handlers,
    addEventListener: (_t: string, h: unknown) => { handlers.add(h) },
    removeEventListener: (_t: string, h: unknown) => { handlers.delete(h) },
  }
}

const P: PendingOrder = { product_code: 'gpt-plus', quantity: 3, note: '客户 A', expected_unit_price_cents: 1200 }

describe('F1 下单结果未知：刷新后找回', () => {
  it('结果未知 → 模拟刷新（同一存储重建）→ 同一代理商仍锁定，同内容沿用原请求号，不同内容拒绝', () => {
    const s = memStore()
    const tab = new OrderRetry(s)
    tab.bind('alice')
    const key = tab.begin(P) // 发出前已落盘
    // 刷新：模块重建
    const again = new OrderRetry(s)
    expect(again.refresh()).toBeNull() // 还没核对身份：不恢复、也不动存储
    expect(s.data[ORDER_RETRY_KEY]).toBeTruthy()
    expect(again.bind('alice')).toEqual(P)
    expect(orderLockState({ busy: false, confirming: false, hasPending: !!again.refresh() })).toEqual({ inputsDisabled: true, unknown: true })
    expect(again.begin(P)).toBe(key)
    expect(() => again.begin({ ...P, quantity: 4 })).toThrow(IdempotencyLockedError)
    expect(() => again.lock.keyFor({ ...P, note: '改了' }, 'alice')).toThrow(IdempotencyLockedError)
    expect(again.lock.keyFor(P, 'alice')).toBe(key)
  })

  it('成功或放弃后清掉，下一笔用新请求号', () => {
    const s = memStore()
    const tab = new OrderRetry(s)
    tab.bind('alice')
    const key = tab.begin(P)
    tab.settle(key)
    expect(s.data[ORDER_RETRY_KEY]).toBeUndefined()
    expect(new OrderRetry(s).bind('alice')).toBeNull()
    const k2 = tab.begin(P)
    expect(k2).not.toBe(key)
    tab.abandon()
    expect(tab.refresh()).toBeNull()
    expect(s.data[ORDER_RETRY_KEY]).toBeUndefined()
  })

  it('同一浏览器换了代理商登录：上一个人的待重试订单丢弃，换回来也找不回', () => {
    const s = memStore()
    const a = new OrderRetry(s)
    a.bind('alice')
    a.begin(P)
    const b = new OrderRetry(s)
    expect(b.bind('bob')).toBeNull()
    expect(s.data[ORDER_RETRY_KEY]).toBeUndefined()
    expect(new OrderRetry(s).bind('alice')).toBeNull()
    // 同一个实例里换人（没刷新页面）也一样
    const c = new OrderRetry(s)
    c.bind('alice')
    c.begin(P)
    expect(c.bind('bob')).toBeNull()
    expect(() => c.begin({ ...P, quantity: 1 })).not.toThrow()
  })

  it('没核对身份不许锁定', () => {
    expect(() => new OrderRetry(memStore()).begin(P)).toThrow()
  })

  it('退出登录清掉记录并忘掉身份', () => {
    const s = memStore()
    const tab = new OrderRetry(s)
    tab.bind('alice')
    tab.begin(P)
    tab.clear()
    expect(s.data[ORDER_RETRY_KEY]).toBeUndefined()
    expect(tab.bound).toBe(false)
    expect(tab.refresh()).toBeNull()
  })

  it('锁定 24 小时后过期', () => {
    const s = memStore()
    let t = 1_000_000_000_000
    const tab = new OrderRetry(s, undefined, () => t)
    tab.bind('alice')
    tab.begin(P)
    t += 24 * 3600 * 1000 - 1
    expect(new OrderRetry(s, undefined, () => t).bind('alice')).toEqual(P)
    t += 2
    expect(new OrderRetry(s, undefined, () => t).bind('alice')).toBeNull()
    expect(s.data[ORDER_RETRY_KEY]).toBeUndefined()
  })

  it('存储内容损坏时当作没有', () => {
    const s = memStore()
    s.data[ORDER_RETRY_KEY] = '{坏的'
    expect(new OrderRetry(s).bind('alice')).toBeNull()
    s.data[ORDER_RETRY_KEY] = JSON.stringify({ owner: 'alice', key: 'k', at: Date.now(), payload: { product_code: 'x', quantity: 0, note: '', expected_unit_price_cents: 1 } })
    expect(new OrderRetry(s).bind('alice')).toBeNull()
  })

  it('存储里只有归属、请求号和下单内容，不含码', () => {
    const s = memStore()
    const tab = new OrderRetry(s)
    tab.bind('alice')
    tab.begin(P)
    const v = JSON.parse(s.data[ORDER_RETRY_KEY])
    expect(Object.keys(v).sort()).toEqual(['at', 'key', 'owner', 'payload'])
    expect(Object.keys(v.payload).sort()).toEqual(['expected_unit_price_cents', 'note', 'product_code', 'quantity'])
    expect(Object.keys(s.data)).toEqual([ORDER_RETRY_KEY])
  })
})

describe('F1 多标签页共用一份', () => {
  it('另一个标签页写入锁定 → 本标签页提交前重读到锁定，只许原样重试', () => {
    const s = memStore()
    const tab1 = new OrderRetry(s)
    const tab2 = new OrderRetry(s)
    tab1.bind('alice')
    tab2.bind('alice') // 页面加载时还没有锁定
    expect(tab2.recheck(null)).toEqual({ kind: 'go' })
    const key = tab1.begin(P) // 标签页 1 发出、结果未知
    // 标签页 2 要下一笔新单：提交前重读，发现锁定
    expect(tab2.recheck(null)).toEqual({ kind: 'locked', pending: P })
    expect(() => tab2.begin({ ...P, quantity: 1 })).toThrow(IdempotencyLockedError)
    // 标签页 2 显示的就是这一笔：可以原样重试，沿用同一个请求号
    expect(tab2.recheck(P)).toEqual({ kind: 'go' })
    expect(tab2.begin(P)).toBe(key)
  })

  it('本标签页锁定的那笔已在别的标签页处理完：不许再发（再发会换新号重复扣款）', () => {
    const s = memStore()
    const tab1 = new OrderRetry(s)
    const tab2 = new OrderRetry(s)
    tab1.bind('alice')
    tab2.bind('alice')
    const key = tab1.begin(P)
    expect(tab2.refresh()).toEqual(P)
    tab1.settle(key) // 标签页 1 原样重试成功
    expect(tab2.recheck(P)).toEqual({ kind: 'settled' })
  })

  it('别的标签页换了一笔锁定：本标签页显示的旧内容不能发', () => {
    const s = memStore()
    const tab1 = new OrderRetry(s)
    const tab2 = new OrderRetry(s)
    tab1.bind('alice')
    tab2.bind('alice')
    tab1.abandon()
    const k1 = tab1.begin(P)
    tab1.settle(k1)
    const other = { ...P, quantity: 9 }
    tab1.begin(other)
    expect(tab2.recheck(P)).toEqual({ kind: 'locked', pending: other })
  })

  it('迟到的结算只清自己那一笔，不清别的标签页后来锁定的', () => {
    const s = memStore()
    const tab1 = new OrderRetry(s)
    const tab2 = new OrderRetry(s)
    tab1.bind('alice')
    tab2.bind('alice')
    const k1 = tab1.begin(P)
    tab2.abandon() // 用户在标签页 2 确认放弃
    const k2 = tab2.begin({ ...P, quantity: 5 })
    tab1.settle(k1) // 标签页 1 的请求这时才明确失败
    expect(tab2.refresh()).toEqual({ ...P, quantity: 5 })
    expect(tab2.begin({ ...P, quantity: 5 })).toBe(k2)
  })
})

describe('F1 存储不可用时退回内存', () => {
  it('读写都抛异常：不报错，本页面内照样锁定', () => {
    const tab = new OrderRetry(throwingStore())
    expect(() => tab.bind('alice')).not.toThrow()
    let key = ''
    expect(() => { key = tab.begin(P) }).not.toThrow()
    expect(tab.refresh()).toEqual(P)
    expect(tab.begin(P)).toBe(key)
    expect(() => tab.begin({ ...P, quantity: 1 })).toThrow(IdempotencyLockedError)
    expect(() => tab.settle(key)).not.toThrow()
    expect(tab.refresh()).toBeNull()
    expect(() => tab.clear()).not.toThrow()
  })

  it('能读不能写（写满、旧版隐私模式）：退回内存，锁定不会因为重读存储而丢', () => {
    const s = memStore()
    s.setItem = () => { throw new Error('QuotaExceededError') }
    const tab = new OrderRetry(s)
    tab.bind('alice')
    const key = tab.begin(P)
    expect(tab.recheck(P)).toEqual({ kind: 'go' })
    expect(tab.begin(P)).toBe(key)
  })

  it('没有存储（服务端渲染、测试环境）：内存行为', () => {
    const tab = new OrderRetry(undefined)
    tab.bind('alice')
    const key = tab.begin(P)
    expect(tab.begin(P)).toBe(key)
    tab.settle(key)
    expect(tab.refresh()).toBeNull()
  })
})

describe('F1 结果未知期间拦截刷新、关标签页（只在记录只在内存里时）', () => {
  it('needsLeaveGuard：有待重试且没落进存储才拦', () => {
    expect(needsLeaveGuard(true, false)).toBe(true)
    expect(needsLeaveGuard(true, true)).toBe(false)
    expect(needsLeaveGuard(false, false)).toBe(false)
    expect(needsLeaveGuard(false, true)).toBe(false)
  })

  it('存储可用：锁定已落 localStorage，刷新、关标签页都能找回，不注册 beforeunload', () => {
    const s = memStore()
    const t = fakeTarget()
    const tab = new OrderRetry(s, createLeaveGuard(t))
    tab.bind('alice')
    const key = tab.begin(P)
    expect(tab.lock.persistent).toBe(true)
    expect(t.handlers.size).toBe(0)
    // 刷新后恢复出锁定也不注册
    const again = new OrderRetry(s, createLeaveGuard(t))
    expect(again.bind('alice')).toEqual(P)
    expect(t.handlers.size).toBe(0)
    again.settle(key)
    expect(t.handlers.size).toBe(0)
  })

  it('存储不可用（只在内存里）：锁定时注册，结算后注销；重复锁定不重复注册；退出登录注销', () => {
    const t = fakeTarget()
    const tab = new OrderRetry(throwingStore(), createLeaveGuard(t))
    tab.bind('alice')
    expect(t.handlers.size).toBe(0)
    const key = tab.begin(P)
    expect(tab.lock.persistent).toBe(false)
    expect(t.handlers.size).toBe(1)
    tab.begin(P)
    expect(t.handlers.size).toBe(1)
    tab.settle(key)
    expect(t.handlers.size).toBe(0)
    tab.begin(P)
    expect(t.handlers.size).toBe(1)
    tab.clear()
    expect(t.handlers.size).toBe(0)
  })

  it('存储能读不能写：写失败那一刻改用内存，随即注册', () => {
    const s = memStore()
    s.setItem = () => { throw new Error('QuotaExceededError') }
    const t = fakeTarget()
    const tab = new OrderRetry(s, createLeaveGuard(t))
    tab.bind('alice')
    expect(t.handlers.size).toBe(0)
    tab.begin(P)
    expect(t.handlers.size).toBe(1)
  })

  it('没有存储：锁定时注册', () => {
    const t = fakeTarget()
    const tab = new OrderRetry(undefined, createLeaveGuard(t))
    tab.bind('alice')
    tab.begin(P)
    expect(t.handlers.size).toBe(1)
  })

  it('beforeunload 处理函数要求浏览器弹确认', () => {
    const t = fakeTarget()
    const g = createLeaveGuard(t)
    g.set(true)
    const h = [...t.handlers][0] as (e: any) => void
    const e = { preventDefault: vi.fn(), returnValue: undefined as unknown }
    h(e)
    expect(e.preventDefault).toHaveBeenCalled()
    expect(e.returnValue).toBe('')
  })

  it('没有 window 时什么都不做', () => {
    expect(() => createLeaveGuard(undefined).set(true)).not.toThrow()
  })
})

describe('F1 模块单例接的是 localStorage', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.resetModules()
  })

  it('刷新（模块重新加载）后从 localStorage 找回同一个请求号', async () => {
    const s = memStore()
    vi.stubGlobal('localStorage', s)
    vi.resetModules()
    const m1 = await import('../agentViews')
    m1.orderRetry.bind('alice')
    const key = m1.orderRetry.begin(P)
    expect(Object.keys(s.data)).toContain(ORDER_RETRY_KEY)
    vi.resetModules()
    const m2 = await import('../agentViews')
    expect(m2.orderRetry).not.toBe(m1.orderRetry)
    expect(m2.orderRetry.bind('alice')).toEqual(P)
    expect(m2.orderRetry.begin(P)).toBe(key)
  })

  it('访问 localStorage 本身就抛异常：模块照常加载', async () => {
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, get() { throw new Error('SecurityError') } })
    try {
      vi.resetModules()
      const m = await import('../agentViews')
      m.orderRetry.bind('alice')
      const key = m.orderRetry.begin(P)
      expect(m.orderRetry.begin(P)).toBe(key)
    } finally {
      delete (globalThis as any).localStorage
    }
  })
})

describe('orderLockState 身份核对前锁定', () => {
  it('ready=false 时输入禁用，默认视为已核对', () => {
    expect(orderLockState({ busy: false, confirming: false, hasPending: false, ready: false }).inputsDisabled).toBe(true)
    expect(orderLockState({ busy: false, confirming: false, hasPending: false })).toEqual({ inputsDisabled: false, unknown: false })
  })
})

describe('最近一笔订单的重复提醒', () => {
  const now = 1_793_808_000 // 北京时间 2026-11-05 00:00:00
  const latest = { id: 7, product_code: 'gpt-plus', product_name: 'Plus', quantity: 3, total_cents: 3600, created_at: now - 120 }

  it('10 分钟内、同商品同数量：提醒，带北京时间和张数', () => {
    expect(recentOrderWarning(latest, 'gpt-plus', 3, now)).toBe('你在 23:58 刚买过同样的 3 张，确认要再买一次吗？')
  })
  it('边界：整 10 分钟仍提醒，超过 1 秒不提醒', () => {
    expect(RECENT_ORDER_WINDOW_SEC).toBe(600)
    expect(recentOrderWarning({ ...latest, created_at: now - 600 }, 'gpt-plus', 3, now)).not.toBe('')
    expect(recentOrderWarning({ ...latest, created_at: now - 601 }, 'gpt-plus', 3, now)).toBe('')
  })
  it('不同数量、不同商品、没有最近订单：不提醒', () => {
    expect(recentOrderWarning(latest, 'gpt-plus', 2, now)).toBe('')
    expect(recentOrderWarning(latest, 'gpt-pro', 3, now)).toBe('')
    expect(recentOrderWarning(null, 'gpt-plus', 3, now)).toBe('')
    expect(recentOrderWarning(undefined, 'gpt-plus', 3, now)).toBe('')
  })
  it('本机时钟比服务端慢（订单时间在「未来」）：仍提醒', () => {
    expect(recentOrderWarning({ ...latest, created_at: now + 30 }, 'gpt-plus', 3, now)).not.toBe('')
  })

  it('pickLatestOrder 取第一页第一条，数据不对返回 null', () => {
    expect(pickLatestOrder({ items: [latest, { ...latest, id: 6 }], total: 2 })).toEqual(latest)
    expect(pickLatestOrder({ items: [], total: 0 })).toBeNull()
    expect(pickLatestOrder({})).toBeNull()
    expect(pickLatestOrder(null)).toBeNull()
    expect(pickLatestOrder({ items: [{ id: 'x' }] })).toBeNull()
  })
})
