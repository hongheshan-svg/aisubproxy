// 账单页：凭卡密查到的本站开通记录、登录信息查到的账单怎么显示。两种都不显示付款金额（代理商进价和最终买家付的钱不一样）。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { createI18n } from 'vue-i18n'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'
import { billingRecordRows, formatUnixTs, invoiceRows, pickBillingResult, type BillingRecord } from '../billingRecord'

const CJK = /[一-鿿]/

function tOf(locale: 'zh' | 'en') {
  const g = createI18n({ legacy: false, locale, messages: { zh, en } }).global
  return (k: string, p?: Record<string, unknown>) => String(g.t(k, p ?? {}))
}

describe('invoiceRows：登录信息查到的账单只显示状态和日期', () => {
  const inv = {
    id: 'in_1', number: 'ABCD-0001', status: 'paid', paid: true, created: 1700000000,
    total: 112000, currency: 'php', amount_paid: 112000,
    description: '1 × ChatGPT Plus (at ₱1,120.00 / month)',
    hosted_invoice_url: 'https://invoice.stripe.com/i/x', invoice_pdf: 'https://pay.stripe.com/invoice/x/pdf',
  }
  it('已付：状态「已付」，日期跟界面语言', () => {
    expect(invoiceRows([inv], tOf('zh'), 'zh')).toEqual([{ key: 'in_1', status: '已付', date: formatUnixTs(1700000000, 'zh') }])
  })
  it('后端多给了金额、币种、描述、链接也不显示', () => {
    const shown = JSON.stringify(invoiceRows([inv], tOf('zh'), 'zh'))
    expect(shown).not.toMatch(/1120|112000|php|PHP|₱|stripe|ChatGPT Plus/)
  })
  it('没付：有状态显示状态，没有状态显示「未付」', () => {
    const rows = invoiceRows([{ id: 'in_2', status: 'open', paid: false, created: 1700000000 }, { number: 'N-3', created: 0 }], tOf('zh'))
    expect(rows.map((r) => r.status)).toEqual(['open', '未付'])
    expect(rows[1]).toEqual({ key: 'N-3', status: '未付', date: '—' })
  })
  it('没有 id 和编号时按顺序给 key，不重复', () => {
    const rows = invoiceRows([{ paid: true }, { paid: true }], tOf('en'), 'en')
    expect(new Set(rows.map((r) => r.key)).size).toBe(2)
    expect(rows[0].status).toBe('Paid')
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
  }
  const val = (rows: { key: string; value: string }[], key: string) => rows.find((r) => r.key === key)?.value

  it('订阅码：五行，标签和值都对；没有付款金额（代理商进价和最终买家付的钱不一样）', () => {
    const rows = billingRecordRows(sub, tOf('zh'), 'zh')
    expect(rows.map((r) => r.key)).toEqual(['account', 'plan', 'redeemedAt', 'accessUntil', 'autoRenew'])
    expect(rows.map((r) => r.label)).toEqual(['账号', '方案', '开通时间', '权益到期', '自动续费'])
    expect(val(rows, 'account')).toBe('bu***@example.com')
    expect(val(rows, 'plan')).toBe('Plus')
    expect(val(rows, 'redeemedAt')).toBe(formatUnixTs(1700000000, 'zh'))
    expect(val(rows, 'accessUntil')).toBe(formatUnixTs(1702592000, 'zh'))
    expect(val(rows, 'autoRenew')).toBe('已关闭')
  })
  it('续费还没核实关掉：显示 —，绝不显示「开启」', () => {
    const rows = billingRecordRows({ ...sub, renewal_off: false }, tOf('zh'))
    expect(val(rows, 'autoRenew')).toBe('—')
  })
  it('没有到期时间：显示 —', () => {
    const { access_until, ...rest } = sub
    void access_until
    expect(val(billingRecordRows(rest, tOf('zh')), 'accessUntil')).toBe('—')
  })
  it('后端多给了金额也不显示', () => {
    const rows = billingRecordRows({ ...sub, amount: '1120.00', currency: 'PHP' } as BillingRecord, tOf('zh'), 'zh')
    expect(rows.map((r) => r.value).join(' ')).not.toMatch(/1120|PHP/)
  })
  it('点数码：不显示到期和自动续费', () => {
    const rows = billingRecordRows({ email: 'a***@x.com', plan: 'credit250', plan_kind: 'credit', redeemed_at: 1700000000, renewal_off: false }, tOf('zh'))
    expect(rows.map((r) => r.key)).toEqual(['account', 'plan', 'redeemedAt'])
    expect(val(rows, 'plan')).toBe('Codex 点数 250')
  })
  it('英文界面：标签和套餐名没有中文', () => {
    const rows = billingRecordRows(sub, tOf('en'), 'en')
    expect(rows.map((r) => r.label + ' ' + r.value).join(' ')).not.toMatch(CJK)
    expect(val(rows, 'autoRenew')).toBe('Off')
  })
})

describe('pickBillingResult：接口返回 → 页面显示哪一块', () => {
  const sub: BillingRecord = { email: 'bu***@example.com', plan: 'plus', plan_kind: 'subscription', redeemed_at: 1700000000, renewal_off: true }
  it('凭卡密查询：只有开通记录，不显示订阅摘要和账单', () => {
    const r = pickBillingResult({ auth_source: 'cdk', record: { ...sub }, note: 'x' })
    expect(r.record).toEqual(sub)
    expect(r.summary).toBeNull()
    expect(r.invoices).toBeNull()
  })
  it('登录信息查询：订阅摘要和账单，没有开通记录', () => {
    const r = pickBillingResult({ auth_source: 'session', summary: { plan_type: 'plus' }, invoices: [{ id: 'in_1' }] })
    expect(r.record).toBeNull()
    expect(r.summary).toEqual({ plan_type: 'plus' })
    expect(r.invoices).toEqual([{ id: 'in_1' }])
  })
  it('登录信息查询缺字段时给空的摘要和空列表', () => {
    const r = pickBillingResult({ auth_source: 'session' })
    expect(r.summary).toEqual({})
    expect(r.invoices).toEqual([])
  })
  it('record 不是对象时不当开通记录', () => {
    for (const bad of [null, 'x', 1, []]) {
      expect(pickBillingResult({ record: bad }).record).toBeNull()
    }
  })
})

describe('账单页接上了这些函数', () => {
  const src = readFileSync(join(__dirname, '..', '..', 'views', 'user', 'BillingCheckView.vue'), 'utf8')
  it('按 pickBillingResult 决定显示哪一块', () => {
    expect(src).toContain('pickBillingResult(d)')
  })
  it('开通记录一块按 billingRecordRows 逐行显示', () => {
    expect(src).toContain('billingRecordRows(record, t, locale)')
    expect(src).toContain("t('billingCheck.recordTitle')")
  })
  it('登录信息查询的账单按 invoiceRows 显示，不碰金额、币种、描述和账单链接', () => {
    expect(src).toContain('invoiceRows(invoices, t, locale)')
    for (const k of ['total', 'currency', 'amount', 'description', 'hosted_invoice_url', 'invoice_pdf', 'invoiceLink']) {
      expect(src).not.toContain(k)
    }
  })
})
