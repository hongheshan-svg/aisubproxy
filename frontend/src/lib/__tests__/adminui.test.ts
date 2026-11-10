import { describe, expect, it } from 'vitest'
import {
  availableCards, FAIL_TEXT, sumTodos, mainUsdBalance, marsEnv, pickTab, tabCrumb, todoNeedsAttention, todoText, PAGE_TABS,
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

describe('sumTodos', () => {
  it('有一项失败整体就是失败，不当 0 相加', () => {
    expect(sumTodos([{ state: 'ok', value: 3 }, { state: 'fail', error: 'e' }]).state).toBe('fail')
    expect(sumTodos([{ state: 'ok', value: 3 }, { state: 'loading' }]).state).toBe('loading')
    expect(sumTodos([{ state: 'ok', value: 3 }, { state: 'ok', value: 0 }])).toEqual({ state: 'ok', value: 3 })
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
  it('MarsWallex 按预设比对', () => {
    const presets = [{ id: 'prod', url: 'https://p.example/api' }, { id: 'test', url: 'https://t.example/api' }]
    expect(marsEnv('', presets, true).label).toBe('生产')
    expect(marsEnv('https://t.example/api/', presets, true).label).toBe('测试')
    expect(marsEnv('https://o.example', presets, true).kind).toBe('custom')
  })
  it('MarsWallex 没有任何凭证时显示未配置，不因默认地址是生产而显示生产', () => {
    const presets = [{ id: 'prod', url: 'https://p.example/api' }]
    const b = marsEnv('', presets, false)
    expect(b.kind).toBe('unconfigured')
    expect(b.label).toBe('未配置')
    expect(marsEnv('https://p.example/api', presets, false).kind).toBe('unconfigured')
  })
})
