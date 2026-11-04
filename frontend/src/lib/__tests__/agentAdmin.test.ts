import { describe, expect, it } from 'vitest'
import {
  adminErrorText, classifyAdjust, matrixChanges, overrideChanges, validProductCode,
} from '../agentAdmin'

describe('matrixChanges', () => {
  const orig = [{ tier_id: 1, product_id: 2, price_cents: 500 }]
  it('没改的格子不返回', () => {
    expect(matrixChanges(orig, { '1:2': '5.00' })).toEqual({ cells: [], errors: [] })
  })
  it('改价按美分返回', () => {
    expect(matrixChanges(orig, { '1:2': '6' }).cells).toEqual([{ tier_id: 1, product_id: 2, price_cents: 600 }])
  })
  it('空串或 0 表示删除', () => {
    expect(matrixChanges(orig, { '1:2': '' }).cells).toEqual([{ tier_id: 1, product_id: 2, price_cents: 0 }])
    expect(matrixChanges(orig, { '1:2': '0' }).cells).toEqual([{ tier_id: 1, product_id: 2, price_cents: 0 }])
  })
  it('原本就没有的格子留空不算改动', () => {
    expect(matrixChanges([], { '1:3': '' })).toEqual({ cells: [], errors: [] })
  })
  it('非法金额报错且不进 cells', () => {
    const r = matrixChanges([], { '1:3': 'abc', '1:4': '-1', '1:5': '1.234' })
    expect(r.errors.length).toBe(3)
    expect(r.cells).toEqual([])
  })
  it('新增格子', () => {
    expect(matrixChanges([], { '2:3': '1.5' }).cells).toEqual([{ tier_id: 2, product_id: 3, price_cents: 150 }])
  })
  it('键格式不对报错', () => {
    expect(matrixChanges([], { 'x': '1' }).errors.length).toBe(1)
  })
})

describe('overrideChanges', () => {
  const orig = [{ product_id: 7, price_cents: 300 }]
  it('只返回不同的格子', () => {
    expect(overrideChanges(orig, { '7': '3' })).toEqual({ cells: [], errors: [] })
    expect(overrideChanges(orig, { '7': '4.5' }).cells).toEqual([{ product_id: 7, price_cents: 450 }])
  })
  it('空串取消覆盖', () => {
    expect(overrideChanges(orig, { '7': '' }).cells).toEqual([{ product_id: 7, price_cents: 0 }])
    expect(overrideChanges([], { '8': '' }).cells).toEqual([])
  })
  it('非法金额报错', () => {
    expect(overrideChanges([], { '8': 'x' }).errors.length).toBe(1)
  })
})

describe('classifyAdjust', () => {
  it('成功', () => {
    expect(classifyAdjust(200, { balance_cents: 100, replayed: false })).toMatchObject({ kind: 'ok', clearKey: true })
  })
  it('重放成功给提示', () => {
    const r = classifyAdjust(200, { balance_cents: 100, replayed: true })
    expect(r.kind).toBe('replayed')
    expect(r.clearKey).toBe(true)
    expect(r.message).toBe('这是之前同一次调账的结果，没有重复入账')
  })
  it('400、409 是明确失败，清键', () => {
    expect(classifyAdjust(400, { error: 'invalid_request', message: '原因不能为空' })).toMatchObject({ kind: 'failed', clearKey: true, message: '原因不能为空' })
    expect(classifyAdjust(409, { error: 'insufficient_balance' })).toMatchObject({ kind: 'failed', clearKey: true })
    expect(classifyAdjust(409, { error: 'idempotency_conflict' }).message).toContain('幂等')
  })
  it('网络异常和 5xx 结果未知，保留键', () => {
    for (const s of [0, 500, 502, 503]) {
      const r = classifyAdjust(s, {})
      expect(r.kind).toBe('unknown')
      expect(r.clearKey).toBe(false)
      expect(r.message).toContain('结果未知，请原样重试')
    }
  })
})

describe('adminErrorText', () => {
  it('优先 message', () => {
    expect(adminErrorText({ error: 'x_y', message: '自定义' })).toBe('自定义')
  })
  it('查编号表，含管理员侧编号', () => {
    expect(adminErrorText({ error: 'username_taken' })).toBe('登录名已被使用')
    expect(adminErrorText({ error: 'tier_in_use' })).toContain('在用')
    expect(adminErrorText({ error: 'name_taken' })).toBe('名称已存在')
    expect(adminErrorText({ error: 'product_code_taken' })).toBe('商品编码已存在')
    expect(adminErrorText({ error: 'busy' })).toBe('系统繁忙，请稍后重试')
  })
  it('旧格式中文文本原样显示', () => {
    expect(adminErrorText({ error: '缺少 Idempotency-Key' })).toBe('缺少 Idempotency-Key')
  })
  it('兜底', () => {
    expect(adminErrorText({}, 0)).toBe('操作失败，请稍后再试')
  })
})

describe('validProductCode', () => {
  it('规则 ^[a-z0-9-]{2,48}$', () => {
    expect(validProductCode('gpt-plus-us')).toBe(true)
    expect(validProductCode('a')).toBe(false)
    expect(validProductCode('Abc')).toBe(false)
    expect(validProductCode('a_b')).toBe(false)
    expect(validProductCode('a'.repeat(49))).toBe(false)
    expect(validProductCode('a'.repeat(48))).toBe(true)
  })
})
