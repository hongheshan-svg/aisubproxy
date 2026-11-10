// 账单页：凭卡密查到的本站开通记录怎么显示，以及会话查询的账单金额按币种小数位格式化。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { createI18n } from 'vue-i18n'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'
import { billingRecordRows, formatInvoiceAmount, formatUnixTs, type BillingRecord } from '../billingRecord'

const CJK = /[一-鿿]/

function tOf(locale: 'zh' | 'en') {
  const g = createI18n({ legacy: false, locale, messages: { zh, en } }).global
  return (k: string, p?: Record<string, unknown>) => String(g.t(k, p ?? {}))
}

describe('formatInvoiceAmount：会话查询的账单金额是最小单位', () => {
  it('两位小数的币种除以 100', () => {
    expect(formatInvoiceAmount(2000, 'usd')).toBe('20.00 USD')
    expect(formatInvoiceAmount(112000, 'php')).toBe('1120.00 PHP')
  })
  it('没有小数的币种不除以 100', () => {
    expect(formatInvoiceAmount(3000, 'jpy')).toBe('3000 JPY')
    expect(formatInvoiceAmount(15990, 'CLP')).toBe('15990 CLP')
    expect(formatInvoiceAmount(9900, 'krw')).toBe('9900 KRW')
  })
  it('没有币种按 USD，读不出数字给 —', () => {
    expect(formatInvoiceAmount(500, '')).toBe('5.00 USD')
    expect(formatInvoiceAmount('abc', 'usd')).toBe('—')
    expect(formatInvoiceAmount(undefined, 'usd')).toBe('—')
  })
})

describe('formatUnixTs', () => {
  it('0、空、读不出给 —', () => {
    expect(formatUnixTs(0)).toBe('—')
    expect(formatUnixTs(undefined)).toBe('—')
    expect(formatUnixTs('x')).toBe('—')
  })
  it('日期格式跟界面语言：英文界面没有中文', () => {
    expect(formatUnixTs(1700000000, 'en')).toBe(new Date(1700000000 * 1000).toLocaleString('en-US'))
    expect(formatUnixTs(1700000000, 'en')).not.toMatch(CJK)
    expect(formatUnixTs(1700000000, 'zh')).toBe(new Date(1700000000 * 1000).toLocaleString('zh-CN'))
  })
})

describe('billingRecordRows：凭卡密查到的开通记录', () => {
  const sub: BillingRecord = {
    email: 'bu***@example.com',
    plan: 'plus',
    plan_kind: 'subscription',
    redeemed_at: 1700000000,
    access_until: 1702592000,
    renewal_off: true,
    amount: '1120.00',
    currency: 'PHP',
  }
  const val = (rows: { key: string; value: string }[], key: string) => rows.find((r) => r.key === key)?.value

  it('订阅码：六行，标签和值都对', () => {
    const rows = billingRecordRows(sub, tOf('zh'), 'zh')
    expect(rows.map((r) => r.key)).toEqual(['account', 'plan', 'redeemedAt', 'amount', 'accessUntil', 'autoRenew'])
    expect(rows.map((r) => r.label)).toEqual(['账号', '方案', '开通时间', '付款金额', '权益到期', '自动续费'])
    expect(val(rows, 'account')).toBe('bu***@example.com')
    expect(val(rows, 'plan')).toBe('Plus')
    expect(val(rows, 'redeemedAt')).toBe(formatUnixTs(1700000000, 'zh'))
    expect(val(rows, 'amount')).toBe('1120.00 PHP')
    expect(val(rows, 'accessUntil')).toBe(formatUnixTs(1702592000, 'zh'))
    expect(val(rows, 'autoRenew')).toBe('已关闭')
  })
  it('续费还没核实关掉：显示 —，绝不显示「开启」', () => {
    const rows = billingRecordRows({ ...sub, renewal_off: false }, tOf('zh'))
    expect(val(rows, 'autoRenew')).toBe('—')
  })
  it('没有金额、到期时间：显示 —', () => {
    const { amount, currency, access_until, ...rest } = sub
    void amount, currency, access_until
    const rows = billingRecordRows(rest, tOf('zh'))
    expect(val(rows, 'amount')).toBe('—')
    expect(val(rows, 'accessUntil')).toBe('—')
  })
  it('点数码：不显示到期和自动续费', () => {
    const rows = billingRecordRows({ email: 'a***@x.com', plan: 'credit250', plan_kind: 'credit', redeemed_at: 1700000000, renewal_off: false, amount: '550.00', currency: 'PHP' }, tOf('zh'))
    expect(rows.map((r) => r.key)).toEqual(['account', 'plan', 'redeemedAt', 'amount'])
    expect(val(rows, 'plan')).toBe('Codex 点数 250')
  })
  it('英文界面：标签和套餐名没有中文', () => {
    const rows = billingRecordRows(sub, tOf('en'), 'en')
    expect(rows.map((r) => r.label + ' ' + r.value).join(' ')).not.toMatch(CJK)
    expect(val(rows, 'autoRenew')).toBe('Off')
  })
})

describe('账单页接上了这些函数', () => {
  const src = readFileSync(join(__dirname, '..', '..', 'views', 'user', 'BillingCheckView.vue'), 'utf8')
  it('开通记录一块按 billingRecordRows 逐行显示', () => {
    expect(src).toContain('billingRecordRows(record, t, locale)')
    expect(src).toContain("t('billingCheck.recordTitle')")
  })
  it('会话查询的账单金额用 formatInvoiceAmount，不再一律除以 100', () => {
    expect(src).toContain('formatInvoiceAmount(inv.total, inv.currency)')
    expect(src).not.toContain('/ 100')
    expect(src).toContain('formatUnixTs(inv.created, locale)')
  })
})
