import { describe, it, expect } from 'vitest'
import { IdempotencyKeeper, IdempotencyLockedError, type KeeperStore } from '../idempotency'

function memStore(): KeeperStore & { data: Record<string, string> } {
  const data: Record<string, string> = {}
  return {
    data,
    getItem: (k) => (k in data ? data[k] : null),
    setItem: (k, v) => { data[k] = v },
    removeItem: (k) => { delete data[k] },
  }
}

describe('IdempotencyKeeper', () => {
  it('内容不变沿用同一个键，内容变了换新键', () => {
    const k = new IdempotencyKeeper()
    const a = k.keyFor({ n: 1 })
    expect(k.keyFor({ n: 1 })).toBe(a)
    expect(k.keyFor({ n: 2 })).not.toBe(a)
  })

  it('结果未知后「刷新」（新建 keeper）仍能找回同一个键', () => {
    const s = memStore()
    const k1 = new IdempotencyKeeper('x', s)
    const key = k1.keyFor({ plan: 'plus', count: 3 })
    k1.markUncertain()
    const k2 = new IdempotencyKeeper('x', s)
    expect(k2.isLocked()).toBe(true)
    expect(k2.keyFor({ plan: 'plus', count: 3 })).toBe(key)
  })

  it('锁定期间内容变了拒绝换键，放弃后才能用新键', () => {
    const s = memStore()
    const k = new IdempotencyKeeper('x', s)
    const key = k.keyFor({ count: 3 })
    k.markUncertain()
    expect(() => k.keyFor({ count: 5 })).toThrow(IdempotencyLockedError)
    k.discard()
    expect(k.isLocked()).toBe(false)
    expect(k.keyFor({ count: 5 })).not.toBe(key)
    expect(Object.keys(s.data)).toHaveLength(0)
  })

  it('明确成功清掉落盘内容；没标记结果未知就不落盘', () => {
    const s = memStore()
    const k = new IdempotencyKeeper('x', s)
    k.keyFor({ a: 1 })
    expect(Object.keys(s.data)).toHaveLength(0)
    k.markUncertain()
    expect(Object.keys(s.data)).toHaveLength(1)
    k.done()
    expect(Object.keys(s.data)).toHaveLength(0)
    expect(new IdempotencyKeeper('x', s).isLocked()).toBe(false)
  })

  it('落盘内容过期或损坏时忽略', () => {
    const s = memStore()
    let t = 1_000_000
    const k = new IdempotencyKeeper('x', s, () => t)
    k.keyFor({ a: 1 })
    k.markUncertain()
    t += 25 * 3600 * 1000
    expect(new IdempotencyKeeper('x', s, () => t).isLocked()).toBe(false)
    s.setItem('x', '{坏的')
    expect(new IdempotencyKeeper('x', s).isLocked()).toBe(false)
  })

  it('存储里只有键和请求参数，不含码', () => {
    const s = memStore()
    const k = new IdempotencyKeeper('x', s)
    k.keyFor({ plan: 'plus', count: 1 })
    k.markUncertain()
    expect(s.data['x']).not.toMatch(/code/i)
  })
})
