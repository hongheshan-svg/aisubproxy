import { describe, expect, it } from 'vitest'
import {
  ACG_SETUP_STEPS, FAILURE_REASON_TEXT, LOCK_HINT, SHARED_STATUS_TEXT, failureBanner, lowBalanceBanner,
  parseAllowlistInput, parseLowBalanceInput, sourceLabel,
} from '../agentShared'

describe('failureBanner', () => {
  it('有失败时给出提示', () => {
    expect(failureBanner({ total: 3, insufficient_balance: 2 })).toBe('最近 24 小时有 3 笔异次元订单取码失败（余额不足 2 笔），客户可能已付款但没收到码')
  })
  it('没有失败或没数据返回空串', () => {
    expect(failureBanner({ total: 0, insufficient_balance: 0 })).toBe('')
    expect(failureBanner(undefined)).toBe('')
    expect(failureBanner(null)).toBe('')
  })
})

describe('lowBalanceBanner', () => {
  it('低于提醒线才提示', () => {
    expect(lowBalanceBanner({ threshold_cents: 200000, below: true })).toBe('余额低于提醒线 $2,000.00')
    expect(lowBalanceBanner({ threshold_cents: 200000, below: false })).toBe('')
    expect(lowBalanceBanner(undefined)).toBe('')
  })
})

describe('parseAllowlistInput', () => {
  it('按逗号、空白、换行切分并去重', () => {
    expect(parseAllowlistInput('1.2.3.4, 10.0.0.0/8\n1.2.3.4  ')).toEqual({ ips: ['1.2.3.4', '10.0.0.0/8'], error: '' })
  })
  it('超过 5 项报错', () => {
    expect(parseAllowlistInput('1.1.1.1 2.2.2.2 3.3.3.3 4.4.4.4 5.5.5.5 6.6.6.6').error).toBe('IP 白名单最多 5 项')
  })
  it('空串得到空列表', () => {
    expect(parseAllowlistInput('')).toEqual({ ips: [], error: '' })
  })
})

describe('parseLowBalanceInput', () => {
  it('空和 0 表示关闭', () => {
    expect(parseLowBalanceInput('')).toEqual({ ok: true, cents: 0 })
    expect(parseLowBalanceInput('0')).toEqual({ ok: true, cents: 0 })
    expect(parseLowBalanceInput('0.00')).toEqual({ ok: true, cents: 0 })
  })
  it('正常金额转美分', () => {
    expect(parseLowBalanceInput('20')).toEqual({ ok: true, cents: 2000 })
  })
  it('负数和过大报范围错误', () => {
    const e = { ok: false, error: '提醒线需为 0 到 10000 美元' }
    expect(parseLowBalanceInput('-1')).toEqual(e)
    expect(parseLowBalanceInput('10000.01')).toEqual(e)
  })
  it('格式不对沿用金额解析的提示', () => {
    expect(parseLowBalanceInput('abc')).toEqual({ ok: false, error: '金额格式不对' })
  })
})

describe('sourceLabel', () => {
  it('已知来源给中文，未知或空给空串', () => {
    expect(sourceLabel('acg')).toBe('异次元')
    expect(sourceLabel('web')).toBe('后台')
    expect(sourceLabel('')).toBe('')
    expect(sourceLabel(undefined)).toBe('')
    expect(sourceLabel('zzz')).toBe('')
  })
})

describe('常量', () => {
  it('接入步骤 4 条，第 2 条提到 USD 和汇率', () => {
    expect(ACG_SETUP_STEPS.length).toBe(4)
    expect(ACG_SETUP_STEPS[1]).toContain('USD')
    expect(ACG_SETUP_STEPS[1]).toContain('汇率')
  })
  it('锁定提示写明最多 15 分钟和「密钥错误」', () => {
    expect(LOCK_HINT).toContain('15 分钟')
    expect(LOCK_HINT).toContain('密钥错误')
  })
  it('状态和失败原因文案', () => {
    expect(SHARED_STATUS_TEXT.admin_disabled).toBe('已被管理员关闭')
    expect(FAILURE_REASON_TEXT.insufficient_balance).toBe('余额不足')
  })
})
