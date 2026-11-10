// 账单页的显示逻辑：凭卡密查到的本站开通记录、登录信息查到的账单逐行怎么显示。
// 两种都不显示付款金额：代理商进价和最终买家付的钱不一样，显示出来会误导。
import { planLabelT } from './plan'

/**
 * 凭卡密查账单返回的本站开通记录（后端 gpt_billing.go 的 billingCDKRecord）。时间是 Unix 秒。
 * 没有付款金额：代理商进价和最终买家付的钱不一样，显示出来会误导。
 */
export interface BillingRecord {
  email: string
  plan: string
  plan_kind: string
  redeemed_at?: number
  access_until?: number
  renewal_off: boolean
}

export interface BillingRecordRow {
  key: string
  label: string
  value: string
}

type TFn = (key: string, params?: Record<string, unknown>) => string

export interface InvoiceRow {
  key: string
  status: string
  date: string
}

/**
 * 登录信息查到的账单逐张显示什么：只有状态和日期。
 * 不显示金额、币种、描述（Stripe 的描述里带单价）和账单链接（点开就是金额），后端也不再给这些字段。
 */
export function invoiceRows(invoices: Record<string, unknown>[], t: TFn, locale?: string): InvoiceRow[] {
  return invoices.map((inv, i) => ({
    key: String(inv.id || inv.number || `#${i}`),
    status: inv.paid ? t('billingCheck.paid') : inv.status ? String(inv.status) : t('billingCheck.unpaid'),
    date: formatUnixTs(inv.created, locale),
  }))
}

/** 账单查询接口的返回在页面上显示哪一块：凭卡密查询只有开通记录，登录信息查询是订阅摘要和账单列表。 */
export interface BillingResult {
  record: BillingRecord | null
  summary: Record<string, unknown> | null
  invoices: Record<string, unknown>[] | null
}

export function pickBillingResult(d: unknown): BillingResult {
  const o = d && typeof d === 'object' ? (d as Record<string, unknown>) : {}
  const rec = o.record
  if (rec && typeof rec === 'object' && !Array.isArray(rec)) {
    return { record: rec as BillingRecord, summary: null, invoices: null }
  }
  const summary = o.summary && typeof o.summary === 'object' ? (o.summary as Record<string, unknown>) : {}
  const invoices = Array.isArray(o.invoices) ? (o.invoices as Record<string, unknown>[]) : []
  return { record: null, summary, invoices }
}

/** 界面语言对应的日期格式；不传时跟浏览器。 */
function dateLocale(locale?: string): string | undefined {
  if (!locale) return undefined
  return locale.startsWith('zh') ? 'zh-CN' : 'en-US'
}

/** Unix 秒按本地时区显示，日期格式跟界面语言（英文界面不出现「上午」）；0、空、读不出给 —。 */
export function formatUnixTs(v: unknown, locale?: string): string {
  const n = Number(v)
  if (!Number.isFinite(n) || n <= 0) return '—'
  try {
    return new Date(n * 1000).toLocaleString(dateLocale(locale))
  } catch {
    return String(v)
  }
}

/**
 * 开通记录逐行显示的内容。订阅码多两行：权益到期、自动续费。
 * 自动续费只有后端核实关掉时才显示「已关闭」，其余一律「—」：本站记录说明不了现在是否还在续费。
 */
export function billingRecordRows(rec: BillingRecord, t: TFn, locale?: string): BillingRecordRow[] {
  const rows: BillingRecordRow[] = [
    { key: 'account', label: t('billingCheck.account'), value: rec.email || '—' },
    { key: 'plan', label: t('billingCheck.plan'), value: rec.plan ? planLabelT(rec.plan, t) : '—' },
    { key: 'redeemedAt', label: t('billingCheck.redeemedAt'), value: formatUnixTs(rec.redeemed_at, locale) },
  ]
  if (rec.plan_kind === 'subscription') {
    rows.push(
      { key: 'accessUntil', label: t('billingCheck.accessUntil'), value: formatUnixTs(rec.access_until, locale) },
      { key: 'autoRenew', label: t('billingCheck.autoRenew'), value: rec.renewal_off ? t('billingCheck.renewOff') : '—' },
    )
  }
  return rows
}
