import { describe, expect, it } from 'vitest'
import { IdempotencyKeeper, newIdempotencyKey } from '../idempotency'

describe('发码幂等键', () => {
  it('生成 UUID 格式且每次不同', () => {
    const a = newIdempotencyKey()
    expect(a).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
    expect(newIdempotencyKey()).not.toBe(a)
  })

  it('同样的请求重试沿用同一个键', () => {
    const k = new IdempotencyKeeper()
    const p = { plan: 'plus', count: 3 }
    expect(k.keyFor({ ...p })).toBe(k.keyFor({ ...p }))
  })

  it('请求内容变了换新键', () => {
    const k = new IdempotencyKeeper()
    const a = k.keyFor({ plan: 'plus', count: 3 })
    expect(k.keyFor({ plan: 'plus', count: 4 })).not.toBe(a)
  })

  it('明确成功后下一次购买用新键', () => {
    const k = new IdempotencyKeeper()
    const a = k.keyFor({ plan: 'plus', count: 3 })
    k.done()
    expect(k.keyFor({ plan: 'plus', count: 3 })).not.toBe(a)
  })
})
