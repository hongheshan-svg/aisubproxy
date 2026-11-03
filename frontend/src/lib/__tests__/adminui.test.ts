import { describe, expect, it } from 'vitest'
import {
  availableCards, FAIL_TEXT, mainUsdBalance, marsEnv, pickTab, tabCrumb, todoNeedsAttention, todoText, zovoEnv, PAGE_TABS,
} from '../adminui'

describe('待办展示：失败不能显示成 0', () => {
  it('失败显示「获取失败」，加载中显示省略号，成功显示数值', () => {
    expect(todoText({ state: 'fail', error: 'x' })).toBe(FAIL_TEXT)
    expect(todoText({ state: 'loading' })).toBe('…')
    expect(todoText({ state: 'ok', value: 0 })).toBe('0')
  })
  it('失败也需要运营关注，0 不需要', () => {
    expect(todoNeedsAttention({ state: 'fail', error: '' })).toBe(true)
    expect(todoNeedsAttention({ state: 'ok', value: 0 })).toBe(false)
    expect(todoNeedsAttention({ state: 'ok', value: 2 })).toBe(true)
  })
})

describe('卡池概览汇总', () => {
  it('可用卡数求和，products 为空时为 0', () => {
    expect(availableCards({ products: [{ available: 2 }, { available: 3 }] })).toBe(5)
    expect(availableCards({ products: null })).toBe(0)
  })
  it('主钱包余额：有 balance_error 或没有美元项返回 null', () => {
    expect(mainUsdBalance({ main_balances: [{ currency: 'USD', available_balance: '12.50' }] })).toBe('12.50')
    expect(mainUsdBalance({ balance_error: 'boom', main_balances: [] })).toBeNull()
    expect(mainUsdBalance({ main_balances: [{ currency: 'USDT', available_balance: '1' }] })).toBeNull()
  })
})

describe('环境徽章', () => {
  it('卡台生产、沙盒、自定义', () => {
    expect(zovoEnv('https://zovocard.com/').label).toBe('生产')
    expect(zovoEnv('').label).toBe('生产')
    expect(zovoEnv('https://sandbox.zovocard.com').label).toBe('沙盒')
    expect(zovoEnv('https://x.example').kind).toBe('custom')
  })
  it('MarsWallex 按预设比对', () => {
    const presets = [{ id: 'prod', url: 'https://p.example/api' }, { id: 'test', url: 'https://t.example/api' }]
    expect(marsEnv('', presets).label).toBe('生产')
    expect(marsEnv('https://t.example/api/', presets).label).toBe('测试')
    expect(marsEnv('https://o.example', presets).kind).toBe('custom')
  })
})

describe('页内标签', () => {
  it('pickTab 非法值回落默认', () => {
    expect(pickTab('holders', ['overview', 'holders'], 'overview')).toBe('holders')
    expect(pickTab('x', ['overview'], 'overview')).toBe('overview')
    expect(pickTab(['holders'], ['holders'], 'a')).toBe('holders')
    expect(pickTab(undefined, ['a'], 'a')).toBe('a')
  })
  it('面包屑第三级', () => {
    expect(tabCrumb(PAGE_TABS, '/ops/cardpool', 'policy')).toBe('策略与健康度')
    expect(tabCrumb(PAGE_TABS, '/ops/cardpool', undefined)).toBe('概览')
    expect(tabCrumb(PAGE_TABS, '/ops/providers', 'x')).toBe('')
  })
})
