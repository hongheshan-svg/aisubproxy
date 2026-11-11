// 放弃结果未知的订单（F2-18）：确认框换成异步的统一确认框后，等待期间别的标签页可能已处理完这笔、或锁定了另一笔。
// abandonShown 在确认之后重读存储：仍是页面上显示的那笔才清（只清这笔的请求号），否则不动存储。
import { describe, expect, it } from 'vitest'
import { ORDER_RETRY_KEY, type RetryStore } from '../retryLock'
import { OrderRetry, type PendingOrder } from '../agentViews'

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

const P: PendingOrder = { product_code: 'gpt-plus', quantity: 3, note: '客户 A', expected_unit_price_cents: 1200 }

describe('OrderRetry.abandonShown（F2-18）', () => {
  it('仍是显示的那笔：清掉，返回 go；下一笔用新请求号', () => {
    const s = memStore()
    const tab = new OrderRetry(s)
    tab.bind('alice')
    const key = tab.begin(P)
    expect(tab.abandonShown(P)).toEqual({ kind: 'go' })
    expect(s.data[ORDER_RETRY_KEY]).toBeUndefined()
    expect(tab.refresh()).toBeNull()
    expect(tab.begin(P)).not.toBe(key)
  })

  it('等待期间别处已处理完：返回 settled，存储不动', () => {
    const s = memStore()
    const tab1 = new OrderRetry(s)
    const tab2 = new OrderRetry(s)
    tab1.bind('alice'); tab2.bind('alice')
    const key = tab1.begin(P)
    tab2.settle(key) // 标签页 2 原样重试成功
    expect(tab1.abandonShown(P)).toEqual({ kind: 'settled' })
    expect(s.data[ORDER_RETRY_KEY]).toBeUndefined()
  })

  it('等待期间别的标签页放弃并锁定了另一笔：不清那一笔，返回 locked 改显示它', () => {
    const s = memStore()
    const tab1 = new OrderRetry(s)
    const tab2 = new OrderRetry(s)
    tab1.bind('alice'); tab2.bind('alice')
    tab1.begin(P)
    tab2.abandon()
    const other = { ...P, quantity: 9 }
    const k2 = tab2.begin(other) // 标签页 2 新下的单还在路上
    const before = s.data[ORDER_RETRY_KEY]
    expect(tab1.abandonShown(P)).toEqual({ kind: 'locked', pending: other })
    expect(s.data[ORDER_RETRY_KEY]).toBe(before)
    expect(tab2.begin(other)).toBe(k2) // 标签页 2 仍可原样重试同一请求号
  })

  it('页面上没有显示待重试（shown 为空）：什么都不清', () => {
    const s = memStore()
    const tab = new OrderRetry(s)
    tab.bind('alice')
    expect(tab.abandonShown(null)).toEqual({ kind: 'settled' })
    tab.begin(P)
    expect(tab.abandonShown(null)).toEqual({ kind: 'locked', pending: P })
    expect(tab.refresh()).toEqual(P)
  })

  it('还没核对身份：什么都不清', () => {
    const s = memStore()
    const a = new OrderRetry(s)
    a.bind('alice')
    a.begin(P)
    const fresh = new OrderRetry(s)
    expect(fresh.abandonShown(P)).toEqual({ kind: 'settled' })
    expect(s.data[ORDER_RETRY_KEY]).toBeTruthy()
  })
})
